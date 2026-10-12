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
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/slntopp/mqtt-go/packet"
)

func TestParseConnect311SeparatesWillFromPassword(t *testing.T) {
	raw := writeConnect(protoLevel311, 0xC6, 30, "cid", "dev", "secret", nil, "devices/dev/state/reported", []byte("bye"))
	r := bytes.NewReader(raw)
	cp, meta, err := readConnect(r)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.cleanStart || !cp.VariableHeader.ConnectFlags.WillFlag {
		t.Fatalf("flags clean=%v will=%v", meta.cleanStart, cp.VariableHeader.ConnectFlags.WillFlag)
	}
	if cp.ConnectPayload.Username != "dev" || cp.ConnectPayload.Password != "secret" {
		t.Fatalf("auth %+v", cp.ConnectPayload)
	}
	if meta.willTopic != "devices/dev/state/reported" || string(meta.willMessage) != "bye" {
		t.Fatalf("will %q %q", meta.willTopic, meta.willMessage)
	}
	if meta.sessionExpiry != 0 {
		t.Fatalf("clean session kept expiry %s", meta.sessionExpiry)
	}
}

func TestParseConnect5SessionAndUserProp(t *testing.T) {
	props := []byte{0x11, 0, 0, 0, 60, 0x21, 0, 8, 0x26, 0, 3, 'a', 'c', 't', 0, 3, 'i', 'o', 't'}
	raw := writeConnect(protoLevel5, 0xC0, 10, "cid", "dev", "pw", props, "", nil)
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
	if cp.ConnectPayload.ClientID != "cid" {
		t.Fatal(cp.ConnectPayload.ClientID)
	}
}

func TestConnAck311HasNoPropertyByte(t *testing.T) {
	var buf bytes.Buffer
	if err := writeConnAck(&buf, protoLevel311, true, reasonSuccess, ""); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()
	want := []byte{0x20, 0x02, 0x01, 0x00}
	if !bytes.Equal(got, want) {
		t.Fatalf("connack %x", got)
	}
}

func TestPubAckHasFixedHeader(t *testing.T) {
	var buf bytes.Buffer
	if err := writeAck(&buf, protoLevel311, packet.PUBACK, 7, reasonSuccess); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()
	if len(got) != 4 || got[0] != 0x40 || got[1] != 0x02 || got[3] != 7 {
		t.Fatalf("puback %x", got)
	}
}

func TestPublishQoS1RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	msg := pubMsg{topic: "devices/dev/state/reported", payload: []byte(`{"t":1}`), qos: 1, id: 9, retain: true}
	if err := writePub(&buf, protoLevel5, msg); err != nil {
		t.Fatal(err)
	}
	typ, flags, body, err := readFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parsePublish(protoLevel5, flags, body)
	if err != nil {
		t.Fatal(err)
	}
	if typ != packet.PUBLISH || !parsed.retain || parsed.id != 9 || string(parsed.payload) != `{"t":1}` {
		t.Fatalf("parsed %+v", parsed)
	}
}

type pipeClient struct {
	t    *testing.T
	conn net.Conn
}

func (p pipeClient) send(b []byte) {
	p.t.Helper()
	if _, err := p.conn.Write(b); err != nil {
		p.t.Fatal(err)
	}
}

func (p pipeClient) frame() (byte, byte, []byte) {
	p.t.Helper()
	_ = p.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	typ, flags, body, err := readFrame(p.conn)
	if err != nil {
		p.t.Fatal(err)
	}
	return typ, flags, body
}

func startSession(t *testing.T, level byte, flags byte, clientID string, props []byte, store *sessionStore, hooks deviceHooks) (pipeClient, byte) {
	t.Helper()
	server, client := net.Pipe()
	go func() {
		cp, meta, err := readConnect(server)
		if err != nil {
			return
		}
		serveDevice(server, "dev", cp, meta, store, hooks)
	}()
	c := pipeClient{t, client}
	c.send(writeConnect(level, flags, 0, clientID, "dev", "pw", props, "", nil))
	typ, _, body := c.frame()
	if typ != packet.CONNACK || len(body) < 2 {
		t.Fatalf("connack type %d body %x", typ, body)
	}
	return c, body[0]
}

