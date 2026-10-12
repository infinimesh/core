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
	"errors"
	"io"
)

func parseSubscribe(level byte, body []byte) (ControlPacket, error) {
	if len(body) < 2 {
		return nil, io.ErrUnexpectedEOF
	}
	req := &Subscribe{
		ProtocolLevel: level,
		PacketID:      u16(body[:2]),
		wire:          wire{remain: len(body)},
	}
	off := 2
	if level == ProtocolMQTT5 {
		_, next, err := takeProperties(body, off)
		if err != nil {
			return nil, err
		}
		off = next
	}
	for off < len(body) {
		topic, n, err := readString(body, off)
		if err != nil {
			return nil, err
		}
		if n >= len(body) {
			return nil, io.ErrUnexpectedEOF
		}
		qos := body[n]
		if qos&0xfc != 0 || qos > 2 {
			return nil, errors.New("bad subscribe qos")
		}
		req.Filters = append(req.Filters, Subscription{Topic: topic, QoS: qos})
		off = n + 1
	}
	if len(req.Filters) == 0 {
		return nil, errors.New("empty subscribe")
	}
	return req, nil
}

func (s *Subscribe) WriteTo(w io.Writer) (int64, error) {
	body := putU16(nil, s.PacketID)
	if s.ProtocolLevel == ProtocolMQTT5 {
		body = append(body, 0)
	}
	for _, f := range s.Filters {
		body = appendString(body, f.Topic)
		body = append(body, f.QoS)
	}
	return writePacket(w, SUBSCRIBE<<4|0x02, body)
}

func parseSubAck(level byte, body []byte) (ControlPacket, error) {
	if len(body) < 2 {
		return nil, io.ErrUnexpectedEOF
	}
	ack := &SubAck{
		ProtocolLevel: level,
		PacketID:      u16(body[:2]),
		wire:          wire{remain: len(body)},
	}
	off := 2
	if level == ProtocolMQTT5 {
		_, next, err := takeProperties(body, off)
		if err != nil {
			return nil, err
		}
		off = next
	}
	ack.Reasons = append([]byte(nil), body[off:]...)
	return ack, nil
}

func (s *SubAck) WriteTo(w io.Writer) (int64, error) {
	body := putU16(nil, s.PacketID)
	if s.ProtocolLevel == ProtocolMQTT5 {
		body = append(body, 0)
	}
	body = append(body, s.Reasons...)
	return writePacket(w, SUBACK<<4, body)
}

func parseUnsubscribe(level byte, body []byte) (ControlPacket, error) {
	if len(body) < 2 {
		return nil, io.ErrUnexpectedEOF
	}
	req := &Unsubscribe{
		ProtocolLevel: level,
		PacketID:      u16(body[:2]),
		wire:          wire{remain: len(body)},
	}
	off := 2
	if level == ProtocolMQTT5 {
		_, next, err := takeProperties(body, off)
		if err != nil {
			return nil, err
		}
		off = next
	}
	for off < len(body) {
		topic, n, err := readString(body, off)
		if err != nil {
			return nil, err
		}
		req.Topics = append(req.Topics, topic)
		off = n
	}
	return req, nil
}

func (u *Unsubscribe) WriteTo(w io.Writer) (int64, error) {
	body := putU16(nil, u.PacketID)
	if u.ProtocolLevel == ProtocolMQTT5 {
		body = append(body, 0)
	}
	for _, topic := range u.Topics {
		body = appendString(body, topic)
	}
	return writePacket(w, UNSUBSCRIBE<<4|0x02, body)
}

func parseUnsubAck(level byte, body []byte) (ControlPacket, error) {
	if len(body) < 2 {
		return nil, io.ErrUnexpectedEOF
	}
	ack := &UnsubAck{
		ProtocolLevel: level,
		PacketID:      u16(body[:2]),
		wire:          wire{remain: len(body)},
	}
	off := 2
	if level == ProtocolMQTT5 {
		_, next, err := takeProperties(body, off)
		if err != nil {
			return nil, err
		}
		off = next
		ack.Reasons = append([]byte(nil), body[off:]...)
	}
	return ack, nil
}

func (u *UnsubAck) WriteTo(w io.Writer) (int64, error) {
	body := putU16(nil, u.PacketID)
	if u.ProtocolLevel == ProtocolMQTT5 {
		body = append(body, 0)
		body = append(body, u.Reasons...)
	}
	return writePacket(w, UNSUBACK<<4, body)
}
