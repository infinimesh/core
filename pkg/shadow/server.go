/*
Copyright © 2021-2023 Infinite Devices GmbH Nikita Ivanovski info@slnt-opp.xyz

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package shadow

import (
	"context"
	"encoding/json"
	"slices"
	"sync"

	"connectrpc.com/connect"
	redis "github.com/go-redis/redis/v8"
	"github.com/infinimesh/core/pkg/observe"
	"github.com/infinimesh/core/pkg/pubsub"
	"go.uber.org/zap"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/infinimesh/proto/shadow"
)

type ReadRecorder interface {
	RecordRead(ctx context.Context, device, account, tokenID string)
}

type ShadowServiceServer struct {
	pb.UnimplementedShadowServiceServer

	log *zap.Logger
	rdb redis.Cmdable
	ps  pubsub.PubSub

	ids sequencer

	reads ReadRecorder

	mu      sync.Mutex
	catalog map[string]deviceMeta
	events  []*pb.Shadow
}

func NewShadowServiceServer(log *zap.Logger, rdb redis.Cmdable, ps pubsub.PubSub) *ShadowServiceServer {
	s := &ShadowServiceServer{
		log:     log.Named("shadow"),
		rdb:     rdb,
		ps:      ps,
		ids:     &atomicSeq{},
		catalog: map[string]deviceMeta{},
	}
	return s
}

func (s *ShadowServiceServer) SetReadRecorder(r ReadRecorder) {
	s.reads = r
}

func (s *ShadowServiceServer) SetSequencer(ids sequencer) {
	if ids != nil {
		s.ids = ids
	}
}

func (s *ShadowServiceServer) Get(ctx context.Context, req *pb.GetRequest) (*pb.GetResponse, error) {
	log := s.log.Named("get")
	pool := requestDevices(req)
	log.Debug("Request received", zap.Strings("pool", pool))

	keys := make([]string, len(pool)*3)
	for i, dev := range pool {
		keys[i*3] = Key(dev, pb.StateKey_REPORTED)
		keys[i*3+1] = Key(dev, pb.StateKey_DESIRED)
		keys[i*3+2] = Key(dev, pb.StateKey_CONNECTION)
	}
	if len(keys) == 0 {
		return nil, rpcErr(connect.CodeInvalidArgument, "no devices specified")
	}
	r := s.rdb.MGet(ctx, keys...)
	states, err := r.Result()
	if err != nil {
		return nil, rpcErr(connect.CodeInternal, "failed to get Shadows")
	}

	log.Debug("Got states", zap.Int("count", len(states)))
	shadows := make([]*pb.Shadow, len(pool))
	for i := range shadows {
		s := &pb.Shadow{
			Device:     pool[i],
			Reported:   &pb.State{},
			Desired:    &pb.State{},
			Connection: &pb.ConnectionState{},
		}
		if states[i*3] != nil {
			state := states[i*3].(string)
			json.Unmarshal([]byte(state), s.Reported)
		}
		if states[i*3+1] != nil {
			state := states[i*3+1].(string)
			json.Unmarshal([]byte(state), s.Desired)
		}
		if states[i*3+2] != nil {
			state := states[i*3+2].(string)
			json.Unmarshal([]byte(state), s.Connection)
		}
		shadows[i] = s
	}
	s.recordReads(ctx, pool)

	return &pb.GetResponse{Shadows: shadows}, nil
}

func (s *ShadowServiceServer) recordReads(ctx context.Context, devices []string) {
	if s == nil || s.reads == nil {
		return
	}
	md, _ := metadata.FromIncomingContext(ctx)
	account, tokenID := firstMeta(md, "account"), firstMeta(md, "jti")
	for _, id := range devices {
		s.reads.RecordRead(ctx, id, account, tokenID)
	}
}

func firstMeta(md metadata.MD, key string) string {
	if md == nil {
		return ""
	}
	vals := md.Get(key)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

func (s *ShadowServiceServer) PatchMany(ctx context.Context, req *pb.PatchManyRequest) (*pb.PatchManyResponse, error) {
	if req.GetDesired() == nil || len(req.GetDevices()) == 0 {
		return nil, rpcErr(connect.CodeInvalidArgument, "devices and desired state are required")
	}
	var n int32
	var results []*pb.DevicePatchResult
	for _, id := range req.GetDevices() {
		if id == "" {
			continue
		}
		res := &pb.DevicePatchResult{Device: id}
		if _, err := s.Patch(ctx, &pb.Shadow{Device: id, Desired: req.GetDesired()}); err != nil {
			res.Error = patchErrText(err)
		} else {
			n++
		}
		results = append(results, res)
	}
	return &pb.PatchManyResponse{Updated: n, Results: results}, nil
}

func (s *ShadowServiceServer) Patch(ctx context.Context, req *pb.Shadow) (*pb.Shadow, error) {
	log := s.log.Named("patch")
	log.Debug("Request received", zap.Any("req", req))

	if req.GetDevice() == "" {
		return nil, rpcErr(connect.CodeInvalidArgument, "no device specified")
	}
	if err := ValidateState(req.Reported); err != nil {
		return nil, err
	}
	if err := ValidateState(req.Desired); err != nil {
		return nil, err
	}

	prevReported, prevDesired := s.bumpVersions(ctx, req)
	now := timestamppb.Now()
	topics := []string{}
	if req.Reported != nil {
		req.Reported.Timestamp = now
		topics = append(topics, "mqtt.incoming", "mqtt.incoming/"+req.Device)
	}
	if req.Desired != nil {
		req.Desired.Timestamp = now
		topics = append(topics, "mqtt.outgoing", "mqtt.outgoing/"+req.Device)
	}

	s.stamp(ctx, req)
	s.ps.TryPub(req, topics...)
	s.publishOTA(ctx, req, topics, otaEvents(prevReported, prevDesired, req))
	if req.Reported != nil || req.Desired != nil {
		observe.ShadowWrites.Inc()
	}

	return req, nil
}

func (s *ShadowServiceServer) Remove(ctx context.Context, req *pb.RemoveRequest) (*pb.Shadow, error) {
	log := s.log.Named("remove")
	log.Debug("Request received", zap.Any("req", req))

	if req.GetDevice() == "" {
		return nil, rpcErr(connect.CodeInvalidArgument, "no device specified")
	}
	if req.GetKey() == "" {
		return nil, rpcErr(connect.CodeInvalidArgument, "key not specified")
	}

	skey := Key(req.GetDevice(), req.StateKey)
	r := s.rdb.Get(ctx, skey)
	raw, err := r.Result()
	if err != nil {
		return nil, rpcErr(connect.CodeInternal, "failed to get Shadow")
	}

	var state pb.State
	err = json.Unmarshal([]byte(raw), &state)
	if err != nil {
		log.Warn("Cannot unmarshal state", zap.String("raw", raw), zap.Error(err))
		return nil, rpcErr(connect.CodeInternal, "cannot Unmarshal state")
	}

	fields := state.Data.Fields
	delete(fields, req.GetKey())

	state.Timestamp = timestamppb.Now()
	log.Debug("Result", zap.Any("state", &state))

	s.Store(log, req.Device, req.StateKey, &state)

	result := &pb.Shadow{
		Device: req.GetDevice(),
	}

	if req.StateKey == pb.StateKey_REPORTED {
		result.Reported = &state
	} else {
		result.Desired = &state
	}

	return result, nil
}

func (s *ShadowServiceServer) PublishDeviceScope(ctx context.Context, ev *pb.Shadow) (*emptypb.Empty, error) {
	if ev.GetDevice() == "" {
		return nil, rpcErr(connect.CodeInvalidArgument, "no device specified")
	}
	if ev.GetEvent() == pb.ShadowEvent_STATE {
		return nil, rpcErr(connect.CodeInvalidArgument, "scope event required")
	}
	s.remember(ev)
	s.stamp(ctx, ev)
	s.ps.TryPub(ev, lifecycleTopic)
	return &emptypb.Empty{}, nil
}

func (s *ShadowServiceServer) Accept(sh *pb.Shadow) {
	if sh == nil {
		return
	}
	s.stamp(context.Background(), sh)
}

func (s *ShadowServiceServer) StreamShadow(req *pb.StreamShadowRequest, srv pb.ShadowService_StreamShadowServer) error {
	log := s.log.Named("stream")
	log.Debug("Request received", zap.Any("req", req))
	observe.Streams.Inc()
	defer observe.Streams.Dec()

	scope, meta, devices := scopeFromRequest(req)
	if len(devices) == 0 && !scope.live() {
		return rpcErr(connect.CodeInvalidArgument, "no devices specified")
	}
	for id, item := range meta {
		s.remember(&pb.Shadow{
			Device:    id,
			Title:     item.title,
			Namespace: item.namespace,
			Tags:      item.tags,
			Event:     pb.ShadowEvent_DEVICE_ADDED,
		})
	}

	ctx := srv.Context()
	messages := make(chan interface{}, 256)
	live := newShadowQueue()
	go func() {
		defer live.close()
		for msg := range messages {
			incoming, ok := msg.(*pb.Shadow)
			if ok && incoming != nil {
				live.push(incoming)
			}
		}
	}()
	topics := subscriptionTopics(devices, scope.live())
	s.ps.AddSub(messages, topics...)
	defer unsub(s.ps, messages)

	replayed := map[uint64]struct{}{}
	deliver := func(ev *pb.Shadow, live bool) error {
		if ev == nil {
			return nil
		}
		seq := ev.GetSequence()
		if seq > 0 {
			if _, ok := replayed[seq]; ok {
				return nil
			}
		}
		if err := s.send(ctx, srv, req, ev, meta); err != nil {
			return err
		}
		if !live && seq > 0 {
			replayed[seq] = struct{}{}
		}
		return nil
	}

	if scope.live() {
		ids := make([]string, 0, len(devices))
		for id := range devices {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			item := meta[id]
			ev := &pb.Shadow{
				Device:    id,
				Title:     item.title,
				Namespace: item.namespace,
				Tags:      item.tags,
				Event:     pb.ShadowEvent_DEVICE_ADDED,
			}
			if ev.Namespace == "" {
				ev.Namespace = scope.namespace
			}
			if err := deliver(ev, false); err != nil {
				log.Warn("Unable to send message", zap.Error(err))
				return nil
			}
		}
	}

	if req.GetResumeAfter() > 0 {
		for _, ev := range s.since(req.GetResumeAfter()) {
			if !eventInStream(devices, scope, ev) {
				continue
			}
			if err := deliver(ev, false); err != nil {
				log.Warn("Unable to send message", zap.Error(err))
				return nil
			}
		}
	}

	if req.Sync && len(devices) > 0 {
		pool := make([]string, 0, len(devices))
		for id := range devices {
			pool = append(pool, id)
		}
		slices.Sort(pool)
		r, err := s.Get(ctx, &pb.GetRequest{Pool: pool})
		if err != nil {
			log.Warn("Couldn't get current devices Shadow state", zap.Error(err))
		} else {
			for _, sh := range r.GetShadows() {
				if err := deliver(sh, false); err != nil {
					log.Warn("Unable to send message", zap.Error(err))
				}
			}
		}
	}

	log.Debug("Listening for messages")

	for {
		incoming, ok := live.pop()
		if !ok {
			select {
			case <-ctx.Done():
				return nil
			case <-live.done:
				incoming, ok = live.pop()
				if !ok {
					return nil
				}
			case <-live.wake:
				continue
			}
		}
		if incoming.GetEvent() == pb.ShadowEvent_TAG_ADDED || incoming.GetEvent() == pb.ShadowEvent_TAG_REMOVED {
			if !scope.wants(incoming) && !eventInStream(devices, scope, incoming) {
				continue
			}
			applyTag(meta, incoming)
			s.remember(incoming)
			if err := deliver(incoming, true); err != nil {
				log.Warn("Unable to send message", zap.Error(err))
				return nil
			}
			continue
		}
		if incoming.GetEvent() == pb.ShadowEvent_DEVICE_ADDED || incoming.GetEvent() == pb.ShadowEvent_DEVICE_REMOVED {
			if !scope.wants(incoming) {
				continue
			}
			id := incoming.GetDevice()
			switch incoming.GetEvent() {
			case pb.ShadowEvent_DEVICE_ADDED:
				s.remember(incoming)
				meta[id] = deviceMeta{title: incoming.GetTitle(), namespace: incoming.GetNamespace(), tags: append([]string(nil), incoming.GetTags()...)}
				if _, exists := devices[id]; !exists {
					devices[id] = struct{}{}
					in, out := deviceTopics(id)
					s.ps.AddSub(messages, in, out)
				}
			case pb.ShadowEvent_DEVICE_REMOVED:
				if _, exists := devices[id]; !exists {
					continue
				}
				delete(devices, id)
				delete(meta, id)
				if incoming.GetAccount() == "" {
					s.remember(incoming)
				}
				in, out := deviceTopics(id)
				go s.ps.Unsub(messages, in, out)
			}
			if err := deliver(incoming, true); err != nil {
				log.Warn("Unable to send message", zap.Error(err))
				return nil
			}
			continue
		}
		if _, watching := devices[incoming.GetDevice()]; !watching {
			continue
		}
		if req.GetResumeAfter() > 0 && incoming.GetSequence() > 0 && incoming.GetSequence() <= req.GetResumeAfter() {
			continue
		}
		if err := deliver(incoming, true); err != nil {
			log.Warn("Unable to send message", zap.Error(err))
			return nil
		}
	}
}

func (s *ShadowServiceServer) send(ctx context.Context, srv pb.ShadowService_StreamShadowServer, req *pb.StreamShadowRequest, sh *pb.Shadow, meta map[string]deviceMeta) error {
	out := proto.Clone(sh).(*pb.Shadow)
	if out.GetEvent() == pb.ShadowEvent_STATE && req != nil && !req.GetOnlyDelta() {
		out = s.expand(ctx, out)
	}
	if req != nil {
		applyInclude(out, req.GetInclude())
	}
	s.fill(out, meta)
	if out.EventId == 0 && s.ids != nil {
		out.EventId = s.ids.Next(ctx)
	}
	if out.Sequence == 0 {
		out.Sequence = out.EventId
	}
	return srv.Send(out)
}

const eventLogCap = 256

func (s *ShadowServiceServer) rememberEvent(sh *pb.Shadow) {
	if sh == nil || sh.GetSequence() == 0 {
		return
	}
	s.mu.Lock()
	s.events = append(s.events, proto.Clone(sh).(*pb.Shadow))
	if len(s.events) > eventLogCap {
		s.events = s.events[len(s.events)-eventLogCap:]
	}
	s.mu.Unlock()
}

func (s *ShadowServiceServer) since(seq uint64) []*pb.Shadow {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*pb.Shadow, 0, len(s.events))
	for _, ev := range s.events {
		if ev.GetSequence() > seq {
			out = append(out, proto.Clone(ev).(*pb.Shadow))
		}
	}
	return out
}

func eventInStream(devices map[string]struct{}, scope streamScope, ev *pb.Shadow) bool {
	if ev == nil {
		return false
	}
	if _, ok := devices[ev.GetDevice()]; ok {
		return true
	}
	return scope.live() && scope.wants(ev)
}

func (s *ShadowServiceServer) bumpVersions(ctx context.Context, req *pb.Shadow) (prevReported, prevDesired *pb.State) {
	if req.GetReported() != nil {
		prevReported, req.Reported.Version = s.previousState(ctx, req.GetDevice(), pb.StateKey_REPORTED)
	}
	if req.GetDesired() != nil {
		prevDesired, req.Desired.Version = s.previousState(ctx, req.GetDevice(), pb.StateKey_DESIRED)
	}
	return prevReported, prevDesired
}

func (s *ShadowServiceServer) previousState(ctx context.Context, device string, key pb.StateKey) (*pb.State, uint64) {
	if s.rdb == nil {
		return nil, 1
	}
	raw, err := s.rdb.Get(ctx, Key(device, key)).Result()
	if err != nil {
		return nil, 1
	}
	var state pb.State
	if json.Unmarshal([]byte(raw), &state) != nil {
		return nil, 1
	}
	return &state, state.GetVersion() + 1
}

func (s *ShadowServiceServer) expand(ctx context.Context, patch *pb.Shadow) *pb.Shadow {
	base := &pb.Shadow{Device: patch.GetDevice()}
	loaded, err := s.Get(ctx, &pb.GetRequest{Pool: []string{patch.GetDevice()}})
	if err == nil && len(loaded.GetShadows()) > 0 && loaded.Shadows[0] != nil {
		base = loaded.Shadows[0]
	}
	if patch.Reported != nil {
		base.Reported = mergeState(base.Reported, patch.Reported)
	}
	if patch.Desired != nil {
		base.Desired = mergeState(base.Desired, patch.Desired)
	}
	if patch.Connection != nil {
		base.Connection = patch.Connection
	}
	base.Device = patch.GetDevice()
	base.Event = patch.GetEvent()
	base.EventId = patch.GetEventId()
	base.Account = patch.GetAccount()
	if patch.Title != "" {
		base.Title = patch.Title
	}
	if patch.Namespace != "" {
		base.Namespace = patch.Namespace
	}
	if len(patch.Tags) > 0 {
		base.Tags = append([]string(nil), patch.GetTags()...)
	}
	return base
}

func mergeState(stored, patch *pb.State) *pb.State {
	if stored == nil || stored.Data == nil {
		return patch
	}
	if patch == nil || patch.Data == nil {
		return stored
	}
	old, err := json.Marshal(stored)
	if err != nil {
		return patch
	}
	next, err := json.Marshal(patch)
	if err != nil {
		return patch
	}
	merged, err := MergeJSON(old, next)
	if err != nil {
		return patch
	}
	var out pb.State
	if json.Unmarshal(merged, &out) != nil {
		return patch
	}
	return &out
}

func applyInclude(sh *pb.Shadow, include []pb.StateKey) {
	if sh == nil || sh.GetEvent() != pb.ShadowEvent_STATE || len(include) == 0 {
		return
	}
	var reported, desired, connection, known bool
	for _, key := range include {
		switch key {
		case pb.StateKey_REPORTED:
			reported, known = true, true
		case pb.StateKey_DESIRED:
			desired, known = true, true
		case pb.StateKey_CONNECTION:
			connection, known = true, true
		}
	}
	if !known {
		return
	}
	if !reported {
		sh.Reported = nil
	}
	if !desired {
		sh.Desired = nil
	}
	if !connection {
		sh.Connection = nil
	}
}

type shadowQueue struct {
	mu   sync.Mutex
	buf  []*pb.Shadow
	wake chan struct{}
	done chan struct{}
}

const shadowQueueCap = 1024

func newShadowQueue() *shadowQueue {
	return &shadowQueue{wake: make(chan struct{}, 1), done: make(chan struct{})}
}

func (q *shadowQueue) push(sh *pb.Shadow) {
	q.mu.Lock()
	if len(q.buf) >= shadowQueueCap {
		q.mu.Unlock()
		return
	}
	q.buf = append(q.buf, sh)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *shadowQueue) pop() (*pb.Shadow, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.buf) == 0 {
		return nil, false
	}
	sh := q.buf[0]
	q.buf[0] = nil
	q.buf = q.buf[1:]
	if len(q.buf) == 0 {
		q.buf = nil
	}
	return sh, true
}

func (q *shadowQueue) close() { close(q.done) }

func unsub[T chan any](ps pubsub.PubSub, ch chan any) {
	go ps.Unsub(ch)

	for range ch {
	}
}
