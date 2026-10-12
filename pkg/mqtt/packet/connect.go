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

func parseConnect(body []byte) (ControlPacket, error) {
	cp := &Connect{wire: wire{remain: len(body)}}
	off := 0
	name, n, err := readString(body, off)
	if err != nil {
		return nil, err
	}
	off = n
	if name != "MQTT" && name != "MQIsdp" {
		return nil, errors.New("invalid protocol")
	}
	if off+4 > len(body) {
		return nil, io.ErrUnexpectedEOF
	}
	level := body[off]
	flags := body[off+1]
	keep := u16(body[off+2 : off+4])
	off += 4
	if flags&0x01 != 0 {
		return nil, errors.New("reserved connect flag")
	}
	willQoS := (flags >> 3) & 0x03
	willFlag := flags&0x04 != 0
	if willQoS == 3 || (!willFlag && (willQoS != 0 || flags&0x20 != 0)) {
		return nil, errors.New("bad will flags")
	}
	cp.ProtocolName = name
	cp.ProtocolLevel = level
	cp.CleanStart = flags&0x02 != 0
	cp.WillFlag = willFlag
	cp.WillQoS = willQoS
	cp.WillRetain = flags&0x20 != 0
	cp.PasswordFlag = flags&0x40 != 0
	cp.UsernameFlag = flags&0x80 != 0
	cp.KeepAlive = keep
	if level == ProtocolMQTT5 {
		raw, next, err := takeProperties(body, off)
		if err != nil {
			return nil, err
		}
		props, err := parseConnectProperties(raw)
		if err != nil {
			return nil, err
		}
		cp.Properties = props
		off = next
	}
	clientID, n, err := readString(body, off)
	if err != nil {
		return nil, err
	}
	off = n
	cp.ClientID = clientID
	if willFlag {
		if level == ProtocolMQTT5 {
			raw, next, err := takeProperties(body, off)
			if err != nil {
				return nil, err
			}
			props, err := parsePublishProperties(raw)
			if err != nil {
				return nil, err
			}
			cp.WillProperties = props
			off = next
		}
		topic, n, err := readString(body, off)
		if err != nil {
			return nil, err
		}
		msg, n, err := readBytes(body, n)
		if err != nil {
			return nil, err
		}
		off = n
		cp.WillTopic = topic
		cp.WillMessage = msg
	}
	if cp.UsernameFlag {
		username, n, err := readString(body, off)
		if err != nil {
			return nil, err
		}
		off = n
		cp.Username = username
	}
	if cp.PasswordFlag {
		pw, _, err := readBytes(body, off)
		if err != nil {
			return nil, err
		}
		cp.Password = string(pw)
	}
	return cp, nil
}

func (c *Connect) flags() byte {
	var flags byte
	if c.CleanStart {
		flags |= 0x02
	}
	if c.WillFlag {
		flags |= 0x04
	}
	flags |= (c.WillQoS & 0x03) << 3
	if c.WillRetain {
		flags |= 0x20
	}
	if c.PasswordFlag {
		flags |= 0x40
	}
	if c.UsernameFlag {
		flags |= 0x80
	}
	return flags
}

func (c *Connect) WriteTo(w io.Writer) (int64, error) {
	name := c.ProtocolName
	if name == "" {
		name = "MQTT"
	}
	body := appendString(nil, name)
	body = append(body, c.ProtocolLevel, c.flags())
	body = putU16(body, c.KeepAlive)
	if c.ProtocolLevel == ProtocolMQTT5 {
		props := c.Properties.encode()
		body = appendRemaining(body, len(props))
		body = append(body, props...)
	}
	body = appendString(body, c.ClientID)
	if c.WillFlag {
		if c.ProtocolLevel == ProtocolMQTT5 {
			props := c.WillProperties.encode()
			body = appendRemaining(body, len(props))
			body = append(body, props...)
		}
		body = appendString(body, c.WillTopic)
		body = appendBytes(body, c.WillMessage)
	}
	if c.UsernameFlag {
		body = appendString(body, c.Username)
	}
	if c.PasswordFlag {
		body = appendBytes(body, []byte(c.Password))
	}
	return writePacket(w, CONNECT<<4, body)
}
