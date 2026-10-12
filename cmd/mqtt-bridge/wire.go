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
	"encoding/binary"
	"errors"
	"io"
	"time"

	"github.com/slntopp/mqtt-go/packet"
)

const (
	reasonSuccess           byte = 0x00
	reasonGrantedQoS0       byte = 0x00
	reasonGrantedQoS1       byte = 0x01
	reasonGrantedQoS2       byte = 0x02
	reasonUnspecified       byte = 0x80
	reasonMalformed         byte = 0x81
	reasonProtocolError     byte = 0x82
	reasonClientIDRejected  byte = 0x85
	reasonNotAuthorized     byte = 0x87
	reasonTopicAliasInvalid byte = 0x94
	reasonPacketTooLarge    byte = 0x95
	reasonPayloadInvalid    byte = 0x99
	reasonQoSNotSupported   byte = 0x9B
	mqtt311IdentifierReject byte = 0x02
	protoLevel311           byte = 4
	protoLevel5             byte = 5
)

type mqttProps struct {
	hasExpiry     bool
	messageExpiry time.Duration
	contentType   string
	responseTopic string
	correlation   []byte
	user          [][2]string
	hasAlias      bool
	topicAlias    uint16
}

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
	willProps     mqttProps
	requestResp   bool
	user          [][2]string
}

func readFrame(r io.Reader) (typ byte, flags byte, body []byte, err error) {
	hdr := make([]byte, 1)
	if _, err = io.ReadFull(r, hdr); err != nil {
		return
	}
	typ = hdr[0] >> 4
	flags = hdr[0] & 0x0f
	remain, err := readRemaining(r)
	if err != nil {
		return
	}
	body = make([]byte, remain)
	if remain > 0 {
		_, err = io.ReadFull(r, body)
	}
	return
}

func readRemaining(r io.Reader) (int, error) {
	mul := 1
	val := 0
	for i := 0; i < 4; i++ {
		b := make([]byte, 1)
		if _, err := io.ReadFull(r, b); err != nil {
			return 0, err
		}
		val += int(b[0]&127) * mul
		if b[0]&128 == 0 {
			return val, nil
		}
		mul *= 128
	}
	return 0, errors.New("malformed remaining length")
}

func writeRemaining(n int) []byte {
	var out []byte
	for {
		b := byte(n % 128)
		n /= 128
		if n > 0 {
			b |= 128
		}
		out = append(out, b)
		if n == 0 {
			return out
		}
	}
}

