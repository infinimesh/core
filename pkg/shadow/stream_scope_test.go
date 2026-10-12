package shadow_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/infinimesh/core/pkg/pubsub"
	"github.com/infinimesh/core/pkg/shadow"
	pb "github.com/infinimesh/proto/shadow"
	"go.uber.org/zap"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

type recStream struct {
	ctx  context.Context
	ch   chan *pb.Shadow
	stop chan struct{}
}

func (r *recStream) Send(sh *pb.Shadow) error {
	select {
	case <-r.stop:
		return errors.New("stop")
	default:
	}
	select {
	case <-r.stop:
		return errors.New("stop")
	case r.ch <- proto.Clone(sh).(*pb.Shadow):
		return nil
	}
}

func (r *recStream) Context() context.Context     { return r.ctx }
func (r *recStream) SetHeader(metadata.MD) error  { return nil }
func (r *recStream) SendHeader(metadata.MD) error { return nil }
func (r *recStream) SetTrailer(metadata.MD)       {}
func (r *recStream) SendMsg(m interface{}) error  { return r.Send(m.(*pb.Shadow)) }
func (r *recStream) RecvMsg(interface{}) error    { return nil }

func waitShadow(t *testing.T, ch chan *pb.Shadow, pred func(*pb.Shadow) bool) *pb.Shadow {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			t.Fatal("timed out waiting for a shadow")
			return nil
		case sh := <-ch:
			if pred(sh) {
				return sh
			}
		}
	}
}

