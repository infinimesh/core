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
	"sync"
	"time"

	"github.com/infinimesh/core/pkg/mqtt/packet"
)

type inflight struct {
	id      uint16
	topic   string
	payload []byte
	qos     byte
	sent    time.Time
	stage   byte
	props   packet.Properties
	expires time.Time
}

type pendingDown struct {
	topic   string
	payload []byte
	qos     byte
	props   packet.Properties
	expires time.Time
}

type qos2In struct {
	deliver  bool
	msg      pubMsg
	deadline time.Time
}

type deviceSession struct {
	mu         sync.Mutex
	deviceID   string
	clientID   string
	expiry     time.Time
	keep       bool
	subQoS     int
	out        []inflight
	pending    []pendingDown
	inQoS2     map[uint16]qos2In
	nextID     uint16
	receiveMax int
	aliases    map[uint16]string
	aliasMax   int
	qos2Max    int
	qos2Hold   time.Duration
}

type sessionStore struct {
	mu   sync.Mutex
	byID map[string]*deviceSession
}

func newSessionStore() *sessionStore {
	return &sessionStore{byID: map[string]*deviceSession{}}
}

func sessionKey(deviceID, clientID string) string {
	return deviceID + "\x00" + clientID
}

func (s *sessionStore) open(deviceID, clientID string, meta connectMeta, now time.Time) (present bool, sess *deviceSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionKey(deviceID, clientID)
	old := s.byID[key]
	if meta.cleanStart || old == nil || (!old.expiry.IsZero() && !now.Before(old.expiry)) {
		sess = &deviceSession{
			deviceID:   deviceID,
			clientID:   clientID,
			subQoS:     -1,
			inQoS2:     map[uint16]qos2In{},
			nextID:     1,
			receiveMax: meta.receiveMax,
			aliases:    map[uint16]string{},
			aliasMax:   serverTopicAliasMax,
		}
		if meta.receiveMax == 0 {
			sess.receiveMax = 65535
		}
		s.byID[key] = sess
		return false, sess
	}
	old.receiveMax = meta.receiveMax
	if old.receiveMax == 0 {
		old.receiveMax = 65535
	}
	old.aliasMax = serverTopicAliasMax
	old.aliases = map[uint16]string{}
	return true, old
}

func (s *sessionStore) close(deviceID, clientID string, meta connectMeta, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionKey(deviceID, clientID)
	sess := s.byID[key]
	if sess == nil {
		return
	}
	if meta.sessionExpiry <= 0 {
		delete(s.byID, key)
		return
	}
	sess.mu.Lock()
	sess.expiry = now.Add(meta.sessionExpiry)
	sess.keep = true
	sess.mu.Unlock()
}

func (s *deviceSession) allocID() uint16 {
	for {
		id := s.nextID
		if id == 0 {
			id = 1
		}
		s.nextID = id + 1
		used := false
		for _, m := range s.out {
			if m.id == id {
				used = true
				break
			}
		}
		if !used {
			return id
		}
	}
}

func (s *deviceSession) enqueue(topic string, payload []byte, qos byte, props packet.Properties) (pubMsg, bool) {
	var exp time.Time
	if props.HasMessageExpiry {
		if props.MessageExpiry <= 0 {
			return pubMsg{}, false
		}
		exp = time.Now().Add(props.MessageExpiry)
	}
	return s.enqueueAt(topic, payload, qos, props, exp)
}

func (s *deviceSession) enqueueAt(topic string, payload []byte, qos byte, props packet.Properties, exp time.Time) (pubMsg, bool) {
	now := time.Now()
	if !exp.IsZero() && !now.Before(exp) {
		return pubMsg{}, false
	}
	msg := pubMsg{
		topic:   topic,
		payload: append([]byte(nil), payload...),
		qos:     qos,
		props:   outboundProps(props, exp, now),
	}
	if qos == 0 {
		return msg, true
	}
	if len(s.out) >= s.receiveMax {
		s.pending = append(s.pending, pendingDown{topic, msg.payload, qos, props, exp})
		return pubMsg{}, false
	}
	id := s.allocID()
	s.out = append(s.out, inflight{
		id: id, topic: topic, payload: msg.payload, qos: qos, sent: now, props: props, expires: exp,
	})
	msg.id = id
	return msg, true
}

