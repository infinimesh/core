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

func parseConnAck(level byte, body []byte) (ControlPacket, error) {
	if len(body) < 2 {
		return nil, io.ErrUnexpectedEOF
	}
	if body[0]&^0x01 != 0 {
		return nil, errors.New("connack flags")
	}
	ack := &ConnAck{
		ProtocolLevel:  level,
		SessionPresent: body[0]&0x01 != 0,
		Reason:         body[1],
		wire:           wire{remain: len(body)},
	}
	if level != ProtocolMQTT5 {
		if len(body) != 2 {
			return nil, errors.New("connack length")
		}
		return ack, nil
	}
	raw, _, err := takeProperties(body, 2)
	if err != nil {
		return nil, err
	}
	off := 0
	for off < len(raw) {
		id := raw[off]
		off++
		switch id {
		case 0x12:
			s, n, err := readString(raw, off)
			if err != nil {
				return nil, err
			}
			ack.AssignedClientID = s
			off = n
		case 0x11, 0x27:
			if off+4 > len(raw) {
				return nil, io.ErrUnexpectedEOF
			}
			off += 4
		case 0x21, 0x22, 0x13:
			if off+2 > len(raw) {
				return nil, io.ErrUnexpectedEOF
			}
			off += 2
		case 0x1F, 0x1A, 0x1C, 0x15:
			_, n, err := readString(raw, off)
			if err != nil {
				return nil, err
			}
			off = n
		case 0x16:
			_, n, err := readBytes(raw, off)
			if err != nil {
				return nil, err
			}
			off = n
		case 0x24, 0x25, 0x28, 0x29, 0x2A:
			if off >= len(raw) {
				return nil, io.ErrUnexpectedEOF
			}
			off++
		case 0x26:
			_, n, err := readString(raw, off)
			if err != nil {
				return nil, err
			}
			_, n, err = readString(raw, n)
			if err != nil {
				return nil, err
			}
			off = n
		default:
			return nil, errors.New("unsupported connack property")
		}
	}
	return ack, nil
}

func (c *ConnAck) WriteTo(w io.Writer) (int64, error) {
	flags := byte(0)
	if c.SessionPresent {
		flags = 0x01
	}
	body := []byte{flags, c.Reason}
	if c.ProtocolLevel == ProtocolMQTT5 {
		var props []byte
		if c.AssignedClientID != "" {
			props = append(props, 0x12)
			props = appendString(props, c.AssignedClientID)
		}
		body = appendRemaining(body, len(props))
		body = append(body, props...)
	}
	return writePacket(w, CONNACK<<4, body)
}

func parseDisconnect(level byte, body []byte) (ControlPacket, error) {
	d := &Disconnect{ProtocolLevel: level, wire: wire{remain: len(body)}}
	if level != ProtocolMQTT5 {
		if len(body) != 0 {
			return nil, errors.New("disconnect length")
		}
		return d, nil
	}
	if len(body) == 0 {
		return d, nil
	}
	d.Reason = body[0]
	if len(body) > 1 {
		if _, _, err := takeProperties(body, 1); err != nil {
			return nil, err
		}
	}
	return d, nil
}

func (d *Disconnect) WriteTo(w io.Writer) (int64, error) {
	if d.ProtocolLevel != ProtocolMQTT5 {
		return writePacket(w, DISCONNECT<<4, nil)
	}
	return writePacket(w, DISCONNECT<<4, []byte{d.Reason})
}

func (p *PingReq) WriteTo(w io.Writer) (int64, error) {
	return writePacket(w, PINGREQ<<4, nil)
}

func (p *PingResp) WriteTo(w io.Writer) (int64, error) {
	return writePacket(w, PINGRESP<<4, nil)
}

func parseAuth(body []byte) (ControlPacket, error) {
	a := &Auth{wire: wire{remain: len(body)}}
	if len(body) == 0 {
		return a, nil
	}
	a.Reason = body[0]
	if len(body) > 1 {
		if _, _, err := takeProperties(body, 1); err != nil {
			return nil, err
		}
	}
	return a, nil
}

func (a *Auth) WriteTo(w io.Writer) (int64, error) {
	return writePacket(w, AUTH<<4, []byte{a.Reason, 0})
}
