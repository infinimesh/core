package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/arangodb/go-driver"
	"github.com/go-redis/redis/v8"
	"github.com/infinimesh/core/pkg/graph"
	"github.com/infinimesh/core/pkg/rules"
	pb "github.com/infinimesh/proto/shadow"
	"go.uber.org/zap"
)

// A new rule is checked against stored state. This is how often that check runs.
const watchEvery = 250 * time.Millisecond

type namespaceSource interface {
	Namespaces(ctx context.Context) ([]string, error)
	Rules(ctx context.Context, ns string) ([]*rules.Rule, error)
	Devices(ctx context.Context, ns string) ([]*pb.DeviceMeta, error)
	LoadAlarms(ctx context.Context, ns string) ([]*rules.Alarm, error)
	SaveAlarms(ctx context.Context, ns string, list []*rules.Alarm) error
}

type dbSource struct {
	db  driver.Database
	rdb *redis.Client
}

func (s dbSource) Namespaces(ctx context.Context) ([]string, error) {
	return namespaces(ctx, s.db)
}

func (s dbSource) Rules(ctx context.Context, ns string) ([]*rules.Rule, error) {
	return loadRules(ctx, s.db, ns)
}

func (s dbSource) Devices(ctx context.Context, ns string) ([]*pb.DeviceMeta, error) {
	rows, err := graph.ListNamespaceDevices(ctx, s.db, ns)
	if err != nil {
		return nil, err
	}
	out := make([]*pb.DeviceMeta, 0, len(rows))
	for _, row := range rows {
		out = append(out, &pb.DeviceMeta{
			Device: row.Device, Title: row.Title, Namespace: ns, Tags: append([]string(nil), row.Tags...),
		})
	}
	return out, nil
}

func (s dbSource) LoadAlarms(ctx context.Context, ns string) ([]*rules.Alarm, error) {
	return loadAlarms(ctx, s.db, ns)
}

func (s dbSource) SaveAlarms(ctx context.Context, ns string, list []*rules.Alarm) error {
	return saveAlarms(ctx, s.db, s.rdb, ns, list)
}

