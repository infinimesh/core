package graph

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/arangodb/go-driver"
	"github.com/go-redis/redis/v8"
	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"github.com/infinimesh/core/pkg/graph/schema"
	inf "github.com/infinimesh/core/pkg/shared"
	pb "github.com/infinimesh/proto/node"
	"github.com/infinimesh/proto/node/access"
)

type fakeICA struct {
	ok      bool
	level   access.Level
	moveErr error
	access  *access.Access
	title   string
	tags    []string
}

func (f fakeICA) GetEdgeCol(ctx context.Context, name string) driver.Collection { return nil }
func (f fakeICA) CheckLink(ctx context.Context, edge driver.Collection, from InfinimeshGraphNode, to InfinimeshGraphNode) bool {
	return false
}
func (f fakeICA) LinkUntil(ctx context.Context, log *zap.Logger, edge driver.Collection, from InfinimeshGraphNode, to InfinimeshGraphNode, lvl access.Level, role access.Role, expiresAt int64) error {
	return nil
}
func (f fakeICA) Link(ctx context.Context, log *zap.Logger, edge driver.Collection, from InfinimeshGraphNode, to InfinimeshGraphNode, lvl access.Level, role access.Role) error {
	return nil
}
func (f fakeICA) Move(ctx context.Context, c InfinimeshController, obj InfinimeshGraphNode, edge driver.Collection, ns string) error {
	return f.moveErr
}
func (f fakeICA) AccessLevel(ctx context.Context, requestor InfinimeshGraphNode, node InfinimeshGraphNode) (bool, access.Level) {
	return f.ok, f.level
}
func (f fakeICA) AccessLevelAndGet(ctx context.Context, log *zap.Logger, account *Account, node InfinimeshGraphNode) error {
	dev, ok := node.(*Device)
	if !ok {
		return nil
	}
	if f.access != nil {
		dev.Access = f.access
	}
	if f.title != "" {
		dev.Title = f.title
	}
	if f.tags != nil {
		dev.Tags = append([]string(nil), f.tags...)
	}
	return nil
}
func (f fakeICA) ListQuery(ctx context.Context, log *zap.Logger, from InfinimeshGraphNode, children string) (driver.Cursor, error) {
	return nil, nil
}
func (f fakeICA) ListOwnedDeep(ctx context.Context, log *zap.Logger, from InfinimeshGraphNode) (*access.Nodes, error) {
	return nil, nil
}
func (f fakeICA) DeleteRecursive(ctx context.Context, log *zap.Logger, from InfinimeshGraphNode) error {
	return nil
}
func (f fakeICA) Toggle(ctx context.Context, node InfinimeshGraphNode, field string) error {
	return nil
}
func (f fakeICA) EnsureRootExists(_log *zap.Logger, rdb *redis.Client, passwd string) error {
	return nil
}

type auditCapture struct {
	rows []auditRow
}

type auditRow struct {
	action, device, account, tokenID string
}

func (a *auditCapture) Record(_ context.Context, action, device, account, tokenID, _ string) {
	a.rows = append(a.rows, auditRow{action, device, account, tokenID})
}

func TestMakeDevicesToken_EmptyDevices(t *testing.T) {
	ctrl := &DevicesController{
		InfinimeshBaseController: InfinimeshBaseController{log: zap.NewNop()},
		SIGNING_KEY:              []byte("secret"),
	}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, schema.ROOT_ACCOUNT_KEY)
	_, err := ctrl.MakeDevicesToken(ctx, connect.NewRequest(&pb.DevicesTokenRequest{}))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "at least one device")
}

func TestMakeDevicesToken_Success(t *testing.T) {
	ctrl := &DevicesController{
		InfinimeshBaseController: InfinimeshBaseController{
			log: zap.NewExample(),
			db:  nil,
		},
		ica_repo:    fakeICA{ok: true, level: access.Level_ADMIN},
		SIGNING_KEY: []byte("secret"),
	}

	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, schema.ROOT_ACCOUNT_KEY)
	req := &pb.DevicesTokenRequest{
		Devices: map[string]access.Level{
			"dev1": access.Level_READ,
		},
		Exp: time.Now().Add(time.Hour).Unix(),
	}

	seen := &auditCapture{}
	ctrl.SetAuditor(seen)
	resp, err := ctrl.MakeDevicesToken(ctx, connect.NewRequest(req))
	assert.NoError(t, err)
	assert.NotEmpty(t, resp.Msg.GetToken())
	if len(seen.rows) != 1 || seen.rows[0].action != auditToken || seen.rows[0].device != "dev1" || seen.rows[0].account != schema.ROOT_ACCOUNT_KEY || seen.rows[0].tokenID == "" {
		t.Fatalf("%+v", seen.rows)
	}

	parsed, err := jwt.Parse(resp.Msg.GetToken(), func(tk *jwt.Token) (interface{}, error) {
		return ctrl.SIGNING_KEY, nil
	})
	assert.NoError(t, err)
	assert.True(t, parsed.Valid)
}

