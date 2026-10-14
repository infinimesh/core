package main

import (
	"context"

	"github.com/go-redis/redis/v8"
)

const pluginTokenChannel = "plugin:token"

func publishPluginToken(ctx context.Context, rdb redis.Cmdable, id string) error {
	if rdb == nil || id == "" {
		return nil
	}
	return rdb.Publish(ctx, pluginTokenChannel, id).Err()
}

func listenPluginTokens(ctx context.Context, rdb *redis.Client, fn func(string)) error {
	if rdb == nil || fn == nil {
		return nil
	}
	sub := rdb.Subscribe(ctx, pluginTokenChannel)
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close()
		return err
	}
	go func() {
		defer sub.Close()
		ch := sub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				if msg != nil && msg.Payload != "" {
					fn(msg.Payload)
				}
			}
		}
	}()
	return nil
}