func serve(ctx context.Context, log *zap.Logger, src namespaceSource, client pb.ShadowServiceClient, actions *rules.Actions) {
	type running struct {
		sig    string
		cancel context.CancelFunc
	}
	active := map[string]running{}
	defer func() {
		for _, cur := range active {
			cur.cancel()
		}
	}()
	for ctx.Err() == nil {
		names, err := src.Namespaces(ctx)
		if err != nil {
			log.Warn("rules", zap.Error(err))
			if !sleep(ctx, watchEvery) {
				return
			}
			continue
		}
		seen := map[string]bool{}
		for _, ns := range names {
			if ns == "" {
				continue
			}
			seen[ns] = true
			set, err := src.Rules(ctx, ns)
			if err != nil {
				log.Warn("load rules", zap.String("namespace", ns), zap.Error(err))
				continue
			}
			devs, err := src.Devices(ctx, ns)
			if err != nil {
				log.Warn("load devices", zap.String("namespace", ns), zap.Error(err))
				continue
			}
			sig := fingerprint(set, devs)
			if cur, ok := active[ns]; ok && cur.sig == sig {
				continue
			}
			if cur, ok := active[ns]; ok {
				cur.cancel()
			}
			sub, cancel := context.WithCancel(ctx)
			active[ns] = running{sig: sig, cancel: cancel}
			go follow(sub, log, src, client, actions, ns)
		}
		for ns, cur := range active {
			if !seen[ns] {
				cur.cancel()
				delete(active, ns)
			}
		}
		if !sleep(ctx, watchEvery) {
			return
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func fingerprint(set []*rules.Rule, devs []*pb.DeviceMeta) string {
	var b strings.Builder
	rs := append([]*rules.Rule(nil), set...)
	sort.Slice(rs, func(i, j int) bool {
		if rs[i] == nil || rs[j] == nil {
			return rs[i] != nil
		}
		return rs[i].Name < rs[j].Name
	})
	for _, r := range rs {
		if r == nil {
			continue
		}
		fmt.Fprintf(&b, "r\t%s\t%s\t%s\t%s\t%s\t%t\n", r.Name, r.When, r.Clear, time.Duration(r.Cooldown), time.Duration(r.Repeat), r.OnClear)
	}
	ds := append([]*pb.DeviceMeta(nil), devs...)
	sort.Slice(ds, func(i, j int) bool { return ds[i].GetDevice() < ds[j].GetDevice() })
	for _, d := range ds {
		tags := append([]string(nil), d.GetTags()...)
		sort.Strings(tags)
		fmt.Fprintf(&b, "d\t%s\t%s\t%s\n", d.GetDevice(), d.GetTitle(), strings.Join(tags, ","))
	}
	return b.String()
}

func streamRequest(ns string, devs []*pb.DeviceMeta) *pb.StreamShadowRequest {
	req := &pb.StreamShadowRequest{Namespace: ns, Sync: len(devs) > 0}
	for _, d := range devs {
		if d == nil || d.GetDevice() == "" {
			continue
		}
		item := &pb.DeviceMeta{
			Device: d.GetDevice(), Title: d.GetTitle(), Namespace: d.GetNamespace(), Tags: append([]string(nil), d.GetTags()...),
		}
		if item.Namespace == "" {
			item.Namespace = ns
		}
		req.Devices = append(req.Devices, item.Device)
		req.Catalog = append(req.Catalog, item)
	}
	return req
}

func follow(ctx context.Context, log *zap.Logger, src namespaceSource, client pb.ShadowServiceClient, actions *rules.Actions, ns string) {
	store, _ := rules.OpenStore("")
	for ctx.Err() == nil {
		devs, err := src.Devices(ctx, ns)
		if err != nil {
			log.Warn("load devices", zap.String("namespace", ns), zap.Error(err))
			if !sleep(ctx, 2*time.Second) {
				return
			}
			continue
		}
		req := streamRequest(ns, devs)
		known := map[string]struct{}{}
		for _, id := range req.GetDevices() {
			known[id] = struct{}{}
		}
		stream, err := client.StreamShadow(ctx, req)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warn("stream", zap.String("namespace", ns), zap.Error(err))
			if !sleep(ctx, 2*time.Second) {
				return
			}
			continue
		}
		for {
			sh, err := stream.Recv()
			if err != nil {
				if ctx.Err() == nil {
					log.Warn("stream closed", zap.String("namespace", ns), zap.Error(err))
				}
				break
			}
			switch sh.GetEvent() {
			case pb.ShadowEvent_DEVICE_ADDED:
				if _, seen := known[sh.GetDevice()]; seen {
					continue
				}
				known[sh.GetDevice()] = struct{}{}
				applyCurrent(ctx, log, src, client, actions, store, ns, sh)
			case pb.ShadowEvent_TAG_ADDED, pb.ShadowEvent_TAG_REMOVED:
				applyCurrent(ctx, log, src, client, actions, store, ns, sh)
			case pb.ShadowEvent_DEVICE_REMOVED:
				delete(known, sh.GetDevice())
			default:
				applyShadow(ctx, log, src, actions, store, ns, sh)
			}
		}
	}
}

func applyCurrent(ctx context.Context, log *zap.Logger, src namespaceSource, client pb.ShadowServiceClient, actions *rules.Actions, store *rules.Store, ns string, ev *pb.Shadow) {
	if ev.GetDevice() == "" {
		return
	}
	res, err := client.Get(ctx, &pb.GetRequest{Devices: []string{ev.GetDevice()}})
	if err != nil {
		log.Warn("current state", zap.String("device", ev.GetDevice()), zap.Error(err))
		return
	}
	for _, cur := range res.GetShadows() {
		if cur.GetDevice() != ev.GetDevice() {
			continue
		}
		if cur.GetNamespace() == "" {
			cur.Namespace = ev.GetNamespace()
		}
		if cur.GetNamespace() == "" {
			cur.Namespace = ns
		}
		if cur.GetTitle() == "" {
			cur.Title = ev.GetTitle()
		}
		if ev.GetEvent() == pb.ShadowEvent_TAG_ADDED || ev.GetEvent() == pb.ShadowEvent_TAG_REMOVED || len(cur.GetTags()) == 0 {
			cur.Tags = append([]string(nil), ev.GetTags()...)
		}
		applyShadow(ctx, log, src, actions, store, ns, cur)
	}
}

func applyShadow(ctx context.Context, log *zap.Logger, src namespaceSource, actions *rules.Actions, store *rules.Store, ns string, sh *pb.Shadow) {
	if sh.GetDevice() == "" || sh.GetEvent() != pb.ShadowEvent_STATE {
		return
	}
	set, err := src.Rules(ctx, ns)
	if err != nil {
		log.Warn("load rules", zap.Error(err))
		return
	}
	if len(set) == 0 {
		return
	}
	current, err := src.LoadAlarms(ctx, ns)
	if err != nil {
		log.Warn("load alarms", zap.Error(err))
		return
	}
	store.Replace(current)
	eng, err := rules.NewEngine(set, store, actions)
	if err != nil {
		log.Warn("engine", zap.Error(err))
		return
	}
	rules.Apply(eng, sh)
	if err := src.SaveAlarms(ctx, ns, store.List()); err != nil {
		log.Warn("save alarms", zap.Error(err))
	}
}
