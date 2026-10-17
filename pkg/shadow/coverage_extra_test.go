package shadow

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/alicebob/miniredis/v2"
	redis "github.com/go-redis/redis/v8"
	"github.com/infinimesh/core/pkg/pubsub"
	pb "github.com/infinimesh/proto/shadow"
	"go.uber.org/zap"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

type readRec struct {
	n       int
	account string
	token   string
}

func (r *readRec) RecordRead(_ context.Context, _ string, account, tokenID string) {
	r.n++
	r.account = account
	r.token = tokenID
}

func TestWireAndSequencer(t *testing.T) {
	err := rpcErr(connect.CodeInvalidArgument, "nope")
	if err == nil || errors.Unwrap(err) == nil {
		t.Fatal(err)
	}
	if rpcErr(0, "ok") == nil {
		t.Fatal("ok code")
	}
	if _, ok := status.FromError(err); !ok {
		t.Fatal("status")
	}

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	seq := NewRedisSequencer(rdb)
	if seq.Next(context.Background()) != 1 {
		t.Fatal("incr")
	}
	mr.Set(eventIDKey, "-1")
	if seq.Next(context.Background()) == 0 {
		t.Fatal("fallback after zero")
	}
	_ = rdb.Close()
	if seq.Next(context.Background()) == 0 {
		t.Fatal("fallback after close")
	}
	if (redisSeq{}).Next(context.Background()) != 0 {
		t.Fatal("empty seq")
	}
}

