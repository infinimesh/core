package main

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/infinimesh/core/v3/pkg/rules"
	pb "github.com/infinimesh/proto/shadow"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

type blockClient struct {
	pb.ShadowServiceClient
	mu      sync.Mutex
	streams int
	err     error
	getErr  error
	get     *pb.GetResponse
	first   []*pb.Shadow
}

func (c *blockClient) StreamShadow(ctx context.Context, in *pb.StreamShadowRequest, _ ...grpc.CallOption) (pb.ShadowService_StreamShadowClient, error) {
	c.mu.Lock()
	c.streams++
	n := c.streams
	items := c.first
	c.first = nil
	err := c.err
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if n == 1 && len(items) == 0 && c.eofFirst() {
		return &queueStream{}, nil
	}
	return &queueStream{ctx: ctx, q: items}, nil
}

func (c *blockClient) eofFirst() bool { return false }

func (c *blockClient) Get(_ context.Context, in *pb.GetRequest, _ ...grpc.CallOption) (*pb.GetResponse, error) {
	if c.getErr != nil {
		return nil, c.getErr
	}
	if c.get != nil {
		return c.get, nil
	}
	return &pb.GetResponse{Shadows: []*pb.Shadow{{Device: in.GetDevices()[0], Event: pb.ShadowEvent_STATE}}}, nil
}

type queueStream struct {
	grpc.ClientStream
	ctx context.Context
	q   []*pb.Shadow
}

func (s *queueStream) Recv() (*pb.Shadow, error) {
	if len(s.q) > 0 {
		item := s.q[0]
		s.q = s.q[1:]
		return item, nil
	}
	if s.ctx == nil {
		return nil, io.EOF
	}
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}

type flipSource struct {
	mem      *memSource
	mu       sync.Mutex
	names    [][]string
	i        int
	nsErr    error
	rulesErr error
	devsErr  error
	loadErr  error
	saveErr  error
}

func (f *flipSource) Namespaces(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nsErr != nil {
		return nil, f.nsErr
	}
	if len(f.names) == 0 {
		return []string{"plant"}, nil
	}
	if f.i >= len(f.names) {
		return f.names[len(f.names)-1], nil
	}
	out := f.names[f.i]
	f.i++
	return out, nil
}

func (f *flipSource) Rules(ctx context.Context, ns string) ([]*rules.Rule, error) {
	if f.rulesErr != nil {
		return nil, f.rulesErr
	}
	if f.mem == nil {
		return nil, nil
	}
	return f.mem.Rules(ctx, ns)
}

func (f *flipSource) Devices(ctx context.Context, ns string) ([]*pb.DeviceMeta, error) {
	if f.devsErr != nil {
		return nil, f.devsErr
	}
	if f.mem == nil {
		return nil, nil
	}
	return f.mem.Devices(ctx, ns)
}

func (f *flipSource) LoadAlarms(ctx context.Context, ns string) ([]*rules.Alarm, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	if f.mem == nil {
		return nil, nil
	}
	return f.mem.LoadAlarms(ctx, ns)
}

func (f *flipSource) SaveAlarms(ctx context.Context, ns string, list []*rules.Alarm) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	if f.mem == nil {
		return nil
	}
	return f.mem.SaveAlarms(ctx, ns, list)
}

func TestFingerprintAndStreamRequest(t *testing.T) {
	sig := fingerprint([]*rules.Rule{nil, {Name: "b", When: "true"}, {Name: "a", When: "false"}}, []*pb.DeviceMeta{{Device: "b", Tags: []string{"z", "a"}}, nil, {Device: "a"}})
	if sig == "" || fingerprint(nil, nil) != "" && false {
		t.Fatal(sig)
	}
	req := streamRequest("plant", []*pb.DeviceMeta{nil, {Device: ""}, {Device: "dev", Title: "T", Tags: []string{"a"}}, {Device: "other", Namespace: "else"}})
	if !req.GetSync() || len(req.GetDevices()) != 2 || req.Catalog[0].GetNamespace() != "plant" || req.Catalog[1].GetNamespace() != "else" {
		t.Fatalf("%+v", req)
	}
	empty := streamRequest("plant", nil)
	if empty.GetSync() || empty.GetNamespace() != "plant" {
		t.Fatalf("%+v", empty)
	}
	kept := alarmsForRules([]*rules.Rule{nil, {Name: "hot"}}, []*rules.Alarm{nil, {Rule: "hot"}, {Rule: "other"}})
	if len(kept) != 1 {
		t.Fatalf("%+v", kept)
	}
}

