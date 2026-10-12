package auth

import (
	"context"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
)

const denyPrefix = "revoked:token:"

type Denylist interface {
	Deny(ctx context.Context, id string, ttl time.Duration) error
	Denied(ctx context.Context, id string) (bool, error)
}

type RedisDenylist struct {
	rdb redis.Cmdable
}

func NewRedisDenylist(rdb redis.Cmdable) *RedisDenylist {
	return &RedisDenylist{rdb: rdb}
}

func (d *RedisDenylist) Deny(ctx context.Context, id string, ttl time.Duration) error {
	if d == nil || d.rdb == nil || id == "" {
		return nil
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	return d.rdb.Set(ctx, denyPrefix+id, "1", ttl).Err()
}

func (d *RedisDenylist) Denied(ctx context.Context, id string) (bool, error) {
	if d == nil || d.rdb == nil || id == "" {
		return false, nil
	}
	n, err := d.rdb.Exists(ctx, denyPrefix+id).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

type MemDenylist struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func NewMemDenylist() *MemDenylist {
	return &MemDenylist{m: map[string]time.Time{}}
}

func (d *MemDenylist) Deny(_ context.Context, id string, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = time.Hour
	}
	d.mu.Lock()
	d.m[id] = time.Now().Add(ttl)
	d.mu.Unlock()
	return nil
}

func (d *MemDenylist) Denied(_ context.Context, id string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	exp, ok := d.m[id]
	if !ok {
		return false, nil
	}
	if time.Now().After(exp) {
		delete(d.m, id)
		return false, nil
	}
	return true, nil
}
