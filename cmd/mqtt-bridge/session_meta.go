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
	"errors"
	"io"
	"time"

	"github.com/infinimesh/core/v3/pkg/mqtt/packet"
)

type connectMeta struct {
	cleanStart    bool
	keepAlive     time.Duration
	sessionExpiry time.Duration
	receiveMax    int
	maxPacketSize int
	topicAliasMax int
	willTopic     string
	willMessage   []byte
	willQoS       byte
	willRetain    bool
	willProps     packet.Properties
	requestResp   bool
	user          [][2]string
}

type pubMsg struct {
	topic   string
	payload []byte
	qos     byte
	dup     bool
	retain  bool
	rel     bool
	id      uint16
	props   packet.Properties
}

func readConnect(r io.Reader) (*packet.Connect, connectMeta, error) {
	pkt, err := packet.ReadPacket(r, 0, 0)
	if err != nil {
		return nil, connectMeta{}, err
	}
	cp, ok := pkt.(*packet.Connect)
	if !ok {
		return nil, connectMeta{}, errors.New("first packet isn't ConnectControlPacket")
	}
	return cp, metaFrom(cp), nil
}

func metaFrom(cp *packet.Connect) connectMeta {
	meta := connectMeta{
		cleanStart:    cp.CleanStart,
		keepAlive:     time.Duration(cp.KeepAlive) * time.Second,
		receiveMax:    int(cp.Properties.ReceiveMaximum),
		maxPacketSize: int(cp.Properties.MaxPacketSize),
		topicAliasMax: int(cp.Properties.TopicAliasMax),
		willTopic:     cp.WillTopic,
		willMessage:   append([]byte(nil), cp.WillMessage...),
		willQoS:       cp.WillQoS,
		willRetain:    cp.WillRetain,
		willProps:     cp.WillProperties,
		requestResp:   cp.Properties.RequestResponse,
	}
	if cp.Properties.HasSessionExpiry {
		meta.sessionExpiry = cp.Properties.SessionExpiry
	} else if cp.ProtocolLevel != packet.ProtocolMQTT5 && !cp.CleanStart {
		meta.sessionExpiry = time.Hour
	}
	if meta.receiveMax == 0 {
		meta.receiveMax = 65535
	}
	for _, u := range cp.Properties.User {
		meta.user = append(meta.user, [2]string{u.Key, u.Value})
	}
	return meta
}

func pubFrom(p *packet.Publish) pubMsg {
	return pubMsg{
		topic:   p.Topic,
		payload: append([]byte(nil), p.Payload...),
		qos:     p.QoS,
		dup:     p.Dup,
		retain:  p.Retain,
		id:      p.PacketID,
		props:   p.Properties,
	}
}

func writeConnAck(w io.Writer, level byte, sessionPresent bool, reason byte, assignedClientID string) error {
	if level != packet.ProtocolMQTT5 && reason > 5 {
		reason = 0x05
	}
	ack := &packet.ConnAck{
		ProtocolLevel:    level,
		SessionPresent:   sessionPresent,
		Reason:           reason,
		AssignedClientID: assignedClientID,
	}
	if level == packet.ProtocolMQTT5 {
		ack.ReceiveMaximum = uint16(serverReceiveMax)
		ack.MaxPacketSize = uint32(serverMaxPacketSize)
		ack.TopicAliasMax = uint16(serverTopicAliasMax)
	}
	_, err := ack.WriteTo(w)
	return err
}

func writePub(w io.Writer, level byte, msg pubMsg) error {
	_, err := (&packet.Publish{
		ProtocolLevel: level,
		Dup:           msg.dup,
		QoS:           msg.qos,
		Retain:        msg.retain,
		Topic:         msg.topic,
		PacketID:      msg.id,
		Payload:       msg.payload,
		Properties:    msg.props,
	}).WriteTo(w)
	return err
}

func writeAck(w io.Writer, level byte, kind byte, id uint16, reason byte) error {
	_, err := (&packet.Ack{ProtocolLevel: level, Kind: kind, PacketID: id, Reason: reason}).WriteTo(w)
	return err
}

func writeSubAck(w io.Writer, level byte, id uint16, codes []byte) error {
	_, err := (&packet.SubAck{ProtocolLevel: level, PacketID: id, Reasons: codes}).WriteTo(w)
	return err
}

func writeUnsubAck(w io.Writer, level byte, id uint16, n int) error {
	var reasons []byte
	if level == packet.ProtocolMQTT5 {
		reasons = make([]byte, n)
	}
	_, err := (&packet.UnsubAck{ProtocolLevel: level, PacketID: id, Reasons: reasons}).WriteTo(w)
	return err
}

func writeDisconnect(w io.Writer, level byte, reason byte) error {
	_, err := (&packet.Disconnect{ProtocolLevel: level, Reason: reason}).WriteTo(w)
	return err
}

func writePingResp(w io.Writer) error {
	_, err := (&packet.PingResp{}).WriteTo(w)
	return err
}