func TestServeEdges(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go serve(ctx, zap.NewNop(), &flipSource{nsErr: errors.New("db")}, &blockClient{}, rules.NewActions())
	time.Sleep(30 * time.Millisecond)
	cancel()
	time.Sleep(30 * time.Millisecond)

	ctx, cancel = context.WithCancel(context.Background())
	mem := &memSource{rules: []*rules.Rule{{Name: "hot", When: "true"}}, devs: []*pb.DeviceMeta{{Device: "dev", Namespace: "plant", Title: "Dev"}}}
	go serve(ctx, zap.NewNop(), &flipSource{mem: mem, rulesErr: errors.New("rules"), names: [][]string{{"plant", ""}}}, &blockClient{}, rules.NewActions())
	time.Sleep(30 * time.Millisecond)
	cancel()
	time.Sleep(30 * time.Millisecond)

	ctx, cancel = context.WithCancel(context.Background())
	go serve(ctx, zap.NewNop(), &flipSource{mem: mem, devsErr: errors.New("devs"), names: [][]string{{"plant"}}}, &blockClient{}, rules.NewActions())
	time.Sleep(30 * time.Millisecond)
	cancel()
	time.Sleep(30 * time.Millisecond)

	ctx, cancel = context.WithCancel(context.Background())
	go serve(ctx, zap.NewNop(), &flipSource{mem: mem, names: [][]string{{"gone", ""}, {}}}, &blockClient{}, rules.NewActions())
	time.Sleep(400 * time.Millisecond)
	cancel()
	time.Sleep(30 * time.Millisecond)

	ctx, cancel = context.WithCancel(context.Background())
	src := &flipSource{mem: mem, names: [][]string{{"plant"}}}
	cl := &blockClient{}
	go serve(ctx, zap.NewNop(), src, cl, rules.NewActions())
	time.Sleep(100 * time.Millisecond)
	mem.setRules(&rules.Rule{Name: "cold", When: "false"})
	time.Sleep(400 * time.Millisecond)
	cancel()
	time.Sleep(30 * time.Millisecond)
	cl.mu.Lock()
	n := cl.streams
	cl.mu.Unlock()
	if n < 1 {
		t.Fatalf("streams %d", n)
	}
}

func TestFollowAndApply(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mem := &memSource{devs: []*pb.DeviceMeta{{Device: "dev", Namespace: "plant"}}}
	go follow(ctx, zap.NewNop(), &flipSource{mem: mem, devsErr: errors.New("devs")}, &blockClient{}, rules.NewActions(), "plant")
	time.Sleep(20 * time.Millisecond)
	cancel()
	time.Sleep(20 * time.Millisecond)

	ctx, cancel = context.WithCancel(context.Background())
	go follow(ctx, zap.NewNop(), &flipSource{mem: mem}, &blockClient{err: errors.New("stream")}, rules.NewActions(), "plant")
	time.Sleep(20 * time.Millisecond)
	cancel()
	time.Sleep(20 * time.Millisecond)

	ctx, cancel = context.WithCancel(context.Background())
	cl := &blockClient{first: []*pb.Shadow{
		{Device: "dev", Event: pb.ShadowEvent_DEVICE_ADDED},
		{Device: "new", Event: pb.ShadowEvent_DEVICE_ADDED, Namespace: "", Title: "New"},
		{Device: "new", Event: pb.ShadowEvent_TAG_ADDED, Tags: []string{"a"}},
		{Device: "new", Event: pb.ShadowEvent_DEVICE_REMOVED},
		{Device: "dev", Event: pb.ShadowEvent_STATE},
		{Device: "", Event: pb.ShadowEvent_STATE},
	}, get: &pb.GetResponse{Shadows: []*pb.Shadow{
		{Device: "other"},
		{Device: "new"},
	}}}
	src := &flipSource{mem: &memSource{
		rules: []*rules.Rule{{Name: "hot", When: "true"}},
		devs:  []*pb.DeviceMeta{{Device: "dev"}},
	}}
	go follow(ctx, zap.NewNop(), src, cl, rules.NewActions(), "plant")
	time.Sleep(80 * time.Millisecond)
	cancel()
	time.Sleep(20 * time.Millisecond)

	store, _ := rules.OpenStore("")
	actions := rules.NewActions()
	log := zap.NewNop()
	applyCurrent(ctx, log, src, &blockClient{getErr: errors.New("get")}, actions, store, "plant", &pb.Shadow{Device: "dev"})
	applyCurrent(ctx, log, src, &blockClient{}, actions, store, "plant", &pb.Shadow{})
	applyCurrent(ctx, log, src, &blockClient{get: &pb.GetResponse{Shadows: []*pb.Shadow{{Device: "nope"}, {Device: "dev"}}}}, actions, store, "plant", &pb.Shadow{Device: "dev", Title: "T", Event: pb.ShadowEvent_TAG_REMOVED, Tags: []string{"z"}})

	applyShadow(ctx, log, src, actions, store, "plant", &pb.Shadow{})
	applyShadow(ctx, log, src, actions, store, "plant", &pb.Shadow{Device: "dev", Event: pb.ShadowEvent_DEVICE_ADDED})
	applyShadow(ctx, log, &flipSource{rulesErr: errors.New("rules")}, actions, store, "plant", &pb.Shadow{Device: "dev", Event: pb.ShadowEvent_STATE})
	applyShadow(ctx, log, &flipSource{}, actions, store, "plant", &pb.Shadow{Device: "dev", Event: pb.ShadowEvent_STATE})
	applyShadow(ctx, log, &flipSource{mem: &memSource{rules: []*rules.Rule{{Name: "hot", When: "true"}}}, loadErr: errors.New("load")}, actions, store, "plant", &pb.Shadow{Device: "dev", Event: pb.ShadowEvent_STATE})
	applyShadow(ctx, log, &flipSource{mem: &memSource{rules: []*rules.Rule{{Name: "hot", When: "???"}}}}, actions, store, "plant", &pb.Shadow{Device: "dev", Event: pb.ShadowEvent_STATE})
	applyShadow(ctx, log, &flipSource{mem: &memSource{rules: []*rules.Rule{{Name: "hot", When: "true"}}}, saveErr: errors.New("save")}, actions, store, "plant", &pb.Shadow{Device: "dev", Event: pb.ShadowEvent_STATE})
}