func writePacket(w io.Writer, first byte, body []byte) error {
	if _, err := w.Write([]byte{first}); err != nil {
		return err
	}
	if _, err := w.Write(writeRemaining(len(body))); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

func readConnect(r io.Reader) (*packet.ConnectControlPacket, connectMeta, error) {
	typ, flags, body, err := readFrame(r)
	if err != nil {
		return nil, connectMeta{}, err
	}
	if typ != packet.CONNECT || flags != 0 {
		return nil, connectMeta{}, errors.New("first packet isn't ConnectControlPacket")
	}
	return parseConnectBody(body)
}

func parseConnectBody(body []byte) (*packet.ConnectControlPacket, connectMeta, error) {
	var meta connectMeta
	meta.receiveMax = 65535
	off := 0
	name, n, err := readMQTTString(body, off)
	if err != nil {
		return nil, meta, err
	}
	off = n
	if name != "MQTT" && name != "MQIsdp" {
		return nil, meta, errors.New("invalid protocol")
	}
	if off+4 > len(body) {
		return nil, meta, io.ErrUnexpectedEOF
	}
	level := body[off]
	flags := body[off+1]
	keep := binary.BigEndian.Uint16(body[off+2 : off+4])
	off += 4
	if flags&0x01 != 0 {
		return nil, meta, errors.New("reserved connect flag")
	}
	willQoS := (flags >> 3) & 0x03
	willFlag := flags&0x04 != 0
	if willQoS == 3 || (!willFlag && (willQoS != 0 || flags&0x20 != 0)) {
		return nil, meta, errors.New("bad will flags")
	}
	meta.cleanStart = flags&0x02 != 0
	meta.keepAlive = time.Duration(keep) * time.Second
	meta.willQoS = willQoS
	meta.willRetain = flags&0x20 != 0
	if level == protoLevel5 {
		plen, n, err := readVarInt(body, off)
		if err != nil {
			return nil, meta, err
		}
		off = n
		if off+plen > len(body) {
			return nil, meta, io.ErrUnexpectedEOF
		}
		if err := parseConnectProps(body[off:off+plen], &meta); err != nil {
			return nil, meta, err
		}
		off += plen
	} else if !meta.cleanStart {
		meta.sessionExpiry = time.Hour
	}
	clientID, n, err := readMQTTString(body, off)
	if err != nil {
		return nil, meta, err
	}
	off = n
	if willFlag {
		if level == protoLevel5 {
			plen, n, err := readVarInt(body, off)
			if err != nil {
				return nil, meta, err
			}
			off = n
			if off+plen > len(body) {
				return nil, meta, io.ErrUnexpectedEOF
			}
			props, err := parsePublishProps(body[off : off+plen])
			if err != nil {
				return nil, meta, err
			}
			meta.willProps = props
			off += plen
		}
		topic, n, err := readMQTTString(body, off)
		if err != nil {
			return nil, meta, err
		}
		off = n
		msg, n, err := readMQTTBytes(body, off)
		if err != nil {
			return nil, meta, err
		}
		off = n
		meta.willTopic = topic
		meta.willMessage = msg
	}
	var username, password string
	if flags&0x80 != 0 {
		username, off, err = readMQTTString(body, off)
		if err != nil {
			return nil, meta, err
		}
	}
	if flags&0x40 != 0 {
		pw, n, err := readMQTTBytes(body, off)
		if err != nil {
			return nil, meta, err
		}
		off = n
		password = string(pw)
	}
	cp := &packet.ConnectControlPacket{
		VariableHeader: packet.ConnectVariableHeader{
			ProtocolName:  name,
			ProtocolLevel: level,
			KeepAlive:     int(keep),
			ConnectFlags: packet.ConnectFlags{
				UserName:   flags&0x80 != 0,
				Password:   flags&0x40 != 0,
				WillRetain: meta.willRetain,
				WillQoS:    int(willQoS),
				WillFlag:   willFlag,
				CleanStart: meta.cleanStart,
			},
		},
		ConnectPayload: packet.ConnectPayload{
			ClientID: clientID,
			Username: username,
			Password: password,
		},
	}
	return cp, meta, nil
}

func parseConnectProps(b []byte, meta *connectMeta) error {
	off := 0
	for off < len(b) {
		id := b[off]
		off++
		switch id {
		case 0x11:
			if off+4 > len(b) {
				return io.ErrUnexpectedEOF
			}
			meta.sessionExpiry = time.Duration(binary.BigEndian.Uint32(b[off:off+4])) * time.Second
			off += 4
		case 0x21:
			if off+2 > len(b) {
				return io.ErrUnexpectedEOF
			}
			meta.receiveMax = int(binary.BigEndian.Uint16(b[off : off+2]))
			off += 2
			if meta.receiveMax == 0 {
				return errors.New("receive maximum is zero")
			}
		case 0x27:
			if off+4 > len(b) {
				return io.ErrUnexpectedEOF
			}
			meta.maxPacketSize = int(binary.BigEndian.Uint32(b[off : off+4]))
			off += 4
			if meta.maxPacketSize == 0 {
				return errors.New("max packet size is zero")
			}
		case 0x22:
			if off+2 > len(b) {
				return io.ErrUnexpectedEOF
			}
			meta.topicAliasMax = int(binary.BigEndian.Uint16(b[off : off+2]))
			off += 2
		case 0x19:
			if off >= len(b) {
				return io.ErrUnexpectedEOF
			}
			meta.requestResp = b[off] == 1
			off++
		case 0x17:
			if off >= len(b) {
				return io.ErrUnexpectedEOF
			}
			off++
		case 0x26:
			k, n, err := readMQTTString(b, off)
			if err != nil {
				return err
			}
			v, n, err := readMQTTString(b, n)
			if err != nil {
				return err
			}
			meta.user = append(meta.user, [2]string{k, v})
			off = n
		default:
			return errors.New("unsupported connect property")
		}
	}
	return nil
}

func parsePublishProps(b []byte) (mqttProps, error) {
	var props mqttProps
	off := 0
	for off < len(b) {
		id := b[off]
		off++
		switch id {
		case 0x02:
			if off+4 > len(b) {
				return props, io.ErrUnexpectedEOF
			}
			props.hasExpiry = true
			props.messageExpiry = time.Duration(binary.BigEndian.Uint32(b[off:off+4])) * time.Second
			off += 4
		case 0x03:
			s, n, err := readMQTTString(b, off)
			if err != nil {
				return props, err
			}
			props.contentType = s
			off = n
		case 0x08:
			s, n, err := readMQTTString(b, off)
			if err != nil {
				return props, err
			}
			props.responseTopic = s
			off = n
		case 0x09:
			raw, n, err := readMQTTBytes(b, off)
			if err != nil {
				return props, err
			}
			props.correlation = raw
			off = n
		case 0x23:
			if off+2 > len(b) {
				return props, io.ErrUnexpectedEOF
			}
			props.hasAlias = true
			props.topicAlias = binary.BigEndian.Uint16(b[off : off+2])
			off += 2
		case 0x26:
			k, n, err := readMQTTString(b, off)
			if err != nil {
				return props, err
			}
			v, n, err := readMQTTString(b, n)
			if err != nil {
				return props, err
			}
			props.user = append(props.user, [2]string{k, v})
			off = n
		case 0x01, 0x0b:
			if off >= len(b) {
				return props, io.ErrUnexpectedEOF
			}
			off++
		default:
			return props, errors.New("unsupported publish property")
		}
	}
	return props, nil
}

type pubMsg struct {
	topic   string
	payload []byte
	qos     byte
	dup     bool
	retain  bool
	rel     bool
	id      uint16
	props   mqttProps
}

func parsePublish(level byte, flags byte, body []byte) (pubMsg, error) {
	var msg pubMsg
	if flags&0x06 == 0x06 {
		return msg, errors.New("invalid qos")
	}
	msg.retain = flags&0x01 != 0
	msg.qos = (flags >> 1) & 0x03
	msg.dup = flags&0x08 != 0
	off := 0
	topic, n, err := readMQTTString(body, off)
	if err != nil {
		return msg, err
	}
	off = n
	msg.topic = topic
	if msg.qos > 0 {
		if off+2 > len(body) {
			return msg, io.ErrUnexpectedEOF
		}
		msg.id = binary.BigEndian.Uint16(body[off : off+2])
		off += 2
	}
	if level == protoLevel5 {
		plen, n, err := readVarInt(body, off)
		if err != nil {
			return msg, err
		}
		off = n
		if off+plen > len(body) {
			return msg, io.ErrUnexpectedEOF
		}
		props, err := parsePublishProps(body[off : off+plen])
		if err != nil {
			return msg, err
		}
		msg.props = props
		off += plen
	}
	msg.payload = append([]byte(nil), body[off:]...)
	return msg, nil
}

type subReq struct {
	id   uint16
	subs []struct {
		topic string
		qos   byte
	}
}

func parseSubscribe(level byte, body []byte) (subReq, error) {
	var req subReq
	if len(body) < 2 {
		return req, io.ErrUnexpectedEOF
	}
	req.id = binary.BigEndian.Uint16(body[:2])
	off := 2
	if level == protoLevel5 {
		plen, n, err := readVarInt(body, off)
		if err != nil {
			return req, err
		}
		off = n + plen
		if off > len(body) {
			return req, io.ErrUnexpectedEOF
		}
	}
	for off < len(body) {
		topic, n, err := readMQTTString(body, off)
		if err != nil {
			return req, err
		}
		if n >= len(body) {
			return req, io.ErrUnexpectedEOF
		}
		qos := body[n]
		if qos&0xfc != 0 || qos > 2 {
			return req, errors.New("bad subscribe qos")
		}
		req.subs = append(req.subs, struct {
			topic string
			qos   byte
		}{topic, qos})
		off = n + 1
	}
	if len(req.subs) == 0 {
		return req, errors.New("empty subscribe")
	}
	return req, nil
}

func parseUnsub(level byte, body []byte) (uint16, []string, error) {
	if len(body) < 2 {
		return 0, nil, io.ErrUnexpectedEOF
	}
	id := binary.BigEndian.Uint16(body[:2])
	off := 2
	if level == protoLevel5 {
		plen, n, err := readVarInt(body, off)
		if err != nil {
			return 0, nil, err
		}
		off = n + plen
	}
	var topics []string
	for off < len(body) {
		topic, n, err := readMQTTString(body, off)
		if err != nil {
			return 0, nil, err
		}
		topics = append(topics, topic)
		off = n
	}
	return id, topics, nil
}

func parseAck(body []byte) (uint16, byte, error) {
	if len(body) < 2 {
		return 0, 0, io.ErrUnexpectedEOF
	}
	id := binary.BigEndian.Uint16(body[:2])
	reason := byte(0)
	if len(body) >= 3 {
		reason = body[2]
	}
	return id, reason, nil
}

func writeConnAck(w io.Writer, level byte, sessionPresent bool, reason byte, assignedClientID string) error {
	flags := byte(0)
	if sessionPresent {
		flags = 0x01
	}
	if level != protoLevel5 {
		if reason > 5 {
			reason = 0x05
		}
		return writePacket(w, packet.CONNACK<<4, []byte{flags, reason})
	}
	props := []byte{}
	if assignedClientID != "" {
		props = append(props, 0x12)
		props = appendMQTTString(props, assignedClientID)
	}
	body := []byte{flags, reason}
	body = append(body, writeRemaining(len(props))...)
	body = append(body, props...)
	return writePacket(w, packet.CONNACK<<4, body)
}

func writePub(w io.Writer, level byte, msg pubMsg) error {
	first := byte(packet.PUBLISH << 4)
	if msg.dup {
		first |= 0x08
	}
	first |= msg.qos << 1
	if msg.retain {
		first |= 0x01
	}
	body := appendMQTTString(nil, msg.topic)
	if msg.qos > 0 {
		id := make([]byte, 2)
		binary.BigEndian.PutUint16(id, msg.id)
		body = append(body, id...)
	}
	if level == protoLevel5 {
		body = append(body, 0)
	}
	body = append(body, msg.payload...)
	return writePacket(w, first, body)
}

func writeAck(w io.Writer, level byte, kind byte, id uint16, reason byte) error {
	first := kind << 4
	if kind == packet.PUBREL {
		first |= 0x02
	}
	body := make([]byte, 2)
	binary.BigEndian.PutUint16(body, id)
	if level == protoLevel5 && (reason != 0 || kind == packet.PUBACK || kind == packet.PUBREC) {
		body = append(body, reason)
	}
	return writePacket(w, first, body)
}

func writeSubAck(w io.Writer, level byte, id uint16, codes []byte) error {
	body := make([]byte, 2)
	binary.BigEndian.PutUint16(body, id)
	if level == protoLevel5 {
		body = append(body, 0)
	}
	body = append(body, codes...)
	return writePacket(w, packet.SUBACK<<4, body)
}

func writeUnsubAck(w io.Writer, level byte, id uint16, n int) error {
	body := make([]byte, 2)
	binary.BigEndian.PutUint16(body, id)
	if level == protoLevel5 {
		body = append(body, 0)
		for i := 0; i < n; i++ {
			body = append(body, reasonSuccess)
		}
	}
	return writePacket(w, packet.UNSUBACK<<4, body)
}

func writeDisconnect(w io.Writer, level byte, reason byte) error {
	if level != protoLevel5 {
		return writePacket(w, packet.DISCONNECT<<4, nil)
	}
	return writePacket(w, packet.DISCONNECT<<4, []byte{reason})
}

func writePingResp(w io.Writer) error {
	return writePacket(w, packet.PINGRESP<<4, nil)
}

func readMQTTString(b []byte, off int) (string, int, error) {
	raw, n, err := readMQTTBytes(b, off)
	return string(raw), n, err
}

func readMQTTBytes(b []byte, off int) ([]byte, int, error) {
	if off+2 > len(b) {
		return nil, off, io.ErrUnexpectedEOF
	}
	n := int(binary.BigEndian.Uint16(b[off : off+2]))
	off += 2
	if off+n > len(b) {
		return nil, off, io.ErrUnexpectedEOF
	}
	return append([]byte(nil), b[off:off+n]...), off + n, nil
}

func readVarInt(b []byte, off int) (int, int, error) {
	mul := 1
	val := 0
	for i := 0; i < 4; i++ {
		if off >= len(b) {
			return 0, off, io.ErrUnexpectedEOF
		}
		c := b[off]
		off++
		val += int(c&127) * mul
		if c&128 == 0 {
			return val, off, nil
		}
		mul *= 128
	}
	return 0, off, errors.New("malformed variable integer")
}

func appendMQTTString(dst []byte, s string) []byte {
	n := make([]byte, 2)
	binary.BigEndian.PutUint16(n, uint16(len(s)))
	dst = append(dst, n...)
	return append(dst, s...)
}

func writeConnect(level byte, flags byte, keepAlive uint16, clientID, username, password string, props []byte, willTopic string, willMsg []byte) []byte {
	body := appendMQTTString(nil, "MQTT")
	body = append(body, level, flags)
	k := make([]byte, 2)
	binary.BigEndian.PutUint16(k, keepAlive)
	body = append(body, k...)
	if level == protoLevel5 {
		if props == nil {
			props = []byte{}
		}
		body = append(body, writeRemaining(len(props))...)
		body = append(body, props...)
	}
	body = appendMQTTString(body, clientID)
	if flags&0x04 != 0 {
		if level == protoLevel5 {
			body = append(body, 0)
		}
		body = appendMQTTString(body, willTopic)
		n := make([]byte, 2)
		binary.BigEndian.PutUint16(n, uint16(len(willMsg)))
		body = append(body, n...)
		body = append(body, willMsg...)
	}
	if flags&0x80 != 0 {
		body = appendMQTTString(body, username)
	}
	if flags&0x40 != 0 {
		n := make([]byte, 2)
		binary.BigEndian.PutUint16(n, uint16(len(password)))
		body = append(body, n...)
		body = append(body, password...)
	}
	out := []byte{packet.CONNECT << 4}
	out = append(out, writeRemaining(len(body))...)
	return append(out, body...)
}
