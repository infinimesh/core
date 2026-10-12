package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	infinimesh "github.com/infinimesh/core/pkg/shared"
	"github.com/infinimesh/proto/node/access"
	"github.com/infinimesh/proto/node/nodeconnect"
	sesspb "github.com/infinimesh/proto/node/sessions"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type dummySessions struct {
	checkErr error
	checked  bool
}

func (d *dummySessions) New(exp int64, client string) *sesspb.Session { return &sesspb.Session{} }
func (d *dummySessions) Store(account string, session *sesspb.Session) error {
	return nil
}
func (d *dummySessions) Check(account, sid string) error {
	d.checked = true
	return d.checkErr
}
func (d *dummySessions) LogActivity(account, sid string, exp int64) error { return nil }
func (d *dummySessions) Get(account string) ([]*sesspb.Session, error)    { return nil, nil }
func (d *dummySessions) GetActivity(account string) (map[string]*timestamppb.Timestamp, error) {
	return map[string]*timestamppb.Timestamp{}, nil
}
func (d *dummySessions) Revoke(account, sid string) error { return nil }

type stubJWT struct {
	claims jwt.MapClaims
	err    error
}

func (s stubJWT) Parse(tokenString string, keyFunc jwt.Keyfunc, options ...jwt.ParserOption) (*jwt.Token, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &jwt.Token{Valid: true, Claims: s.claims}, nil
}

func TestConnectStandardAuthMiddleware_SessionOK(t *testing.T) {
	claims := jwt.MapClaims{
		infinimesh.INFINIMESH_ACCOUNT_CLAIM: "acc",
		infinimesh.INFINIMESH_SESSION_CLAIM: "sid",
		"exp":                               float64(time.Now().Add(time.Hour).Unix()),
		infinimesh.INFINIMESH_ROOT_CLAIM:    true,
	}
	sess := &dummySessions{}
	interceptor := &interceptor{
		log:         zap.NewNop(),
		jwt:         stubJWT{claims: claims},
		sessions:    sess,
		signing_key: []byte("k"),
	}

	ctx, logActivity, err := interceptor.ConnectStandardAuthMiddleware(context.Background(), []byte("k"), "stub-token")
	assert.NoError(t, err)
	assert.True(t, logActivity)
	assert.True(t, sess.checked)
	assert.Equal(t, "acc", ctx.Value(infinimesh.InfinimeshAccountCtxKey))
	assert.Equal(t, "sid", ctx.Value(infinimesh.InfinimeshSessionCtxKey))
	assert.Equal(t, true, ctx.Value(infinimesh.InfinimeshRootCtxKey))
}

func TestConnectStandardAuthMiddleware_NoSessionFlag(t *testing.T) {
	claims := jwt.MapClaims{
		infinimesh.INFINIMESH_ACCOUNT_CLAIM:   "acc",
		infinimesh.INFINIMESH_NOSESSION_CLAIM: true,
		"exp":                                 float64(time.Now().Add(time.Hour).Unix()),
	}
	sess := &dummySessions{}
	interceptor := &interceptor{
		log:         zap.NewNop(),
		jwt:         stubJWT{claims: claims},
		sessions:    sess,
		signing_key: []byte("k"),
	}

	_, logActivity, err := interceptor.ConnectStandardAuthMiddleware(context.Background(), []byte("k"), "stub-token")
	assert.NoError(t, err)
	assert.False(t, logActivity)
	assert.False(t, sess.checked)
}

type countJWT struct {
	claims jwt.MapClaims
	n      int
}

func (c *countJWT) Parse(tokenString string, keyFunc jwt.Keyfunc, options ...jwt.ParserOption) (*jwt.Token, error) {
	c.n++
	return &jwt.Token{Valid: true, Claims: c.claims}, nil
}

type fakeStreamConn struct {
	procedure string
	header    http.Header
}

func (f fakeStreamConn) Spec() connect.Spec           { return connect.Spec{Procedure: f.procedure} }
func (f fakeStreamConn) Peer() connect.Peer           { return connect.Peer{} }
func (f fakeStreamConn) Receive(any) error            { return nil }
func (f fakeStreamConn) RequestHeader() http.Header   { return f.header }
func (f fakeStreamConn) Send(any) error               { return nil }
func (f fakeStreamConn) ResponseHeader() http.Header  { return http.Header{} }
func (f fakeStreamConn) ResponseTrailer() http.Header { return http.Header{} }

