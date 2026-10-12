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
	"testing"
	"time"
)

func roundTrip(t *testing.T, p ControlPacket, level byte) ControlPacket {
	t.Helper()
	var buf bytes.Buffer
	if _, err := p.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPacket(&buf, level, 0)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestConnect311SeparatesWillFromPassword(t *testing.T) {
	in := &Connect{
		ProtocolLevel: ProtocolMQTT311,
		CleanStart:    true,
		WillFlag:      true,
		UsernameFlag:  true,
		PasswordFlag:  true,
		KeepAlive:     30,
		ClientID:      "cid",
		WillTopic:     "devices/dev/state/reported",
		WillMessage:   []byte("bye"),
		Username:      "dev",
		Password:      "secret",
	}
	got := roundTrip(t, in, 0).(*Connect)
	if !got.CleanStart || !got.WillFlag {
		t.Fatalf("flags clean=%v will=%v", got.CleanStart, got.WillFlag)
	}
	if got.Username != "dev" || got.Password != "secret" {
		t.Fatalf("auth %q %q", got.Username, got.Password)
	}
	if got.WillTopic != "devices/dev/state/reported" || string(got.WillMessage) != "bye" {
		t.Fatalf("will %q %q", got.WillTopic, got.WillMessage)
	}
	if got.Properties.HasSessionExpiry {
		t.Fatal("3.1.1 connect carried session expiry")
	}
}

func TestConnect5SessionAndUserProperty(t *testing.T) {
	in := &Connect{
		ProtocolLevel: ProtocolMQTT5,
		UsernameFlag:  true,
		PasswordFlag:  true,
		KeepAlive:     10,
		ClientID:      "cid",
		Username:      "dev",
		Password:      "pw",
		Properties: ConnectProperties{
			HasSessionExpiry: true,
			SessionExpiry:    time.Minute,
			ReceiveMaximum:   8,
			User:             []UserProperty{{Key: "act", Value: "iot"}},
		},
	}
	got := roundTrip(t, in, 0).(*Connect)
	if got.CleanStart || !got.Properties.HasSessionExpiry || got.Properties.SessionExpiry != time.Minute {
		t.Fatalf("session %+v", got.Properties)
	}
	if got.Properties.ReceiveMaximum != 8 || len(got.Properties.User) != 1 || got.Properties.User[0] != (UserProperty{"act", "iot"}) {
		t.Fatalf("props %+v", got.Properties)
	}
	if got.ClientID != "cid" || got.Username != "dev" || got.Password != "pw" {
		t.Fatalf("payload %+v", got)
	}
}

func TestConnAck311HasNoPropertyByte(t *testing.T) {
	var buf bytes.Buffer
	_, err := (&ConnAck{ProtocolLevel: ProtocolMQTT311, SessionPresent: true}).WriteTo(&buf)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x20, 0x02, 0x01, 0x00}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("connack %x", buf.Bytes())
	}
	got, err := ReadPacket(bytes.NewReader(buf.Bytes()), ProtocolMQTT311, 0)
	if err != nil {
		t.Fatal(err)
	}
	ack := got.(*ConnAck)
	if !ack.SessionPresent || ack.Reason != CodeAccepted {
		t.Fatalf("%+v", ack)
	}
}

func TestConnAck5AssignedClientID(t *testing.T) {
	in := &ConnAck{ProtocolLevel: ProtocolMQTT5, Reason: CodeAccepted, AssignedClientID: "dev"}
	got := roundTrip(t, in, ProtocolMQTT5).(*ConnAck)
	if got.AssignedClientID != "dev" || got.Reason != CodeAccepted {
		t.Fatalf("%+v", got)
	}
}

func TestPubAckHasFixedHeader(t *testing.T) {
	var buf bytes.Buffer
	_, err := (&Ack{ProtocolLevel: ProtocolMQTT311, Kind: PUBACK, PacketID: 7}).WriteTo(&buf)
	if err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()
	if len(got) != 4 || got[0] != 0x40 || got[1] != 0x02 || got[3] != 7 {
		t.Fatalf("puback %x", got)
	}
}

