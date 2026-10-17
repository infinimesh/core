package main

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/infinimesh/core/v3/pkg/graph"
	inf "github.com/infinimesh/core/v3/pkg/shared"
	"github.com/infinimesh/proto/shadow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestOtherReplicaClosesTheAddonStream(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	plugs := &fakeOTAPlugins{ok: true, plug: graph.OTAPlugin{ID: "plug", TokenID: "jti-1"}}
	api := &ShadowAPI{log: zap.NewNop(), plugins: plugs}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, listenPluginTokens(ctx, rdb, api.PluginTokenChanged))

	streamCtx := context.WithValue(ctx, inf.InfinimeshPluginCtxKey, "plug")
	streamCtx = context.WithValue(streamCtx, inf.InfinimeshJTICtxKey, "jti-1")
	started := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		errCh <- api.copyShadowStream(streamCtx, func() (*shadow.Shadow, error) {
			close(started)
			<-streamCtx.Done()
			return nil, streamCtx.Err()
		}, func(*shadow.Shadow) error { return nil })
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not start")
	}
	require.Equal(t, 1, plugs.calls)

	require.NoError(t, publishPluginToken(context.Background(), rdb, "plug"))
	select {
	case err := <-errCh:
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
		assert.ErrorContains(t, err, "token is revoked")
	case <-time.After(2 * time.Second):
		t.Fatal("stream on the other replica stayed open")
	}

	plugs.plug.TokenID = "jti-2"
	err := api.addonTokenCurrent(streamCtx)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	require.Equal(t, 2, plugs.calls)
}