func TestMakeDevicesToken_Stream(t *testing.T) {
	ctrl := &DevicesController{
		InfinimeshBaseController: InfinimeshBaseController{log: zap.NewExample()},
		ica_repo:                 fakeICA{ok: true, level: access.Level_READ},
		SIGNING_KEY:              []byte("secret"),
	}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, schema.ROOT_ACCOUNT_KEY)

	resp, err := ctrl.MakeDevicesToken(ctx, connect.NewRequest(&pb.DevicesTokenRequest{
		Stream:    true,
		Namespace: "plant",
		Exp:       time.Now().Add(time.Hour).Unix(),
	}))
	assert.NoError(t, err)
	parsed, err := jwt.Parse(resp.Msg.GetToken(), func(tk *jwt.Token) (interface{}, error) {
		return ctrl.SIGNING_KEY, nil
	})
	assert.NoError(t, err)
	claims := parsed.Claims.(jwt.MapClaims)
	assert.Equal(t, schema.ROOT_ACCOUNT_KEY, claims[inf.INFINIMESH_ACCOUNT_CLAIM])
	assert.Equal(t, "plant", claims[inf.INFINIMESH_NAMESPACE_CLAIM])
	assert.Equal(t, true, claims[inf.INFINIMESH_STREAM_CLAIM])
	assert.Equal(t, true, claims[inf.INFINIMESH_NOSESSION_CLAIM])
	assert.Nil(t, claims[inf.INFINIMESH_DEVICES_CLAIM])
	assert.Nil(t, claims[inf.INFINIMESH_ROOT_CLAIM])
	_, hasExp := claims["exp"]
	assert.True(t, hasExp)

	resp, err = ctrl.MakeDevicesToken(ctx, connect.NewRequest(&pb.DevicesTokenRequest{Stream: true}))
	assert.NoError(t, err)
	parsed, err = jwt.Parse(resp.Msg.GetToken(), func(tk *jwt.Token) (interface{}, error) {
		return ctrl.SIGNING_KEY, nil
	})
	assert.NoError(t, err)
	claims = parsed.Claims.(jwt.MapClaims)
	assert.Nil(t, claims[inf.INFINIMESH_NAMESPACE_CLAIM])
	_, hasExp = claims["exp"]
	assert.False(t, hasExp)

	denied := &DevicesController{
		InfinimeshBaseController: InfinimeshBaseController{log: zap.NewExample()},
		ica_repo:                 fakeICA{ok: true, level: access.Level_NONE},
		SIGNING_KEY:              []byte("secret"),
	}
	_, err = denied.MakeDevicesToken(ctx, connect.NewRequest(&pb.DevicesTokenRequest{Stream: true, Namespace: "plant"}))
	assert.Error(t, err)

	_, err = ctrl.MakeDevicesToken(ctx, connect.NewRequest(&pb.DevicesTokenRequest{
		Stream:  true,
		Devices: map[string]access.Level{"dev1": access.Level_READ},
	}))
	assert.Error(t, err)
	_, err = ctrl.MakeDevicesToken(ctx, connect.NewRequest(&pb.DevicesTokenRequest{Stream: true, Post: true}))
	assert.Error(t, err)
}

func TestMakeDevicesToken_NotEnoughAccess(t *testing.T) {
	ctrl := &DevicesController{
		InfinimeshBaseController: InfinimeshBaseController{
			log: zap.NewExample(),
			db:  nil,
		},
		ica_repo:    fakeICA{ok: false, level: access.Level_NONE},
		SIGNING_KEY: []byte("secret"),
	}

	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, schema.ROOT_ACCOUNT_KEY)
	req := &pb.DevicesTokenRequest{
		Devices: map[string]access.Level{
			"dev1": access.Level_READ,
		},
	}

	_, err := ctrl.MakeDevicesToken(ctx, connect.NewRequest(req))
	assert.Error(t, err)
}

func TestMakeDevicesToken_PostDoesNotCopyCallerLevel(t *testing.T) {
	ctrl := &DevicesController{
		InfinimeshBaseController: InfinimeshBaseController{log: zap.NewExample()},
		ica_repo:                 fakeICA{ok: true, level: access.Level_ADMIN},
		SIGNING_KEY:              []byte("secret"),
	}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, schema.ROOT_ACCOUNT_KEY)
	resp, err := ctrl.MakeDevicesToken(ctx, connect.NewRequest(&pb.DevicesTokenRequest{
		Devices: map[string]access.Level{"dev1": access.Level_NONE},
		Post:    true,
	}))
	assert.NoError(t, err)
	parsed, err := jwt.Parse(resp.Msg.GetToken(), func(tk *jwt.Token) (interface{}, error) {
		return ctrl.SIGNING_KEY, nil
	})
	assert.NoError(t, err)
	claims := parsed.Claims.(jwt.MapClaims)
	devices := claims[inf.INFINIMESH_DEVICES_CLAIM].(map[string]interface{})
	assert.Equal(t, float64(access.Level_READ), devices["dev1"])
	assert.Equal(t, true, claims[inf.INFINIMESH_POST_CLAIM])
	exp, ok := claims["exp"].(float64)
	assert.True(t, ok)
	assert.InDelta(t, float64(time.Now().Add(registryDeviceTokenTTL).Unix()), exp, 5)
	assert.NotEmpty(t, claims[inf.INFINIMESH_JTI_CLAIM])
}

func TestRevokeDevicesToken(t *testing.T) {
	deny := &memDeny{}
	ctrl := &DevicesController{
		InfinimeshBaseController: InfinimeshBaseController{log: zap.NewExample()},
		SIGNING_KEY:              []byte("secret"),
	}
	_, err := ctrl.RevokeDevicesToken(context.Background(), connect.NewRequest(&pb.RevokeDeviceTokenRequest{Id: "jti-1"}))
	assert.EqualError(t, err, "unavailable: token revocation is not configured")

	ctrl.SetDenylist(deny)
	_, err = ctrl.RevokeDevicesToken(context.Background(), connect.NewRequest(&pb.RevokeDeviceTokenRequest{Id: "jti-1"}))
	assert.NoError(t, err)
	assert.Equal(t, "jti-1", deny.id)
	assert.Equal(t, registryDeviceTokenTTL, deny.ttl)
}

type memDeny struct {
	id  string
	ttl time.Duration
	err error
}

func (m *memDeny) Deny(_ context.Context, id string, ttl time.Duration) error {
	m.id = id
	m.ttl = ttl
	return m.err
}
