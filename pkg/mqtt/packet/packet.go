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

import "io"

const (
	ProtocolMQTT311 byte = 4
	ProtocolMQTT5   byte = 5
)

const (
	CONNECT     byte = 1
	CONNACK     byte = 2
	PUBLISH     byte = 3
	PUBACK      byte = 4
	PUBREC      byte = 5
	PUBREL      byte = 6
	PUBCOMP     byte = 7
	SUBSCRIBE   byte = 8
	SUBACK      byte = 9
	UNSUBSCRIBE byte = 10
	UNSUBACK    byte = 11
	PINGREQ     byte = 12
	PINGRESP    byte = 13
	DISCONNECT  byte = 14
	AUTH        byte = 15
)

const (
	ReasonSuccess           byte = 0x00
	ReasonGrantedQoS0       byte = 0x00
	ReasonGrantedQoS1       byte = 0x01
	ReasonGrantedQoS2       byte = 0x02
	ReasonNormalDisconn     byte = 0x00
	ReasonUnspecified       byte = 0x80
	ReasonMalformed         byte = 0x81
	ReasonProtocolError     byte = 0x82
	ReasonImplSpecific      byte = 0x83
	ReasonUnsupportedProto  byte = 0x84
	ReasonClientIDRejected  byte = 0x85
	ReasonBadCredentials    byte = 0x86
	ReasonNotAuthorized     byte = 0x87
	ReasonServerUnavailable byte = 0x88
	ReasonServerBusy        byte = 0x89
	ReasonBanned            byte = 0x8A
	ReasonBadAuthMethod     byte = 0x8C
	ReasonTopicAliasInvalid byte = 0x94
	ReasonPacketTooLarge    byte = 0x95
	ReasonPacketIDInUse     byte = 0x91
	ReasonPacketIDNotFound  byte = 0x92
	ReasonPayloadInvalid    byte = 0x99
	ReasonQoSNotSupported   byte = 0x9B
)

// MQTT 3.1.1 CONNACK return codes.
const (
	CodeAccepted           byte = 0x00
	CodeUnacceptableProto  byte = 0x01
	CodeIdentifierRejected byte = 0x02
	CodeServerUnavailable  byte = 0x03
	CodeBadCredentials     byte = 0x04
	CodeNotAuthorized      byte = 0x05
)

type ControlPacket interface {
	WriteTo(io.Writer) (int64, error)
}

// ReadPacket reads one control packet. level is the negotiated protocol level.
// Pass 0 while reading CONNECT. maxSize is the MQTT 5 maximum packet size.
// Zero means no limit.
func ReadPacket(r io.Reader, level byte, maxSize int) (ControlPacket, error) {
	hdr := make([]byte, 1)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	typ := hdr[0] >> 4
	flags := hdr[0] & 0x0f
	remain, width, err := readRemaining(r)
	if err != nil {
		return nil, err
	}
	if maxSize > 0 && 1+width+remain > maxSize {
		return nil, ErrPacketTooLarge
	}
	body := make([]byte, remain)
	if remain > 0 {
		if _, err = io.ReadFull(r, body); err != nil {
			return nil, err
		}
	}
	var pkt ControlPacket
	switch typ {
	case CONNECT:
		if flags != 0 {
			return nil, ProtocolError{Msg: "connect flags"}
		}
		pkt, err = parseConnect(body)
	case CONNACK:
		pkt, err = parseConnAck(level, body)
	case PUBLISH:
		pkt, err = parsePublish(level, flags, body)
	case PUBACK, PUBREC, PUBREL, PUBCOMP:
		pkt, err = parseAck(level, typ, flags, body)
	case SUBSCRIBE:
		if flags != 0x02 {
			return nil, ProtocolError{Msg: "subscribe flags"}
		}
		pkt, err = parseSubscribe(level, body)
	case SUBACK:
		pkt, err = parseSubAck(level, body)
	case UNSUBSCRIBE:
		if flags != 0x02 {
			return nil, ProtocolError{Msg: "unsubscribe flags"}
		}
		pkt, err = parseUnsubscribe(level, body)
	case UNSUBACK:
		pkt, err = parseUnsubAck(level, body)
	case PINGREQ:
		if flags != 0 || len(body) != 0 {
			return nil, ProtocolError{Msg: "pingreq"}
		}
		return &PingReq{wire: wire{remain: len(body)}}, nil
	case PINGRESP:
		if flags != 0 || len(body) != 0 {
			return nil, ProtocolError{Msg: "pingresp"}
		}
		return &PingResp{wire: wire{remain: len(body)}}, nil
	case DISCONNECT:
		pkt, err = parseDisconnect(level, body)
	case AUTH:
		if flags != 0 {
			return nil, ProtocolError{Msg: "auth flags"}
		}
		pkt, err = parseAuth(body)
	default:
		return nil, ProtocolError{Msg: "unknown packet type"}
	}
	if err != nil {
		return nil, protocol(err)
	}
	return pkt, nil
}
