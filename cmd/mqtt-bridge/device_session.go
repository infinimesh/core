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
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/infinimesh/core/pkg/mqtt/packet"
)

var (
	bridgeSessions = newSessionStore()
	retryAfter     = 5 * time.Second
)

type deviceHooks struct {
	reported   func(payload []byte, props packet.Properties, retain bool) bool
	connected  func(up bool)
	enabled    func() bool
	subscribed func(send func([]byte))
	unsub      func()
	will       func(topic string, payload []byte, retain bool)
}

func serveDevice(c net.Conn, deviceID string, cp *packet.Connect, meta connectMeta, store *sessionStore, hooks deviceHooks) {
	level := cp.ProtocolLevel
	clientID := cp.ClientID
	assigned := ""
	if clientID == "" {
		if !meta.cleanStart && level != packet.ProtocolMQTT5 {
			_ = writeConnAck(c, level, false, packet.CodeIdentifierRejected, "")
			return
		}
		clientID = deviceID
		assigned = deviceID
	}
	if meta.receiveMax == 0 {
		meta.receiveMax = 65535
	}
	present, sess := store.open(deviceID, clientID, meta, time.Now())
	if err := writeConnAck(c, level, present, packet.ReasonSuccess, assigned); err != nil {
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
			_ = write(func(w io.Writer) error { return writeDisconnect(w, level, packet.ReasonNotAuthorized) })
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
		pkt, err := packet.ReadPacket(c, level, 0)
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
						_ = write(func(w io.Writer) error { return writeAck(w, level, packet.PUBREL, msg.id, packet.ReasonSuccess) })
						continue
					}
					_ = write(func(w io.Writer) error { return writePub(w, level, msg) })
				}
				continue
			}
			var pe packet.ProtocolError
			if errors.As(err, &pe) {
				_ = write(func(w io.Writer) error { return writeDisconnect(w, level, packet.ReasonMalformed) })
				return
			}
			unclean = true
			return
		}
		last = time.Now()
		if meta.maxPacketSize > 0 && packet.PacketRemain(pkt)+5 > meta.maxPacketSize {
			_ = write(func(w io.Writer) error { return writeDisconnect(w, level, packet.ReasonPacketTooLarge) })
			return
		}
		switch p := pkt.(type) {
		case *packet.PingReq:
			if err := write(func(w io.Writer) error { return writePingResp(w) }); err != nil {
				unclean = true
				return
			}
		case *packet.Disconnect:
			return
		case *packet.Publish:
			if !acceptPublish(c, write, level, deviceID, sess, hooks, pubFrom(p)) {
				return
			}
		case *packet.Ack:
			if err := handleAck(write, level, sess, sendQueued, p); err != nil {
				unclean = true
				return
			}
		case *packet.Subscribe:
			codes := make([]byte, len(p.Filters))
			want := false
			for i, sub := range p.Filters {
				if classifyTopic(sub.Topic, deviceID) != topicDesired {
					if level == packet.ProtocolMQTT5 {
						codes[i] = packet.ReasonNotAuthorized
					} else {
						codes[i] = packet.ReasonUnspecified
					}
					continue
				}
				codes[i] = sub.QoS
				sess.mu.Lock()
				sess.subQoS = int(sub.QoS)
				sess.mu.Unlock()
				want = true
			}
			if err := write(func(w io.Writer) error { return writeSubAck(w, level, p.PacketID, codes) }); err != nil {
				unclean = true
				return
			}
			if want && hooks.subscribed != nil {
				hooks.subscribed(sendDesired)
			}
		case *packet.Unsubscribe:
			for _, topic := range p.Topics {
				if classifyTopic(topic, deviceID) == topicDesired {
					sess.mu.Lock()
					sess.subQoS = -1
					sess.mu.Unlock()
					if hooks.unsub != nil {
						hooks.unsub()
					}
				}
			}
			if err := write(func(w io.Writer) error { return writeUnsubAck(w, level, p.PacketID, len(p.Topics)) }); err != nil {
				unclean = true
				return
			}
		default:
			_ = write(func(w io.Writer) error { return writeDisconnect(w, level, packet.ReasonMalformed) })
			return
		}
	}
}

