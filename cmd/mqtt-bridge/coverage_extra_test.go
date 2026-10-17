package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/infinimesh/core/pkg/mqtt/packet"
	"github.com/infinimesh/core/pkg/pubsub"
	pb "github.com/infinimesh/proto/node"
	"github.com/infinimesh/proto/node/access"
	devpb "github.com/infinimesh/proto/node/devices"
	nspb "github.com/infinimesh/proto/node/namespaces"
	stpb "github.com/infinimesh/proto/shadow"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fpCall struct {
	dev *devpb.Device
	err error
}

type scriptDevices struct {
	pb.DevicesServiceClient
	fp       []fpCall
	token    *devpb.Device
	tokenErr error
}

func (s *scriptDevices) GetByFingerprint(_ context.Context, _ *devpb.GetByFingerprintRequest, _ ...grpc.CallOption) (*devpb.Device, error) {
	if len(s.fp) == 0 {
		return nil, errors.New("unexpected fingerprint")
	}
	next := s.fp[0]
	s.fp = s.fp[1:]
	return next.dev, next.err
}

func (s *scriptDevices) GetByToken(context.Context, *devpb.Device, ...grpc.CallOption) (*devpb.Device, error) {
	return s.token, s.tokenErr
}

type shadowGet struct {
	res *stpb.GetResponse
	err error
}

type scriptShadow struct {
	stpb.ShadowServiceClient
	mu   sync.Mutex
	gets []shadowGet
}

func (s *scriptShadow) Get(context.Context, *stpb.GetRequest, ...grpc.CallOption) (*stpb.GetResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.gets) == 0 {
		return nil, errors.New("unexpected get")
	}
	next := s.gets[0]
	s.gets = s.gets[1:]
	return next.res, next.err
}

func TestVerifyBasicAuthEmptyPassword(t *testing.T) {
	_, err := verifyBasicAuth(&packet.Connect{Username: "dev"})
	if err == nil {
		t.Fatal("empty password accepted")
	}
}

func TestReadConnectRejects(t *testing.T) {
	if _, _, err := readConnect(bytes.NewReader(nil)); err == nil {
		t.Fatal("empty reader")
	}
	var buf bytes.Buffer
	if _, err := (&packet.PingReq{}).WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readConnect(&buf); err == nil {
		t.Fatal("ping accepted as connect")
	}
}

func TestWriteUnsubAckAndPingResp(t *testing.T) {
	var buf bytes.Buffer
	if err := writeUnsubAck(&buf, packet.ProtocolMQTT311, 4, 2); err != nil {
		t.Fatal(err)
	}
	if err := writeUnsubAck(&buf, packet.ProtocolMQTT5, 5, 2); err != nil {
		t.Fatal(err)
	}
	if err := writePingResp(&buf); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Fatal("empty")
	}
}

func TestSessionEdges(t *testing.T) {
	mem := &memorySessions{}
	store := newSessionStore()
	store.flush(time.Now())
	store.persist = mem
	store.close("missing", "cid", connectMeta{}, time.Now())

	meta := connectMeta{sessionExpiry: time.Hour, receiveMax: 4}
	now := time.Unix(1_700_000_000, 0)
	_, sess := store.open("dev", "cid", meta, now)
	sess.mu.Lock()
	sess.pending = []pendingDown{{topic: "t", payload: []byte("p"), qos: 1, expires: now.Add(time.Minute)}}
	sess.inQoS2 = map[uint16]qos2In{3: {deliver: true, deadline: now.Add(time.Minute), msg: pubMsg{topic: "t", payload: []byte("q"), qos: 2, id: 3}}}
	sess.out = append(sess.out, inflight{id: 1, topic: "t", payload: []byte("a"), qos: 1, sent: now})
	sess.mu.Unlock()
	store.close("dev", "cid", meta, now)

	present, loaded := store.open("dev", "cid", connectMeta{sessionExpiry: time.Hour}, now.Add(time.Second))
	if !present || loaded.receiveMax != 65535 || len(loaded.pending) != 1 || len(loaded.inQoS2) != 1 || len(loaded.out) != 1 {
		t.Fatalf("resume present %v recv %d pending %d in %d out %d", present, loaded.receiveMax, len(loaded.pending), len(loaded.inQoS2), len(loaded.out))
	}

	present, fresh := store.open("dev", "cid", connectMeta{cleanStart: true, sessionExpiry: time.Hour, receiveMax: 3}, now)
	if present || fresh.receiveMax != 3 {
		t.Fatalf("clean present %v recv %d", present, fresh.receiveMax)
	}
	store.close("dev", "cid", connectMeta{}, now)
	if _, ok := mem.load(sessionKey("dev", "cid")); ok {
		t.Fatal("expiry 0 kept the session")
	}

	_, sess = store.open("dev", "old", meta, now)
	store.close("dev", "old", meta, now)
	present, _ = store.open("dev", "old", meta, now.Add(2*time.Hour))
	if present {
		t.Fatal("expired session resumed")
	}

	wrapped := &deviceSession{nextID: 0, out: []inflight{{id: 1}}}
	if id := wrapped.allocID(); id != 2 {
		t.Fatalf("alloc %d", id)
	}
	if _, ok := wrapped.enqueue("t", []byte{1}, 1, packet.Properties{HasMessageExpiry: true}); ok {
		t.Fatal("zero expiry queued")
	}
	if _, ok := wrapped.enqueueAt("t", []byte{1}, 1, packet.Properties{}, now); ok {
		t.Fatal("past expiry queued")
	}
	if wrapped.ack(99) {
		t.Fatal("ack of missing id")
	}
	wrapped.out = []inflight{{id: 7, qos: 2}, {id: 8, qos: 1}, {id: 9, qos: 2, stage: 1}}
	if _, ok := wrapped.markPubRel(7); !ok || wrapped.out[0].stage != 1 {
		t.Fatal("mark pubrel")
	}
	if _, ok := wrapped.markPubRel(8); ok {
		t.Fatal("qos 1 marked")
	}
	if _, ok := wrapped.markPubRel(9); ok {
		t.Fatal("stage 1 marked again")
	}

	props := packet.Properties{HasMessageExpiry: true, MessageExpiry: time.Second}
	if got := outboundProps(props, now.Add(-time.Second), now); got.MessageExpiry != 0 {
		t.Fatalf("left %s", got.MessageExpiry)
	}
	if got := outboundProps(props, now.Add(1500*time.Millisecond), now); got.MessageExpiry != 2*time.Second {
		t.Fatalf("ceil %s", got.MessageExpiry)
	}
	plain := packet.Properties{ContentType: "text/plain"}
	if outboundProps(plain, time.Time{}, now).ContentType != "text/plain" {
		t.Fatal("props rewritten")
	}

	dueSess := &deviceSession{out: []inflight{
		{id: 1, expires: now.Add(-time.Second), sent: now.Add(-time.Minute), qos: 1, topic: "t", payload: []byte("a")},
		{id: 2, stage: 1, sent: now.Add(-time.Minute), qos: 2},
		{id: 3, sent: now, qos: 1, topic: "t", payload: []byte("b")},
	}}
	due := dueSess.due(now, time.Second)
	if len(due) != 1 || !due[0].rel || due[0].id != 2 || len(dueSess.out) != 2 {
		t.Fatalf("due %#v out %#v", due, dueSess.out)
	}
	unackedSess := &deviceSession{out: []inflight{
		{id: 1, expires: now.Add(-time.Second), stage: 0, topic: "t", payload: []byte("a"), qos: 1},
		{id: 4, topic: "t", payload: []byte("c"), qos: 1},
	}}
	left := unackedSess.unacked(now)
	if len(left) != 1 || left[0].id != 4 || len(unackedSess.out) != 1 {
		t.Fatalf("unacked %#v", left)
	}

	var blank memorySessions
	blank.drop("gone")
	blank.save("k", sessionSnap{SubQoS: 1}, 0)
	if _, ok := blank.load("k"); ok {
		t.Fatal("ttl 0 saved")
	}
	blank.save("k", sessionSnap{SubQoS: 1}, time.Second)
	if snap, ok := blank.load("k"); !ok || snap.SubQoS != 1 {
		t.Fatal("memory load")
	}
}

