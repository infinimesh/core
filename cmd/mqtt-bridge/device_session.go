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
	"io"
	"net"
	"sync"
	"time"

	"github.com/slntopp/mqtt-go/packet"
)

var (
	bridgeSessions = newSessionStore()
	retryAfter     = 5 * time.Second
)

type deviceHooks struct {
	reported   func(payload []byte, props mqttProps, retain bool) bool
	connected  func(up bool)
	enabled    func() bool
	subscribed func(send func([]byte))
	unsub      func()
	will       func(topic string, payload []byte, retain bool)
}

func serveDevice(c net.Conn, deviceID string, cp *packet.ConnectControlPacket, meta connectMeta, store *sessionStore, hooks deviceHooks) {
	level := cp.VariableHeader.ProtocolLevel
	clientID := cp.ConnectPayload.ClientID
	assigned := ""
	if clientID == "" {
		if !meta.cleanStart && level != protoLevel5 {
			_ = writeConnAck(c, level, false, mqtt311IdentifierReject, "")
			return
		}
		clientID = deviceID
		assigned = deviceID
	}
	if meta.receiveMax == 0 {
		meta.receiveMax = 65535
	}
	present, sess := store.open(deviceID, clientID, meta, time.Now())
	if err := writeConnAck(c, level, present, reasonSuccess, assigned); err != nil {
		return
	}
	if hooks.connected != nil {
		hooks.connected(true)
		defer hooks.connected(false)
	}
	unclean := false
	defer func() {
		if unclean && meta.willTopic != "" && hooks.will != nil {
			hooks.will(meta.willTopic, meta.willMessage, meta.willRetain)
		}
		store.close(deviceID, clientID, meta, time.Now())
	}()

	var writeMu sync.Mutex
	write := func(fn func(io.Writer) error) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return fn(c)
	}
	sendQueued := func(msg pubMsg, ok bool) {
		if !ok {
			return
		}
		_ = write(func(w io.Writer) error { return writePub(w, level, msg) })
	}
	sendDesired := func(payload []byte) {
		sess.mu.Lock()
		if sess.subQoS < 0 {
			sess.mu.Unlock()
			return
		}
		msg, ok := sess.enqueue(desiredTopic(deviceID), payload, byte(sess.subQoS))
		sess.mu.Unlock()
		sendQueued(msg, ok)
	}

	if present {
		sess.mu.Lock()
		pending := sess.unacked()
		sess.mu.Unlock()
		for _, msg := range pending {
			_ = write(func(w io.Writer) error { return writePub(w, level, msg) })
		}
	}

	last := time.Now()
	for {
		if hooks.enabled != nil && !hooks.enabled() {
			_ = write(func(w io.Writer) error { return writeDisconnect(w, level, reasonNotAuthorized) })
			return
		}
		deadline := time.Time{}
		if meta.keepAlive > 0 {
			deadline = last.Add(meta.keepAlive + meta.keepAlive/2)
		}
		sess.mu.Lock()
		waiting := len(sess.out) > 0
		sess.mu.Unlock()
		if waiting {
			retryAt := time.Now().Add(retryAfter)
			if deadline.IsZero() || retryAt.Before(deadline) {
				deadline = retryAt
			}
		}
		if err := c.SetReadDeadline(deadline); err != nil {
			unclean = true
			return
		}
		typ, flags, body, err := readFrame(c)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if meta.keepAlive > 0 && time.Since(last) >= meta.keepAlive+meta.keepAlive/2 {
					unclean = true
					return
				}
				sess.mu.Lock()
				due := sess.due(time.Now(), retryAfter)
				sess.mu.Unlock()
				for _, msg := range due {
					if msg.rel {
						_ = write(func(w io.Writer) error { return writeAck(w, level, packet.PUBREL, msg.id, reasonSuccess) })
						continue
					}
					_ = write(func(w io.Writer) error { return writePub(w, level, msg) })
				}
				continue
			}
			unclean = true
			return
		}
		last = time.Now()
		if meta.maxPacketSize > 0 && len(body)+5 > meta.maxPacketSize {
			_ = write(func(w io.Writer) error { return writeDisconnect(w, level, reasonPacketTooLarge) })
			return
		}
		switch typ {
		case packet.PINGREQ:
			if err := write(func(w io.Writer) error { return writePingResp(w) }); err != nil {
				unclean = true
				return
			}
		case packet.DISCONNECT:
			return
		case packet.PUBLISH:
			msg, err := parsePublish(level, flags, body)
			if err != nil {
				_ = write(func(w io.Writer) error { return writeDisconnect(w, level, reasonMalformed) })
				return
			}
			if !acceptPublish(c, write, level, deviceID, sess, hooks, msg) {
				return
			}
		case packet.PUBACK:
			id, _, err := parseAck(body)
			if err != nil {
				_ = write(func(w io.Writer) error { return writeDisconnect(w, level, reasonMalformed) })
				return
			}
			sess.mu.Lock()
			sess.ack(id)
			next, ok := sess.popPending()
			sess.mu.Unlock()
			sendQueued(next, ok)
		case packet.PUBREC:
			id, _, err := parseAck(body)
			if err != nil {
				_ = write(func(w io.Writer) error { return writeDisconnect(w, level, reasonMalformed) })
				return
			}
			sess.mu.Lock()
			_, ok := sess.markPubRel(id)
			sess.mu.Unlock()
			if ok {
				_ = write(func(w io.Writer) error { return writeAck(w, level, packet.PUBREL, id, reasonSuccess) })
			}
		case packet.PUBREL:
			if flags != 0x02 {
				_ = write(func(w io.Writer) error { return writeDisconnect(w, level, reasonMalformed) })
				return
			}
			id, _, err := parseAck(body)
			if err != nil {
				_ = write(func(w io.Writer) error { return writeDisconnect(w, level, reasonMalformed) })
				return
			}
			sess.mu.Lock()
			_, known := sess.inQoS2[id]
			if known {
				delete(sess.inQoS2, id)
			}
			sess.mu.Unlock()
			reason := reasonSuccess
			if !known && level == protoLevel5 {
				reason = 0x92
			}
			if err := write(func(w io.Writer) error { return writeAck(w, level, packet.PUBCOMP, id, reason) }); err != nil {
				unclean = true
				return
			}
		case packet.PUBCOMP:
			id, _, err := parseAck(body)
			if err != nil {
				_ = write(func(w io.Writer) error { return writeDisconnect(w, level, reasonMalformed) })
				return
			}
			sess.mu.Lock()
			sess.ack(id)
			next, ok := sess.popPending()
			sess.mu.Unlock()
			sendQueued(next, ok)
		case packet.SUBSCRIBE:
			if flags != 0x02 {
				_ = write(func(w io.Writer) error { return writeDisconnect(w, level, reasonMalformed) })
				return
			}
			req, err := parseSubscribe(level, body)
			if err != nil {
				_ = write(func(w io.Writer) error { return writeDisconnect(w, level, reasonMalformed) })
				return
			}
			codes := make([]byte, len(req.subs))
			want := false
			for i, sub := range req.subs {
				if classifyTopic(sub.topic, deviceID) != topicDesired {
					if level == protoLevel5 {
						codes[i] = reasonNotAuthorized
					} else {
						codes[i] = reasonUnspecified
					}
					continue
				}
				codes[i] = sub.qos
				sess.mu.Lock()
				sess.subQoS = int(sub.qos)
				sess.mu.Unlock()
				want = true
			}
			if err := write(func(w io.Writer) error { return writeSubAck(w, level, req.id, codes) }); err != nil {
				unclean = true
				return
			}
			if want && hooks.subscribed != nil {
				hooks.subscribed(sendDesired)
			}
		case packet.UNSUBSCRIBE:
			if flags != 0x02 {
				_ = write(func(w io.Writer) error { return writeDisconnect(w, level, reasonMalformed) })
				return
			}
			id, topics, err := parseUnsub(level, body)
			if err != nil {
				_ = write(func(w io.Writer) error { return writeDisconnect(w, level, reasonMalformed) })
				return
			}
			for _, topic := range topics {
				if classifyTopic(topic, deviceID) == topicDesired {
					sess.mu.Lock()
					sess.subQoS = -1
					sess.mu.Unlock()
					if hooks.unsub != nil {
						hooks.unsub()
					}
				}
			}
			if err := write(func(w io.Writer) error { return writeUnsubAck(w, level, id, len(topics)) }); err != nil {
				unclean = true
				return
			}
		default:
			_ = write(func(w io.Writer) error { return writeDisconnect(w, level, reasonMalformed) })
			return
		}
	}
}

