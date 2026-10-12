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
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/infinimesh/core/pkg/mqtt/packet"
)

var (
	bridgeSessions      = newSessionStore()
	retryAfter          = 5 * time.Second
	enabledCheckAfter   = 30 * time.Second
	qos2HoldFor         = 2 * time.Minute
	serverReceiveMax    = 32
	serverTopicAliasMax = 16
	serverMaxPacketSize = 1 << 20
)

type deviceHooks struct {
	reported   func(payload []byte, props packet.Properties, retain bool) bool
	connected  func(up bool)
	enabled    func() bool
	subscribed func(send func([]byte, packet.Properties))
	unsub      func()
	will       func(topic string, payload []byte, retain bool)
}

func serveDevice(c net.Conn, deviceID string, cp *packet.Connect, meta connectMeta, store *sessionStore, hooks deviceHooks) {
	retry := retryAfter
	enabledEvery := enabledCheckAfter
	maxPacket := serverMaxPacketSize
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
	sess.mu.Lock()
	sess.qos2Max = serverReceiveMax
	sess.qos2Hold = qos2HoldFor
	sess.mu.Unlock()
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
		for ok {
			if meta.maxPacketSize > 0 && pubSize(level, msg) > meta.maxPacketSize {
				if msg.qos == 0 {
					return
				}
				sess.mu.Lock()
				sess.ack(msg.id)
				msg, ok = sess.popPending()
				sess.mu.Unlock()
				continue
			}
			_ = write(func(w io.Writer) error { return writePub(w, level, msg) })
			return
		}
	}
	sendDesired := func(payload []byte, props packet.Properties) {
		if props.ContentType == "" {
			props.ContentType = "application/json"
		}
		sess.mu.Lock()
		if sess.subQoS < 0 {
			sess.mu.Unlock()
			return
		}
		msg, ok := sess.enqueue(desiredTopic(deviceID), payload, byte(sess.subQoS), props)
		sess.mu.Unlock()
		sendQueued(msg, ok)
	}

	if present {
		sess.mu.Lock()
		pending := sess.unacked(time.Now())
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
		if hooks.enabled != nil {
			deadline = sooner(deadline, time.Now().Add(enabledEvery))
		}
		sess.mu.Lock()
		waiting := len(sess.out) > 0
		qos2At := sess.nextQoS2Deadline()
		sess.mu.Unlock()
		if waiting {
			deadline = sooner(deadline, time.Now().Add(retry))
		}
		if !qos2At.IsZero() {
			deadline = sooner(deadline, qos2At)
		}
		if err := c.SetReadDeadline(deadline); err != nil {
			unclean = true
			return
		}
		pkt, err := packet.ReadPacket(c, level, maxPacket)
		if err != nil {
			if errors.Is(err, packet.ErrPacketTooLarge) {
				_ = write(func(w io.Writer) error { return writeDisconnect(w, level, packet.ReasonPacketTooLarge) })
				return
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if meta.keepAlive > 0 && time.Since(last) >= meta.keepAlive+meta.keepAlive/2 {
					unclean = true
					return
				}
				sess.mu.Lock()
				sess.sweepQoS2(time.Now())
				due := sess.due(time.Now(), retry)
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
			if p.Kind == packet.PUBREL {
				if err := finishQoS2(write, level, sess, hooks, p.PacketID); err != nil {
					unclean = true
					return
				}
				continue
			}
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
	case packet.PUBCOMP:
		sess.mu.Lock()
		sess.ack(p.PacketID)
		next, ok := sess.popPending()
		sess.mu.Unlock()
		sendQueued(next, ok)
	}
	return nil
}

func finishQoS2(write func(func(io.Writer) error) error, level byte, sess *deviceSession, hooks deviceHooks, id uint16) error {
	sess.mu.Lock()
	held, known := sess.takeQoS2(id, time.Now())
	sess.mu.Unlock()
	if known && held.deliver && hooks.reported != nil {
		hooks.reported(held.msg.payload, held.msg.props, held.msg.retain)
	}
	reason := packet.ReasonSuccess
	if !known && level == packet.ProtocolMQTT5 {
		reason = packet.ReasonPacketIDNotFound
	}
	return write(func(w io.Writer) error {
		return writeAck(w, level, packet.PUBCOMP, id, reason)
	})
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
	sess.mu.Unlock()

	kind := classifyTopic(msg.topic, deviceID)
	if kind != topicReported {
		if msg.qos == 2 {
			ack, open := holdQoS2(write, level, sess, msg, false)
			if !open {
				return false
			}
			if !ack {
				return true
			}
		}
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
		if msg.qos == 2 {
			ack, open := holdQoS2(write, level, sess, msg, false)
			if !open {
				return false
			}
			if !ack {
				return true
			}
		}
		return ackPublish(write, level, msg, packet.ReasonSuccess)
	}
	if msg.qos == 2 {
		ack, open := holdQoS2(write, level, sess, msg, true)
		if !open {
			return false
		}
		if !ack {
			return true
		}
		return ackPublish(write, level, msg, packet.ReasonSuccess)
	}
	if hooks.reported != nil {
		if !hooks.reported(msg.payload, msg.props, msg.retain) {
			return ackPublish(write, level, msg, packet.ReasonPayloadInvalid)
		}
	}
	return ackPublish(write, level, msg, packet.ReasonSuccess)
}

func holdQoS2(write func(func(io.Writer) error) error, level byte, sess *deviceSession, msg pubMsg, deliver bool) (ack bool, open bool) {
	deadline := time.Now().Add(sess.qos2Hold)
	if deliver && msg.props.HasMessageExpiry && msg.props.MessageExpiry > 0 {
		exp := time.Now().Add(msg.props.MessageExpiry)
		if exp.Before(deadline) {
			deadline = exp
		}
	}
	sess.mu.Lock()
	_, ok := sess.trackQoS2(msg.id, qos2In{deliver: deliver, msg: msg, deadline: deadline}, time.Now())
	sess.mu.Unlock()
	if ok {
		return true, true
	}
	if level != packet.ProtocolMQTT5 {
		_ = write(func(w io.Writer) error { return writeDisconnect(w, level, packet.ReasonUnspecified) })
		return false, false
	}
	_ = write(func(w io.Writer) error { return writeAck(w, level, packet.PUBREC, msg.id, packet.ReasonReceiveMaximum) })
	return false, true
}

func sooner(deadline, at time.Time) time.Time {
	if deadline.IsZero() || at.Before(deadline) {
		return at
	}
	return deadline
}

func pubSize(level byte, msg pubMsg) int {
	var buf bytes.Buffer
	_ = writePub(&buf, level, msg)
	return buf.Len()
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
