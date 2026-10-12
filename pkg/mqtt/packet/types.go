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

// ProtocolError is a packet that broke the MQTT rules. The bytes were read.
// A socket error is not a ProtocolError.
type ProtocolError struct{ Msg string }

func (e ProtocolError) Error() string { return e.Msg }

func protocol(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(ProtocolError); ok {
		return err
	}
	return ProtocolError{Msg: err.Error()}
}

// wire records the remaining length so a caller can apply its own size cap
// after the packet has been read.
type wire struct{ remain int }

func (w wire) Remain() int { return w.remain }

// PacketRemain is the remaining length of p, excluding the fixed header.
func PacketRemain(p ControlPacket) int {
	type rem interface{ Remain() int }
	if r, ok := p.(rem); ok {
		return r.Remain()
	}
	return 0
}

type Connect struct {
	ProtocolName   string
	ProtocolLevel  byte
	CleanStart     bool
	WillFlag       bool
	WillQoS        byte
	WillRetain     bool
	PasswordFlag   bool
	UsernameFlag   bool
	KeepAlive      uint16
	Properties     ConnectProperties
	ClientID       string
	WillProperties Properties
	WillTopic      string
	WillMessage    []byte
	Username       string
	Password       string
	wire
}

type Publish struct {
	ProtocolLevel byte
	Dup           bool
	QoS           byte
	Retain        bool
	Topic         string
	PacketID      uint16
	Properties    Properties
	Payload       []byte
	wire
}

type Ack struct {
	ProtocolLevel byte
	Kind          byte
	PacketID      uint16
	Reason        byte
	wire
}

type Subscription struct {
	Topic string
	QoS   byte
}

type Subscribe struct {
	ProtocolLevel byte
	PacketID      uint16
	Filters       []Subscription
	wire
}

type SubAck struct {
	ProtocolLevel byte
	PacketID      uint16
	Reasons       []byte
	wire
}

type Unsubscribe struct {
	ProtocolLevel byte
	PacketID      uint16
	Topics        []string
	wire
}

type UnsubAck struct {
	ProtocolLevel byte
	PacketID      uint16
	Reasons       []byte
	wire
}

type ConnAck struct {
	ProtocolLevel    byte
	SessionPresent   bool
	Reason           byte
	AssignedClientID string
	ReceiveMaximum   uint16
	MaxPacketSize    uint32
	TopicAliasMax    uint16
	wire
}

type Disconnect struct {
	ProtocolLevel byte
	Reason        byte
	wire
}

type PingReq struct{ wire }

type PingResp struct{ wire }

type Auth struct {
	Reason byte
	wire
}
