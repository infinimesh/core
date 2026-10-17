package shadow

import (
	"context"
	"testing"
	"time"

	"github.com/infinimesh/core/v3/pkg/pubsub"
	pb "github.com/infinimesh/proto/shadow"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestStampAssignsAndKeepsEventID(t *testing.T) {
	s := NewShadowServiceServer(zap.NewNop(), nil, nil)
	kept := &pb.Shadow{Device: "d", EventId: 7}
	s.stamp(context.Background(), kept)
	if kept.EventId != 7 {
		t.Fatalf("kept %d", kept.EventId)
	}
	assigned := &pb.Shadow{Device: "d"}
	s.Accept(assigned)
	if assigned.EventId == 0 {
		t.Fatal("expected an event id")
	}
	s.Accept(nil)
	s.stamp(context.Background(), nil)
}

func TestRedisSequencerFallsBack(t *testing.T) {
	seq := NewRedisSequencer(nil)
	a := seq.Next(context.Background())
	b := seq.Next(context.Background())
	if a == 0 || b <= a {
		t.Fatalf("ids %d %d", a, b)
	}
}

func TestRememberShareRemovalKeepsCatalog(t *testing.T) {
	s := NewShadowServiceServer(zap.NewNop(), nil, nil)
	s.remember(&pb.Shadow{
		Device: "d", Title: "Pump", Namespace: "plant", Tags: []string{"water"}, Event: pb.ShadowEvent_DEVICE_ADDED,
	})
	s.remember(&pb.Shadow{Device: "d", Account: "acc", Event: pb.ShadowEvent_DEVICE_REMOVED})
	kept := &pb.Shadow{Device: "d"}
	s.fill(kept, nil)
	if kept.Title != "Pump" || kept.Namespace != "plant" {
		t.Fatalf("share removal cleared the catalog: %+v", kept)
	}
	s.remember(&pb.Shadow{Device: "d", Event: pb.ShadowEvent_DEVICE_REMOVED})
	cleared := &pb.Shadow{Device: "d"}
	s.fill(cleared, nil)
	if cleared.Title != "" || cleared.Namespace != "" {
		t.Fatalf("removal left catalog metadata: %+v", cleared)
	}
}

func TestScopeWants(t *testing.T) {
	ns := streamScope{namespace: "plant", namespaces: map[string]struct{}{"plant": {}}}
	if !ns.live() || !ns.wants(&pb.Shadow{Namespace: "plant", Account: "other"}) {
		t.Fatal("namespace stream should accept its namespace")
	}
	if ns.wants(&pb.Shadow{Namespace: "other", Account: "acc"}) {
		t.Fatal("namespace stream should ignore another namespace")
	}

	acc := streamScope{account: "acc", namespaces: map[string]struct{}{"plant": {}}}
	if !acc.wants(&pb.Shadow{Account: "acc", Namespace: "elsewhere"}) {
		t.Fatal("account stream should accept a direct share")
	}
	if !acc.wants(&pb.Shadow{Namespace: "plant"}) {
		t.Fatal("account stream should accept a visible namespace")
	}
	if acc.wants(&pb.Shadow{Namespace: "nope"}) {
		t.Fatal("account stream should ignore an unknown namespace")
	}
	var device streamScope
	if device.live() || device.wants(&pb.Shadow{Namespace: "plant"}) {
		t.Fatal("device stream is not live")
	}
}

func TestPublishDeviceScope(t *testing.T) {
	ps := pubsub.New(4)
	ch := ps.Sub(lifecycleTopic)
	s := NewShadowServiceServer(zap.NewNop(), nil, ps)

	_, err := s.PublishDeviceScope(context.Background(), &pb.Shadow{Event: pb.ShadowEvent_DEVICE_ADDED})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty device: %v", err)
	}
	_, err = s.PublishDeviceScope(context.Background(), &pb.Shadow{Device: "d"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("state event: %v", err)
	}

	_, err = s.PublishDeviceScope(context.Background(), &pb.Shadow{
		Device: "d", Title: "Pump", Namespace: "plant", Tags: []string{"water"}, Event: pb.ShadowEvent_DEVICE_ADDED,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-ch:
		sh := msg.(*pb.Shadow)
		if sh.GetEventId() == 0 || sh.GetTitle() != "Pump" || sh.GetNamespace() != "plant" || sh.GetEvent() != pb.ShadowEvent_DEVICE_ADDED {
			t.Fatalf("%+v", sh)
		}
	case <-time.After(time.Second):
		t.Fatal("scope event was not published")
	}

	filled := &pb.Shadow{Device: "d"}
	s.fill(filled, nil)
	if filled.Title != "Pump" || filled.Namespace != "plant" || len(filled.Tags) != 1 || filled.Tags[0] != "water" {
		t.Fatalf("catalog %+v", filled)
	}
	filled.Title = "kept"
	s.fill(filled, nil)
	if filled.Title != "kept" {
		t.Fatalf("overwrote title %q", filled.Title)
	}
}

func TestApplyTag(t *testing.T) {
	meta := map[string]deviceMeta{"d": {tags: []string{"a"}}}
	applyTag(meta, &pb.Shadow{Device: "d", Event: pb.ShadowEvent_TAG_ADDED, Tag: "b"})
	if len(meta["d"].tags) != 2 || meta["d"].tags[1] != "b" {
		t.Fatalf("added %+v", meta["d"].tags)
	}
	applyTag(meta, &pb.Shadow{Device: "d", Event: pb.ShadowEvent_TAG_REMOVED, Tag: "a", Tags: []string{"b"}})
	if len(meta["d"].tags) != 1 || meta["d"].tags[0] != "b" {
		t.Fatalf("removed %+v", meta["d"].tags)
	}
}
