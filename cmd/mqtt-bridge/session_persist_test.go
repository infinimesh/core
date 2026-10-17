package main

import (
	"testing"
	"time"

	"github.com/infinimesh/core/v3/pkg/mqtt/packet"
)

func TestSessionReloadsFromStore(t *testing.T) {
	mem := &memorySessions{m: map[string]sessionSnap{}}
	store := newSessionStore()
	store.persist = mem
	meta := connectMeta{sessionExpiry: time.Hour}
	_, sess := store.open("dev", "cid", meta, time.Unix(10, 0))
	sess.mu.Lock()
	sess.subQoS = 1
	msg, ok := sess.enqueueAt("devices/dev/state/desired", []byte(`{"led":1}`), 1, packet.Properties{}, time.Time{})
	sess.mu.Unlock()
	if !ok || msg.id == 0 {
		t.Fatalf("enqueue %+v", msg)
	}
	store.close("dev", "cid", meta, time.Unix(20, 0))

	next := newSessionStore()
	next.persist = mem
	present, loaded := next.open("dev", "cid", connectMeta{sessionExpiry: time.Hour}, time.Unix(30, 0))
	if !present {
		t.Fatal("session not present")
	}
	loaded.mu.Lock()
	defer loaded.mu.Unlock()
	if loaded.subQoS != 1 || len(loaded.out) != 1 || string(loaded.out[0].payload) != `{"led":1}` {
		t.Fatalf("loaded qos %d out %#v", loaded.subQoS, loaded.out)
	}
}
