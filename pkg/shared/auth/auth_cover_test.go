package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	infinimesh "github.com/infinimesh/core/pkg/shared"
	"github.com/infinimesh/proto/node/access"
	"github.com/infinimesh/proto/node/nodeconnect"
	"github.com/infinimesh/proto/pairing/pairingconnect"
)

type logSessions struct {
	dummySessions
	err error
	ch  chan struct{}
}

func (s *logSessions) LogActivity(string, string, int64) error {
	if s.ch != nil {
		select {
		case s.ch <- struct{}{}:
		default:
		}
	}
	return s.err
}

func TestDenylist(t *testing.T) {
	ctx := context.Background()
	var redisList *RedisDenylist
	if err := redisList.Deny(ctx, "id", time.Minute); err != nil {
		t.Fatal(err)
	}
	denied, err := redisList.Denied(ctx, "id")
	if denied || err != nil {
		t.Fatalf("%v %v", denied, err)
	}
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	list := NewRedisDenylist(rdb)
	if err := list.Deny(ctx, "", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := list.Deny(ctx, "jti", 0); err != nil {
		t.Fatal(err)
	}
	ok, err := list.Denied(ctx, "jti")
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	ok, err = list.Denied(ctx, "")
	if err != nil || ok {
		t.Fatalf("empty %v %v", ok, err)
	}
	mr.Close()
	if _, err := list.Denied(ctx, "jti"); err == nil {
		t.Fatal("redis down")
	}
	if err := NewRedisDenylist(nil).Deny(ctx, "jti", time.Minute); err != nil {
		t.Fatal(err)
	}

	mem := NewMemDenylist()
	if err := mem.Deny(ctx, "a", time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	ok, err = mem.Denied(ctx, "a")
	if err != nil || ok {
		t.Fatalf("expired %v %v", ok, err)
	}
	if err := mem.Deny(ctx, "b", 0); err != nil {
		t.Fatal(err)
	}
	ok, err = mem.Denied(ctx, "b")
	if err != nil || !ok {
		t.Fatalf("held %v %v", ok, err)
	}
	ok, err = mem.Denied(ctx, "missing")
	if err != nil || ok {
		t.Fatal(ok, err)
	}
}

func TestMakeTokenAndBlank(t *testing.T) {
	i := NewAuthInterceptor(zap.NewNop(), nil, nil, []byte("secret"))
	raw, err := i.MakeToken("acc")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jwt.Parse(raw, func(t *jwt.Token) (interface{}, error) {
		return []byte("secret"), nil
	})
	if err != nil || !parsed.Valid {
		t.Fatal(err)
	}
	ctx, activity, err := i.ConnectBlankMiddleware(context.Background(), []byte("secret"), raw)
	if err != nil || activity || ctx == nil {
		t.Fatal(err)
	}
	called := false
	next := i.WrapStreamingClient(func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		called = true
		return nil
	})
	if next(context.Background(), connect.Spec{}) != nil || !called {
		t.Fatal("streaming client")
	}
	mw := SelectMiddleware(i, nodeconnect.DevicesServiceClaimProcedure)
	if _, _, err := mw(context.Background(), []byte("secret"), ""); err != nil {
		t.Fatal(err)
	}
	header := http.Header{}
	err = i.WrapStreamingHandler(func(context.Context, connect.StreamingHandlerConn) error {
		return nil
	})(context.Background(), fakeStreamConn{procedure: pairingconnect.PairingServiceConnectProcedure, header: header})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAuthBranches(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	if err := NewRedisDenylist(rdb).Deny(context.Background(), "revoked", time.Hour); err != nil {
		t.Fatal(err)
	}
	plugin := jwt.MapClaims{
		infinimesh.INFINIMESH_PLUGIN_CLAIM:       "plug",
		infinimesh.INFINIMESH_OTA_PROVIDER_CLAIM: "mender",
		infinimesh.INFINIMESH_JTI_CLAIM:          "jti",
	}
	ok := &interceptor{log: zap.NewNop(), jwt: stubJWT{claims: plugin}, rdb: rdb, signing_key: []byte("k")}
	ctx, _, err := ok.pluginAuth(context.Background(), plugin)
	assert.NoError(t, err)
	assert.Equal(t, "mender", ctx.Value(infinimesh.InfinimeshOTAProviderCtxKey))

	revoked := jwt.MapClaims{
		infinimesh.INFINIMESH_PLUGIN_CLAIM:       "plug",
		infinimesh.INFINIMESH_OTA_PROVIDER_CLAIM: "webhook",
		infinimesh.INFINIMESH_JTI_CLAIM:          "revoked",
	}
	bad := &interceptor{log: zap.NewNop(), jwt: stubJWT{claims: revoked}, rdb: rdb}
	_, _, err = bad.pluginAuth(context.Background(), revoked)
	assert.Error(t, err)

	mr.Close()
	_, _, err = bad.pluginAuth(context.Background(), plugin)
	assert.Error(t, err)

	broken := &interceptor{log: zap.NewNop(), jwt: stubJWT{claims: jwt.MapClaims{
		infinimesh.INFINIMESH_PLUGIN_CLAIM:       "plug",
		infinimesh.INFINIMESH_OTA_PROVIDER_CLAIM: "other",
		infinimesh.INFINIMESH_JTI_CLAIM:          "jti",
	}}}
	_, _, err = broken.pluginAuth(context.Background(), broken.jwt.(stubJWT).claims)
	assert.Error(t, err)
	_, _, err = broken.pluginAuth(context.Background(), jwt.MapClaims{
		infinimesh.INFINIMESH_PLUGIN_CLAIM:       "",
		infinimesh.INFINIMESH_OTA_PROVIDER_CLAIM: "hawkbit",
	})
	assert.Error(t, err)

	device := jwt.MapClaims{
		infinimesh.INFINIMESH_DEVICES_CLAIM: map[string]any{"dev": float64(access.Level_READ)},
		infinimesh.INFINIMESH_JTI_CLAIM:     "jti",
		infinimesh.INFINIMESH_POST_CLAIM:    true,
	}
	live := miniredis.RunT(t)
	liveRDB := redis.NewClient(&redis.Options{Addr: live.Addr()})
	dev := &interceptor{log: zap.NewNop(), jwt: stubJWT{claims: device}, rdb: liveRDB}
	ctx, _, err = dev.ConnectDeviceAuthMiddleware(context.Background(), []byte("k"), "tok")
	assert.NoError(t, err)
	assert.Equal(t, true, ctx.Value(infinimesh.InfinimeshPostCtxKey))
	assert.Equal(t, "jti", ctx.Value(infinimesh.InfinimeshJTICtxKey))
	_ = NewRedisDenylist(liveRDB).Deny(context.Background(), "jti", time.Hour)
	_, _, err = dev.ConnectDeviceAuthMiddleware(context.Background(), []byte("k"), "tok")
	assert.Error(t, err)
	live.Close()
	_, _, err = dev.ConnectDeviceAuthMiddleware(context.Background(), []byte("k"), "tok")
	assert.Error(t, err)

	both := jwt.MapClaims{
		infinimesh.INFINIMESH_ACCOUNT_CLAIM:   "acc",
		infinimesh.INFINIMESH_DEVICES_CLAIM:   map[string]any{"dev": float64(1)},
		infinimesh.INFINIMESH_NOSESSION_CLAIM: true,
	}
	acc := &interceptor{log: zap.NewNop(), jwt: stubJWT{claims: both}, sessions: &dummySessions{}, signing_key: []byte("k")}
	_, _, err = SelectMiddleware(acc, nodeconnect.ShadowServiceAssignOTAProcedure)(context.Background(), []byte("k"), "tok")
	assert.NoError(t, err)
	_, _, err = SelectMiddleware(acc, nodeconnect.DevicesServiceGetByTokenProcedure)(context.Background(), []byte("k"), "tok")
	assert.NoError(t, err)
	_, _, err = SelectMiddleware(acc, nodeconnect.ShadowServicePatchManyProcedure)(context.Background(), []byte("k"), "tok")
	assert.NoError(t, err)

	std := jwt.MapClaims{
		infinimesh.INFINIMESH_ACCOUNT_CLAIM:   "acc",
		infinimesh.INFINIMESH_NOSESSION_CLAIM: true,
		infinimesh.INFINIMESH_JTI_CLAIM:       "sid",
		infinimesh.INFINIMESH_ROOT_CLAIM:      "false",
	}
	root := &interceptor{log: zap.NewNop(), jwt: stubJWT{claims: std}, sessions: &dummySessions{}}
	ctx, _, err = root.ConnectStandardAuthMiddleware(context.Background(), []byte("k"), "tok")
	assert.NoError(t, err)
	assert.Equal(t, false, ctx.Value(infinimesh.InfinimeshRootCtxKey))
	for _, rootVal := range []any{"0", "1", float64(0), float64(1), float64(2), "nope"} {
		claims := jwt.MapClaims{
			infinimesh.INFINIMESH_ACCOUNT_CLAIM:   "acc",
			infinimesh.INFINIMESH_NOSESSION_CLAIM: true,
			infinimesh.INFINIMESH_ROOT_CLAIM:      rootVal,
		}
		gate := &interceptor{log: zap.NewNop(), jwt: stubJWT{claims: claims}, sessions: &dummySessions{}}
		_, _, err = gate.ConnectStandardAuthMiddleware(context.Background(), []byte("k"), "tok")
		assert.NoError(t, err)
	}

	expired := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"exp": time.Now().Add(-time.Hour).Unix()})
	signed, err := expired.SignedString([]byte("k"))
	assert.NoError(t, err)
	real := NewAuthInterceptor(zap.NewNop(), nil, nil, []byte("k"))
	_, _, err = real.ConnectStandardAuthMiddleware(context.Background(), []byte("k"), signed)
	assert.Error(t, err)
	none := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"account": "acc"})
	noneRaw, err := none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	assert.NoError(t, err)
	_, _, err = real.ConnectStandardAuthMiddleware(context.Background(), []byte("k"), noneRaw)
	assert.Error(t, err)

	badClaims := &interceptor{log: zap.NewNop(), jwt: claimsJWT{token: &jwt.Token{Valid: false, Claims: jwt.MapClaims{}}}}
	_, err = connectValidateToken(badClaims.jwt, []byte("k"), "t")
	assert.Error(t, err)
	badClaims.jwt = claimsJWT{token: &jwt.Token{Valid: true, Claims: jwt.RegisteredClaims{}}}
	_, err = connectValidateToken(badClaims.jwt, []byte("k"), "t")
	assert.Error(t, err)

	sess := &logSessions{err: errors.New("log"), ch: make(chan struct{}, 1)}
	with := &interceptor{log: zap.NewNop(), jwt: stubJWT{claims: jwt.MapClaims{
		infinimesh.INFINIMESH_ACCOUNT_CLAIM: "acc",
		infinimesh.INFINIMESH_SESSION_CLAIM: "sid",
		"exp":                               float64(20),
	}}, sessions: sess, signing_key: []byte("k")}
	ctx, activity, err := with.ConnectStandardAuthMiddleware(context.Background(), []byte("k"), "tok")
	assert.NoError(t, err)
	assert.True(t, activity)
	with.connectHandleLogActivity(context.Background())
	with.connectHandleLogActivity(ctx)
	select {
	case <-sess.ch:
	case <-time.After(time.Second):
		t.Fatal("activity")
	}

	err = with.WrapStreamingHandler(func(context.Context, connect.StreamingHandlerConn) error {
		return nil
	})(context.Background(), fakeStreamConn{
		procedure: nodeconnect.AccountsServiceTokenProcedure,
		header:    http.Header{"Authorization": []string{"Bearer tok"}},
	})
	assert.NoError(t, err)
	err = with.WrapStreamingHandler(func(context.Context, connect.StreamingHandlerConn) error {
		return errors.New("next")
	})(context.Background(), fakeStreamConn{procedure: "/not.a.service/Method", header: http.Header{}})
	assert.Error(t, err)
	streamDev := &interceptor{log: zap.NewNop(), jwt: stubJWT{claims: device}, rdb: nil}
	err = streamDev.WrapStreamingHandler(func(ctx context.Context, _ connect.StreamingHandlerConn) error {
		if ctx.Value(infinimesh.InfinimeshPostCtxKey) != true {
			return errors.New("post")
		}
		return nil
	})(context.Background(), fakeStreamConn{
		procedure: nodeconnect.ShadowServiceGetProcedure,
		header:    http.Header{"Authorization": []string{"Bearer tok"}},
	})
	assert.NoError(t, err)

	badTok := &interceptor{log: zap.NewNop(), jwt: errJWT{}}
	_, _, err = badTok.ConnectAssignOTAMiddleware(context.Background(), []byte("k"), "bad")
	assert.Error(t, err)
	_, _, err = badTok.ConnectRequestOTAMiddleware(context.Background(), []byte("k"), "bad")
	assert.Error(t, err)
	_, _, err = badTok.ConnectAccountOrPluginMiddleware(context.Background(), []byte("k"), "bad")
	assert.Error(t, err)
	_, _, err = badTok.ConnectShadowPatchMiddleware(context.Background(), []byte("k"), "bad")
	assert.Error(t, err)
	_, _, err = badTok.ConnectShadowStreamMiddleware(context.Background(), []byte("k"), "bad")
	assert.Error(t, err)
	_, _, err = badTok.ConnectStreamTokenMiddleware(context.Background(), []byte("k"), "bad")
	assert.Error(t, err)
	empty := &interceptor{log: zap.NewNop(), jwt: stubJWT{claims: jwt.MapClaims{infinimesh.INFINIMESH_STREAM_CLAIM: true}}}
	_, _, err = empty.ConnectStreamTokenMiddleware(context.Background(), []byte("k"), "tok")
	assert.Error(t, err)
}

type claimsJWT struct{ token *jwt.Token }

func (c claimsJWT) Parse(string, jwt.Keyfunc, ...jwt.ParserOption) (*jwt.Token, error) {
	return c.token, nil
}

type errJWT struct{}

func (errJWT) Parse(string, jwt.Keyfunc, ...jwt.ParserOption) (*jwt.Token, error) {
	return nil, errors.New("bad")
}