func TestDispatchReadsAndStateHelpers(t *testing.T) {
	if firstMeta(nil, "account") != "" || firstMeta(metadata.Pairs(), "account") != "" {
		t.Fatal("empty meta")
	}
	if firstMeta(metadata.Pairs("account", "acc"), "account") != "acc" {
		t.Fatal("meta")
	}

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ps := pubsub.New(8)
	s := NewShadowServiceServer(zap.NewNop(), rdb, ps)
	rec := &readRec{}
	s.SetReadRecorder(rec)
	s.SetReadRecorder(nil)
	s.SetReadRecorder(rec)
	raw, err := json.Marshal(&pb.State{Version: 4})
	if err != nil {
		t.Fatal(err)
	}
	if err := rdb.Set(context.Background(), Key("dev", pb.StateKey_REPORTED), raw, 0).Err(); err != nil {
		t.Fatal(err)
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("account", "acc", "jti", "tok"))
	res, err := s.Get(ctx, &pb.GetRequest{Device: "dev"})
	if err != nil || rec.n != 1 || rec.account != "acc" || rec.token != "tok" || res.GetShadows()[0].GetDevice() != "dev" {
		t.Fatalf("get %v %+v", err, rec)
	}
	var nilServer *ShadowServiceServer
	nilServer.recordReads(ctx, []string{"dev"})

	called := false
	s.Dispatch(nil, nil)
	s.Dispatch(&pb.Shadow{Device: "dev", EventId: 9}, func() { called = true })
	s.Dispatch(&pb.Shadow{Device: "dev"}, nil)
	if !called {
		t.Fatal("pub")
	}
	s.publishStamped(ctx, nil)
	s.publishOTA(ctx, &pb.Shadow{Device: "dev"}, nil, []pb.ShadowEvent{pb.ShadowEvent_OTA_STATUS_CHANGED})
	s.publishOTA(ctx, &pb.Shadow{Device: "dev"}, []string{"ota"}, []pb.ShadowEvent{pb.ShadowEvent_OTA_STATUS_CHANGED})

	prev, ver := s.previousState(ctx, "missing", pb.StateKey_DESIRED)
	if prev != nil || ver != 1 {
		t.Fatalf("miss %v %d", prev, ver)
	}
	if err := rdb.Set(ctx, Key("bad", pb.StateKey_REPORTED), "{", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, ver := s.previousState(ctx, "bad", pb.StateKey_REPORTED); ver != 1 {
		t.Fatal(ver)
	}
	if _, ver := s.previousState(ctx, "dev", pb.StateKey_REPORTED); ver != 5 {
		t.Fatalf("version %d", ver)
	}
	bare := NewShadowServiceServer(zap.NewNop(), nil, nil)
	if _, ver := bare.previousState(ctx, "dev", pb.StateKey_REPORTED); ver != 1 {
		t.Fatal(ver)
	}

	expanded := s.expand(ctx, &pb.Shadow{
		Device: "dev", Title: "Pump", Namespace: "plant", Tags: []string{"a"},
		Reported:   &pb.State{Data: &structpb.Struct{Fields: map[string]*structpb.Value{"a": structpb.NewNumberValue(1)}}},
		Connection: &pb.ConnectionState{Connected: true},
		Event:      pb.ShadowEvent_STATE,
	})
	if expanded.GetTitle() != "Pump" || expanded.GetConnection() == nil || expanded.GetReported() == nil {
		t.Fatalf("%+v", expanded)
	}
	if mergeState(nil, &pb.State{Version: 1}) == nil || mergeState(&pb.State{}, &pb.State{Version: 2}) == nil {
		t.Fatal("merge nil")
	}
	if mergeState(&pb.State{Data: &structpb.Struct{}}, nil) == nil {
		t.Fatal("merge stored")
	}
	merged := mergeState(&pb.State{Version: 1, Data: &structpb.Struct{Fields: map[string]*structpb.Value{"a": structpb.NewNumberValue(1)}}},
		&pb.State{Version: 2, Data: &structpb.Struct{Fields: map[string]*structpb.Value{"b": structpb.NewNumberValue(2)}}})
	if merged.GetData().GetFields()["b"] == nil {
		t.Fatal("merge")
	}
	nan := &pb.State{Data: &structpb.Struct{Fields: map[string]*structpb.Value{"n": structpb.NewNumberValue(math.NaN())}}}
	s.MergeAndStore(zap.NewNop(), "dev", pb.StateKey_REPORTED, nan)

	applyInclude(nil, []pb.StateKey{pb.StateKey_REPORTED})
	applyInclude(&pb.Shadow{Event: pb.ShadowEvent_DEVICE_ADDED, Reported: &pb.State{}}, []pb.StateKey{pb.StateKey_REPORTED})
	applyInclude(&pb.Shadow{Event: pb.ShadowEvent_STATE}, nil)
	kept := &pb.Shadow{Event: pb.ShadowEvent_STATE, Reported: &pb.State{}, Desired: &pb.State{}, Connection: &pb.ConnectionState{}}
	applyInclude(kept, []pb.StateKey{pb.StateKey(99)})
	if kept.Reported == nil {
		t.Fatal("unknown include")
	}
	applyInclude(kept, []pb.StateKey{pb.StateKey_REPORTED})
	if kept.Reported == nil || kept.Desired != nil || kept.Connection != nil {
		t.Fatal("include")
	}

	if requestDevices(nil) != nil || len(requestDevices(&pb.GetRequest{Device: "a"})) != 1 {
		t.Fatal("devices")
	}
	if len(requestDevices(&pb.GetRequest{Devices: []string{"a", "b"}})) != 2 || len(requestDevices(&pb.GetRequest{Pool: []string{"c"}})) != 1 {
		t.Fatal("pool")
	}

	sc, meta, devices := scopeFromRequest(&pb.StreamShadowRequest{
		Devices: []string{"", "dev"}, Catalog: []*pb.DeviceMeta{{}, {Device: "cat", Title: "C", Namespace: "plant", Tags: []string{"t"}}},
		Account: "acc", Namespaces: []string{"", "plant"},
	})
	if !sc.live() || len(devices) != 2 || meta["cat"].title != "C" {
		t.Fatal("scope")
	}
	if sc.wants(nil) || sc.wants(&pb.Shadow{Namespace: "other"}) || !sc.wants(&pb.Shadow{Account: "acc"}) || !sc.wants(&pb.Shadow{Namespace: "plant"}) {
		t.Fatal("wants")
	}
	if (streamScope{}).wants(&pb.Shadow{Device: "dev"}) || (streamScope{namespace: "plant"}).wants(&pb.Shadow{Namespace: "other"}) {
		t.Fatal("wants miss")
	}
	if eventInStream(nil, streamScope{}, nil) || !eventInStream(devices, streamScope{}, &pb.Shadow{Device: "dev"}) {
		t.Fatal("event")
	}
	if !eventInStream(map[string]struct{}{}, sc, &pb.Shadow{Account: "acc"}) || eventInStream(map[string]struct{}{}, streamScope{}, &pb.Shadow{Device: "x"}) {
		t.Fatal("event scope")
	}

	s.remember(nil)
	s.remember(&pb.Shadow{Event: pb.ShadowEvent_DEVICE_REMOVED})
	s.rememberEvent(nil)
	s.rememberEvent(&pb.Shadow{Device: "dev"})
	for i := 1; i <= 257; i++ {
		s.rememberEvent(&pb.Shadow{Device: "dev", Namespace: "plant", Sequence: uint64(i)})
	}
	if len(s.events["plant"]) != 256 {
		t.Fatalf("log %d", len(s.events["plant"]))
	}
	empty := s.replayNamespaces(streamScope{}, nil, map[string]struct{}{})
	if len(empty) != 1 {
		t.Fatalf("empty replay %v", empty)
	}
	s.catalog["solo"] = deviceMeta{namespace: "west"}
	fromCat := s.replayNamespaces(streamScope{}, nil, map[string]struct{}{"solo": {}})
	if len(fromCat) != 1 || fromCat[0] != "west" {
		t.Fatalf("catalog %v", fromCat)
	}
	s.fill(nil, nil)
	filled := &pb.Shadow{Device: "solo"}
	s.fill(filled, nil)
	if filled.Namespace != "west" {
		t.Fatal(filled.Namespace)
	}

	applyTag(nil, &pb.Shadow{Device: "dev"})
	applyTag(meta, &pb.Shadow{})
	applyTag(meta, &pb.Shadow{Device: "cat", Tags: []string{"n"}, Title: "Pump", Namespace: "east"})
	applyTag(meta, &pb.Shadow{Device: "cat", Event: pb.ShadowEvent_TAG_REMOVED, Tag: "n"})
	applyTag(meta, &pb.Shadow{Device: "cat", Event: pb.ShadowEvent_TAG_ADDED, Tag: "z"})
	if meta["cat"].title != "Pump" || meta["cat"].namespace != "east" {
		t.Fatal(meta["cat"])
	}

	if patchErrText(nil) != "" || patchErrText(errors.New("plain")) != "plain" {
		t.Fatal("text")
	}
	if patchErrText(status.Error(3, "bad")) != "bad" || patchErrText(connect.NewError(connect.CodeAborted, errors.New("stop"))) != "stop" {
		t.Fatal("typed")
	}
}

func TestOTAFieldBranches(t *testing.T) {
	num := func(n float64) *structpb.Value { return structpb.NewNumberValue(n) }
	str := func(s string) *structpb.Value { return structpb.NewStringValue(s) }
	bad := []struct {
		key string
		v   *structpb.Value
	}{
		{"version", num(1)},
		{"sha256", num(1)},
		{"sha256", str("ABC")},
		{"sha256", str("aa")},
		{"size", str("1")},
		{"size", num(-1)},
		{"size", num(1.5)},
		{"status", num(1)},
		{"status", str("nope")},
		{"updated_at", num(1)},
		{"updated_at", str("yesterday")},
		{"other", str("x")},
	}
	for _, tc := range bad {
		if err := validateOTAField(tc.key, tc.v); err == nil {
			t.Fatalf("accepted %s", tc.key)
		}
	}
	hex := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := validateOTAField("sha256", str(hex)); err != nil {
		t.Fatal(err)
	}
	if err := validateOTAField("sha256", str("")); err != nil {
		t.Fatal(err)
	}
	if err := validateOTAField("size", num(0)); err != nil {
		t.Fatal(err)
	}
	if err := validateOTAField("status", str("idle")); err != nil {
		t.Fatal(err)
	}
	if err := validateOTAField("updated_at", str(time.Now().UTC().Format(time.RFC3339))); err != nil {
		t.Fatal(err)
	}
	if !isNull(nil) || isNull(str("a")) {
		t.Fatal("null")
	}
	if sha256Hex("AA") || sha256Hex(hex[:63]+"G") {
		t.Fatal("hex")
	}
	if otaEvents(nil, nil, nil) != nil {
		t.Fatal("events")
	}
	prev := &pb.State{Data: &structpb.Struct{Fields: map[string]*structpb.Value{
		"ota": structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{"version": str("1"), "status": str("idle")}}),
	}}}
	if otaChanged(nil, nil, "version") || otaChanged(prev, &pb.State{}, "version") || otaChanged(prev, &pb.State{Data: &structpb.Struct{}}, "version") {
		t.Fatal("unchanged")
	}
	cleared := &pb.State{Data: &structpb.Struct{Fields: map[string]*structpb.Value{"ota": structpb.NewNullValue()}}}
	if !otaChanged(prev, cleared, "version") || otaChanged(nil, cleared, "version") {
		t.Fatal("null ota")
	}
	same := &pb.State{Data: &structpb.Struct{Fields: map[string]*structpb.Value{
		"ota": structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{"version": str("1")}}),
	}}}
	changed := &pb.State{Data: &structpb.Struct{Fields: map[string]*structpb.Value{
		"ota": structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{"status": str("failed")}}),
	}}}
	if otaChanged(prev, same, "version") || !otaChanged(prev, changed, "status") {
		t.Fatal("compare")
	}
	if _, ok := otaString(nil, "version"); ok {
		t.Fatal("string nil")
	}
	if _, ok := otaString(&pb.State{Data: &structpb.Struct{Fields: map[string]*structpb.Value{"ota": num(1)}}}, "version"); ok {
		t.Fatal("ota not object")
	}
	if _, ok := otaString(prev, "missing"); ok {
		t.Fatal("missing key")
	}
	if _, ok := otaString(&pb.State{Data: &structpb.Struct{Fields: map[string]*structpb.Value{
		"ota": structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{"version": num(1)}}),
	}}}, "version"); ok {
		t.Fatal("not string")
	}
	st := &pb.State{Data: &structpb.Struct{Fields: map[string]*structpb.Value{"ota": num(1)}}}
	if err := ValidateState(st); err == nil {
		t.Fatal("ota object")
	}
}

