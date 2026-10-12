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
	"time"
)

type UserProperty struct {
	Key   string
	Value string
}

// Properties are the MQTT 5 properties carried on PUBLISH and on a will.
type Properties struct {
	HasMessageExpiry bool
	MessageExpiry    time.Duration
	ContentType      string
	ResponseTopic    string
	CorrelationData  []byte
	HasTopicAlias    bool
	TopicAlias       uint16
	User             []UserProperty
}

// ConnectProperties are the MQTT 5 properties carried on CONNECT.
type ConnectProperties struct {
	HasSessionExpiry bool
	SessionExpiry    time.Duration
	ReceiveMaximum   uint16
	MaxPacketSize    uint32
	TopicAliasMax    uint16
	RequestResponse  bool
	User             []UserProperty
}

func takeProperties(b []byte, off int) ([]byte, int, error) {
	n, next, err := readVarInt(b, off)
	if err != nil {
		return nil, off, err
	}
	if next+n > len(b) {
		return nil, off, io.ErrUnexpectedEOF
	}
	return b[next : next+n], next + n, nil
}

func parsePublishProperties(b []byte) (Properties, error) {
	var p Properties
	off := 0
	for off < len(b) {
		id := b[off]
		off++
		switch id {
		case 0x02:
			if off+4 > len(b) {
				return p, io.ErrUnexpectedEOF
			}
			p.HasMessageExpiry = true
			p.MessageExpiry = time.Duration(u32(b[off:off+4])) * time.Second
			off += 4
		case 0x03:
			s, n, err := readString(b, off)
			if err != nil {
				return p, err
			}
			p.ContentType = s
			off = n
		case 0x08:
			s, n, err := readString(b, off)
			if err != nil {
				return p, err
			}
			p.ResponseTopic = s
			off = n
		case 0x09:
			raw, n, err := readBytes(b, off)
			if err != nil {
				return p, err
			}
			p.CorrelationData = raw
			off = n
		case 0x23:
			if off+2 > len(b) {
				return p, io.ErrUnexpectedEOF
			}
			p.HasTopicAlias = true
			p.TopicAlias = u16(b[off : off+2])
			off += 2
		case 0x26:
			k, n, err := readString(b, off)
			if err != nil {
				return p, err
			}
			v, n, err := readString(b, n)
			if err != nil {
				return p, err
			}
			p.User = append(p.User, UserProperty{k, v})
			off = n
		case 0x01, 0x0b:
			if id == 0x0b {
				_, n, err := readVarInt(b, off)
				if err != nil {
					return p, err
				}
				off = n
				continue
			}
			if off >= len(b) {
				return p, io.ErrUnexpectedEOF
			}
			off++
		default:
			return p, errors.New("unsupported publish property")
		}
	}
	return p, nil
}

func (p Properties) encode() []byte {
	var b []byte
	if p.HasMessageExpiry {
		b = append(b, 0x02)
		b = putU32(b, uint32(p.MessageExpiry/time.Second))
	}
	if p.ContentType != "" {
		b = append(b, 0x03)
		b = appendString(b, p.ContentType)
	}
	if p.ResponseTopic != "" {
		b = append(b, 0x08)
		b = appendString(b, p.ResponseTopic)
	}
	if p.CorrelationData != nil {
		b = append(b, 0x09)
		b = appendBytes(b, p.CorrelationData)
	}
	if p.HasTopicAlias {
		b = append(b, 0x23)
		b = putU16(b, p.TopicAlias)
	}
	for _, u := range p.User {
		b = append(b, 0x26)
		b = appendString(b, u.Key)
		b = appendString(b, u.Value)
	}
	return b
}

func parseConnectProperties(b []byte) (ConnectProperties, error) {
	var p ConnectProperties
	off := 0
	for off < len(b) {
		id := b[off]
		off++
		switch id {
		case 0x11:
			if off+4 > len(b) {
				return p, io.ErrUnexpectedEOF
			}
			p.HasSessionExpiry = true
			p.SessionExpiry = time.Duration(u32(b[off:off+4])) * time.Second
			off += 4
		case 0x21:
			if off+2 > len(b) {
				return p, io.ErrUnexpectedEOF
			}
			p.ReceiveMaximum = u16(b[off : off+2])
			off += 2
			if p.ReceiveMaximum == 0 {
				return p, errors.New("receive maximum is zero")
			}
		case 0x27:
			if off+4 > len(b) {
				return p, io.ErrUnexpectedEOF
			}
			p.MaxPacketSize = u32(b[off : off+4])
			off += 4
			if p.MaxPacketSize == 0 {
				return p, errors.New("maximum packet size is zero")
			}
		case 0x22:
			if off+2 > len(b) {
				return p, io.ErrUnexpectedEOF
			}
			p.TopicAliasMax = u16(b[off : off+2])
			off += 2
		case 0x19:
			if off >= len(b) {
				return p, io.ErrUnexpectedEOF
			}
			p.RequestResponse = b[off] == 1
			off++
		case 0x17:
			if off >= len(b) {
				return p, io.ErrUnexpectedEOF
			}
			off++
		case 0x26:
			k, n, err := readString(b, off)
			if err != nil {
				return p, err
			}
			v, n, err := readString(b, n)
			if err != nil {
				return p, err
			}
			p.User = append(p.User, UserProperty{k, v})
			off = n
		default:
			return p, errors.New("unsupported connect property")
		}
	}
	return p, nil
}

func (p ConnectProperties) encode() []byte {
	var b []byte
	if p.HasSessionExpiry {
		b = append(b, 0x11)
		b = putU32(b, uint32(p.SessionExpiry/time.Second))
	}
	if p.ReceiveMaximum != 0 {
		b = append(b, 0x21)
		b = putU16(b, p.ReceiveMaximum)
	}
	if p.MaxPacketSize != 0 {
		b = append(b, 0x27)
		b = putU32(b, p.MaxPacketSize)
	}
	if p.TopicAliasMax != 0 {
		b = append(b, 0x22)
		b = putU16(b, p.TopicAliasMax)
	}
	if p.RequestResponse {
		b = append(b, 0x19, 1)
	}
	for _, u := range p.User {
		b = append(b, 0x26)
		b = appendString(b, u.Key)
		b = appendString(b, u.Value)
	}
	return b
}
