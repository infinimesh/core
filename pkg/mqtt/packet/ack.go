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

func parseAck(level, kind, flags byte, body []byte) (ControlPacket, error) {
	if kind == PUBREL {
		if flags != 0x02 {
			return nil, errors.New("pubrel flags")
		}
	} else if flags != 0 {
		return nil, errors.New("ack flags")
	}
	if len(body) < 2 {
		return nil, io.ErrUnexpectedEOF
	}
	ack := &Ack{
		ProtocolLevel: level,
		Kind:          kind,
		PacketID:      u16(body[:2]),
		wire:          wire{remain: len(body)},
	}
	if len(body) >= 3 {
		ack.Reason = body[2]
	}
	return ack, nil
}

func (a *Ack) WriteTo(w io.Writer) (int64, error) {
	first := a.Kind << 4
	if a.Kind == PUBREL {
		first |= 0x02
	}
	body := putU16(nil, a.PacketID)
	if a.ProtocolLevel == ProtocolMQTT5 && (a.Reason != 0 || a.Kind == PUBACK || a.Kind == PUBREC) {
		body = append(body, a.Reason)
	}
	return writePacket(w, first, body)
}