func publishPacket(level byte, topic string, payload []byte, qos byte, id uint16, props []byte) []byte {
	body := appendMQTTString(nil, topic)
	if qos > 0 {
		b := make([]byte, 2)
		binary.BigEndian.PutUint16(b, id)
		body = append(body, b...)
	}
	if level == protoLevel5 {
		if props == nil {
			props = []byte{}
		}
		body = append(body, writeRemaining(len(props))...)
		body = append(body, props...)
	}
	body = append(body, payload...)
	first := byte(packet.PUBLISH<<4) | qos<<1
	out := []byte{first}
	out = append(out, writeRemaining(len(body))...)
	return append(out, body...)
}

func subscribePacket(level byte, id uint16, topic string, qos byte) []byte {
	body := make([]byte, 2)
	binary.BigEndian.PutUint16(body, id)
	if level == protoLevel5 {
		body = append(body, 0)
	}
	body = appendMQTTString(body, topic)
	body = append(body, qos)
	out := []byte{packet.SUBSCRIBE<<4 | 0x02}
	out = append(out, writeRemaining(len(body))...)
	return append(out, body...)
}

func TestServeRejectsDesiredPublishAndAcksReported(t *testing.T) {
	var got [][]byte
	store := newSessionStore()
	c, _ := startSession(t, protoLevel5, 0xC2, "cid", nil, store, deviceHooks{
		reported: func(payload []byte, props mqttProps, retain bool) bool {
			if props.contentType != "application/json" || props.responseTopic != desiredTopic("dev") || string(props.correlation) != "abc" {
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
	props := []byte{0x03, 0, 16}
	props = append(props, []byte("application/json")...)
	props = append(props, 0x08)
	props = appendMQTTString(props, desiredTopic("dev"))
	props = append(props, 0x09, 0, 3, 'a', 'b', 'c')
	pub := publishPacket(protoLevel5, reportedTopic("dev"), []byte(`{"t":1}`), 1, 4, props)
	pub[0] |= 0x01
	c.send(pub)
	typ, _, body := c.frame()
	if typ != packet.PUBACK || body[2] != reasonSuccess {
		t.Fatalf("puback %d %x", typ, body)
	}
	c.send(publishPacket(protoLevel5, desiredTopic("dev"), []byte(`{"t":1}`), 1, 5, nil))
	typ, _, body = c.frame()
	if typ != packet.PUBACK || body[2] != reasonNotAuthorized {
		t.Fatalf("desired puback %d %x", typ, body)
	}
	if len(got) != 1 || string(got[0]) != `{"t":1}` {
		t.Fatalf("stored %#v", got)
	}
}

func TestServeQoS2DeliversOnce(t *testing.T) {
	n := 0
	c, _ := startSession(t, protoLevel311, 0xC2, "cid", nil, newSessionStore(), deviceHooks{
		reported: func(payload []byte, _ mqttProps, _ bool) bool {
			n++
			return true
		},
	})
	defer c.conn.Close()
	c.send(publishPacket(protoLevel311, reportedTopic("dev"), []byte(`{"ok":true}`), 2, 3, nil))
	typ, _, body := c.frame()
	if typ != packet.PUBREC {
		t.Fatalf("pubrec %d %x", typ, body)
	}
	c.send(publishPacket(protoLevel311, reportedTopic("dev"), []byte(`{"ok":true}`), 2, 3, nil))
	typ, _, _ = c.frame()
	if typ != packet.PUBREC {
		t.Fatalf("dup pubrec %d", typ)
	}
	rel := []byte{packet.PUBREL<<4 | 0x02, 0x02, 0, 3}
	c.send(rel)
	typ, flags, body := c.frame()
	if typ != packet.PUBCOMP || flags != 0 {
		t.Fatalf("pubcomp %d %x %x", typ, flags, body)
	}
	if n != 1 {
		t.Fatalf("deliveries %d", n)
	}
}

func TestServeSubscribeGrantsDesiredOnly(t *testing.T) {
	var qos byte = 9
	c, _ := startSession(t, protoLevel311, 0xC2, "cid", nil, newSessionStore(), deviceHooks{
		subscribed: func(send func([]byte)) {
			send([]byte(`{"led":1}`))
		},
	})
	defer c.conn.Close()
	body := make([]byte, 2)
	binary.BigEndian.PutUint16(body, 1)
	body = appendMQTTString(body, desiredTopic("dev"))
	body = append(body, 1)
	body = appendMQTTString(body, reportedTopic("dev"))
	body = append(body, 1)
	pkt := []byte{packet.SUBSCRIBE<<4 | 0x02}
	pkt = append(pkt, writeRemaining(len(body))...)
	pkt = append(pkt, body...)
	c.send(pkt)
	typ, _, ack := c.frame()
	if typ != packet.SUBACK || ack[2] != reasonGrantedQoS1 || ack[3] != reasonUnspecified {
		t.Fatalf("suback %d %x", typ, ack)
	}
	typ, flags, pub := c.frame()
	msg, err := parsePublish(protoLevel311, flags, pub)
	if err != nil || typ != packet.PUBLISH {
		t.Fatal(err)
	}
	qos = msg.qos
	if qos != 1 || msg.topic != desiredTopic("dev") || string(msg.payload) != `{"led":1}` {
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
		subscribed: func(send func([]byte)) {
			send([]byte(`{"led":1}`))
		},
	}
	c, ackFlags := startSession(t, protoLevel311, 0xC0, "cid", nil, store, hooks)
	if ackFlags&0x01 != 0 {
		t.Fatal("new session presented")
	}
	c.send(subscribePacket(protoLevel311, 1, desiredTopic("dev"), 1))
	typ, _, _ := c.frame()
	if typ != packet.SUBACK {
		t.Fatalf("suback %d", typ)
	}
	typ, flags, body := c.frame()
	if typ != packet.PUBLISH || flags&0x08 != 0 {
		t.Fatalf("first publish flags %x", flags)
	}
	c.conn.Close()
	select {
	case <-left:
	case <-time.After(2 * time.Second):
		t.Fatal("first session did not end")
	}
	c, ackFlags = startSession(t, protoLevel311, 0xC0, "cid", nil, store, hooks)
	defer c.conn.Close()
	if ackFlags&0x01 == 0 {
		t.Fatal("session not present")
	}
	typ, flags, body = c.frame()
	msg, err := parsePublish(protoLevel311, flags, body)
	if err != nil || typ != packet.PUBLISH || !msg.dup || string(msg.payload) != `{"led":1}` {
		t.Fatalf("replay typ %d dup %v err %v body %x", typ, msg.dup, err, body)
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
	raw := writeConnect(protoLevel311, 0xC6, 0, "cid", "dev", "pw", nil, reportedTopic("dev"), []byte("gone"))
	if _, err := client.Write(raw); err != nil {
		t.Fatal(err)
	}
	typ, _, _, err := readFrame(client)
	if err != nil || typ != packet.CONNACK {
		t.Fatal(err)
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
	raw := writeConnect(protoLevel5, 0xC6, 1, "cid", "dev", "pw", nil, reportedTopic("dev"), []byte("x"))
	if _, err := client.Write(raw); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readFrame(client); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("keepalive did not expire the session")
	}
}

func TestServeTopicAlias(t *testing.T) {
	props := []byte{0x22, 0, 4}
	got := make(chan struct{}, 2)
	c, _ := startSession(t, protoLevel5, 0xC2, "cid", props, newSessionStore(), deviceHooks{
		reported: func([]byte, mqttProps, bool) bool {
			got <- struct{}{}
			return true
		},
	})
	defer c.conn.Close()
	aliasProps := []byte{0x23, 0, 1}
	c.send(publishPacket(protoLevel5, reportedTopic("dev"), []byte(`{"a":1}`), 0, 0, aliasProps))
	c.send(publishPacket(protoLevel5, "", []byte(`{"a":2}`), 0, 0, aliasProps))
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
	c, _ := startSession(t, protoLevel5, 0xC2, "cid", nil, newSessionStore(), deviceHooks{
		reported: func([]byte, mqttProps, bool) bool {
			n++
			return true
		},
	})
	defer c.conn.Close()
	props := []byte{0x02, 0, 0, 0, 0}
	c.send(publishPacket(protoLevel5, reportedTopic("dev"), []byte(`{"a":1}`), 1, 1, props))
	typ, _, body := c.frame()
	if typ != packet.PUBACK || body[2] != reasonSuccess {
		t.Fatalf("ack %d %x", typ, body)
	}
	if n != 0 {
		t.Fatal("expired message stored")
	}
}

func TestBadJSONIsNotStored(t *testing.T) {
	n := 0
	c, _ := startSession(t, protoLevel5, 0xC2, "cid", nil, newSessionStore(), deviceHooks{
		reported: func([]byte, mqttProps, bool) bool {
			n++
			return false
		},
	})
	defer c.conn.Close()
	c.send(publishPacket(protoLevel5, reportedTopic("dev"), []byte(`not-json`), 1, 1, nil))
	typ, _, body := c.frame()
	if typ != packet.PUBACK || body[2] != reasonPayloadInvalid || n != 1 {
		t.Fatalf("ack %d %x calls %d", typ, body, n)
	}
}