func TestRedisSessionsAndFlush(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	rs := newRedisSessions(rdb)
	if rs == nil || sessionRedisKey("dev\x00cid") == "" {
		t.Fatal("redis sessions")
	}
	rs.save("k", sessionSnap{}, 0)
	if _, ok := rs.load("missing"); ok {
		t.Fatal("missing key")
	}
	require.NoError(t, rdb.Set(context.Background(), sessionRedisKey("bad"), "not-json", time.Minute).Err())
	if _, ok := rs.load("bad"); ok {
		t.Fatal("bad json loaded")
	}
	want := sessionSnap{SubQoS: 1, NextID: 4, ReceiveMax: 8}
	rs.save("k", want, time.Minute)
	got, ok := rs.load("k")
	if !ok || got.SubQoS != 1 || got.NextID != 4 {
		t.Fatalf("load %+v %v", got, ok)
	}
	rs.drop("k")
	if _, ok := rs.load("k"); ok {
		t.Fatal("dropped key remained")
	}

	mem := &memorySessions{}
	store := newSessionStore()
	store.persist = mem
	now := time.Unix(1_700_000_000, 0)
	_, kept := store.open("dev", "keep", connectMeta{receiveMax: 2}, now)
	kept.mu.Lock()
	kept.persistFor = 0
	kept.expiry = time.Time{}
	kept.mu.Unlock()
	_, expired := store.open("dev", "exp", connectMeta{receiveMax: 2}, now)
	expired.mu.Lock()
	expired.expiry = now.Add(-time.Second)
	expired.persistFor = time.Hour
	expired.mu.Unlock()
	_, soon := store.open("dev", "soon", connectMeta{receiveMax: 2}, now)
	soon.mu.Lock()
	soon.expiry = now.Add(time.Minute)
	soon.mu.Unlock()
	store.flush(now)
	if _, ok := mem.load(sessionKey("dev", "keep")); !ok {
		t.Fatal("zero expiry was not flushed")
	}
	if _, ok := mem.load(sessionKey("dev", "exp")); ok {
		t.Fatal("expired session flushed")
	}
	if _, ok := mem.load(sessionKey("dev", "soon")); !ok {
		t.Fatal("live session not flushed")
	}

	go store.keep()
	time.Sleep(1100 * time.Millisecond)
	if _, ok := mem.load(sessionKey("dev", "soon")); !ok {
		t.Fatal("keep did not flush")
	}
}

func TestLogErrorAndCloseAndLookup(t *testing.T) {
	left, right := net.Pipe()
	go LogErrorAndClose(left, errors.New("nope"))
	_ = right.SetReadDeadline(time.Now().Add(2 * time.Second))
	pkt, err := packet.ReadPacket(right, packet.ProtocolMQTT311, 0)
	if err != nil {
		t.Fatal(err)
	}
	ack := pkt.(*packet.ConnAck)
	if ack.Reason != packet.CodeUnacceptableProto {
		t.Fatalf("ack %+v", ack)
	}
	right.Close()

	prev := client
	t.Cleanup(func() { client = prev })
	if internal_ctx == nil {
		internal_ctx = context.Background()
	}
	dev := &devpb.Device{Uuid: "dev", Enabled: true}
	client = &scriptDevices{fp: []fpCall{{err: errors.New("down")}, {dev: dev}, {dev: dev}}}
	if _, err := GetByFingerprintAndVerify([]byte{1}, func(*devpb.Device) bool { return true }); err == nil {
		t.Fatal("lookup error ignored")
	}
	if _, err := GetByFingerprintAndVerify([]byte{1}, func(*devpb.Device) bool { return false }); err == nil {
		t.Fatal("rejected device returned")
	}
	got, err := GetByFingerprintAndVerify([]byte{1}, func(d *devpb.Device) bool { return d.GetUuid() == "dev" })
	if err != nil || got.GetUuid() != "dev" {
		t.Fatalf("lookup %v %v", got, err)
	}
}