func (s *deviceSession) popPending() (pubMsg, bool) {
	now := time.Now()
	for len(s.pending) > 0 && len(s.out) < s.receiveMax {
		p := s.pending[0]
		s.pending = s.pending[1:]
		if !p.expires.IsZero() && !now.Before(p.expires) {
			continue
		}
		return s.enqueueAt(p.topic, p.payload, p.qos, p.props, p.expires)
	}
	return pubMsg{}, false
}

func (s *deviceSession) ack(id uint16) bool {
	for i, m := range s.out {
		if m.id == id {
			s.out = append(s.out[:i], s.out[i+1:]...)
			return true
		}
	}
	return false
}

func (s *deviceSession) markPubRel(id uint16) (inflight, bool) {
	for i, m := range s.out {
		if m.id == id && m.qos == 2 && m.stage == 0 {
			s.out[i].stage = 1
			s.out[i].sent = time.Now()
			return s.out[i], true
		}
	}
	return inflight{}, false
}

func (m inflight) asPub(dup bool, now time.Time) pubMsg {
	return pubMsg{
		topic: m.topic, payload: m.payload, qos: m.qos, id: m.id, dup: dup,
		props: outboundProps(m.props, m.expires, now),
	}
}

func outboundProps(props packet.Properties, expires, now time.Time) packet.Properties {
	if !props.HasMessageExpiry || expires.IsZero() {
		return props
	}
	left := expires.Sub(now)
	if left <= 0 {
		props.MessageExpiry = 0
		return props
	}
	secs := (left + time.Second - 1) / time.Second
	props.MessageExpiry = secs * time.Second
	return props
}

func (s *deviceSession) due(now time.Time, after time.Duration) []pubMsg {
	kept := s.out[:0]
	var out []pubMsg
	for _, m := range s.out {
		if !m.expires.IsZero() && !now.Before(m.expires) {
			continue
		}
		if now.Sub(m.sent) >= after {
			m.sent = now
			if m.stage == 0 {
				out = append(out, m.asPub(true, now))
			} else {
				out = append(out, pubMsg{id: m.id, rel: true})
			}
		}
		kept = append(kept, m)
	}
	s.out = kept
	return out
}

func (s *deviceSession) unacked(now time.Time) []pubMsg {
	kept := s.out[:0]
	var out []pubMsg
	for _, m := range s.out {
		if !m.expires.IsZero() && !now.Before(m.expires) {
			continue
		}
		kept = append(kept, m)
		if m.stage == 0 {
			out = append(out, m.asPub(true, now))
		}
	}
	s.out = kept
	return out
}

func (s *deviceSession) sweepQoS2(now time.Time) {
	for id, h := range s.inQoS2 {
		if !now.Before(h.deadline) {
			delete(s.inQoS2, id)
		}
	}
}

func (s *deviceSession) nextQoS2Deadline() time.Time {
	var at time.Time
	for _, h := range s.inQoS2 {
		if at.IsZero() || h.deadline.Before(at) {
			at = h.deadline
		}
	}
	return at
}

func (s *deviceSession) trackQoS2(id uint16, h qos2In, now time.Time) (dup bool, ok bool) {
	s.sweepQoS2(now)
	if _, exists := s.inQoS2[id]; exists {
		return true, true
	}
	if len(s.inQoS2) >= s.qos2Max {
		return false, false
	}
	s.inQoS2[id] = h
	return false, true
}

func (s *deviceSession) takeQoS2(id uint16, now time.Time) (qos2In, bool) {
	s.sweepQoS2(now)
	h, ok := s.inQoS2[id]
	if !ok {
		return qos2In{}, false
	}
	delete(s.inQoS2, id)
	if !now.Before(h.deadline) {
		return qos2In{}, false
	}
	return h, true
}
