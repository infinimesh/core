package shadow

import (
	"context"
	"slices"
	"sync/atomic"

	redis "github.com/go-redis/redis/v8"

	pb "github.com/infinimesh/proto/shadow"
)

const lifecycleTopic = "devices.lifecycle"

// Inbound stamps a shadow when it enters this process from RabbitMQ.
// The shadow command sets it to the running server.
var Inbound func(*pb.Shadow)

// Dispatch stamps a shadow and publishes it without letting a later sequence go out first.
// The shadow command sets it to the running server.
var Dispatch func(*pb.Shadow, func())

const eventIDKey = "shadow:event_id"

type sequencer interface {
	Next(context.Context) uint64
}

type atomicSeq struct {
	n atomic.Uint64
}

func (a *atomicSeq) Next(context.Context) uint64 {
	return a.n.Add(1)
}

type redisSeq struct {
	rdb redis.Cmdable
	fb  sequencer
}

func NewRedisSequencer(rdb redis.Cmdable) sequencer {
	return redisSeq{rdb: rdb, fb: &atomicSeq{}}
}

func (r redisSeq) Next(ctx context.Context) uint64 {
	if r.rdb != nil {
		n, err := r.rdb.Incr(ctx, eventIDKey).Result()
		if err == nil && n > 0 {
			return uint64(n)
		}
	}
	if r.fb != nil {
		return r.fb.Next(ctx)
	}
	return 0
}

type deviceMeta struct {
	title     string
	namespace string
	tags      []string
}

type streamScope struct {
	namespace  string
	namespaces map[string]struct{}
	account    string
}

func scopeFromRequest(req *pb.StreamShadowRequest) (streamScope, map[string]deviceMeta, map[string]struct{}) {
	sc := streamScope{namespace: req.GetNamespace(), account: req.GetAccount()}
	if req.GetNamespace() != "" || len(req.GetNamespaces()) > 0 || req.GetAccount() != "" {
		sc.namespaces = map[string]struct{}{}
		if req.GetNamespace() != "" {
			sc.namespaces[req.GetNamespace()] = struct{}{}
		}
		for _, ns := range req.GetNamespaces() {
			if ns != "" {
				sc.namespaces[ns] = struct{}{}
			}
		}
	}

	devices := map[string]struct{}{}
	for _, id := range req.GetDevices() {
		if id != "" {
			devices[id] = struct{}{}
		}
	}
	meta := map[string]deviceMeta{}
	for _, item := range req.GetCatalog() {
		if item.GetDevice() == "" {
			continue
		}
		meta[item.GetDevice()] = deviceMeta{
			title:     item.GetTitle(),
			namespace: item.GetNamespace(),
			tags:      append([]string(nil), item.GetTags()...),
		}
		devices[item.GetDevice()] = struct{}{}
	}
	return sc, meta, devices
}

func (sc streamScope) live() bool {
	return sc.namespace != "" || sc.account != "" || len(sc.namespaces) > 0
}

func (sc streamScope) wants(ev *pb.Shadow) bool {
	if !sc.live() || ev == nil {
		return false
	}
	if sc.namespace != "" {
		return ev.GetNamespace() == sc.namespace
	}
	if ev.GetAccount() != "" && ev.GetAccount() == sc.account {
		return true
	}
	if ns := ev.GetNamespace(); ns != "" {
		_, ok := sc.namespaces[ns]
		return ok
	}
	return false
}

func deviceTopics(id string) (string, string) {
	return "mqtt.incoming/" + id, "mqtt.outgoing/" + id
}

func subscriptionTopics(devices map[string]struct{}, live bool) []string {
	ids := make([]string, 0, len(devices))
	for id := range devices {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	topics := make([]string, 0, len(ids)*2+1)
	for _, id := range ids {
		in, out := deviceTopics(id)
		topics = append(topics, in, out)
	}
	if live {
		topics = append(topics, lifecycleTopic)
	}
	return topics
}

func applyTag(meta map[string]deviceMeta, ev *pb.Shadow) {
	if meta == nil || ev == nil || ev.GetDevice() == "" {
		return
	}
	item := meta[ev.GetDevice()]
	if len(ev.GetTags()) > 0 {
		item.tags = append([]string(nil), ev.GetTags()...)
	} else if ev.GetEvent() == pb.ShadowEvent_TAG_ADDED && ev.GetTag() != "" && !slices.Contains(item.tags, ev.GetTag()) {
		item.tags = append(item.tags, ev.GetTag())
	} else if ev.GetEvent() == pb.ShadowEvent_TAG_REMOVED && ev.GetTag() != "" {
		next := item.tags[:0]
		for _, tag := range item.tags {
			if tag != ev.GetTag() {
				next = append(next, tag)
			}
		}
		item.tags = next
	}
	if ev.GetTitle() != "" {
		item.title = ev.GetTitle()
	}
	if ev.GetNamespace() != "" {
		item.namespace = ev.GetNamespace()
	}
	meta[ev.GetDevice()] = item
}

func (s *ShadowServiceServer) remember(sh *pb.Shadow) {
	if sh.GetDevice() == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sh.GetEvent() == pb.ShadowEvent_DEVICE_REMOVED {
		if sh.GetAccount() == "" {
			delete(s.catalog, sh.GetDevice())
		}
		return
	}
	s.catalog[sh.GetDevice()] = deviceMeta{
		title:     sh.GetTitle(),
		namespace: sh.GetNamespace(),
		tags:      append([]string(nil), sh.GetTags()...),
	}
}

func (s *ShadowServiceServer) fill(sh *pb.Shadow, meta map[string]deviceMeta) {
	if sh == nil {
		return
	}
	item, ok := meta[sh.GetDevice()]
	if !ok {
		s.mu.Lock()
		item, ok = s.catalog[sh.GetDevice()]
		s.mu.Unlock()
	}
	if !ok {
		return
	}
	if sh.Title == "" {
		sh.Title = item.title
	}
	if sh.Namespace == "" {
		sh.Namespace = item.namespace
	}
	if len(sh.Tags) == 0 && len(item.tags) > 0 {
		sh.Tags = append([]string(nil), item.tags...)
	}
}

func (s *ShadowServiceServer) stamp(ctx context.Context, sh *pb.Shadow) {
	if sh == nil {
		return
	}
	if sh.EventId == 0 && s.ids != nil {
		sh.EventId = s.ids.Next(ctx)
	}
	if sh.Sequence == 0 {
		sh.Sequence = sh.EventId
	}
	s.rememberEvent(sh)
	s.fill(sh, nil)
}

func requestDevices(req *pb.GetRequest) []string {
	if req == nil {
		return nil
	}
	if req.GetDevice() != "" {
		return []string{req.GetDevice()}
	}
	if len(req.GetDevices()) > 0 {
		return append([]string(nil), req.GetDevices()...)
	}
	return append([]string(nil), req.GetPool()...)
}
