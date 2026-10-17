/*
Copyright © 2021-2026 Infinite Devices GmbH

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
package packet

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func wantReadErr(t *testing.T, raw []byte, level byte) {
	t.Helper()
	if _, err := ReadPacket(bytes.NewReader(raw), level, 0); err == nil {
		t.Fatalf("accepted %x", raw)
	}
}

type barePacket struct{}

func (barePacket) WriteTo(io.Writer) (int64, error) { return 0, nil }

func TestProtocolAndRemain(t *testing.T) {
	if protocol(nil) != nil {
		t.Fatal("nil")
	}
	wrapped := protocol(io.ErrUnexpectedEOF)
	if wrapped.(ProtocolError).Error() != io.ErrUnexpectedEOF.Error() {
		t.Fatal(wrapped)
	}
	already := ProtocolError{Msg: "keep"}
	if protocol(already).(ProtocolError).Msg != "keep" {
		t.Fatal("rewrapped")
	}
	if PacketRemain(barePacket{}) != 0 {
		t.Fatal("bare")
	}
	ping := roundTrip(t, &PingReq{}, ProtocolMQTT311).(*PingReq)
	if PacketRemain(ping) != 0 {
		t.Fatal(PacketRemain(ping))
	}
	if PacketRemain(&Disconnect{wire: wire{remain: 4}}) != 4 {
		t.Fatal("remain")
	}
}

func TestIOEdges(t *testing.T) {
	if _, _, err := readRemaining(bytes.NewReader([]byte{0x80, 0x80, 0x80, 0x80})); err == nil {
		t.Fatal("malformed remaining")
	}
	if _, _, err := readRemaining(bytes.NewReader([]byte{0x80})); err == nil {
		t.Fatal("short remaining")
	}
	n, w, err := readRemaining(bytes.NewReader([]byte{0x80, 0x01}))
	if err != nil || n != 128 || w != 2 {
		t.Fatalf("varint %d %d %v", n, w, err)
	}
	if _, _, err := readVarInt([]byte{0x80}, 0); err == nil {
		t.Fatal("short varint")
	}
	if _, _, err := readVarInt([]byte{0x80, 0x80, 0x80, 0x80}, 0); err == nil {
		t.Fatal("malformed varint")
	}
	if v, off, err := readVarInt([]byte{0x80, 0x01}, 0); err != nil || v != 128 || off != 2 {
		t.Fatalf("%d %d %v", v, off, err)
	}
	if _, _, err := readBytes([]byte{0x00}, 0); err == nil {
		t.Fatal("short length")
	}
	if _, _, err := readBytes([]byte{0x00, 0x02, 'a'}, 0); err == nil {
		t.Fatal("short value")
	}
	if _, _, err := readString([]byte{0x00}, 0); err == nil {
		t.Fatal("short string")
	}
	if len(appendRemaining(nil, 200)) != 2 {
		t.Fatal("remaining width")
	}
	if u16([]byte{0x01, 0x02}) != 0x0102 || u32([]byte{0, 0, 0, 9}) != 9 {
		t.Fatal("endian")
	}
	raw, off, err := takeProperties([]byte{0x80, 0x01}, 0)
	if err != io.ErrUnexpectedEOF || raw != nil || off != 0 {
		t.Fatal(err)
	}
}

func TestReadPacketRejects(t *testing.T) {
	if _, err := ReadPacket(bytes.NewReader(nil), 0, 0); err == nil {
		t.Fatal("empty")
	}
	wantReadErr(t, []byte{0x30}, ProtocolMQTT311)
	wantReadErr(t, []byte{0x30, 0x80, 0x80, 0x80, 0x80}, ProtocolMQTT311)
	wantReadErr(t, []byte{0x30, 0x05, 0x00}, ProtocolMQTT311)
	wantReadErr(t, []byte{0x00, 0x00}, ProtocolMQTT311)
	wantReadErr(t, []byte{0x11, 0x00}, 0)
	wantReadErr(t, []byte{0x80, 0x00}, ProtocolMQTT311)
	wantReadErr(t, []byte{0xA0, 0x00}, ProtocolMQTT311)
	wantReadErr(t, []byte{0xF1, 0x00}, ProtocolMQTT5)
	wantReadErr(t, []byte{0xC1, 0x00}, ProtocolMQTT311)
	wantReadErr(t, []byte{0xC0, 0x01, 0x00}, ProtocolMQTT311)
	wantReadErr(t, []byte{0xD1, 0x00}, ProtocolMQTT311)
	wantReadErr(t, []byte{0xD0, 0x01, 0x00}, ProtocolMQTT311)
	wantReadErr(t, []byte{0x36, 0x00}, ProtocolMQTT311)
	wantReadErr(t, []byte{0x30, 0x00}, ProtocolMQTT311)
	wantReadErr(t, []byte{0x32, 0x03, 0x00, 0x01, 'a'}, ProtocolMQTT311)
	wantReadErr(t, []byte{0x30, 0x04, 0x00, 0x01, 'a', 0x80}, ProtocolMQTT5)
	wantReadErr(t, []byte{0x40, 0x01, 0x00}, ProtocolMQTT311)
	wantReadErr(t, []byte{0x41, 0x02, 0x00, 0x01}, ProtocolMQTT311)
	wantReadErr(t, []byte{0x60, 0x02, 0x00, 0x01}, ProtocolMQTT311)
	wantReadErr(t, []byte{0xE0, 0x01, 0x00}, ProtocolMQTT311)
	_, err := ReadPacket(bytes.NewReader([]byte{0x11, 0x00}), 0, 0)
	if _, ok := err.(ProtocolError); !ok {
		t.Fatal(err)
	}
	got, err := ReadPacket(bytes.NewReader([]byte{0xC0, 0x00}), ProtocolMQTT311, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.(*PingReq); !ok {
		t.Fatalf("%T", got)
	}
	if _, err := ReadPacket(bytes.NewReader([]byte{0xC0, 0x00}), ProtocolMQTT311, 1); err != ErrPacketTooLarge {
		t.Fatal(err)
	}
}

func TestPingDisconnectAuthRoundTrip(t *testing.T) {
	if _, ok := roundTrip(t, &PingResp{}, ProtocolMQTT311).(*PingResp); !ok {
		t.Fatal("pingresp")
	}
	d311 := roundTrip(t, &Disconnect{ProtocolLevel: ProtocolMQTT311}, ProtocolMQTT311).(*Disconnect)
	if d311.Reason != 0 {
		t.Fatal(d311.Reason)
	}
	d5 := roundTrip(t, &Disconnect{ProtocolLevel: ProtocolMQTT5, Reason: ReasonServerBusy}, ProtocolMQTT5).(*Disconnect)
	if d5.Reason != ReasonServerBusy {
		t.Fatal(d5.Reason)
	}
	if _, err := parseDisconnect(ProtocolMQTT5, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := parseDisconnect(ProtocolMQTT5, []byte{ReasonNormalDisconn, 0x00}); err != nil {
		t.Fatal(err)
	}
	if _, err := parseDisconnect(ProtocolMQTT5, []byte{ReasonNormalDisconn, 0x80}); err == nil {
		t.Fatal("bad disconnect props")
	}
	auth := roundTrip(t, &Auth{Reason: ReasonContinueAuthentication()}, ProtocolMQTT5).(*Auth)
	if auth.Reason != 0x18 {
		t.Fatal(auth.Reason)
	}
	empty, err := ReadPacket(bytes.NewReader([]byte{0xF0, 0x00}), ProtocolMQTT5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if empty.(*Auth).Reason != 0 {
		t.Fatal("empty auth")
	}
	if _, err := parseAuth([]byte{0x18, 0x00}); err != nil {
		t.Fatal(err)
	}
	if _, err := parseAuth([]byte{0x18, 0x80}); err == nil {
		t.Fatal("bad auth props")
	}
}

func ReasonContinueAuthentication() byte { return 0x18 }

func TestConnectEdges(t *testing.T) {
	in := &Connect{
		ProtocolName:  "MQIsdp",
		ProtocolLevel: 3,
		CleanStart:    true,
		KeepAlive:     9,
		ClientID:      "c",
	}
	got := roundTrip(t, in, 0).(*Connect)
	if got.ProtocolName != "MQIsdp" || got.ProtocolLevel != 3 || got.ClientID != "c" {
		t.Fatalf("%+v", got)
	}
	badName := *in
	badName.ProtocolName = "HTTP"
	var buf bytes.Buffer
	if _, err := badName.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	wantReadErr(t, buf.Bytes(), 0)

	if _, err := parseConnect(appendString(nil, "MQTT")); err == nil {
		t.Fatal("truncated header")
	}
	if _, err := parseConnect(nil); err == nil {
		t.Fatal("empty")
	}

	base := &Connect{ProtocolLevel: ProtocolMQTT311, CleanStart: true, ClientID: "c"}
	buf.Reset()
	if _, err := base.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	raw := append([]byte(nil), buf.Bytes()...)
	nameAt := bytes.Index(raw, []byte("MQTT"))
	flagAt := nameAt + 5
	reserved := append([]byte(nil), raw...)
	reserved[flagAt] |= 0x01
	wantReadErr(t, reserved, 0)
	willQoS := append([]byte(nil), raw...)
	willQoS[flagAt] |= 0x18
	wantReadErr(t, willQoS, 0)
	retain := append([]byte(nil), raw...)
	retain[flagAt] |= 0x20
	wantReadErr(t, retain, 0)

	userOnly := &Connect{ProtocolLevel: ProtocolMQTT311, UsernameFlag: true, Username: "u", ClientID: "c"}
	got = roundTrip(t, userOnly, 0).(*Connect)
	if got.Username != "u" || got.Password != "" {
		t.Fatalf("%+v", got)
	}
	passOnly := &Connect{ProtocolLevel: ProtocolMQTT311, PasswordFlag: true, Password: "p", ClientID: "c"}
	got = roundTrip(t, passOnly, 0).(*Connect)
	if got.Password != "p" {
		t.Fatal(got.Password)
	}

	buf.Reset()
	if _, err := userOnly.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	cut := buf.Bytes()
	cut = cut[:len(cut)-3]
	wantReadErr(t, cut, 0)

	will := &Connect{
		ProtocolLevel: ProtocolMQTT5,
		WillFlag:      true,
		WillQoS:       1,
		WillRetain:    true,
		ClientID:      "c",
		WillTopic:     "w",
		WillMessage:   []byte("m"),
		WillProperties: Properties{
			ContentType:     "text/plain",
			User:            []UserProperty{{Key: "k", Value: "v"}},
			HasTopicAlias:   true,
			TopicAlias:      2,
			ResponseTopic:   "r",
			CorrelationData: []byte{9},
		},
		Properties: ConnectProperties{
			HasSessionExpiry: true,
			SessionExpiry:    2 * time.Second,
			ReceiveMaximum:   4,
			MaxPacketSize:    8,
			TopicAliasMax:    1,
			RequestResponse:  true,
			User:             []UserProperty{{Key: "k", Value: "v"}},
		},
	}
	full := roundTrip(t, will, 0).(*Connect)
	if full.WillTopic != "w" || string(full.WillMessage) != "m" || full.WillProperties.ContentType != "text/plain" {
		t.Fatalf("%+v", full)
	}
	if !full.Properties.RequestResponse || full.Properties.TopicAliasMax != 1 || full.Properties.User[0].Key != "k" {
		t.Fatalf("%+v", full.Properties)
	}

	buf.Reset()
	if _, err := will.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	shortWill := buf.Bytes()
	shortWill = shortWill[:len(shortWill)-4]
	wantReadErr(t, shortWill, 0)
}

func TestPublishPropertiesAndDup(t *testing.T) {
	in := &Publish{
		ProtocolLevel: ProtocolMQTT5,
		Dup:           true,
		QoS:           2,
		Topic:         "t",
		PacketID:      3,
		Payload:       bytes.Repeat([]byte{'a'}, 200),
		Properties: Properties{
			HasMessageExpiry: true,
			MessageExpiry:    5 * time.Second,
			ContentType:      "a",
			ResponseTopic:    "b",
			CorrelationData:  []byte("c"),
			HasTopicAlias:    true,
			TopicAlias:       2,
			User:             []UserProperty{{Key: "k", Value: "v"}},
		},
	}
	got := roundTrip(t, in, ProtocolMQTT5).(*Publish)
	if !got.Dup || got.QoS != 2 || got.PacketID != 3 || len(got.Payload) != 200 {
		t.Fatalf("%+v", got)
	}
	if got.Properties.MessageExpiry != 5*time.Second || got.Properties.User[0] != (UserProperty{"k", "v"}) {
		t.Fatalf("%+v", got.Properties)
	}

	ok, err := parsePublishProperties([]byte{
		0x02, 0, 0, 0, 5,
		0x03, 0, 1, 'a',
		0x08, 0, 1, 'b',
		0x09, 0, 1, 'c',
		0x23, 0, 2,
		0x26, 0, 1, 'k', 0, 1, 'v',
		0x01, 1,
		0x0b, 0x05,
	})
	if err != nil || ok.ContentType != "a" || !ok.HasTopicAlias || len(ok.User) != 1 {
		t.Fatalf("%+v %v", ok, err)
	}
	for _, raw := range [][]byte{
		{0x02, 0, 0},
		{0x03, 0x00},
		{0x08, 0x00},
		{0x09, 0x00},
		{0x23, 0x00},
		{0x26, 0, 1, 'k'},
		{0x26, 0, 1, 'k', 0x00},
		{0x0b, 0x80},
		{0x01},
		{0x99},
	} {
		if _, err := parsePublishProperties(raw); err == nil {
			t.Fatalf("accepted %x", raw)
		}
	}
	props, err := parseConnectProperties([]byte{
		0x11, 0, 0, 0, 2,
		0x21, 0, 4,
		0x27, 0, 0, 0, 8,
		0x22, 0, 1,
		0x19, 1,
		0x17, 0,
		0x26, 0, 1, 'k', 0, 1, 'v',
	})
	if err != nil || !props.RequestResponse || props.ReceiveMaximum != 4 || !props.HasSessionExpiry {
		t.Fatalf("%+v %v", props, err)
	}
	off, err := parseConnectProperties([]byte{0x19, 0})
	if err != nil || off.RequestResponse {
		t.Fatalf("%+v %v", off, err)
	}
	for _, raw := range [][]byte{
		{0x11, 0, 0},
		{0x21, 0},
		{0x21, 0, 0},
		{0x27, 0, 0, 0},
		{0x27, 0, 0, 0, 0},
		{0x22, 0},
		{0x19},
		{0x17},
		{0x26, 0, 1, 'k'},
		{0x26, 0, 1, 'k', 0x00},
		{0x01},
	} {
		if _, err := parseConnectProperties(raw); err == nil {
			t.Fatalf("accepted %x", raw)
		}
	}
	encoded := (Properties{CorrelationData: []byte{}}).encode()
	if len(encoded) == 0 {
		t.Fatal("empty correlation")
	}
}

func TestAckKinds(t *testing.T) {
	for _, kind := range []byte{PUBACK, PUBREC, PUBREL, PUBCOMP} {
		reason := byte(0)
		if kind == PUBCOMP {
			reason = ReasonPacketIDNotFound
		}
		in := &Ack{ProtocolLevel: ProtocolMQTT5, Kind: kind, PacketID: 9, Reason: reason}
		got := roundTrip(t, in, ProtocolMQTT5).(*Ack)
		if got.Kind != kind || got.PacketID != 9 || got.Reason != reason {
			t.Fatalf("%+v", got)
		}
	}
	plain := roundTrip(t, &Ack{ProtocolLevel: ProtocolMQTT5, Kind: PUBCOMP, PacketID: 1}, ProtocolMQTT5).(*Ack)
	if plain.Reason != 0 || plain.Kind != PUBCOMP {
		t.Fatalf("%+v", plain)
	}
	rel := roundTrip(t, &Ack{ProtocolLevel: ProtocolMQTT311, Kind: PUBREC, PacketID: 2}, ProtocolMQTT311).(*Ack)
	if rel.PacketID != 2 {
		t.Fatal(rel.PacketID)
	}
}

func TestSubscribeFamily(t *testing.T) {
	sub := roundTrip(t, &Subscribe{
		ProtocolLevel: ProtocolMQTT5,
		PacketID:      4,
		Filters:       []Subscription{{Topic: "a/b", QoS: 2}},
	}, ProtocolMQTT5).(*Subscribe)
	if sub.Filters[0].QoS != 2 || sub.PacketID != 4 {
		t.Fatalf("%+v", sub)
	}
	ack := roundTrip(t, &SubAck{ProtocolLevel: ProtocolMQTT5, PacketID: 4, Reasons: []byte{ReasonGrantedQoS2}}, ProtocolMQTT5).(*SubAck)
	if ack.Reasons[0] != ReasonGrantedQoS2 {
		t.Fatal(ack.Reasons)
	}
	unsub := roundTrip(t, &Unsubscribe{ProtocolLevel: ProtocolMQTT5, PacketID: 5, Topics: []string{"a/b", "c"}}, ProtocolMQTT5).(*Unsubscribe)
	if len(unsub.Topics) != 2 || unsub.Topics[1] != "c" {
		t.Fatalf("%+v", unsub)
	}
	unsub311 := roundTrip(t, &Unsubscribe{ProtocolLevel: ProtocolMQTT311, PacketID: 6, Topics: []string{"z"}}, ProtocolMQTT311).(*Unsubscribe)
	if unsub311.Topics[0] != "z" {
		t.Fatal(unsub311.Topics)
	}
	uack := roundTrip(t, &UnsubAck{ProtocolLevel: ProtocolMQTT5, PacketID: 5, Reasons: []byte{ReasonSuccess, ReasonNoSubscriptionExisted()}}, ProtocolMQTT5).(*UnsubAck)
	if uack.PacketID != 5 || len(uack.Reasons) != 2 {
		t.Fatalf("%+v", uack)
	}
	uack311 := roundTrip(t, &UnsubAck{ProtocolLevel: ProtocolMQTT311, PacketID: 6, Reasons: []byte{1}}, ProtocolMQTT311).(*UnsubAck)
	if uack311.PacketID != 6 || len(uack311.Reasons) != 0 {
		t.Fatalf("%+v", uack311)
	}
	empty := &Subscribe{ProtocolLevel: ProtocolMQTT311, PacketID: 1}
	var buf bytes.Buffer
	if _, err := empty.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	wantReadErr(t, buf.Bytes(), ProtocolMQTT311)

	if _, err := parseSubscribe(ProtocolMQTT311, []byte{0x00}); err == nil {
		t.Fatal("short subscribe")
	}
	if _, err := parseSubscribe(ProtocolMQTT311, append(putU16(nil, 1), 0x00, 0x01, 'a')); err == nil {
		t.Fatal("missing qos")
	}
	if _, err := parseSubscribe(ProtocolMQTT311, append(putU16(nil, 1), appendString(nil, "a")...)); err == nil {
		t.Fatal("qos truncated")
	}
	badQoS := append(putU16(nil, 1), appendString(nil, "a")...)
	badQoS = append(badQoS, 3)
	if _, err := parseSubscribe(ProtocolMQTT311, badQoS); err == nil {
		t.Fatal("qos")
	}
	if _, err := parseSubscribe(ProtocolMQTT5, []byte{0x00, 0x01, 0x80}); err == nil {
		t.Fatal("subscribe props")
	}
	if _, err := parseSubAck(ProtocolMQTT311, []byte{0x00}); err == nil {
		t.Fatal("short suback")
	}
	if _, err := parseSubAck(ProtocolMQTT5, []byte{0x00, 0x01, 0x80}); err == nil {
		t.Fatal("suback props")
	}
	if _, err := parseUnsubscribe(ProtocolMQTT311, []byte{0x00}); err == nil {
		t.Fatal("short unsubscribe")
	}
	if _, err := parseUnsubscribe(ProtocolMQTT311, append(putU16(nil, 1), 0x00, 0x02, 'a')); err == nil {
		t.Fatal("topic truncated")
	}
	if _, err := parseUnsubscribe(ProtocolMQTT5, []byte{0x00, 0x01, 0x80}); err == nil {
		t.Fatal("unsubscribe props")
	}
	none, err := parseUnsubscribe(ProtocolMQTT311, putU16(nil, 3))
	if err != nil || len(none.(*Unsubscribe).Topics) != 0 {
		t.Fatal(err)
	}
	if _, err := parseUnsubAck(ProtocolMQTT311, []byte{0x00}); err == nil {
		t.Fatal("short unsuback")
	}
	if _, err := parseUnsubAck(ProtocolMQTT5, []byte{0x00, 0x01, 0x80}); err == nil {
		t.Fatal("unsuback props")
	}
	kept, err := parseUnsubAck(ProtocolMQTT311, []byte{0x00, 0x04, 0x00})
	if err != nil || len(kept.(*UnsubAck).Reasons) != 0 {
		t.Fatal("311 reasons")
	}
}

func ReasonNoSubscriptionExisted() byte { return 0x11 }

func TestConnAckProperties(t *testing.T) {
	var props []byte
	props = append(props, 0x12)
	props = appendString(props, "cid")
	props = append(props, 0x11, 0, 0, 0, 1)
	props = append(props, 0x27, 0, 0, 0, 10)
	props = append(props, 0x21, 0, 5)
	props = append(props, 0x22, 0, 3)
	props = append(props, 0x13, 0, 15)
	for _, id := range []byte{0x1F, 0x1A, 0x1C, 0x15} {
		props = append(props, id)
		props = appendString(props, "s")
	}
	props = append(props, 0x16)
	props = appendBytes(props, []byte{7})
	for _, id := range []byte{0x24, 0x25, 0x28, 0x29, 0x2A} {
		props = append(props, id, 1)
	}
	props = append(props, 0x26)
	props = appendString(props, "k")
	props = appendString(props, "v")
	body := append([]byte{0x00, CodeAccepted}, appendRemaining(nil, len(props))...)
	body = append(body, props...)
	got, err := parseConnAck(ProtocolMQTT5, body)
	if err != nil {
		t.Fatal(err)
	}
	ack := got.(*ConnAck)
	if ack.AssignedClientID != "cid" || ack.MaxPacketSize != 10 || ack.ReceiveMaximum != 5 || ack.TopicAliasMax != 3 {
		t.Fatalf("%+v", ack)
	}
	if _, err := parseConnAck(ProtocolMQTT311, []byte{0x00}); err == nil {
		t.Fatal("short")
	}
	if _, err := parseConnAck(ProtocolMQTT311, []byte{0x02, 0x00}); err == nil {
		t.Fatal("flags")
	}
	if _, err := parseConnAck(ProtocolMQTT311, []byte{0x00, 0x00, 0x00}); err == nil {
		t.Fatal("length")
	}
	if _, err := parseConnAck(ProtocolMQTT5, []byte{0x01, 0x00, 0x80}); err == nil {
		t.Fatal("props")
	}
	for _, raw := range [][]byte{
		{0x12},
		{0x11, 0, 0},
		{0x27, 0, 0, 0},
		{0x27, 0, 0, 0, 0},
		{0x21, 0},
		{0x21, 0, 0},
		{0x22},
		{0x13},
		{0x1F},
		{0x16},
		{0x24},
		{0x26, 0, 1, 'k'},
		{0x26, 0, 1, 'k', 0, 1},
		{0x01},
	} {
		packet := append([]byte{0x00, 0x00}, appendRemaining(nil, len(raw))...)
		packet = append(packet, raw...)
		if _, err := parseConnAck(ProtocolMQTT5, packet); err == nil {
			t.Fatalf("accepted %x", raw)
		}
	}
}