func TestShadowStreamAuth(t *testing.T) {
	deviceClaims := jwt.MapClaims{
		infinimesh.INFINIMESH_DEVICES_CLAIM: map[string]any{"dev": float64(access.Level_READ)},
	}
	jwtDevices := &countJWT{claims: deviceClaims}
	devices := &interceptor{log: zap.NewNop(), jwt: jwtDevices, signing_key: []byte("k")}
	ctx, _, err := devices.ConnectShadowStreamMiddleware(context.Background(), []byte("k"), "tok")
	assert.NoError(t, err)
	assert.Equal(t, 2, jwtDevices.n)
	pool := ctx.Value(infinimesh.InfinimeshDevicesCtxKey).(map[string]access.Level)
	assert.Equal(t, access.Level_READ, pool["dev"])

	accountClaims := jwt.MapClaims{
		infinimesh.INFINIMESH_ACCOUNT_CLAIM:   "acc",
		infinimesh.INFINIMESH_NOSESSION_CLAIM: true,
	}
	jwtAccount := &countJWT{claims: accountClaims}
	accounts := &interceptor{log: zap.NewNop(), jwt: jwtAccount, signing_key: []byte("k"), sessions: &dummySessions{}}
	ctx, activity, err := accounts.ConnectShadowStreamMiddleware(context.Background(), []byte("k"), "tok")
	assert.NoError(t, err)
	assert.False(t, activity)
	assert.Equal(t, 2, jwtAccount.n)
	assert.Equal(t, "acc", ctx.Value(infinimesh.InfinimeshAccountCtxKey))

	sessionClaims := jwt.MapClaims{
		infinimesh.INFINIMESH_ACCOUNT_CLAIM: "acc",
		infinimesh.INFINIMESH_SESSION_CLAIM: "sid",
		"exp":                               float64(time.Now().Add(time.Hour).Unix()),
	}
	jwtSession := &countJWT{claims: sessionClaims}
	sess := &dummySessions{}
	withSession := &interceptor{log: zap.NewNop(), jwt: jwtSession, signing_key: []byte("k"), sessions: sess}
	_, activity, err = withSession.ConnectShadowStreamMiddleware(context.Background(), []byte("k"), "tok")
	assert.NoError(t, err)
	assert.True(t, activity)
	assert.True(t, sess.checked)
	assert.Equal(t, 2, jwtSession.n)

	for _, procedure := range []string{
		nodeconnect.ShadowServiceGetProcedure,
		nodeconnect.ShadowServicePatchProcedure,
		nodeconnect.ShadowServiceRemoveProcedure,
	} {
		mw := SelectMiddleware(accounts, procedure)
		_, _, err = mw(context.Background(), []byte("k"), "tok")
		assert.Error(t, err, procedure)
	}

	header := http.Header{"Authorization": []string{"Bearer tok"}}
	err = devices.WrapStreamingHandler(func(ctx context.Context, _ connect.StreamingHandlerConn) error {
		pool, _ := ctx.Value(infinimesh.InfinimeshDevicesCtxKey).(map[string]access.Level)
		if pool["dev"] != access.Level_READ {
			return assert.AnError
		}
		return nil
	})(context.Background(), fakeStreamConn{procedure: nodeconnect.ShadowServiceStreamShadowProcedure, header: header})
	assert.NoError(t, err)

	err = accounts.WrapStreamingHandler(func(ctx context.Context, _ connect.StreamingHandlerConn) error {
		if ctx.Value(infinimesh.InfinimeshAccountCtxKey) != "acc" {
			return assert.AnError
		}
		return nil
	})(context.Background(), fakeStreamConn{procedure: nodeconnect.ShadowServiceStreamShadowSyncProcedure, header: header})
	assert.NoError(t, err)
}

func TestStreamTokenAuth(t *testing.T) {
	claims := jwt.MapClaims{
		infinimesh.INFINIMESH_ACCOUNT_CLAIM:   "acc",
		infinimesh.INFINIMESH_NAMESPACE_CLAIM: "plant",
		infinimesh.INFINIMESH_NOSESSION_CLAIM: true,
		infinimesh.INFINIMESH_STREAM_CLAIM:    true,
	}
	counter := &countJWT{claims: claims}
	interceptor := &interceptor{log: zap.NewNop(), jwt: counter, signing_key: []byte("k")}

	ctx, activity, err := interceptor.ConnectShadowStreamMiddleware(context.Background(), []byte("k"), "tok")
	assert.NoError(t, err)
	assert.False(t, activity)
	assert.Equal(t, 2, counter.n)
	assert.Equal(t, "acc", ctx.Value(infinimesh.InfinimeshAccountCtxKey))
	assert.Equal(t, "plant", ctx.Value(infinimesh.InfinimeshNamespaceCtxKey))
	assert.Equal(t, true, ctx.Value(infinimesh.InfinimeshStreamCtxKey))

	_, _, err = interceptor.ConnectStandardAuthMiddleware(context.Background(), []byte("k"), "tok")
	assert.EqualError(t, err, "permission_denied: streaming token is read-only")

	_, _, err = interceptor.ConnectDeviceAuthMiddleware(context.Background(), []byte("k"), "tok")
	assert.EqualError(t, err, "permission_denied: streaming token is read-only")

	header := http.Header{"Authorization": []string{"Bearer tok"}}
	for _, procedure := range []string{
		nodeconnect.ShadowServiceStreamShadowProcedure,
		nodeconnect.ShadowServiceStreamShadowSyncProcedure,
	} {
		counter.n = 0
		err = interceptor.WrapStreamingHandler(func(ctx context.Context, _ connect.StreamingHandlerConn) error {
			if ctx.Value(infinimesh.InfinimeshAccountCtxKey) != "acc" {
				return assert.AnError
			}
			if ctx.Value(infinimesh.InfinimeshNamespaceCtxKey) != "plant" {
				return assert.AnError
			}
			return nil
		})(context.Background(), fakeStreamConn{procedure: procedure, header: header})
		assert.NoError(t, err)
		assert.Equal(t, 2, counter.n)
	}
}

func TestConnectStandardAuthMiddleware_SessionCheckFails(t *testing.T) {
	claims := jwt.MapClaims{
		infinimesh.INFINIMESH_ACCOUNT_CLAIM: "acc",
		infinimesh.INFINIMESH_SESSION_CLAIM: "sid",
		"exp":                               float64(time.Now().Add(time.Hour).Unix()),
	}
	sess := &dummySessions{checkErr: assert.AnError}
	interceptor := &interceptor{
		log:         zap.NewNop(),
		jwt:         stubJWT{claims: claims},
		sessions:    sess,
		signing_key: []byte("k"),
	}

	_, _, err := interceptor.ConnectStandardAuthMiddleware(context.Background(), []byte("k"), "stub-token")
	assert.Error(t, err)
	assert.True(t, sess.checked)
}