func useBridge(t *testing.T, devices *scriptDevices, sh stpb.ShadowServiceClient) pubsub.PubSub {
	t.Helper()
	prevC, prevS, prevPS := client, shadow, ps
	bus := pubsub.New(16)
	client = devices
	shadow = sh
	ps = bus
	if internal_ctx == nil {
		internal_ctx = context.Background()
	}
	t.Cleanup(func() { client, shadow, ps = prevC, prevS, prevPS })
	return bus
}

func waitDown(t *testing.T, bus pubsub.PubSub) func() {
	t.Helper()
	ch := bus.Sub("mqtt.incoming")
	return func() {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			select {
			case msg := <-ch:
				sh, ok := msg.(*stpb.Shadow)
				if ok && sh.GetConnection() != nil && !sh.GetConnection().GetConnected() {
					return
				}
			case <-deadline:
				t.Fatal("disconnect was not published")
			}
		}
	}
}

func bridgeDevice(id string, devRate, nsRate float64) *devpb.Device {
	ns := "plant"
	return &devpb.Device{
		Uuid: id, Enabled: true, BasicEnabled: true, Token: "tok", Title: "Pump", Tags: []string{"a"},
		Access: &access.Access{Namespace: &ns},
		Quota:  &nspb.Quota{DeviceMessagesPerSecond: devRate, NamespaceMessagesPerSecond: nsRate},
	}
}

func TestHandleConnFlows(t *testing.T) {
	dev := bridgeDevice("dev", 100, 0.5)
	data, err := structpb.NewStruct(map[string]any{"led": float64(1)})
	if err != nil {
		t.Fatal(err)
	}
	devices := &scriptDevices{token: dev}
	shadows := &scriptShadow{gets: []shadowGet{{res: &stpb.GetResponse{Shadows: []*stpb.Shadow{{
		Device:  "dev",
		Desired: &stpb.State{Data: data, Version: 2},
	}}}}}}
	bus := useBridge(t, devices, shadows)
	down := waitDown(t, bus)

	server, client := net.Pipe()
	go HandleConn(server, &packet.Connect{
		ProtocolLevel: packet.ProtocolMQTT5,
		CleanStart:    true,
		ClientID:      "cid",
		Username:      "dev",
	}, connectMeta{cleanStart: true, willTopic: reportedTopic("dev"), willMessage: []byte("{")}, bridgeDevice("dev", 100, 0.5))
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := packet.ReadPacket(client, packet.ProtocolMQTT5, 0); err != nil {
		t.Fatal(err)
	}

	client.Write(packetBytes(t, &packet.Subscribe{
		ProtocolLevel: packet.ProtocolMQTT5,
		PacketID:      1,
		Filters: []packet.Subscription{
			{Topic: desiredTopic("dev"), QoS: 1},
			{Topic: reportedTopic("dev"), QoS: 1},
		},
	}))
	sub := mustPacket(t, client).(*packet.SubAck)
	if len(sub.Reasons) != 2 || sub.Reasons[1] != packet.ReasonNotAuthorized {
		t.Fatalf("suback %v", sub.Reasons)
	}
	_ = mustPacket(t, client)

	client.Write(packetBytes(t, &packet.Publish{
		ProtocolLevel: packet.ProtocolMQTT5,
		QoS:           1,
		PacketID:      9,
		Topic:         reportedTopic("dev"),
		Payload:       []byte(`{"t":1}`),
	}))
	puback := mustPacket(t, client).(*packet.Ack)
	if puback.Kind != packet.PUBACK || puback.Reason != packet.ReasonQuotaExceeded {
		t.Fatalf("quota %+v", puback)
	}

	empty := &stpb.Shadow{Device: "dev", Desired: &stpb.State{Timestamp: timestamppb.New(time.Unix(10, 0))}}
	fresh := &stpb.Shadow{Device: "dev", Desired: &stpb.State{Timestamp: timestamppb.New(time.Unix(50, 0)), Data: data}}
	stale := &stpb.Shadow{Device: "dev", Desired: &stpb.State{Timestamp: timestamppb.New(time.Unix(40, 0)), Data: data}}
	bus.TryPub(empty, "mqtt.outgoing/dev")
	bus.TryPub(fresh, "mqtt.outgoing/dev")
	bus.TryPub(stale, "mqtt.outgoing/dev")
	pushed := mustPacket(t, client).(*packet.Publish)
	if string(pushed.Payload) == "" {
		t.Fatal("empty desired")
	}

	client.Write(packetBytes(t, &packet.PingReq{}))
	if _, ok := mustPacket(t, client).(*packet.PingResp); !ok {
		t.Fatal("pingresp")
	}
	client.Write(packetBytes(t, &packet.Unsubscribe{
		ProtocolLevel: packet.ProtocolMQTT5,
		PacketID:      3,
		Topics:        []string{reportedTopic("dev"), desiredTopic("dev")},
	}))
	if _, ok := mustPacket(t, client).(*packet.UnsubAck); !ok {
		t.Fatal("unsuback")
	}
	if _, err := (&packet.Disconnect{ProtocolLevel: packet.ProtocolMQTT5}).WriteTo(client); err != nil {
		t.Fatal(err)
	}
	client.Close()
	down()
}

