/*
Copyright © 2021-2023 Infinite Devices GmbH

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
package main

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/infinimesh/core/pkg/mqtt/packet"
)

func connectBytes(t *testing.T, cp *packet.Connect) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := cp.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func flagsConnect(level, flags byte, keep uint16, clientID, user, pass string, props packet.ConnectProperties, willTopic string, willMsg []byte) *packet.Connect {
	return &packet.Connect{
		ProtocolLevel: level,
		CleanStart:    flags&0x02 != 0,
		WillFlag:      flags&0x04 != 0,
		WillQoS:       (flags >> 3) & 0x03,
		WillRetain:    flags&0x20 != 0,
		PasswordFlag:  flags&0x40 != 0,
		UsernameFlag:  flags&0x80 != 0,
		KeepAlive:     keep,
		Properties:    props,
		ClientID:      clientID,
		WillTopic:     willTopic,
		WillMessage:   willMsg,
		Username:      user,
		Password:      pass,
	}
}

func TestParseConnect311SeparatesWillFromPassword(t *testing.T) {
	raw := connectBytes(t, flagsConnect(packet.ProtocolMQTT311, 0xC6, 30, "cid", "dev", "secret", packet.ConnectProperties{}, "devices/dev/state/reported", []byte("bye")))
	cp, meta, err := readConnect(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !meta.cleanStart || !cp.WillFlag {
		t.Fatalf("flags clean=%v will=%v", meta.cleanStart, cp.WillFlag)
	}
	if cp.Username != "dev" || cp.Password != "secret" {
		t.Fatalf("auth %q %q", cp.Username, cp.Password)
	}
	if meta.willTopic != "devices/dev/state/reported" || string(meta.willMessage) != "bye" {
		t.Fatalf("will %q %q", meta.willTopic, meta.willMessage)
	}
	if meta.sessionExpiry != 0 {
		t.Fatalf("clean session kept expiry %s", meta.sessionExpiry)
	}
}

func TestParseConnect5SessionAndUserProp(t *testing.T) {
	props := packet.ConnectProperties{
		HasSessionExpiry: true,
		SessionExpiry:    time.Minute,
		ReceiveMaximum:   8,
		User:             []packet.UserProperty{{Key: "act", Value: "iot"}},
	}
	raw := connectBytes(t, flagsConnect(packet.ProtocolMQTT5, 0xC0, 10, "cid", "dev", "pw", props, "", nil))
	cp, meta, err := readConnect(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if meta.cleanStart || meta.sessionExpiry != time.Minute || meta.receiveMax != 8 {
		t.Fatalf("meta %+v", meta)
	}
	if len(meta.user) != 1 || meta.user[0] != [2]string{"act", "iot"} {
		t.Fatalf("user %#v", meta.user)
	}
	if cp.ClientID != "cid" {
		t.Fatal(cp.ClientID)
	}
}

func TestConnAck311HasNoPropertyByte(t *testing.T) {
	var buf bytes.Buffer
	if err := writeConnAck(&buf, packet.ProtocolMQTT311, true, packet.ReasonSuccess, ""); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()
	want := []byte{0x20, 0x02, 0x01, 0x00}
	if !bytes.Equal(got, want) {
		t.Fatalf("connack %x", got)
	}
}

func TestConnAck311MapsMQTT5Reason(t *testing.T) {
	var buf bytes.Buffer
	if err := writeConnAck(&buf, packet.ProtocolMQTT311, false, packet.ReasonNotAuthorized, ""); err != nil {
		t.Fatal(err)
	}
	want := []byte{0x20, 0x02, 0x00, 0x05}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("connack %x", buf.Bytes())
	}
}

type pipeClient struct {
	t     *testing.T
	conn  net.Conn
	level byte
}

func (p pipeClient) send(b []byte) {
	p.t.Helper()
	if _, err := p.conn.Write(b); err != nil {
		p.t.Fatal(err)
	}
}

func (p pipeClient) next() packet.ControlPacket {
	p.t.Helper()
	_ = p.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	pkt, err := packet.ReadPacket(p.conn, p.level, 0)
	if err != nil {
		p.t.Fatal(err)
	}
	return pkt
}

func startSession(t *testing.T, level byte, flags byte, clientID string, props packet.ConnectProperties, store *sessionStore, hooks deviceHooks) (pipeClient, bool) {
	t.Helper()
	server, client := net.Pipe()
	go func() {
		cp, meta, err := readConnect(server)
		if err != nil {
			return
		}
		serveDevice(server, "dev", cp, meta, store, hooks)
	}()
	c := pipeClient{t, client, level}
	c.send(connectBytes(t, flagsConnect(level, flags, 0, clientID, "dev", "pw", props, "", nil)))
	pkt := c.next()
	ack, ok := pkt.(*packet.ConnAck)
	if !ok {
		t.Fatalf("connack %T", pkt)
	}
	return c, ack.SessionPresent
}

func packetBytes(t *testing.T, p packet.ControlPacket) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := p.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestServeRejectsDesiredPublishAndAcksReported(t *testing.T) {
	var got [][]byte
	store := newSessionStore()
	c, _ := startSession(t, packet.ProtocolMQTT5, 0xC2, "cid", packet.ConnectProperties{}, store, deviceHooks{
		reported: func(payload []byte, props packet.Properties, retain bool) bool {
			if props.ContentType != "application/json" || props.ResponseTopic != desiredTopic("dev") || string(props.CorrelationData) != "abc" {
				t.Errorf("props %+v", props)
			}
			if !retain {
				t.Error("retain")
			}
			got = append(got, append([]byte(nil), payload...))
			return true
		},
	})
	defer c.conn.Close()
	c.send(packetBytes(t, &packet.Publish{
		ProtocolLevel: packet.ProtocolMQTT5,
		Retain:        true,
		QoS:           1,
		Topic:         reportedTopic("dev"),
		PacketID:      4,
		Payload:       []byte(`{"t":1}`),
		Properties: packet.Properties{
			ContentType:     "application/json",
			ResponseTopic:   desiredTopic("dev"),
			CorrelationData: []byte("abc"),
		},
	}))
	ack := c.next().(*packet.Ack)
	if ack.Kind != packet.PUBACK || ack.Reason != packet.ReasonSuccess {
		t.Fatalf("puback %+v", ack)
	}
	c.send(packetBytes(t, &packet.Publish{
		ProtocolLevel: packet.ProtocolMQTT5,
		QoS:           1,
		Topic:         desiredTopic("dev"),
		PacketID:      5,
		Payload:       []byte(`{"t":1}`),
	}))
	ack = c.next().(*packet.Ack)
	if ack.Kind != packet.PUBACK || ack.Reason != packet.ReasonNotAuthorized {
		t.Fatalf("desired puback %+v", ack)
	}
	if len(got) != 1 || string(got[0]) != `{"t":1}` {
		t.Fatalf("stored %#v", got)
	}
}

func TestServeQoS2DeliversOnce(t *testing.T) {
	n := 0
	c, _ := startSession(t, packet.ProtocolMQTT311, 0xC2, "cid", packet.ConnectProperties{}, newSessionStore(), deviceHooks{
		reported: func(payload []byte, _ packet.Properties, _ bool) bool {
			n++
			return true
		},
	})
	defer c.conn.Close()
	pub := &packet.Publish{ProtocolLevel: packet.ProtocolMQTT311, QoS: 2, Topic: reportedTopic("dev"), PacketID: 3, Payload: []byte(`{"ok":true}`)}
	c.send(packetBytes(t, pub))
	ack := c.next().(*packet.Ack)
	if ack.Kind != packet.PUBREC {
		t.Fatalf("pubrec %+v", ack)
	}
	if n != 0 {
		t.Fatal("stored before PUBREL")
	}
	c.send(packetBytes(t, pub))
	ack = c.next().(*packet.Ack)
	if ack.Kind != packet.PUBREC {
		t.Fatalf("dup pubrec %+v", ack)
	}
	if n != 0 {
		t.Fatal("duplicate stored before PUBREL")
	}
	c.send(packetBytes(t, &packet.Ack{ProtocolLevel: packet.ProtocolMQTT311, Kind: packet.PUBREL, PacketID: 3}))
	ack = c.next().(*packet.Ack)
	if ack.Kind != packet.PUBCOMP || ack.PacketID != 3 {
		t.Fatalf("pubcomp %+v", ack)
	}
	if n != 1 {
		t.Fatalf("deliveries %d", n)
	}
}

func TestServeSubscribeGrantsDesiredOnly(t *testing.T) {
	c, _ := startSession(t, packet.ProtocolMQTT311, 0xC2, "cid", packet.ConnectProperties{}, newSessionStore(), deviceHooks{
		subscribed: func(send func([]byte, packet.Properties)) {
			send([]byte(`{"led":1}`), packet.Properties{})
		},
	})
	defer c.conn.Close()
	c.send(packetBytes(t, &packet.Subscribe{
		ProtocolLevel: packet.ProtocolMQTT311,
		PacketID:      1,
		Filters: []packet.Subscription{
			{Topic: desiredTopic("dev"), QoS: 1},
			{Topic: reportedTopic("dev"), QoS: 1},
		},
	}))
	ack := c.next().(*packet.SubAck)
	if len(ack.Reasons) != 2 || ack.Reasons[0] != packet.ReasonGrantedQoS1 || ack.Reasons[1] != packet.ReasonUnspecified {
		t.Fatalf("suback %v", ack.Reasons)
	}
	msg := c.next().(*packet.Publish)
	if msg.QoS != 1 || msg.Topic != desiredTopic("dev") || string(msg.Payload) != `{"led":1}` {
		t.Fatalf("down %+v", msg)
	}
}

func TestServeReconnectResendsInflight(t *testing.T) {
	store := newSessionStore()
	left := make(chan struct{}, 1)
	hooks := deviceHooks{
		connected: func(up bool) {
			if !up {
				select {
				case left <- struct{}{}:
				default:
				}
			}
		},
		subscribed: func(send func([]byte, packet.Properties)) {
			send([]byte(`{"led":1}`), packet.Properties{})
		},
	}
	c, present := startSession(t, packet.ProtocolMQTT311, 0xC0, "cid", packet.ConnectProperties{}, store, hooks)
	if present {
		t.Fatal("new session presented")
	}
	c.send(packetBytes(t, &packet.Subscribe{
		ProtocolLevel: packet.ProtocolMQTT311,
		PacketID:      1,
		Filters:       []packet.Subscription{{Topic: desiredTopic("dev"), QoS: 1}},
	}))
	if _, ok := c.next().(*packet.SubAck); !ok {
		t.Fatal("suback")
	}
	first := c.next().(*packet.Publish)
	if first.Dup {
		t.Fatal("first publish was a duplicate")
	}
	c.conn.Close()
	select {
	case <-left:
	case <-time.After(2 * time.Second):
		t.Fatal("first session did not end")
	}
	c, present = startSession(t, packet.ProtocolMQTT311, 0xC0, "cid", packet.ConnectProperties{}, store, hooks)
	defer c.conn.Close()
	if !present {
		t.Fatal("session not present")
	}
	msg := c.next().(*packet.Publish)
	if !msg.Dup || string(msg.Payload) != `{"led":1}` {
		t.Fatalf("replay dup %v payload %q", msg.Dup, msg.Payload)
	}
}

func TestServeWillOnDrop(t *testing.T) {
	server, client := net.Pipe()
	got := make(chan string, 1)
	go func() {
		cp, meta, err := readConnect(server)
		if err != nil {
			return
		}
		serveDevice(server, "dev", cp, meta, newSessionStore(), deviceHooks{
			will: func(topic string, payload []byte, _ bool) {
				got <- topic + " " + string(payload)
			},
		})
	}()
	raw := connectBytes(t, flagsConnect(packet.ProtocolMQTT311, 0xC6, 0, "cid", "dev", "pw", packet.ConnectProperties{}, reportedTopic("dev"), []byte("gone")))
	if _, err := client.Write(raw); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	pkt, err := packet.ReadPacket(client, packet.ProtocolMQTT311, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pkt.(*packet.ConnAck); !ok {
		t.Fatalf("%T", pkt)
	}
	client.Close()
	select {
	case msg := <-got:
		if msg != reportedTopic("dev")+" gone" {
			t.Fatal(msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("will not published")
	}
}

func TestServeKeepAliveExpires(t *testing.T) {
	server, client := net.Pipe()
	got := make(chan struct{}, 1)
	go func() {
		cp, meta, err := readConnect(server)
		if err != nil {
			return
		}
		serveDevice(server, "dev", cp, meta, newSessionStore(), deviceHooks{
			will: func(string, []byte, bool) { got <- struct{}{} },
		})
	}()
	raw := connectBytes(t, flagsConnect(packet.ProtocolMQTT5, 0xC6, 1, "cid", "dev", "pw", packet.ConnectProperties{}, reportedTopic("dev"), []byte("x")))
	if _, err := client.Write(raw); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := packet.ReadPacket(client, packet.ProtocolMQTT5, 0); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("keepalive did not expire the session")
	}
}

func TestServeTopicAlias(t *testing.T) {
	got := make(chan struct{}, 2)
	c, _ := startSession(t, packet.ProtocolMQTT5, 0xC2, "cid", packet.ConnectProperties{TopicAliasMax: 4}, newSessionStore(), deviceHooks{
		reported: func([]byte, packet.Properties, bool) bool {
			got <- struct{}{}
			return true
		},
	})
	defer c.conn.Close()
	alias := packet.Properties{HasTopicAlias: true, TopicAlias: 1}
	c.send(packetBytes(t, &packet.Publish{
		ProtocolLevel: packet.ProtocolMQTT5,
		Topic:         reportedTopic("dev"),
		Payload:       []byte(`{"a":1}`),
		Properties:    alias,
	}))
	c.send(packetBytes(t, &packet.Publish{
		ProtocolLevel: packet.ProtocolMQTT5,
		Payload:       []byte(`{"a":2}`),
		Properties:    alias,
	}))
	for i := 0; i < 2; i++ {
		select {
		case <-got:
		case <-time.After(2 * time.Second):
			t.Fatalf("alias deliveries %d", i)
		}
	}
}

func TestMessageExpiryZeroDropsPayload(t *testing.T) {
	n := 0
	c, _ := startSession(t, packet.ProtocolMQTT5, 0xC2, "cid", packet.ConnectProperties{}, newSessionStore(), deviceHooks{
		reported: func([]byte, packet.Properties, bool) bool {
			n++
			return true
		},
	})
	defer c.conn.Close()
	c.send(packetBytes(t, &packet.Publish{
		ProtocolLevel: packet.ProtocolMQTT5,
		QoS:           1,
		PacketID:      1,
		Topic:         reportedTopic("dev"),
		Payload:       []byte(`{"a":1}`),
		Properties:    packet.Properties{HasMessageExpiry: true},
	}))
	ack := c.next().(*packet.Ack)
	if ack.Kind != packet.PUBACK || ack.Reason != packet.ReasonSuccess {
		t.Fatalf("ack %+v", ack)
	}
	if n != 0 {
		t.Fatal("expired message stored")
	}
}

func TestMeta311CleanSessionZeroLastsOneHour(t *testing.T) {
	raw := connectBytes(t, flagsConnect(packet.ProtocolMQTT311, 0xC0, 10, "cid", "dev", "pw", packet.ConnectProperties{}, "", nil))
	_, meta, err := readConnect(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if meta.cleanStart || meta.sessionExpiry != time.Hour {
		t.Fatalf("meta %+v", meta)
	}
}

func TestMeta5WithoutExpiryDoesNotPersist(t *testing.T) {
	raw := connectBytes(t, flagsConnect(packet.ProtocolMQTT5, 0xC0, 10, "cid", "dev", "pw", packet.ConnectProperties{}, "", nil))
	_, meta, err := readConnect(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if meta.sessionExpiry != 0 {
		t.Fatalf("expiry %s", meta.sessionExpiry)
	}
}

func TestEmptyClientID311Rejected(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	go func() {
		cp, meta, err := readConnect(server)
		if err != nil {
			return
		}
		serveDevice(server, "dev", cp, meta, newSessionStore(), deviceHooks{})
	}()
	raw := connectBytes(t, flagsConnect(packet.ProtocolMQTT311, 0xC0, 0, "", "dev", "pw", packet.ConnectProperties{}, "", nil))
	if _, err := client.Write(raw); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	pkt, err := packet.ReadPacket(client, packet.ProtocolMQTT311, 0)
	if err != nil {
		t.Fatal(err)
	}
	ack := pkt.(*packet.ConnAck)
	if ack.Reason != packet.CodeIdentifierRejected {
		t.Fatalf("%+v", ack)
	}
}

func TestEmptyClientID5IsAssigned(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	go func() {
		cp, meta, err := readConnect(server)
		if err != nil {
			return
		}
		serveDevice(server, "dev", cp, meta, newSessionStore(), deviceHooks{})
	}()
	raw := connectBytes(t, flagsConnect(packet.ProtocolMQTT5, 0xC0, 0, "", "dev", "pw", packet.ConnectProperties{}, "", nil))
	if _, err := client.Write(raw); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	pkt, err := packet.ReadPacket(client, packet.ProtocolMQTT5, 0)
	if err != nil {
		t.Fatal(err)
	}
	ack := pkt.(*packet.ConnAck)
	if ack.Reason != packet.ReasonSuccess || ack.AssignedClientID != "dev" {
		t.Fatalf("%+v", ack)
	}
}

func Test311UnauthorizedQoS1Disconnects(t *testing.T) {
	n := 0
	c, _ := startSession(t, packet.ProtocolMQTT311, 0xC2, "cid", packet.ConnectProperties{}, newSessionStore(), deviceHooks{
		reported: func([]byte, packet.Properties, bool) bool {
			n++
			return true
		},
	})
	defer c.conn.Close()
	c.send(packetBytes(t, &packet.Publish{
		ProtocolLevel: packet.ProtocolMQTT311,
		QoS:           1,
		PacketID:      1,
		Topic:         desiredTopic("dev"),
		Payload:       []byte(`{"t":1}`),
	}))
	if _, ok := c.next().(*packet.Disconnect); !ok {
		t.Fatal("expected disconnect")
	}
	if n != 0 {
		t.Fatal("stored")
	}
}

func TestQoS0UnauthorizedDisconnects(t *testing.T) {
	c, _ := startSession(t, packet.ProtocolMQTT5, 0xC2, "cid", packet.ConnectProperties{}, newSessionStore(), deviceHooks{})
	defer c.conn.Close()
	c.send(packetBytes(t, &packet.Publish{
		ProtocolLevel: packet.ProtocolMQTT5,
		Topic:         desiredTopic("dev"),
		Payload:       []byte(`{"t":1}`),
	}))
	if _, ok := c.next().(*packet.Disconnect); !ok {
		t.Fatal("expected disconnect")
	}
}

func Test311BadJSONQoS1StillAcks(t *testing.T) {
	n := 0
	c, _ := startSession(t, packet.ProtocolMQTT311, 0xC2, "cid", packet.ConnectProperties{}, newSessionStore(), deviceHooks{
		reported: func([]byte, packet.Properties, bool) bool {
			n++
			return false
		},
	})
	defer c.conn.Close()
	c.send(packetBytes(t, &packet.Publish{
		ProtocolLevel: packet.ProtocolMQTT311,
		QoS:           1,
		PacketID:      1,
		Topic:         reportedTopic("dev"),
		Payload:       []byte(`not-json`),
	}))
	ack := c.next().(*packet.Ack)
	if ack.Kind != packet.PUBACK || ack.Reason != packet.ReasonSuccess || n != 1 {
		t.Fatalf("ack %+v calls %d", ack, n)
	}
}

func TestDisconnectDoesNotFireWill(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	willed := make(chan struct{}, 1)
	left := make(chan struct{})
	go func() {
		cp, meta, err := readConnect(server)
		if err != nil {
			return
		}
		serveDevice(server, "dev", cp, meta, newSessionStore(), deviceHooks{
			connected: func(up bool) {
				if !up {
					close(left)
				}
			},
			will: func(string, []byte, bool) { willed <- struct{}{} },
		})
	}()
	raw := connectBytes(t, flagsConnect(packet.ProtocolMQTT311, 0xC6, 0, "cid", "dev", "pw", packet.ConnectProperties{}, reportedTopic("dev"), []byte("gone")))
	if _, err := client.Write(raw); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := packet.ReadPacket(client, packet.ProtocolMQTT311, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := (&packet.Disconnect{ProtocolLevel: packet.ProtocolMQTT311}).WriteTo(client); err != nil {
		t.Fatal(err)
	}
	select {
	case <-left:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not end")
	}
	select {
	case <-willed:
		t.Fatal("will fired after disconnect")
	default:
	}
}

func TestMQTT5ExpiryZeroDropsInflight(t *testing.T) {
	store := newSessionStore()
	left := make(chan struct{}, 1)
	hooks := deviceHooks{
		connected: func(up bool) {
			if !up {
				select {
				case left <- struct{}{}:
				default:
				}
			}
		},
		subscribed: func(send func([]byte, packet.Properties)) {
			send([]byte(`{"led":1}`), packet.Properties{})
		},
	}
	c, present := startSession(t, packet.ProtocolMQTT5, 0xC0, "cid", packet.ConnectProperties{}, store, hooks)
	if present {
		t.Fatal("new session presented")
	}
	c.send(packetBytes(t, &packet.Subscribe{
		ProtocolLevel: packet.ProtocolMQTT5,
		PacketID:      1,
		Filters:       []packet.Subscription{{Topic: desiredTopic("dev"), QoS: 1}},
	}))
	if _, ok := c.next().(*packet.SubAck); !ok {
		t.Fatal("suback")
	}
	if _, ok := c.next().(*packet.Publish); !ok {
		t.Fatal("publish")
	}
	if _, err := (&packet.Disconnect{ProtocolLevel: packet.ProtocolMQTT5}).WriteTo(c.conn); err != nil {
		t.Fatal(err)
	}
	select {
	case <-left:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not end")
	}
	c.conn.Close()
	c, present = startSession(t, packet.ProtocolMQTT5, 0xC0, "cid", packet.ConnectProperties{}, store, hooks)
	defer c.conn.Close()
	if present {
		t.Fatal("expiry 0 resumed the session")
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, err := packet.ReadPacket(c.conn, packet.ProtocolMQTT5, 0); err == nil {
		t.Fatal("inflight was resent")
	}
}

func TestBadJSONIsNotStored(t *testing.T) {
	n := 0
	c, _ := startSession(t, packet.ProtocolMQTT5, 0xC2, "cid", packet.ConnectProperties{}, newSessionStore(), deviceHooks{
		reported: func([]byte, packet.Properties, bool) bool {
			n++
			return false
		},
	})
	defer c.conn.Close()
	c.send(packetBytes(t, &packet.Publish{
		ProtocolLevel: packet.ProtocolMQTT5,
		QoS:           1,
		PacketID:      1,
		Topic:         reportedTopic("dev"),
		Payload:       []byte(`not-json`),
	}))
	ack := c.next().(*packet.Ack)
	if ack.Kind != packet.PUBACK || ack.Reason != packet.ReasonPayloadInvalid || n != 1 {
		t.Fatalf("ack %+v calls %d", ack, n)
	}
}

func TestServeConnAckAdvertisesServerLimits(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	go func() {
		cp, meta, err := readConnect(server)
		if err != nil {
			return
		}
		serveDevice(server, "dev", cp, meta, newSessionStore(), deviceHooks{})
	}()
	raw := connectBytes(t, flagsConnect(packet.ProtocolMQTT5, 0xC2, 0, "cid", "dev", "pw", packet.ConnectProperties{TopicAliasMax: 1}, "", nil))
	if _, err := client.Write(raw); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	pkt, err := packet.ReadPacket(client, packet.ProtocolMQTT5, 0)
	if err != nil {
		t.Fatal(err)
	}
	ack := pkt.(*packet.ConnAck)
	if ack.ReceiveMaximum != uint16(serverReceiveMax) || ack.MaxPacketSize != uint32(serverMaxPacketSize) || ack.TopicAliasMax != uint16(serverTopicAliasMax) {
		t.Fatalf("%+v", ack)
	}
}

func TestInboundAliasIgnoresClientMaximum(t *testing.T) {
	got := make(chan struct{}, 2)
	c, _ := startSession(t, packet.ProtocolMQTT5, 0xC2, "cid", packet.ConnectProperties{TopicAliasMax: 0}, newSessionStore(), deviceHooks{
		reported: func([]byte, packet.Properties, bool) bool {
			got <- struct{}{}
			return true
		},
	})
	defer c.conn.Close()
	alias := packet.Properties{HasTopicAlias: true, TopicAlias: 1}
	c.send(packetBytes(t, &packet.Publish{
		ProtocolLevel: packet.ProtocolMQTT5,
		Topic:         reportedTopic("dev"),
		Payload:       []byte(`{"a":1}`),
		Properties:    alias,
	}))
	c.send(packetBytes(t, &packet.Publish{
		ProtocolLevel: packet.ProtocolMQTT5,
		Payload:       []byte(`{"a":2}`),
		Properties:    alias,
	}))
	for i := 0; i < 2; i++ {
		select {
		case <-got:
		case <-time.After(2 * time.Second):
			t.Fatalf("alias deliveries %d", i)
		}
	}
}

func TestAliasAboveServerMaximumDisconnects(t *testing.T) {
	c, _ := startSession(t, packet.ProtocolMQTT5, 0xC2, "cid", packet.ConnectProperties{TopicAliasMax: 100}, newSessionStore(), deviceHooks{})
	defer c.conn.Close()
	c.send(packetBytes(t, &packet.Publish{
		ProtocolLevel: packet.ProtocolMQTT5,
		Topic:         reportedTopic("dev"),
		Payload:       []byte(`{"a":1}`),
		Properties:    packet.Properties{HasTopicAlias: true, TopicAlias: uint16(serverTopicAliasMax + 1)},
	}))
	disc, ok := c.next().(*packet.Disconnect)
	if !ok || disc.Reason != packet.ReasonTopicAliasInvalid {
		t.Fatalf("disconnect %+v", disc)
	}
}

func TestUnknownTopicAliasDisconnects(t *testing.T) {
	c, _ := startSession(t, packet.ProtocolMQTT5, 0xC2, "cid", packet.ConnectProperties{}, newSessionStore(), deviceHooks{})
	defer c.conn.Close()
	c.send(packetBytes(t, &packet.Publish{
		ProtocolLevel: packet.ProtocolMQTT5,
		Payload:       []byte(`{"a":1}`),
		Properties:    packet.Properties{HasTopicAlias: true, TopicAlias: 2},
	}))
	disc, ok := c.next().(*packet.Disconnect)
	if !ok || disc.Reason != packet.ReasonTopicAliasInvalid {
		t.Fatalf("disconnect %+v", disc)
	}
}

func TestAliasZeroDisconnects(t *testing.T) {
	c, _ := startSession(t, packet.ProtocolMQTT5, 0xC2, "cid", packet.ConnectProperties{}, newSessionStore(), deviceHooks{})
	defer c.conn.Close()
	c.send(packetBytes(t, &packet.Publish{
		ProtocolLevel: packet.ProtocolMQTT5,
		Topic:         reportedTopic("dev"),
		Payload:       []byte(`{"a":1}`),
		Properties:    packet.Properties{HasTopicAlias: true},
	}))
	disc, ok := c.next().(*packet.Disconnect)
	if !ok || disc.Reason != packet.ReasonTopicAliasInvalid {
		t.Fatalf("disconnect %+v", disc)
	}
}

func TestDesiredKeepsPropertiesOnRetry(t *testing.T) {
	old := retryAfter
	retryAfter = 200 * time.Millisecond
	t.Cleanup(func() { retryAfter = old })
	want := packet.Properties{
		ContentType:     "text/plain",
		ResponseTopic:   "reply/dev",
		CorrelationData: []byte{1, 2, 3},
		User:            []packet.UserProperty{{Key: "k", Value: "v"}},
	}
	c, _ := startSession(t, packet.ProtocolMQTT5, 0xC2, "cid", packet.ConnectProperties{}, newSessionStore(), deviceHooks{
		subscribed: func(send func([]byte, packet.Properties)) {
			send([]byte(`{"led":1}`), want)
		},
	})
	defer c.conn.Close()
	c.send(packetBytes(t, &packet.Subscribe{
		ProtocolLevel: packet.ProtocolMQTT5,
		PacketID:      1,
		Filters:       []packet.Subscription{{Topic: desiredTopic("dev"), QoS: 1}},
	}))
	if _, ok := c.next().(*packet.SubAck); !ok {
		t.Fatal("suback")
	}
	check := func(dup bool) {
		t.Helper()
		msg := c.next().(*packet.Publish)
		if msg.Dup != dup || msg.Properties.ContentType != want.ContentType || msg.Properties.ResponseTopic != want.ResponseTopic {
			t.Fatalf("dup %v props %+v", msg.Dup, msg.Properties)
		}
		if !bytes.Equal(msg.Properties.CorrelationData, want.CorrelationData) || len(msg.Properties.User) != 1 || msg.Properties.User[0] != want.User[0] {
			t.Fatalf("props %+v", msg.Properties)
		}
	}
	check(false)
	check(true)
}

func TestDesiredDefaultsContentType(t *testing.T) {
	c, _ := startSession(t, packet.ProtocolMQTT5, 0xC2, "cid", packet.ConnectProperties{}, newSessionStore(), deviceHooks{
		subscribed: func(send func([]byte, packet.Properties)) {
			send([]byte(`{"led":1}`), packet.Properties{})
		},
	})
	defer c.conn.Close()
	c.send(packetBytes(t, &packet.Subscribe{
		ProtocolLevel: packet.ProtocolMQTT5,
		PacketID:      1,
		Filters:       []packet.Subscription{{Topic: desiredTopic("dev"), QoS: 0}},
	}))
	if _, ok := c.next().(*packet.SubAck); !ok {
		t.Fatal("suback")
	}
	msg := c.next().(*packet.Publish)
	if msg.Properties.ContentType != "application/json" {
		t.Fatalf("content type %q", msg.Properties.ContentType)
	}
}

func TestQueuedMessageExpiresBeforeDelivery(t *testing.T) {
	c, _ := startSession(t, packet.ProtocolMQTT5, 0xC2, "cid", packet.ConnectProperties{ReceiveMaximum: 1}, newSessionStore(), deviceHooks{
		subscribed: func(send func([]byte, packet.Properties)) {
			send([]byte(`{"n":1}`), packet.Properties{})
			send([]byte(`{"n":2}`), packet.Properties{HasMessageExpiry: true, MessageExpiry: 200 * time.Millisecond})
		},
	})
	defer c.conn.Close()
	c.send(packetBytes(t, &packet.Subscribe{
		ProtocolLevel: packet.ProtocolMQTT5,
		PacketID:      7,
		Filters:       []packet.Subscription{{Topic: desiredTopic("dev"), QoS: 1}},
	}))
	if _, ok := c.next().(*packet.SubAck); !ok {
		t.Fatal("suback")
	}
	first := c.next().(*packet.Publish)
	if string(first.Payload) != `{"n":1}` {
		t.Fatalf("first %q", first.Payload)
	}
	time.Sleep(400 * time.Millisecond)
	c.send(packetBytes(t, &packet.Ack{ProtocolLevel: packet.ProtocolMQTT5, Kind: packet.PUBACK, PacketID: first.PacketID}))
	_ = c.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err := packet.ReadPacket(c.conn, packet.ProtocolMQTT5, 0); err == nil {
		t.Fatal("expired message delivered")
	}
}

func TestQueuedMessageDeliveredBeforeExpiry(t *testing.T) {
	c, _ := startSession(t, packet.ProtocolMQTT5, 0xC2, "cid", packet.ConnectProperties{ReceiveMaximum: 1}, newSessionStore(), deviceHooks{
		subscribed: func(send func([]byte, packet.Properties)) {
			send([]byte(`{"n":1}`), packet.Properties{})
			send([]byte(`{"n":2}`), packet.Properties{HasMessageExpiry: true, MessageExpiry: 30 * time.Second})
		},
	})
	defer c.conn.Close()
	c.send(packetBytes(t, &packet.Subscribe{
		ProtocolLevel: packet.ProtocolMQTT5,
		PacketID:      7,
		Filters:       []packet.Subscription{{Topic: desiredTopic("dev"), QoS: 1}},
	}))
	if _, ok := c.next().(*packet.SubAck); !ok {
		t.Fatal("suback")
	}
	first := c.next().(*packet.Publish)
	c.send(packetBytes(t, &packet.Ack{ProtocolLevel: packet.ProtocolMQTT5, Kind: packet.PUBACK, PacketID: first.PacketID}))
	second := c.next().(*packet.Publish)
	if string(second.Payload) != `{"n":2}` || !second.Properties.HasMessageExpiry || second.Properties.MessageExpiry <= 0 {
		t.Fatalf("second %+v", second)
	}
}

func TestQoS2ReceiveMaximum(t *testing.T) {
	old := serverReceiveMax
	serverReceiveMax = 2
	t.Cleanup(func() { serverReceiveMax = old })
	n := 0
	c, _ := startSession(t, packet.ProtocolMQTT5, 0xC2, "cid", packet.ConnectProperties{}, newSessionStore(), deviceHooks{
		reported: func([]byte, packet.Properties, bool) bool {
			n++
			return true
		},
	})
	defer c.conn.Close()
	publish := func(id uint16) {
		t.Helper()
		c.send(packetBytes(t, &packet.Publish{
			ProtocolLevel: packet.ProtocolMQTT5,
			QoS:           2,
			PacketID:      id,
			Topic:         reportedTopic("dev"),
			Payload:       []byte(`{"ok":true}`),
		}))
	}
	for id := uint16(1); id <= 2; id++ {
		publish(id)
		ack := c.next().(*packet.Ack)
		if ack.Kind != packet.PUBREC || ack.Reason != packet.ReasonSuccess || ack.PacketID != id {
			t.Fatalf("pubrec %+v", ack)
		}
	}
	if n != 0 {
		t.Fatal("stored before PUBREL")
	}
	publish(3)
	ack := c.next().(*packet.Ack)
	if ack.Kind != packet.PUBREC || ack.Reason != packet.ReasonReceiveMaximum || ack.PacketID != 3 {
		t.Fatalf("over cap %+v", ack)
	}
	c.send(packetBytes(t, &packet.Ack{ProtocolLevel: packet.ProtocolMQTT5, Kind: packet.PUBREL, PacketID: 1}))
	ack = c.next().(*packet.Ack)
	if ack.Kind != packet.PUBCOMP || ack.Reason != packet.ReasonSuccess || n != 1 {
		t.Fatalf("pubcomp %+v deliveries %d", ack, n)
	}
	c.send(packetBytes(t, &packet.Ack{ProtocolLevel: packet.ProtocolMQTT5, Kind: packet.PUBREL, PacketID: 3}))
	ack = c.next().(*packet.Ack)
	if ack.Kind != packet.PUBCOMP || ack.Reason != packet.ReasonPacketIDNotFound || n != 1 {
		t.Fatalf("missing %+v deliveries %d", ack, n)
	}
}

func TestQoS2HoldExpires(t *testing.T) {
	old := qos2HoldFor
	qos2HoldFor = 200 * time.Millisecond
	t.Cleanup(func() { qos2HoldFor = old })
	n := 0
	c, _ := startSession(t, packet.ProtocolMQTT5, 0xC2, "cid", packet.ConnectProperties{}, newSessionStore(), deviceHooks{
		reported: func([]byte, packet.Properties, bool) bool {
			n++
			return true
		},
	})
	defer c.conn.Close()
	c.send(packetBytes(t, &packet.Publish{
		ProtocolLevel: packet.ProtocolMQTT5,
		QoS:           2,
		PacketID:      4,
		Topic:         reportedTopic("dev"),
		Payload:       []byte(`{"ok":true}`),
	}))
	ack := c.next().(*packet.Ack)
	if ack.Kind != packet.PUBREC || ack.Reason != packet.ReasonSuccess {
		t.Fatalf("pubrec %+v", ack)
	}
	time.Sleep(400 * time.Millisecond)
	c.send(packetBytes(t, &packet.Ack{ProtocolLevel: packet.ProtocolMQTT5, Kind: packet.PUBREL, PacketID: 4}))
	ack = c.next().(*packet.Ack)
	if ack.Kind != packet.PUBCOMP || ack.Reason != packet.ReasonPacketIDNotFound || n != 0 {
		t.Fatalf("pubcomp %+v deliveries %d", ack, n)
	}
}

func TestQoS2MessageExpiryDropsBeforePubRel(t *testing.T) {
	n := 0
	c, _ := startSession(t, packet.ProtocolMQTT5, 0xC2, "cid", packet.ConnectProperties{}, newSessionStore(), deviceHooks{
		reported: func([]byte, packet.Properties, bool) bool {
			n++
			return true
		},
	})
	defer c.conn.Close()
	c.send(packetBytes(t, &packet.Publish{
		ProtocolLevel: packet.ProtocolMQTT5,
		QoS:           2,
		PacketID:      5,
		Topic:         reportedTopic("dev"),
		Payload:       []byte(`{"ok":true}`),
		Properties:    packet.Properties{HasMessageExpiry: true, MessageExpiry: time.Second},
	}))
	ack := c.next().(*packet.Ack)
	if ack.Kind != packet.PUBREC || ack.Reason != packet.ReasonSuccess || n != 0 {
		t.Fatalf("pubrec %+v deliveries %d", ack, n)
	}
	time.Sleep(1500 * time.Millisecond)
	c.send(packetBytes(t, &packet.Ack{ProtocolLevel: packet.ProtocolMQTT5, Kind: packet.PUBREL, PacketID: 5}))
	ack = c.next().(*packet.Ack)
	if ack.Kind != packet.PUBCOMP || ack.Reason != packet.ReasonPacketIDNotFound || n != 0 {
		t.Fatalf("pubcomp %+v deliveries %d", ack, n)
	}
}

func TestDisabledDeviceDisconnectsWithoutWill(t *testing.T) {
	old := enabledCheckAfter
	enabledCheckAfter = 150 * time.Millisecond
	t.Cleanup(func() { enabledCheckAfter = old })
	server, client := net.Pipe()
	defer client.Close()
	willed := make(chan struct{}, 1)
	calls := 0
	go func() {
		cp, meta, err := readConnect(server)
		if err != nil {
			return
		}
		serveDevice(server, "dev", cp, meta, newSessionStore(), deviceHooks{
			enabled: func() bool {
				calls++
				return calls == 1
			},
			will: func(string, []byte, bool) { willed <- struct{}{} },
		})
	}()
	raw := connectBytes(t, flagsConnect(packet.ProtocolMQTT5, 0xC6, 0, "cid", "dev", "pw", packet.ConnectProperties{}, reportedTopic("dev"), []byte("gone")))
	if _, err := client.Write(raw); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := packet.ReadPacket(client, packet.ProtocolMQTT5, 0); err != nil {
		t.Fatal(err)
	}
	pkt, err := packet.ReadPacket(client, packet.ProtocolMQTT5, 0)
	if err != nil {
		t.Fatal(err)
	}
	disc, ok := pkt.(*packet.Disconnect)
	if !ok || disc.Reason != packet.ReasonNotAuthorized {
		t.Fatalf("%T %+v", pkt, pkt)
	}
	select {
	case <-willed:
		t.Fatal("will stored for a disabled device")
	default:
	}
}
