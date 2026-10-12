package graph

import (
	"net/http"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
)

const idempotencyTTL = 24 * time.Hour

type idemHit struct {
	at  time.Time
	raw []byte
}

var idempotency = struct {
	mu sync.Mutex
	m  map[string]idemHit
}{m: map[string]idemHit{}}

func idemKey(h http.Header, scope string) string {
	if h == nil {
		return ""
	}
	key := h.Get("Idempotency-Key")
	if key == "" {
		return ""
	}
	return scope + "\x00" + key
}

func loadIdem(h http.Header, scope string, dst proto.Message) bool {
	key := idemKey(h, scope)
	if key == "" {
		return false
	}
	idempotency.mu.Lock()
	defer idempotency.mu.Unlock()
	hit, ok := idempotency.m[key]
	if !ok || time.Since(hit.at) > idempotencyTTL {
		delete(idempotency.m, key)
		return false
	}
	return proto.Unmarshal(hit.raw, dst) == nil
}

func saveIdem(h http.Header, scope string, msg proto.Message) {
	key := idemKey(h, scope)
	if key == "" || msg == nil {
		return
	}
	raw, err := proto.Marshal(msg)
	if err != nil {
		return
	}
	idempotency.mu.Lock()
	idempotency.m[key] = idemHit{at: time.Now(), raw: raw}
	idempotency.mu.Unlock()
}