func TestHandleConnBranches(t *testing.T) {
	ns := "plant"
	base := &devpb.Device{Uuid: "dev", Enabled: true, Token: "tok", Title: "Pump", Tags: []string{"a"}, Access: &access.Access{Namespace: &ns}}

	t.Run("token error and bad json", func(t *testing.T) {
		devices := &scriptDevices{tokenErr: errors.New("down")}
		bus := useBridge(t, devices, nil)
		down := waitDown(t, bus)
		server, client := net.Pipe()
		defer client.Close()
		go HandleConn(server, &packet.Connect{ProtocolLevel: packet.ProtocolMQTT5, CleanStart: true, ClientID: "cid"}, connectMeta{cleanStart: true}, base)
		if _, err := readLevel(client, packet.ProtocolMQTT5); err != nil {
			t.Fatal(err)
		}
		_, _ = client.Write(packetBytes(t, &packet.Publish{
			ProtocolLevel: packet.ProtocolMQTT5, Topic: reportedTopic("dev"), Payload: []byte("nope"),
		}))
		disc, err := readLevel(client, packet.ProtocolMQTT5)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := disc.(*packet.Disconnect); !ok {
			t.Fatalf("%T", disc)
		}
		down()
	})

	t.Run("disabled", func(t *testing.T) {
		off := protoCloneDevice(base)
		off.Enabled = false
		devices := &scriptDevices{token: off}
		bus := useBridge(t, devices, nil)
		down := waitDown(t, bus)
		server, client := net.Pipe()
		defer client.Close()
		go HandleConn(server, &packet.Connect{ProtocolLevel: packet.ProtocolMQTT5, CleanStart: true, ClientID: "off"}, connectMeta{cleanStart: true, willTopic: desiredTopic("dev"), willMessage: []byte(`{"a":1}`)}, base)
		if _, err := readLevel(client, packet.ProtocolMQTT5); err != nil {
			t.Fatal(err)
		}
		disc, err := readLevel(client, packet.ProtocolMQTT5)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := disc.(*packet.Disconnect); !ok {
			t.Fatalf("%T", disc)
		}
		down()
	})

	t.Run("shadow misses", func(t *testing.T) {
		devices := &scriptDevices{token: base}
		shadows := &scriptShadow{gets: []shadowGet{
			{err: errors.New("no")},
			{res: &stpb.GetResponse{}},
			{res: &stpb.GetResponse{Shadows: []*stpb.Shadow{{Device: "dev"}}}},
		}}
		bus := useBridge(t, devices, shadows)
		for i := 0; i < 3; i++ {
			down := waitDown(t, bus)
			server, client := net.Pipe()
			go HandleConn(server, &packet.Connect{ProtocolLevel: packet.ProtocolMQTT5, CleanStart: true, ClientID: "sh"}, connectMeta{cleanStart: true}, base)
			if _, err := readLevel(client, packet.ProtocolMQTT5); err != nil {
				t.Fatal(err)
			}
			_, _ = client.Write(packetBytes(t, &packet.Subscribe{
				ProtocolLevel: packet.ProtocolMQTT5, PacketID: 1,
				Filters: []packet.Subscription{{Topic: desiredTopic("dev"), QoS: 0}},
			}))
			if _, err := readLevel(client, packet.ProtocolMQTT5); err != nil {
				t.Fatal(err)
			}
			_, _ = (&packet.Disconnect{ProtocolLevel: packet.ProtocolMQTT5}).WriteTo(client)
			client.Close()
			down()
		}
	})

	t.Run("no quota and will", func(t *testing.T) {
		open := protoCloneDevice(base)
		open.Quota = nil
		devices := &scriptDevices{token: open}
		bus := useBridge(t, devices, nil)
		down := waitDown(t, bus)
		ch := bus.Sub("mqtt.incoming")
		server, client := net.Pipe()
		go HandleConn(server, &packet.Connect{ProtocolLevel: packet.ProtocolMQTT311, CleanStart: true, ClientID: "will"}, connectMeta{
			cleanStart: true, willTopic: reportedTopic("dev"), willMessage: []byte(`{"bye":true}`),
		}, open)
		if _, err := readLevel(client, packet.ProtocolMQTT311); err != nil {
			t.Fatal(err)
		}
		_, _ = client.Write(packetBytes(t, &packet.Publish{
			ProtocolLevel: packet.ProtocolMQTT311, Topic: reportedTopic("dev"), Payload: []byte(`{"hi":1}`),
		}))
		client.Close()
		waitBus(t, ch, 2)
		down()
	})

	t.Run("device rate", func(t *testing.T) {
		limited := bridgeDevice("slow", 0.5, 100)
		devices := &scriptDevices{token: limited}
		bus := useBridge(t, devices, nil)
		down := waitDown(t, bus)
		server, client := net.Pipe()
		defer client.Close()
		go HandleConn(server, &packet.Connect{ProtocolLevel: packet.ProtocolMQTT5, CleanStart: true, ClientID: "slow"}, connectMeta{cleanStart: true}, limited)
		if _, err := readLevel(client, packet.ProtocolMQTT5); err != nil {
			t.Fatal(err)
		}
		_, _ = client.Write(packetBytes(t, &packet.Publish{
			ProtocolLevel: packet.ProtocolMQTT5, QoS: 0, Topic: reportedTopic("slow"), Payload: []byte(`{"t":1}`),
		}))
		disc, err := readLevel(client, packet.ProtocolMQTT5)
		if err != nil {
			t.Fatal(err)
		}
		if d, ok := disc.(*packet.Disconnect); !ok || d.Reason != packet.ReasonQuotaExceeded {
			t.Fatalf("%T %+v", disc, disc)
		}
		down()
	})
}

func protoCloneDevice(in *devpb.Device) *devpb.Device {
	return proto.Clone(in).(*devpb.Device)
}

func readLevel(c net.Conn, level byte) (packet.ControlPacket, error) {
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	return packet.ReadPacket(c, level, 0)
}