func TestPersisterStoresIncoming(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ps := pubsub.New(4)
	s := NewShadowServiceServer(zap.NewNop(), rdb, ps)
	done := make(chan struct{})
	go func() {
		s.Persister()
		close(done)
	}()
	data, err := structpb.NewStruct(map[string]any{"led": true})
	if err != nil {
		t.Fatal(err)
	}
	msg := &pb.Shadow{
		Device:     "dev",
		Event:      pb.ShadowEvent_STATE,
		Reported:   &pb.State{Data: data},
		Desired:    &pb.State{Data: data},
		Connection: &pb.ConnectionState{Connected: true},
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		ps.TryPub(&pb.Shadow{Device: "dev", Event: pb.ShadowEvent_DEVICE_ADDED}, "mqtt.incoming")
		ps.TryPub(msg, "mqtt.incoming")
		if mr.Exists(Key("dev", pb.StateKey_REPORTED)) && mr.Exists(Key("dev", pb.StateKey_CONNECTION)) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !mr.Exists(Key("dev", pb.StateKey_REPORTED)) || !mr.Exists(Key("dev", pb.StateKey_DESIRED)) || !mr.Exists(Key("dev", pb.StateKey_CONNECTION)) {
		t.Fatal("keys missing")
	}
	ps.Shutdown()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("persister")
	}
}