func handleAck(write func(func(io.Writer) error) error, level byte, sess *deviceSession, sendQueued func(pubMsg, bool), p *packet.Ack) error {
	switch p.Kind {
	case packet.PUBACK:
		sess.mu.Lock()
		sess.ack(p.PacketID)
		next, ok := sess.popPending()
		sess.mu.Unlock()
		sendQueued(next, ok)
	case packet.PUBREC:
		sess.mu.Lock()
		_, ok := sess.markPubRel(p.PacketID)
		sess.mu.Unlock()
		if ok {
			_ = write(func(w io.Writer) error {
				return writeAck(w, level, packet.PUBREL, p.PacketID, packet.ReasonSuccess)
			})
		}
	case packet.PUBREL:
		sess.mu.Lock()
		_, known := sess.inQoS2[p.PacketID]
		if known {
			delete(sess.inQoS2, p.PacketID)
		}
		sess.mu.Unlock()
		reason := packet.ReasonSuccess
		if !known && level == packet.ProtocolMQTT5 {
			reason = packet.ReasonPacketIDNotFound
		}
		if err := write(func(w io.Writer) error {
			return writeAck(w, level, packet.PUBCOMP, p.PacketID, reason)
		}); err != nil {
			return err
		}
	case packet.PUBCOMP:
		sess.mu.Lock()
		sess.ack(p.PacketID)
		next, ok := sess.popPending()
		sess.mu.Unlock()
		sendQueued(next, ok)
	}
	return nil
}

func acceptPublish(c net.Conn, write func(func(io.Writer) error) error, level byte, deviceID string, sess *deviceSession, hooks deviceHooks, msg pubMsg) bool {
	sess.mu.Lock()
	if msg.props.HasTopicAlias {
		if sess.aliasMax == 0 || msg.props.TopicAlias == 0 || int(msg.props.TopicAlias) > sess.aliasMax {
			sess.mu.Unlock()
			_ = write(func(w io.Writer) error { return writeDisconnect(w, level, packet.ReasonTopicAliasInvalid) })
			return false
		}
		if msg.topic != "" {
			sess.aliases[msg.props.TopicAlias] = msg.topic
		} else if topic, ok := sess.aliases[msg.props.TopicAlias]; ok {
			msg.topic = topic
		} else {
			sess.mu.Unlock()
			_ = write(func(w io.Writer) error { return writeDisconnect(w, level, packet.ReasonTopicAliasInvalid) })
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
		if msg.qos == 0 || level != packet.ProtocolMQTT5 {
			_ = write(func(w io.Writer) error { return writeDisconnect(w, level, packet.ReasonNotAuthorized) })
			return false
		}
		kindByte := packet.PUBACK
		if msg.qos == 2 {
			kindByte = packet.PUBREC
		}
		_ = write(func(w io.Writer) error { return writeAck(w, level, kindByte, msg.id, packet.ReasonNotAuthorized) })
		return true
	}
	if msg.props.HasMessageExpiry && msg.props.MessageExpiry == 0 {
		return ackPublish(write, level, msg, packet.ReasonSuccess)
	}
	if !dupQoS2 && hooks.reported != nil {
		if !hooks.reported(msg.payload, msg.props, msg.retain) {
			return ackPublish(write, level, msg, packet.ReasonPayloadInvalid)
		}
	}
	return ackPublish(write, level, msg, packet.ReasonSuccess)
}

func ackPublish(write func(func(io.Writer) error) error, level byte, msg pubMsg, reason byte) bool {
	switch msg.qos {
	case 0:
		if reason != packet.ReasonSuccess {
			_ = write(func(w io.Writer) error { return writeDisconnect(w, level, reason) })
			return false
		}
		return true
	case 1:
		if level != packet.ProtocolMQTT5 {
			reason = packet.ReasonSuccess
		}
		return write(func(w io.Writer) error { return writeAck(w, level, packet.PUBACK, msg.id, reason) }) == nil
	case 2:
		if level != packet.ProtocolMQTT5 && reason != packet.ReasonSuccess {
			_ = write(func(w io.Writer) error { return writeDisconnect(w, level, reason) })
			return false
		}
		if level != packet.ProtocolMQTT5 {
			reason = packet.ReasonSuccess
		}
		return write(func(w io.Writer) error { return writeAck(w, level, packet.PUBREC, msg.id, reason) }) == nil
	default:
		_ = write(func(w io.Writer) error { return writeDisconnect(w, level, packet.ReasonQoSNotSupported) })
		return false
	}
}
