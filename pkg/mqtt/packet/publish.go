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

func parsePublish(level, flags byte, body []byte) (ControlPacket, error) {
	if flags&0x06 == 0x06 {
		return nil, errors.New("invalid qos")
	}
	msg := &Publish{
		ProtocolLevel: level,
		Retain:        flags&0x01 != 0,
		QoS:           (flags >> 1) & 0x03,
		Dup:           flags&0x08 != 0,
		wire:          wire{remain: len(body)},
	}
	topic, n, err := readString(body, 0)
	if err != nil {
		return nil, err
	}
	off := n
	msg.Topic = topic
	if msg.QoS > 0 {
		if off+2 > len(body) {
			return nil, io.ErrUnexpectedEOF
		}
		msg.PacketID = u16(body[off : off+2])
		off += 2
	}
	if level == ProtocolMQTT5 {
		raw, next, err := takeProperties(body, off)
		if err != nil {
			return nil, err
		}
		props, err := parsePublishProperties(raw)
		if err != nil {
			return nil, err
		}
		msg.Properties = props
		off = next
	}
	msg.Payload = append([]byte(nil), body[off:]...)
	return msg, nil
}

func (p *Publish) WriteTo(w io.Writer) (int64, error) {
	first := byte(PUBLISH << 4)
	if p.Dup {
		first |= 0x08
	}
	first |= p.QoS << 1
	if p.Retain {
		first |= 0x01
	}
	body := appendString(nil, p.Topic)
	if p.QoS > 0 {
		body = putU16(body, p.PacketID)
	}
	if p.ProtocolLevel == ProtocolMQTT5 {
		props := p.Properties.encode()
		body = appendRemaining(body, len(props))
		body = append(body, props...)
	}
	body = append(body, p.Payload...)
	return writePacket(w, first, body)
}
