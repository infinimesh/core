package main

import (
	"context"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/infinimesh/core/pkg/pubsub"
	"github.com/infinimesh/core/pkg/rules"
	"github.com/infinimesh/core/pkg/shadow"
	pb "github.com/infinimesh/proto/shadow"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestIngestThenRuleRaisesAcksAndClears(t *testing.T) {
	if watchEvery >= 15*time.Second {
		t.Fatal("a new rule waits on the namespace poll")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, client, _ := startShadow(t, ctx)

	patchTemp(t, ctx, client, "pump", 80)
	waitTemp(t, ctx, client, "pump", 80)

	mem := &memSource{devs: []*pb.DeviceMeta{{
		Device: "pump", Title: "Pump", Namespace: "plant", Tags: []string{"water"},
	}}}
	cap := &capClient{ShadowServiceClient: client}
	go serve(ctx, zap.NewNop(), mem, cap, rules.NewActions())

	marked := time.Now()
	mem.setRules(&rules.Rule{Name: "hot", When: "reported.temp > 70"})
	raised := waitAlarm(t, mem, "pump", rules.Raised)
	if time.Since(marked) >= 2*time.Second {
		t.Fatal("rule was not checked against the stored reading")
	}
	if raised.Count != 1 {
		t.Fatalf("count %d", raised.Count)
	}
	if !cap.synced("plant", "pump") {
		t.Fatal("stream did not watch the device")
	}

	mem.setState("hot", "pump", rules.Acked)
	if a, ok := mem.alarm("hot", "pump"); !ok || a.State != rules.Acked {
		t.Fatal("ack was not stored")
	}
	time.Sleep(2 * watchEvery)
	if a, ok := mem.alarm("hot", "pump"); !ok || a.State != rules.Acked {
		t.Fatalf("ack lost: %+v", a)
	}

	patchTemp(t, ctx, client, "pump", 20)
	cleared := waitAlarm(t, mem, "pump", rules.Cleared)
	if cleared.AckedAt.IsZero() || cleared.ClearedAt.IsZero() {
		t.Fatalf("times %+v", cleared)
	}
}

func TestAddedDeviceIsCheckedAgainstStoredState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, client, svc := startShadow(t, ctx)

	mem := &memSource{
		rules: []*rules.Rule{{Name: "hot", When: "reported.temp > 70"}},
		devs: []*pb.DeviceMeta{{
			Device: "idle", Title: "Idle", Namespace: "plant",
		}},
	}
	cap := &capClient{ShadowServiceClient: client}
	go serve(ctx, zap.NewNop(), mem, cap, rules.NewActions())
	deadline := time.Now().Add(2 * time.Second)
	for !cap.synced("plant", "idle") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !cap.synced("plant", "idle") {
		t.Fatal("stream did not start")
	}

	patchTemp(t, ctx, client, "hot", 90)
	waitTemp(t, ctx, client, "hot", 90)
	if _, err := svc.PublishDeviceScope(ctx, &pb.Shadow{
		Device: "hot", Namespace: "plant", Title: "Hot", Event: pb.ShadowEvent_DEVICE_ADDED,
	}); err != nil {
		t.Fatal(err)
	}
	waitAlarm(t, mem, "hot", rules.Raised)
}

func startShadow(t *testing.T, ctx context.Context) (*redis.Client, pb.ShadowServiceClient, *shadow.ShadowServiceServer) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ps := pubsub.New(16)
	svc := shadow.NewShadowServiceServer(zap.NewNop(), rdb, ps)
	go svc.Persister()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	pb.RegisterShadowServiceServer(gs, svc)
	go gs.Serve(lis)
	t.Cleanup(func() {
		gs.Stop()
		ps.Shutdown()
	})
	conn, err := grpc.DialContext(ctx, "bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return rdb, pb.NewShadowServiceClient(conn), svc
}

func patchTemp(t *testing.T, ctx context.Context, client pb.ShadowServiceClient, device string, temp float64) {
	t.Helper()
	st, err := structpb.NewStruct(map[string]any{"temp": temp})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Patch(ctx, &pb.Shadow{Device: device, Reported: &pb.State{Data: st}}); err != nil {
		t.Fatal(err)
	}
}

func waitTemp(t *testing.T, ctx context.Context, client pb.ShadowServiceClient, device string, temp float64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		res, err := client.Get(ctx, &pb.GetRequest{Device: device})
		if err == nil {
			for _, sh := range res.GetShadows() {
				if sh.GetDevice() == device && sh.GetReported().GetData().AsMap()["temp"] == temp {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("stored temp for %s was not %v", device, temp)
}

func waitAlarm(t *testing.T, src *memSource, device, state string) rules.Alarm {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if a, ok := src.alarm("hot", device); ok && a.State == state {
			return a
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("alarm for %s did not become %s", device, state)
	return rules.Alarm{}
}

type capClient struct {
	pb.ShadowServiceClient
	mu   sync.Mutex
	reqs []*pb.StreamShadowRequest
}

func (c *capClient) StreamShadow(ctx context.Context, in *pb.StreamShadowRequest, opts ...grpc.CallOption) (pb.ShadowService_StreamShadowClient, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, proto.Clone(in).(*pb.StreamShadowRequest))
	c.mu.Unlock()
	return c.ShadowServiceClient.StreamShadow(ctx, in, opts...)
}

func (c *capClient) synced(ns, device string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, req := range c.reqs {
		if req.GetNamespace() == ns && req.GetSync() && slices.Contains(req.GetDevices(), device) {
			return true
		}
	}
	return false
}

type memSource struct {
	mu     sync.Mutex
	rules  []*rules.Rule
	devs   []*pb.DeviceMeta
	alarms []*rules.Alarm
}

func TestAlarmsForRulesDropsNameKey(t *testing.T) {
	set := []*rules.Rule{{ID: "id-a", Name: "hot"}, {ID: "id-b", Name: "hot"}}
	kept := alarmsForRules(set, []*rules.Alarm{
		{Rule: "hot", Device: "d1", State: rules.Raised},
		{Rule: "id-a", Device: "d1", State: rules.Acked},
		{Rule: "id-b", Device: "d2", State: rules.Raised},
	})
	if len(kept) != 2 || kept[0].Rule != "id-a" || kept[1].Rule != "id-b" {
		t.Fatalf("%+v", kept)
	}
}

func (m *memSource) setRules(rs ...*rules.Rule) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rules = append([]*rules.Rule(nil), rs...)
}

func (m *memSource) setState(rule, device, state string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for _, a := range m.alarms {
		if a.Rule == rule && a.Device == device {
			a.State = state
			if state == rules.Acked {
				a.AckedAt = now
			}
		}
	}
}

func (m *memSource) alarm(rule, device string) (rules.Alarm, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.alarms {
		if a.Rule == rule && a.Device == device {
			return *a, true
		}
	}
	return rules.Alarm{}, false
}

func (m *memSource) Namespaces(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.rules) == 0 {
		return nil, nil
	}
	return []string{"plant"}, nil
}

func (m *memSource) Rules(context.Context, string) ([]*rules.Rule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*rules.Rule, len(m.rules))
	copy(out, m.rules)
	return out, nil
}

func (m *memSource) Devices(context.Context, string) ([]*pb.DeviceMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*pb.DeviceMeta, len(m.devs))
	for i, d := range m.devs {
		out[i] = proto.Clone(d).(*pb.DeviceMeta)
	}
	return out, nil
}

func (m *memSource) LoadAlarms(context.Context, string) ([]*rules.Alarm, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*rules.Alarm, len(m.alarms))
	for i, a := range m.alarms {
		c := *a
		out[i] = &c
	}
	return out, nil
}

func (m *memSource) SaveAlarms(_ context.Context, _ string, list []*rules.Alarm) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alarms = nil
	for _, a := range list {
		c := *a
		m.alarms = append(m.alarms, &c)
	}
	return nil
}