func TestStreamShadow_NamespaceFeed(t *testing.T) {
	ps := pubsub.New(16)
	svc := shadow.NewShadowServiceServer(zap.NewNop(), nil, ps)
	st := &recStream{
		ctx:  context.Background(),
		ch:   make(chan *pb.Shadow, 32),
		stop: make(chan struct{}),
	}
	done := make(chan struct{})
	go func() {
		_ = svc.StreamShadow(&pb.StreamShadowRequest{
			Namespace:  "plant",
			Namespaces: []string{"plant"},
			Account:    "acc",
			OnlyDelta:  true,
			Catalog: []*pb.DeviceMeta{{
				Device: "dev-a", Title: "Pump", Namespace: "plant", Tags: []string{"water"},
			}},
		}, st)
		close(done)
	}()

	added := waitShadow(t, st.ch, func(sh *pb.Shadow) bool {
		return sh.GetDevice() == "dev-a" && sh.GetEvent() == pb.ShadowEvent_DEVICE_ADDED
	})
	if added.GetEventId() == 0 || added.GetTitle() != "Pump" || added.GetNamespace() != "plant" || len(added.GetTags()) != 1 {
		t.Fatalf("initial %+v", added)
	}

	deadline := time.Now().Add(2 * time.Second)
	var ready *pb.Shadow
	for ready == nil && time.Now().Before(deadline) {
		ps.TryPub(&pb.Shadow{Device: "dev-a", Reported: &pb.State{}}, "mqtt.incoming/dev-a")
		select {
		case sh := <-st.ch:
			if sh.GetDevice() == "dev-a" && sh.GetEvent() == pb.ShadowEvent_STATE && sh.Reported != nil {
				ready = sh
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
	if ready == nil || ready.GetTitle() != "Pump" || ready.GetNamespace() != "plant" || ready.GetEventId() == 0 {
		t.Fatalf("state was not decorated: %+v", ready)
	}

	ps.TryPub(&pb.Shadow{Device: "dev-a", EventId: 99, Reported: &pb.State{}}, "mqtt.incoming")
	ps.TryPub(&pb.Shadow{Device: "dev-b", Reported: &pb.State{}}, "mqtt.incoming/dev-b")
	ps.TryPub(&pb.Shadow{
		Device: "dev-a", EventId: 42, Title: "custom", Reported: &pb.State{},
	}, "mqtt.incoming/dev-a")
	kept := waitShadow(t, st.ch, func(sh *pb.Shadow) bool { return sh.GetEventId() == 42 })
	if kept.GetTitle() != "custom" {
		t.Fatalf("publisher title %q", kept.GetTitle())
	}
	select {
	case sh := <-st.ch:
		if sh.GetEventId() == 99 || sh.GetDevice() == "dev-b" {
			t.Fatalf("delivered a shadow outside the subscription: %+v", sh)
		}
		st.ch <- sh
	case <-time.After(50 * time.Millisecond):
	}

	ps.TryPub(&pb.Shadow{
		Device: "dev-c", Namespace: "other", Title: "Other", Event: pb.ShadowEvent_DEVICE_ADDED,
	}, "devices.lifecycle")
	ps.TryPub(&pb.Shadow{
		Device: "dev-b", Namespace: "plant", Title: "Valve", Tags: []string{"steam"}, Event: pb.ShadowEvent_DEVICE_ADDED,
	}, "devices.lifecycle")
	bAdded := waitShadow(t, st.ch, func(sh *pb.Shadow) bool {
		return sh.GetDevice() == "dev-b" && sh.GetEvent() == pb.ShadowEvent_DEVICE_ADDED
	})
	if bAdded.GetTitle() != "Valve" {
		t.Fatalf("added %+v", bAdded)
	}

	deadline = time.Now().Add(2 * time.Second)
	var bState *pb.Shadow
	for bState == nil && time.Now().Before(deadline) {
		ps.TryPub(&pb.Shadow{Device: "dev-c", Reported: &pb.State{}}, "mqtt.incoming/dev-c")
		ps.TryPub(&pb.Shadow{
			Device: "dev-b", Connection: &pb.ConnectionState{Connected: true},
		}, "mqtt.incoming/dev-b")
		select {
		case sh := <-st.ch:
			if sh.GetDevice() == "dev-c" {
				t.Fatalf("other namespace device was delivered: %+v", sh)
			}
			if sh.GetDevice() == "dev-b" && sh.GetConnection().GetConnected() {
				bState = sh
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
	if bState == nil || bState.GetTitle() != "Valve" || bState.GetNamespace() != "plant" || len(bState.GetTags()) != 1 {
		t.Fatalf("connection state %+v", bState)
	}

	ps.TryPub(&pb.Shadow{
		Device: "dev-a", Namespace: "plant", Title: "Pump 2", Tags: []string{"water", "hot"}, Event: pb.ShadowEvent_DEVICE_ADDED,
	}, "devices.lifecycle")
	refreshed := waitShadow(t, st.ch, func(sh *pb.Shadow) bool {
		return sh.GetDevice() == "dev-a" && sh.GetEvent() == pb.ShadowEvent_DEVICE_ADDED && sh.GetTitle() == "Pump 2"
	})
	if len(refreshed.GetTags()) != 2 {
		t.Fatalf("refresh %+v", refreshed)
	}
	ps.TryPub(&pb.Shadow{Device: "dev-a", Reported: &pb.State{}}, "mqtt.incoming/dev-a")
	again := waitShadow(t, st.ch, func(sh *pb.Shadow) bool {
		return sh.GetDevice() == "dev-a" && sh.GetEvent() == pb.ShadowEvent_STATE
	})
	if again.GetTitle() != "Pump 2" {
		t.Fatalf("refreshed state title %q", again.GetTitle())
	}

	ps.TryPub(&pb.Shadow{
		Device: "dev-b", Namespace: "plant", Event: pb.ShadowEvent_DEVICE_REMOVED,
	}, "devices.lifecycle")
	waitShadow(t, st.ch, func(sh *pb.Shadow) bool {
		return sh.GetDevice() == "dev-b" && sh.GetEvent() == pb.ShadowEvent_DEVICE_REMOVED
	})

	removed := false
	until := time.Now().Add(time.Second)
	for !removed && time.Now().Before(until) {
		ps.TryPub(&pb.Shadow{Device: "dev-b", Reported: &pb.State{}}, "mqtt.incoming/dev-b")
		ps.TryPub(&pb.Shadow{Device: "dev-a", Reported: &pb.State{}}, "mqtt.incoming/dev-a")
		sawA := false
		sawB := false
		timer := time.NewTimer(40 * time.Millisecond)
	drain:
		for {
			select {
			case sh := <-st.ch:
				if sh.GetDevice() == "dev-b" && sh.GetEvent() == pb.ShadowEvent_STATE {
					sawB = true
				}
				if sh.GetDevice() == "dev-a" && sh.GetEvent() == pb.ShadowEvent_STATE {
					sawA = true
				}
			case <-timer.C:
				break drain
			}
		}
		if sawA && !sawB {
			removed = true
		}
	}
	if !removed {
		t.Fatal("device stayed subscribed after DEVICE_REMOVED")
	}

	close(st.stop)
	ps.TryPub(&pb.Shadow{Device: "dev-a", Reported: &pb.State{}}, "mqtt.incoming/dev-a")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not return")
	}
}

func TestStreamShadow_AccountDirectShare(t *testing.T) {
	ps := pubsub.New(8)
	svc := shadow.NewShadowServiceServer(zap.NewNop(), nil, ps)
	st := &recStream{
		ctx:  context.Background(),
		ch:   make(chan *pb.Shadow, 16),
		stop: make(chan struct{}),
	}
	done := make(chan struct{})
	go func() {
		_ = svc.StreamShadow(&pb.StreamShadowRequest{
			Account:    "acc",
			Namespaces: []string{"plant"},
			OnlyDelta:  true,
		}, st)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	var shared *pb.Shadow
	for shared == nil && time.Now().Before(deadline) {
		ps.TryPub(&pb.Shadow{
			Device: "dev-share", Namespace: "elsewhere", Account: "acc", Title: "Shared", Event: pb.ShadowEvent_DEVICE_ADDED,
		}, "devices.lifecycle")
		ps.TryPub(&pb.Shadow{
			Device: "dev-nope", Namespace: "nope", Title: "Hidden", Event: pb.ShadowEvent_DEVICE_ADDED,
		}, "devices.lifecycle")
		select {
		case sh := <-st.ch:
			if sh.GetDevice() == "dev-nope" {
				t.Fatalf("unknown namespace was delivered: %+v", sh)
			}
			if sh.GetDevice() == "dev-share" && sh.GetEvent() == pb.ShadowEvent_DEVICE_ADDED {
				shared = sh
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
	if shared == nil || shared.GetAccount() != "acc" || shared.GetTitle() != "Shared" {
		t.Fatalf("direct share %+v", shared)
	}

	deadline = time.Now().Add(2 * time.Second)
	var state *pb.Shadow
	for state == nil && time.Now().Before(deadline) {
		ps.TryPub(&pb.Shadow{Device: "dev-nope", Reported: &pb.State{}}, "mqtt.incoming/dev-nope")
		ps.TryPub(&pb.Shadow{Device: "dev-share", Reported: &pb.State{}}, "mqtt.incoming/dev-share")
		select {
		case sh := <-st.ch:
			if sh.GetDevice() == "dev-nope" {
				t.Fatalf("hidden device state was delivered: %+v", sh)
			}
			if sh.GetDevice() == "dev-share" && sh.GetEvent() == pb.ShadowEvent_STATE {
				state = sh
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
	if state == nil || state.GetTitle() != "Shared" || state.GetNamespace() != "elsewhere" {
		t.Fatalf("shared state %+v", state)
	}

	deadline = time.Now().Add(2 * time.Second)
	var plant *pb.Shadow
	for plant == nil && time.Now().Before(deadline) {
		ps.TryPub(&pb.Shadow{
			Device: "dev-plant", Namespace: "plant", Title: "Pump", Event: pb.ShadowEvent_DEVICE_ADDED,
		}, "devices.lifecycle")
		select {
		case sh := <-st.ch:
			if sh.GetDevice() == "dev-plant" && sh.GetEvent() == pb.ShadowEvent_DEVICE_ADDED {
				plant = sh
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
	if plant == nil || plant.GetTitle() != "Pump" || plant.GetNamespace() != "plant" {
		t.Fatalf("namespace add %+v", plant)
	}

	close(st.stop)
	ps.TryPub(&pb.Shadow{Device: "dev-share", Reported: &pb.State{}}, "mqtt.incoming/dev-share")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not return")
	}
}

func TestStreamShadow_EmptyNamespaceStaysOpen(t *testing.T) {
	ps := pubsub.New(4)
	svc := shadow.NewShadowServiceServer(zap.NewNop(), nil, ps)
	st := &recStream{
		ctx:  context.Background(),
		ch:   make(chan *pb.Shadow, 4),
		stop: make(chan struct{}),
	}
	done := make(chan struct{})
	go func() {
		_ = svc.StreamShadow(&pb.StreamShadowRequest{Namespace: "plant", OnlyDelta: true}, st)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	var added *pb.Shadow
	for added == nil && time.Now().Before(deadline) {
		ps.TryPub(&pb.Shadow{
			Device: "dev-a", Namespace: "plant", Title: "Pump", Event: pb.ShadowEvent_DEVICE_ADDED,
		}, "devices.lifecycle")
		select {
		case sh := <-st.ch:
			if sh.GetEvent() == pb.ShadowEvent_DEVICE_ADDED && sh.GetDevice() == "dev-a" {
				added = sh
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
	if added.GetTitle() != "Pump" {
		t.Fatalf("%+v", added)
	}
	close(st.stop)
	ps.TryPub(&pb.Shadow{Device: "dev-a", Namespace: "plant", Event: pb.ShadowEvent_DEVICE_REMOVED}, "devices.lifecycle")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not return")
	}
}
