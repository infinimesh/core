package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/infinimesh/core/pkg/mqtt/packet"
)

type sessionPersist interface {
	save(key string, snap sessionSnap, ttl time.Duration)
	load(key string) (sessionSnap, bool)
	drop(key string)
}

type sessionSnap struct {
	Expiry     time.Time      `json:"expiry"`
	Keep       bool           `json:"keep"`
	SubQoS     int            `json:"sub_qos"`
	Out        []inflightSnap `json:"out"`
	Pending    []pendingSnap  `json:"pending"`
	In         []qos2Snap     `json:"in"`
	NextID     uint16         `json:"next_id"`
	ReceiveMax int            `json:"receive_max"`
	QoS2Max    int            `json:"qos2_max"`
	QoS2Hold   time.Duration  `json:"qos2_hold"`
	PersistFor time.Duration  `json:"persist_for"`
}

type inflightSnap struct {
	ID      uint16            `json:"id"`
	Topic   string            `json:"topic"`
	Payload []byte            `json:"payload"`
	QoS     byte              `json:"qos"`
	Sent    time.Time         `json:"sent"`
	Stage   byte              `json:"stage"`
	Props   packet.Properties `json:"props"`
	Expires time.Time         `json:"expires"`
}

type pendingSnap struct {
	Topic   string            `json:"topic"`
	Payload []byte            `json:"payload"`
	QoS     byte              `json:"qos"`
	Props   packet.Properties `json:"props"`
	Expires time.Time         `json:"expires"`
}

type qos2Snap struct {
	ID       uint16            `json:"id"`
	Deliver  bool              `json:"deliver"`
	Topic    string            `json:"topic"`
	Payload  []byte            `json:"payload"`
	QoS      byte              `json:"qos"`
	Dup      bool              `json:"dup"`
	Retain   bool              `json:"retain"`
	Rel      bool              `json:"rel"`
	PacketID uint16            `json:"packet_id"`
	Props    packet.Properties `json:"props"`
	Deadline time.Time         `json:"deadline"`
}

func (s *deviceSession) snap() sessionSnap {
	out := sessionSnap{
		Expiry: s.expiry, Keep: s.keep, SubQoS: s.subQoS, NextID: s.nextID,
		ReceiveMax: s.receiveMax, QoS2Max: s.qos2Max, QoS2Hold: s.qos2Hold, PersistFor: s.persistFor,
	}
	for _, m := range s.out {
		out.Out = append(out.Out, inflightSnap{
			ID: m.id, Topic: m.topic, Payload: append([]byte(nil), m.payload...), QoS: m.qos,
			Sent: m.sent, Stage: m.stage, Props: m.props, Expires: m.expires,
		})
	}
	for _, m := range s.pending {
		out.Pending = append(out.Pending, pendingSnap{
			Topic: m.topic, Payload: append([]byte(nil), m.payload...), QoS: m.qos, Props: m.props, Expires: m.expires,
		})
	}
	for id, h := range s.inQoS2 {
		out.In = append(out.In, qos2Snap{
			ID: id, Deliver: h.deliver, Topic: h.msg.topic, Payload: append([]byte(nil), h.msg.payload...),
			QoS: h.msg.qos, Dup: h.msg.dup, Retain: h.msg.retain, Rel: h.msg.rel, PacketID: h.msg.id,
			Props: h.msg.props, Deadline: h.deadline,
		})
	}
	return out
}

func (snap sessionSnap) device(deviceID, clientID string) *deviceSession {
	s := &deviceSession{
		deviceID: deviceID, clientID: clientID, expiry: snap.Expiry, keep: snap.Keep, subQoS: snap.SubQoS,
		nextID: snap.NextID, receiveMax: snap.ReceiveMax, aliases: map[uint16]string{}, aliasMax: serverTopicAliasMax,
		qos2Max: snap.QoS2Max, qos2Hold: snap.QoS2Hold, persistFor: snap.PersistFor, inQoS2: map[uint16]qos2In{},
	}
	for _, m := range snap.Out {
		s.out = append(s.out, inflight{
			id: m.ID, topic: m.Topic, payload: m.Payload, qos: m.QoS, sent: m.Sent, stage: m.Stage, props: m.Props, expires: m.Expires,
		})
	}
	for _, m := range snap.Pending {
		s.pending = append(s.pending, pendingDown{topic: m.Topic, payload: m.Payload, qos: m.QoS, props: m.Props, expires: m.Expires})
	}
	for _, m := range snap.In {
		s.inQoS2[m.ID] = qos2In{
			deliver: m.Deliver, deadline: m.Deadline,
			msg: pubMsg{topic: m.Topic, payload: m.Payload, qos: m.QoS, dup: m.Dup, retain: m.Retain, rel: m.Rel, id: m.PacketID, props: m.Props},
		}
	}
	return s
}

func (s *sessionStore) flush(now time.Time) {
	if s.persist == nil {
		return
	}
	type item struct {
		key  string
		snap sessionSnap
		ttl  time.Duration
	}
	s.mu.Lock()
	var items []item
	for key, sess := range s.byID {
		sess.mu.Lock()
		ttl := sess.persistFor
		if ttl <= 0 {
			ttl = time.Hour
		}
		if !sess.expiry.IsZero() {
			if !sess.expiry.After(now) {
				sess.mu.Unlock()
				continue
			}
			ttl = sess.expiry.Sub(now)
		}
		items = append(items, item{key, sess.snap(), ttl})
		sess.mu.Unlock()
	}
	p := s.persist
	s.mu.Unlock()
	for _, it := range items {
		p.save(it.key, it.snap, it.ttl)
	}
}

func (s *sessionStore) keep() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for now := range t.C {
		s.flush(now)
	}
}

type redisSessions struct {
	rdb *redis.Client
}

func newRedisSessions(rdb *redis.Client) *redisSessions {
	return &redisSessions{rdb: rdb}
}

func sessionRedisKey(key string) string {
	return "mqtt:sess:" + base64.RawURLEncoding.EncodeToString([]byte(key))
}

func (r *redisSessions) save(key string, snap sessionSnap, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return
	}
	_ = r.rdb.Set(context.Background(), sessionRedisKey(key), raw, ttl).Err()
}

func (r *redisSessions) load(key string) (sessionSnap, bool) {
	raw, err := r.rdb.Get(context.Background(), sessionRedisKey(key)).Bytes()
	if err != nil {
		return sessionSnap{}, false
	}
	var snap sessionSnap
	if json.Unmarshal(raw, &snap) != nil {
		return sessionSnap{}, false
	}
	return snap, true
}

func (r *redisSessions) drop(key string) {
	_ = r.rdb.Del(context.Background(), sessionRedisKey(key)).Err()
}

type memorySessions struct {
	m map[string]sessionSnap
}

func (m *memorySessions) save(key string, snap sessionSnap, ttl time.Duration) {
	if m.m == nil {
		m.m = map[string]sessionSnap{}
	}
	if ttl <= 0 {
		delete(m.m, key)
		return
	}
	m.m[key] = snap
}

func (m *memorySessions) load(key string) (sessionSnap, bool) {
	snap, ok := m.m[key]
	return snap, ok
}

func (m *memorySessions) drop(key string) {
	delete(m.m, key)
}