func acceptPublish(c net.Conn, write func(func(io.Writer) error) error, level byte, deviceID string, sess *deviceSession, hooks deviceHooks, msg pubMsg) bool {
	sess.mu.Lock()
	if msg.props.hasAlias {
		if sess.aliasMax == 0 || msg.props.topicAlias == 0 || int(msg.props.topicAlias) > sess.aliasMax {
			sess.mu.Unlock()
			_ = write(func(w io.Writer) error { return writeDisconnect(w, level, reasonTopicAliasInvalid) })
			return false
		}
		if msg.topic != "" {
			sess.aliases[msg.props.topicAlias] = msg.topic
		} else if topic, ok := sess.aliases[msg.props.topicAlias]; ok {
			msg.topic = topic
		} else {
			sess.mu.Unlock()
			_ = write(func(w io.Writer) error { return writeDisconnect(w, level, reasonTopicAliasInvalid) })
			return false
		}
	}
	dupQoS2 := false
	if msg.qos == 2 {
		if _, ok := sess.inQoS2[msg.id]; ok {
			dupQoS2 = true
		} else {
			sess.inQoS2[msg.id] = struct{}{}
		}
	}
	sess.mu.Unlock()

	kind := classifyTopic(msg.topic, deviceID)
	if kind != topicReported {
		if msg.qos == 0 || level != protoLevel5 {
			_ = write(func(w io.Writer) error { return writeDisconnect(w, level, reasonNotAuthorized) })
			return false
		}
		kindByte := packet.PUBACK
		if msg.qos == 2 {
			kindByte = packet.PUBREC
		}
		_ = write(func(w io.Writer) error { return writeAck(w, level, byte(kindByte), msg.id, reasonNotAuthorized) })
		return true
	}
	if msg.props.hasExpiry && msg.props.messageExpiry == 0 {
		return ackPublish(write, level, msg, reasonSuccess)
	}
	if !dupQoS2 && hooks.reported != nil {
		if !hooks.reported(msg.payload, msg.props, msg.retain) {
			return ackPublish(write, level, msg, reasonPayloadInvalid)
		}
	}
	return ackPublish(write, level, msg, reasonSuccess)
}

func ackPublish(write func(func(io.Writer) error) error, level byte, msg pubMsg, reason byte) bool {
	switch msg.qos {
	case 0:
		if reason != reasonSuccess {
			_ = write(func(w io.Writer) error { return writeDisconnect(w, level, reason) })
			return false
		}
		return true
	case 1:
		if level != protoLevel5 {
			reason = reasonSuccess
		}
		return write(func(w io.Writer) error { return writeAck(w, level, packet.PUBACK, msg.id, reason) }) == nil
	case 2:
		if level != protoLevel5 && reason != reasonSuccess {
			_ = write(func(w io.Writer) error { return writeDisconnect(w, level, reason) })
			return false
		}
		if level != protoLevel5 {
			reason = reasonSuccess
		}
		return write(func(w io.Writer) error { return writeAck(w, level, packet.PUBREC, msg.id, reason) }) == nil
	default:
		_ = write(func(w io.Writer) error { return writeDisconnect(w, level, reasonQoSNotSupported) })
		return false
	}
}