func TestPubRelFlags(t *testing.T) {
	var buf bytes.Buffer
	_, err := (&Ack{ProtocolLevel: ProtocolMQTT311, Kind: PUBREL, PacketID: 3}).WriteTo(&buf)
	if err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	if raw[0] != 0x62 {
		t.Fatalf("pubrel %x", raw)
	}
	got, err := ReadPacket(bytes.NewReader(raw), ProtocolMQTT311, 0)
	if err != nil {
		t.Fatal(err)
	}
	ack := got.(*Ack)
	if ack.Kind != PUBREL || ack.PacketID != 3 {
		t.Fatalf("%+v", ack)
	}
}

func TestPublishQoS1RoundTrip(t *testing.T) {
	in := &Publish{
		ProtocolLevel: ProtocolMQTT5,
		QoS:           1,
		Retain:        true,
		Topic:         "devices/dev/state/reported",
		PacketID:      9,
		Payload:       []byte(`{"t":1}`),
		Properties: Properties{
			ContentType:     "application/json",
			ResponseTopic:   "devices/dev/state/desired",
			CorrelationData: []byte("abc"),
			HasTopicAlias:   true,
			TopicAlias:      1,
		},
	}
	got := roundTrip(t, in, ProtocolMQTT5).(*Publish)
	if !got.Retain || got.PacketID != 9 || string(got.Payload) != `{"t":1}` {
		t.Fatalf("%+v", got)
	}
	if got.Properties.ContentType != "application/json" || got.Properties.TopicAlias != 1 || string(got.Properties.CorrelationData) != "abc" {
		t.Fatalf("props %+v", got.Properties)
	}
}

func TestSubscribeRoundTrip(t *testing.T) {
	in := &Subscribe{
		ProtocolLevel: ProtocolMQTT311,
		PacketID:      1,
		Filters: []Subscription{
			{Topic: "devices/dev/state/desired", QoS: 1},
			{Topic: "devices/dev/state/reported", QoS: 0},
		},
	}
	got := roundTrip(t, in, ProtocolMQTT311).(*Subscribe)
	if got.PacketID != 1 || len(got.Filters) != 2 || got.Filters[0].QoS != 1 || got.Filters[1].Topic != "devices/dev/state/reported" {
		t.Fatalf("%+v", got)
	}
	ackIn := &SubAck{ProtocolLevel: ProtocolMQTT311, PacketID: 1, Reasons: []byte{ReasonGrantedQoS1, ReasonUnspecified}}
	ack := roundTrip(t, ackIn, ProtocolMQTT311).(*SubAck)
	if ack.Reasons[0] != ReasonGrantedQoS1 || ack.Reasons[1] != ReasonUnspecified {
		t.Fatalf("%v", ack.Reasons)
	}
}

func TestMessageExpiryZeroRoundTrip(t *testing.T) {
	in := &Publish{
		ProtocolLevel: ProtocolMQTT5,
		QoS:           1,
		PacketID:      1,
		Topic:         "t",
		Payload:       []byte("x"),
		Properties:    Properties{HasMessageExpiry: true, MessageExpiry: 0},
	}
	got := roundTrip(t, in, ProtocolMQTT5).(*Publish)
	if !got.Properties.HasMessageExpiry || got.Properties.MessageExpiry != 0 {
		t.Fatalf("%+v", got.Properties)
	}
}

func TestReadPacketRejectsOversizedBeforeBody(t *testing.T) {
	raw := []byte{0x30, 0x80, 0x01}
	_, err := ReadPacket(bytes.NewReader(raw), ProtocolMQTT311, 4)
	if err != ErrPacketTooLarge {
		t.Fatal(err)
	}
}

func FuzzReadPacket(f *testing.F) {
	f.Add([]byte{0x20, 0x02, 0x01, 0x00})
	f.Add([]byte{0x10, 0x0c, 0x00, 0x04, 'M', 'Q', 'T', 'T', 0x04, 0x02, 0x00, 0x00, 0x00, 0x00})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ReadPacket(bytes.NewReader(data), ProtocolMQTT311, 1<<16)
		_, _ = ReadPacket(bytes.NewReader(data), ProtocolMQTT5, 1<<16)
	})
}
