package graph

import (
	"context"
	"time"

	"github.com/go-redis/redis/v8"
)

type RedisClaims struct{ rdb *redis.Client }

func NewRedisClaims(rdb *redis.Client) *RedisClaims { return &RedisClaims{rdb: rdb} }

func (r *RedisClaims) Put(ctx context.Context, token, namespace string, ttl time.Duration) error {
	return r.rdb.Set(ctx, "claim:"+token, namespace, ttl).Err()
}

func (r *RedisClaims) Take(ctx context.Context, token string) (string, error) {
	return r.rdb.GetDel(ctx, "claim:"+token).Result()
}