func mustPacket(t *testing.T, c net.Conn) packet.ControlPacket {
	t.Helper()
	pkt, err := readLevel(c, packet.ProtocolMQTT5)
	if err != nil {
		t.Fatal(err)
	}
	return pkt
}

func waitBus(t *testing.T, ch chan interface{}, n int) {
	t.Helper()
	got := 0
	deadline := time.After(2 * time.Second)
	for got < n {
		select {
		case <-ch:
			got++
		case <-deadline:
			t.Fatalf("bus messages %d", got)
		}
	}
}

func TestHandleBackChannelAndUnsub(t *testing.T) {
	data, err := structpb.NewStruct(map[string]any{"led": true})
	if err != nil {
		t.Fatal(err)
	}
	var got [][]byte
	var connected int
	ch := make(chan interface{}, 4)
	done := make(chan struct{})
	go func() {
		handleBackChannel(log, ch, func(p []byte, _ packet.Properties) { got = append(got, p) }, func() { connected++ })
		close(done)
	}()
	ch <- &stpb.Shadow{Device: "dev"}
	ch <- &stpb.Shadow{Device: "dev", Desired: &stpb.State{Timestamp: timestamppb.New(time.Unix(20, 0)), Data: data}}
	ch <- &stpb.Shadow{Device: "dev", Desired: &stpb.State{Timestamp: timestamppb.New(time.Unix(10, 0)), Data: data}}
	close(ch)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("back channel")
	}
	if connected != 1 || len(got) != 1 {
		t.Fatalf("connected %d payloads %d", connected, len(got))
	}
	idle := make(chan interface{})
	close(idle)
	handleBackChannel(log, idle, func([]byte, packet.Properties) {}, nil)

	bus := pubsub.New(2)
	sub := bus.Sub("mqtt.outgoing/dev")
	finished := make(chan struct{})
	go func() {
		unsub(bus, sub)
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("unsub did not return")
	}
}

func TestHandleTCPConnections(t *testing.T) {
	prevC, prevPS := client, ps
	t.Cleanup(func() { client, ps = prevC, prevPS })
	ps = pubsub.New(8)
	down := waitDown(t, ps)
	if internal_ctx == nil {
		internal_ctx = context.Background()
	}
	okDev := bridgeDevice("dev", 10, 10)
	client = &scriptDevices{
		token: okDev,
		fp: []fpCall{
			{err: errors.New("missing")},
			{dev: &devpb.Device{Uuid: "other", Enabled: true, BasicEnabled: true}},
			{dev: &devpb.Device{Uuid: "dev", Enabled: true, BasicEnabled: false}},
			{dev: &devpb.Device{Uuid: "dev", Enabled: false, BasicEnabled: true}},
			{dev: okDev},
		},
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() {
		HandleTCPConnections(ln)
		close(stopped)
	}()
	pass := base64.StdEncoding.EncodeToString([]byte{1, 2, 3})
	dial := func() net.Conn {
		t.Helper()
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	junk := dial()
	_, _ = junk.Write([]byte{0x01, 0x00})
	_ = junk.Close()

	send := func(user, password string) net.Conn {
		t.Helper()
		c := dial()
		raw := connectBytes(t, flagsConnect(packet.ProtocolMQTT311, 0xC2, 0, "cid", user, password, packet.ConnectProperties{}, "", nil))
		if _, err := c.Write(raw); err != nil {
			t.Fatal(err)
		}
		if _, err := readLevel(c, packet.ProtocolMQTT311); err != nil {
			t.Fatal(err)
		}
		return c
	}
	_ = send("dev", "")
	for i := 0; i < 5; i++ {
		c := send("dev", pass)
		if i == 4 {
			_, _ = (&packet.Disconnect{ProtocolLevel: packet.ProtocolMQTT311}).WriteTo(c)
			c.Close()
			down()
			continue
		}
		c.Close()
	}
	_ = ln.Close()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("accept loop did not return")
	}
}

func TestHandleTLSConn(t *testing.T) {
	cert, err := tls.LoadX509KeyPair(filepath.Join(coreRoot(t), "hack", "server.crt"), filepath.Join(coreRoot(t), "hack", "server.key"))
	if err != nil {
		t.Fatal(err)
	}
	t.Run("plain", func(t *testing.T) {
		defer func() { _ = recover() }()
		a, b := net.Pipe()
		defer b.Close()
		handleTLSConn(a)
		t.Fatal("plain connection did not panic")
	})
	t.Run("handshake", func(t *testing.T) {
		old := tlsHandshakeTimeout
		tlsHandshakeTimeout = 40 * time.Millisecond
		t.Cleanup(func() { tlsHandshakeTimeout = old })
		a, b := net.Pipe()
		defer b.Close()
		srv := tls.Server(a, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
		handleTLSConn(srv)
	})
	t.Run("not mqtt", func(t *testing.T) {
		a, b := net.Pipe()
		defer a.Close()
		srv := tls.Server(a, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
		go func() {
			c := tls.Client(b, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})
			if err := c.Handshake(); err != nil {
				return
			}
			_ = c.Close()
		}()
		handleTLSConn(srv)
	})
	t.Run("no client cert", func(t *testing.T) {
		a, b := net.Pipe()
		defer a.Close()
		srv := tls.Server(a, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
		go func() {
			c := tls.Client(b, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})
			if err := c.Handshake(); err != nil {
				return
			}
			raw := connectBytes(t, flagsConnect(packet.ProtocolMQTT5, 0xC2, 0, "cid", "dev", "pw", packet.ConnectProperties{}, "", nil))
			_, _ = c.Write(raw)
			_ = c.Close()
		}()
		handleTLSConn(srv)
	})
	t.Run("enabled and disabled", func(t *testing.T) {
		prevC, prevPS := client, ps
		t.Cleanup(func() { client, ps = prevC, prevPS })
		ps = pubsub.New(4)
		down := waitDown(t, ps)
		if internal_ctx == nil {
			internal_ctx = context.Background()
		}
		okDev := bridgeDevice("dev", 10, 10)
		client = &scriptDevices{
			token: okDev,
			fp: []fpCall{
				{dev: &devpb.Device{Uuid: "dev", Enabled: false}},
				{dev: okDev},
			},
		}
		ln, err := tls.Listen("tcp", "127.0.0.1:0", serverTLSConfig([]tls.Certificate{cert}))
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		dial := func() *tls.Conn {
			t.Helper()
			c, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
				InsecureSkipVerify: true,
				Certificates:       []tls.Certificate{newDeviceCert(t)},
				MinVersion:         tls.VersionTLS12,
			})
			if err != nil {
				t.Fatal(err)
			}
			return c
		}
		for _, enabled := range []bool{false, true} {
			accepted := make(chan struct{})
			go func() {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				close(accepted)
				handleTLSConn(c)
			}()
			c := dial()
			raw := connectBytes(t, flagsConnect(packet.ProtocolMQTT311, 0xC2, 0, "cid", "dev", "pw", packet.ConnectProperties{}, "", nil))
			if _, err := c.Write(raw); err != nil {
				t.Fatal(err)
			}
			if _, err := readLevel(c, packet.ProtocolMQTT311); err != nil {
				t.Fatal(err)
			}
			if enabled {
				_, _ = (&packet.Disconnect{ProtocolLevel: packet.ProtocolMQTT311}).WriteTo(c)
				c.Close()
				down()
			} else {
				c.Close()
			}
			select {
			case <-accepted:
			case <-time.After(2 * time.Second):
				t.Fatal("not accepted")
			}
		}
	})
}

