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
)

type inflight struct {
	id      uint16
	topic   string
	payload []byte
	qos     byte
	sent    time.Time
	stage   byte
}

type pendingDown struct {
	topic   string
	payload []byte
	qos     byte
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
	inQoS2     map[uint16]struct{}
	nextID     uint16
	receiveMax int
	aliases    map[uint16]string
	aliasMax   int
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
			inQoS2:     map[uint16]struct{}{},
			nextID:     1,
			receiveMax: meta.receiveMax,
			aliases:    map[uint16]string{},
			aliasMax:   meta.topicAliasMax,
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
	old.aliasMax = meta.topicAliasMax
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

func (s *deviceSession) enqueue(topic string, payload []byte, qos byte) (pubMsg, bool) {
	if qos == 0 {
		return pubMsg{topic: topic, payload: append([]byte(nil), payload...), qos: 0}, true
	}
	if len(s.out) >= s.receiveMax {
		s.pending = append(s.pending, pendingDown{topic, append([]byte(nil), payload...), qos})
		return pubMsg{}, false
	}
	id := s.allocID()
	s.out = append(s.out, inflight{
		id: id, topic: topic, payload: append([]byte(nil), payload...), qos: qos, sent: time.Now(),
	})
	return pubMsg{topic: topic, payload: append([]byte(nil), payload...), qos: qos, id: id}, true
}

func (s *deviceSession) popPending() (pubMsg, bool) {
	if len(s.pending) == 0 || len(s.out) >= s.receiveMax {
		return pubMsg{}, false
	}
	p := s.pending[0]
	s.pending = s.pending[1:]
	return s.enqueue(p.topic, p.payload, p.qos)
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

func (s *deviceSession) due(now time.Time, after time.Duration) []pubMsg {
	var out []pubMsg
	for i, m := range s.out {
		if now.Sub(m.sent) < after {
			continue
		}
		s.out[i].sent = now
		if m.stage == 0 {
			out = append(out, pubMsg{topic: m.topic, payload: m.payload, qos: m.qos, id: m.id, dup: true})
		} else {
			out = append(out, pubMsg{id: m.id, rel: true})
		}
	}
	return out
}

func (s *deviceSession) unacked() []pubMsg {
	var out []pubMsg
	for _, m := range s.out {
		if m.stage == 0 {
			out = append(out, pubMsg{topic: m.topic, payload: m.payload, qos: m.qos, id: m.id, dup: true})
		}
	}
	return out
}