func TestPrintConnStateAndCertEdges(t *testing.T) {
	root := coreRoot(t)
	cert, err := tls.LoadX509KeyPair(filepath.Join(root, "hack", "server.crt"), filepath.Join(root, "hack", "server.key"))
	if err != nil {
		t.Fatal(err)
	}
	a, b := net.Pipe()
	defer b.Close()
	srv := tls.Server(a, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	printConnState(srv)
	if _, err := clientFingerprint(srv); err == nil {
		t.Fatal("fingerprint without a peer cert")
	}
	_ = a.Close()

	if _, err := loadServerCerts(filepath.Join(t.TempDir(), "missing"), "", "", "", ""); err == nil {
		t.Fatal("missing acme")
	}
	if _, err := loadServerCerts("", filepath.Join(t.TempDir(), "no.crt"), filepath.Join(t.TempDir(), "no.key"), "", ""); err == nil {
		t.Fatal("missing pair")
	}
	certs, err := loadServerCerts("", filepath.Join(root, "hack", "server.crt"), filepath.Join(root, "hack", "server.key"), "", "")
	if err != nil || len(certs) != 1 {
		t.Fatalf("rsa only %v %v", certs, err)
	}
	if err := requireECDSA(tls.Certificate{}); err == nil {
		t.Fatal("empty cert")
	}
	if err := requireECDSA(tls.Certificate{Certificate: [][]byte{[]byte("no")}}); err == nil {
		t.Fatal("garbage cert")
	}
	if err := requireECDSA(tls.Certificate{Leaf: &x509.Certificate{PublicKeyAlgorithm: x509.RSA}}); err == nil {
		t.Fatal("rsa leaf accepted")
	}
}

func bufWrite() (func(func(io.Writer) error) error, *bytes.Buffer) {
	var buf bytes.Buffer
	return func(fn func(io.Writer) error) error { return fn(&buf) }, &buf
}

func openTestSession() *deviceSession {
	_, sess := newSessionStore().open("dev", "cid", connectMeta{cleanStart: true, receiveMax: 8}, time.Now())
	sess.qos2Max = 2
	sess.qos2Hold = time.Minute
	return sess
}

func TestAcceptPublishEdges(t *testing.T) {
	write, _ := bufWrite()
	sess := openTestSession()
	sess.aliasMax = 0
	bad := pubMsg{id: 1, qos: 1, topic: reportedTopic("dev"), payload: []byte(`{}`), props: packet.Properties{HasTopicAlias: true, TopicAlias: 1}}
	if acceptPublish(nil, write, packet.ProtocolMQTT5, "dev", sess, deviceHooks{}, bad) {
		t.Fatal("alias accepted")
	}
	sess.aliasMax = 4
	unknown := pubMsg{id: 2, qos: 0, props: packet.Properties{HasTopicAlias: true, TopicAlias: 2}}
	if acceptPublish(nil, write, packet.ProtocolMQTT5, "dev", sess, deviceHooks{}, unknown) {
		t.Fatal("unknown alias")
	}

	unauth := pubMsg{id: 3, qos: 2, topic: desiredTopic("dev"), payload: []byte(`{}`)}
	if !acceptPublish(nil, write, packet.ProtocolMQTT5, "dev", sess, deviceHooks{}, unauth) {
		t.Fatal("mqtt5 qos2 unauth")
	}
	sess.qos2Max = 0
	if !acceptPublish(nil, write, packet.ProtocolMQTT5, "dev", sess, deviceHooks{}, pubMsg{id: 4, qos: 2, topic: desiredTopic("dev")}) {
		t.Fatal("qos2 cap")
	}
	sess.qos2Max = 2
	if acceptPublish(nil, write, packet.ProtocolMQTT311, "dev", sess, deviceHooks{}, pubMsg{id: 5, qos: 0, topic: desiredTopic("dev")}) {
		t.Fatal("qos0 unauth")
	}
	if acceptPublish(nil, write, packet.ProtocolMQTT311, "dev", sess, deviceHooks{allow: func() bool { return false }}, pubMsg{id: 6, qos: 2, topic: reportedTopic("dev"), payload: []byte(`{}`)}) {
		t.Fatal("311 quota")
	}
	if !acceptPublish(nil, write, packet.ProtocolMQTT5, "dev", sess, deviceHooks{allow: func() bool { return false }}, pubMsg{id: 7, qos: 2, topic: reportedTopic("dev"), payload: []byte(`{}`)}) {
		t.Fatal("mqtt5 quota")
	}
	if acceptPublish(nil, write, packet.ProtocolMQTT5, "dev", sess, deviceHooks{allow: func() bool { return false }}, pubMsg{id: 8, qos: 0, topic: reportedTopic("dev"), payload: []byte(`{}`)}) {
		t.Fatal("qos0 quota")
	}
	exp := pubMsg{id: 9, qos: 2, topic: reportedTopic("dev"), payload: []byte(`{}`), props: packet.Properties{HasMessageExpiry: true}}
	if !acceptPublish(nil, write, packet.ProtocolMQTT5, "dev", sess, deviceHooks{reported: func([]byte, packet.Properties, bool) bool { return false }}, exp) {
		t.Fatal("expiry 0")
	}
	if !acceptPublish(nil, write, packet.ProtocolMQTT5, "dev", sess, deviceHooks{reported: func([]byte, packet.Properties, bool) bool { return true }}, pubMsg{id: 10, qos: 2, topic: reportedTopic("dev"), payload: []byte(`{}`)}) {
		t.Fatal("qos2 deliver")
	}
	sess.qos2Max = 0
	if acceptPublish(nil, write, packet.ProtocolMQTT311, "dev", sess, deviceHooks{}, pubMsg{id: 11, qos: 2, topic: reportedTopic("dev"), payload: []byte(`{}`)}) {
		t.Fatal("311 qos2 cap")
	}

	fail := func(func(io.Writer) error) error { return errors.New("write") }
	held := openTestSession()
	held.qos2Max = 0
	if _, open := holdQoS2(fail, packet.ProtocolMQTT311, held, pubMsg{id: 1, qos: 2}, false); open {
		t.Fatal("311 hold stayed open")
	}
	ok, _ := bufWrite()
	if _, open := holdQoS2(ok, packet.ProtocolMQTT5, held, pubMsg{id: 2, qos: 2}, false); !open {
		t.Fatal("mqtt5 hold closed")
	}
	if err := finishQoS2(fail, packet.ProtocolMQTT5, held, deviceHooks{}, 99); err == nil {
		t.Fatal("finish write")
	}
	if err := finishQoS2(ok, packet.ProtocolMQTT311, held, deviceHooks{reported: func([]byte, packet.Properties, bool) bool { return true }}, 2); err != nil {
		t.Fatal(err)
	}
	sendN := 0
	_ = handleAck(ok, packet.ProtocolMQTT5, held, func(pubMsg, bool) { sendN++ }, &packet.Ack{Kind: packet.PUBACK, PacketID: 1})
	held.out = []inflight{{id: 4, qos: 2}}
	_ = handleAck(ok, packet.ProtocolMQTT5, held, func(pubMsg, bool) { sendN++ }, &packet.Ack{Kind: packet.PUBREC, PacketID: 4})
	_ = handleAck(ok, packet.ProtocolMQTT5, held, func(pubMsg, bool) { sendN++ }, &packet.Ack{Kind: packet.PUBREC, PacketID: 99})
	_ = handleAck(ok, packet.ProtocolMQTT5, held, func(pubMsg, bool) { sendN++ }, &packet.Ack{Kind: packet.PUBCOMP, PacketID: 4})
	if sendN < 2 {
		t.Fatalf("acks %d", sendN)
	}

	ackW, _ := bufWrite()
	if ackPublish(ackW, packet.ProtocolMQTT5, pubMsg{qos: 0}, packet.ReasonSuccess) != true {
		t.Fatal("qos0")
	}
	if ackPublish(ackW, packet.ProtocolMQTT5, pubMsg{qos: 0}, packet.ReasonQuotaExceeded) {
		t.Fatal("qos0 fail")
	}
	if !ackPublish(ackW, packet.ProtocolMQTT311, pubMsg{qos: 1, id: 1}, packet.ReasonQuotaExceeded) {
		t.Fatal("311 qos1")
	}
	if !ackPublish(ackW, packet.ProtocolMQTT5, pubMsg{qos: 2, id: 1}, packet.ReasonSuccess) {
		t.Fatal("qos2")
	}
	if ackPublish(ackW, packet.ProtocolMQTT311, pubMsg{qos: 2, id: 1}, packet.ReasonQuotaExceeded) {
		t.Fatal("311 qos2 reason")
	}
	if ackPublish(ackW, packet.ProtocolMQTT5, pubMsg{qos: 3, id: 1}, packet.ReasonSuccess) {
		t.Fatal("qos3")
	}
	if pubSize(packet.ProtocolMQTT5, pubMsg{topic: "t", payload: []byte("a")}) == 0 {
		t.Fatal("size")
	}
	now := time.Now()
	if !sooner(time.Time{}, now).Equal(now) || !sooner(now.Add(time.Second), now).Equal(now) || !sooner(now, now.Add(time.Second)).Equal(now) {
		t.Fatal("sooner")
	}
}

type flipConn struct {
	net.Conn
	failWrite   bool
	failDeadlne bool
}

func (c *flipConn) Write(p []byte) (int, error) {
	if c.failWrite {
		return 0, errors.New("closed")
	}
	return c.Conn.Write(p)
}

func (c *flipConn) SetReadDeadline(ts time.Time) error {
	if c.failDeadlne {
		return errors.New("deadline")
	}
	return c.Conn.SetReadDeadline(ts)
}

func TestServeDeviceEdges(t *testing.T) {
	server, client := net.Pipe()
	client.Close()
	serveDevice(server, "dev", &packet.Connect{ProtocolLevel: packet.ProtocolMQTT5, ClientID: "cid", CleanStart: true}, connectMeta{cleanStart: true}, newSessionStore(), deviceHooks{})

	server, client = net.Pipe()
	wrapped := &flipConn{Conn: server, failDeadlne: true}
	willed := make(chan struct{}, 1)
	go serveDevice(wrapped, "dev", &packet.Connect{ProtocolLevel: packet.ProtocolMQTT5, ClientID: "cid", CleanStart: true}, connectMeta{cleanStart: true, willTopic: reportedTopic("dev"), willMessage: []byte("gone")}, newSessionStore(), deviceHooks{
		will: func(string, []byte, bool) { willed <- struct{}{} },
	})
	if _, err := readLevel(client, packet.ProtocolMQTT5); err != nil {
		t.Fatal(err)
	}
	select {
	case <-willed:
	case <-time.After(2 * time.Second):
		t.Fatal("will")
	}
	client.Close()

	oldMax := serverMaxPacketSize
	serverMaxPacketSize = 8
	t.Cleanup(func() { serverMaxPacketSize = oldMax })
	c, _ := startSession(t, packet.ProtocolMQTT5, 0xC2, "big", packet.ConnectProperties{}, newSessionStore(), deviceHooks{})
	defer c.conn.Close()
	c.send([]byte{0x30, 20})
	if disc, ok := c.next().(*packet.Disconnect); !ok || disc.Reason != packet.ReasonPacketTooLarge {
		t.Fatalf("too big %+v", disc)
	}

	c, _ = startSession(t, packet.ProtocolMQTT5, 0xC2, "bad", packet.ConnectProperties{}, newSessionStore(), deviceHooks{})
	defer c.conn.Close()
	c.send([]byte{0x80, 0x00})
	if _, ok := c.next().(*packet.Disconnect); !ok {
		t.Fatal("protocol")
	}

	c, _ = startSession(t, packet.ProtocolMQTT5, 0xC2, "auth", packet.ConnectProperties{}, newSessionStore(), deviceHooks{})
	defer c.conn.Close()
	c.send(packetBytes(t, &packet.Auth{Reason: 0}))
	if _, ok := c.next().(*packet.Disconnect); !ok {
		t.Fatal("auth")
	}
	serverMaxPacketSize = oldMax

	ready := make(chan func([]byte, packet.Properties), 1)
	c, _ = startSession(t, packet.ProtocolMQTT5, 0xC2, "sub", packet.ConnectProperties{MaxPacketSize: 1, ReceiveMaximum: 1}, newSessionStore(), deviceHooks{
		subscribed: func(s func([]byte, packet.Properties)) { ready <- s },
		unsub:      func() {},
	})
	defer c.conn.Close()
	c.send(packetBytes(t, &packet.Subscribe{
		ProtocolLevel: packet.ProtocolMQTT5, PacketID: 1,
		Filters: []packet.Subscription{{Topic: desiredTopic("dev"), QoS: 1}},
	}))
	if _, ok := c.next().(*packet.SubAck); !ok {
		t.Fatal("suback")
	}
	var send func([]byte, packet.Properties)
	select {
	case send = <-ready:
	case <-time.After(time.Second):
		t.Fatal("subscribe hook")
	}
	send([]byte(`{"n":1}`), packet.Properties{})
	send([]byte(`{"n":2}`), packet.Properties{})
	_ = c.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, _ = packet.ReadPacket(c.conn, packet.ProtocolMQTT5, 0)
	c.send(packetBytes(t, &packet.Unsubscribe{
		ProtocolLevel: packet.ProtocolMQTT5, PacketID: 2, Topics: []string{desiredTopic("dev")},
	}))
	_ = c.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := packet.ReadPacket(c.conn, packet.ProtocolMQTT5, 0); err != nil {
		t.Fatal(err)
	}
	send([]byte(`{"n":3}`), packet.Properties{})

	old := retryAfter
	retryAfter = 150 * time.Millisecond
	t.Cleanup(func() { retryAfter = old })
	c, _ = startSession(t, packet.ProtocolMQTT5, 0xC2, "rel", packet.ConnectProperties{}, newSessionStore(), deviceHooks{
		subscribed: func(s func([]byte, packet.Properties)) {
			s([]byte(`{"led":1}`), packet.Properties{})
		},
	})
	defer c.conn.Close()
	c.send(packetBytes(t, &packet.Subscribe{
		ProtocolLevel: packet.ProtocolMQTT5, PacketID: 1,
		Filters: []packet.Subscription{{Topic: desiredTopic("dev"), QoS: 2}},
	}))
	if _, ok := c.next().(*packet.SubAck); !ok {
		t.Fatal("suback")
	}
	pub := c.next().(*packet.Publish)
	c.send(packetBytes(t, &packet.Ack{ProtocolLevel: packet.ProtocolMQTT5, Kind: packet.PUBREC, PacketID: pub.PacketID}))
	rel := c.next().(*packet.Ack)
	if rel.Kind != packet.PUBREL {
		t.Fatalf("pubrel %+v", rel)
	}
	again := c.next().(*packet.Ack)
	if again.Kind != packet.PUBREL {
		t.Fatalf("retry %+v", again)
	}

	srv, raw := net.Pipe()
	fc := &flipConn{Conn: srv}
	go func() {
		cp, meta, err := readConnect(srv)
		if err != nil {
			return
		}
		fc.Conn = srv
		serveDevice(fc, "dev", cp, meta, newSessionStore(), deviceHooks{})
	}()
	pc := pipeClient{t, raw, packet.ProtocolMQTT5}
	pc.send(connectBytes(t, flagsConnect(packet.ProtocolMQTT5, 0xC2, 0, "ping", "dev", "pw", packet.ConnectProperties{}, "", nil)))
	_ = pc.next()
	fc.failWrite = true
	pc.send(packetBytes(t, &packet.PingReq{}))
	time.Sleep(50 * time.Millisecond)
	raw.Close()
}
