package graph

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/alicebob/miniredis/v2"
	"github.com/arangodb/go-driver"
	"github.com/go-redis/redis/v8"
	driver_mocks "github.com/infinimesh/core/v3/mocks/github.com/arangodb/go-driver"
	"github.com/infinimesh/core/v3/pkg/credentials"
	"github.com/infinimesh/core/v3/pkg/graph/schema"
	"github.com/infinimesh/core/v3/pkg/rules"
	inf "github.com/infinimesh/core/v3/pkg/shared"
	pb "github.com/infinimesh/proto/node"
	"github.com/infinimesh/proto/node/access"
	accpb "github.com/infinimesh/proto/node/accounts"
	devpb "github.com/infinimesh/proto/node/devices"
	nspb "github.com/infinimesh/proto/node/namespaces"
	sesspb "github.com/infinimesh/proto/node/sessions"
	plg "github.com/infinimesh/proto/plugins"
	shadowpb "github.com/infinimesh/proto/shadow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

type nopShadow struct{}

func (nopShadow) Get(context.Context, *shadowpb.GetRequest, ...grpc.CallOption) (*shadowpb.GetResponse, error) {
	return &shadowpb.GetResponse{}, nil
}

func TestContextDepthAndNamespaceFilter(t *testing.T) {
	assert.Equal(t, 10, DepthValue(context.Background()))
	assert.Equal(t, 3, DepthValue(WithDepth(context.Background(), 3)))
	assert.Equal(t, "", NSFilterValue(context.Background()))
	assert.Equal(t, "plant", NSFilterValue(WithNamespaceFilter(context.Background(), "plant")))
}

func TestTokenFieldHelpers(t *testing.T) {
	assert.Equal(t, int64(5), requestExpiry(5, 9))
	assert.Equal(t, int64(9), requestExpiry(0, 9))
	on := true
	assert.True(t, requestInfinite(&pb.TokenRequest{Infinite: &on}))
	assert.False(t, requestInfinite(&pb.TokenRequest{Infinite: boolPtr(false), Inf: &on}))
	assert.True(t, requestInfinite(&pb.TokenRequest{Inf: &on}))
	assert.False(t, requestInfinite(&pb.TokenRequest{}))
}

func boolPtr(v bool) *bool { return &v }

func TestIdempotencyEdges(t *testing.T) {
	assert.False(t, loadIdem(nil, "edge", &pb.DeleteResponse{}))
	saveIdem(nil, "edge", &pb.DeleteResponse{})
	h := http.Header{}
	saveIdem(h, "edge", &pb.DeleteResponse{})
	saveIdem(h, "edge", nil)
	assert.False(t, loadIdem(h, "edge", &pb.DeleteResponse{}))

	h.Set("Idempotency-Key", "bad-raw")
	key := idemKey(h, "edge")
	idempotency.mu.Lock()
	idempotency.m[key] = idemHit{at: time.Now(), raw: []byte{0xff}}
	old := http.Header{}
	old.Set("Idempotency-Key", "stale")
	idempotency.m[idemKey(old, "edge")] = idemHit{at: time.Now().Add(-48 * time.Hour), raw: []byte{1}}
	idempotency.mu.Unlock()
	assert.False(t, loadIdem(h, "edge", &pb.DeleteResponse{}))
	assert.False(t, loadIdem(old, "edge", &pb.DeleteResponse{}))
}

func TestRedisClaims(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	claims := NewRedisClaims(rdb)
	ctx := context.Background()
	require.NoError(t, claims.Put(ctx, "tok", "ns", time.Minute))
	ns, err := claims.Take(ctx, "tok")
	require.NoError(t, err)
	assert.Equal(t, "ns", ns)
	_, err = claims.Take(ctx, "missing")
	assert.Error(t, err)
}

func TestLDAPProviders(t *testing.T) {
	prev, prevMap := credentials.LDAP_CONFIGURED, credentials.LDAP.Providers
	t.Cleanup(func() {
		credentials.LDAP_CONFIGURED = prev
		credentials.LDAP.Providers = prevMap
	})
	svc := &InternalService{}
	credentials.LDAP_CONFIGURED = false
	_, err := svc.GetLDAPProviders(context.Background(), connect.NewRequest(&pb.EmptyMessage{}))
	assert.Error(t, err)

	credentials.LDAP_CONFIGURED = true
	credentials.LDAP.Providers = map[string]credentials.LDAPProvider{"desk": {}}
	res, err := svc.GetLDAPProviders(context.Background(), connect.NewRequest(&pb.EmptyMessage{}))
	require.NoError(t, err)
	assert.Contains(t, res.Msg.Providers, "desk")
}

func TestFakeRepoAndConstructors(t *testing.T) {
	f := fakeICARepo{accessLevel: access.Level_READ, accessOK: true}
	ctx := context.Background()
	assert.Nil(t, f.GetEdgeCol(ctx, "e"))
	assert.False(t, f.CheckLink(ctx, nil, nil, nil))
	assert.NoError(t, f.Link(ctx, zap.NewNop(), nil, nil, nil, access.Level_READ, access.Role_UNSET))
	assert.NoError(t, f.LinkUntil(ctx, zap.NewNop(), nil, nil, nil, access.Level_READ, access.Role_UNSET, 1))
	assert.NoError(t, f.Move(ctx, nil, nil, nil, "ns"))
	ok, lvl := f.AccessLevel(ctx, nil, nil)
	assert.True(t, ok)
	assert.Equal(t, access.Level_READ, lvl)
	require.NoError(t, f.AccessLevelAndGet(ctx, zap.NewNop(), nil, &Account{Account: &accpb.Account{}}))
	require.NoError(t, f.AccessLevelAndGet(ctx, zap.NewNop(), nil, &Namespace{Namespace: &nspb.Namespace{}}))
	require.NoError(t, f.AccessLevelAndGet(ctx, zap.NewNop(), nil, NewBlankDeviceDocument("d")))
	cur, err := f.ListQuery(ctx, zap.NewNop(), nil, "Devices")
	assert.NoError(t, err)
	assert.Nil(t, cur)
	nodes, err := f.ListOwnedDeep(ctx, zap.NewNop(), nil)
	assert.NoError(t, err)
	assert.Nil(t, nodes)
	assert.NoError(t, f.DeleteRecursive(ctx, zap.NewNop(), nil))
	assert.NoError(t, f.Toggle(ctx, nil, "enabled"))
	assert.NoError(t, f.EnsureRootExists(zap.NewNop(), nil, "pw"))

	db := driver_mocks.NewMockDatabase(t)
	col := driver_mocks.NewMockCollection(t)
	db.On("Collection", mock.Anything, mock.Anything).Return(col, nil)
	db.On("Graph", mock.Anything, mock.Anything).Return(stubGraph{col: col}, nil)
	require.NotNil(t, NewAccountsController(zap.NewNop(), db, nil))
	require.NotNil(t, NewNamespacesController(zap.NewNop(), db))
	require.NotNil(t, NewPluginsController(zap.NewNop(), db))
	ica := NewInfinimeshCommonActionsRepo(db)
	dev := NewDevicesController(zap.NewNop(), db, nil, ica)
	require.NotNil(t, dev)
	mod := NewDevicesControllerModule(zap.NewNop(), db, nil)
	require.NotNil(t, mod.Handler())
	mod.SetSigningKey([]byte("k"))
	mod.SetScopePublisher(nil)
	mod.SetDenylist(nil)
	mod.SetClaimTokens(nil)
	mod.SetAuditor(nil)
	require.NotNil(t, NewGrantsController(zap.NewNop(), db, flexRepo{edge: col}))
	require.NotNil(t, NewRulesController(zap.NewNop(), db, flexRepo{edge: col}))
	aud := NewAuditController(zap.NewNop(), db, flexRepo{})
	require.NotNil(t, aud)
	aud.SetShadow(nopShadow{})
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(StatusFromString(connect.CodeInternal, "bad %s", "x")))
}

func TestCutPageEdges(t *testing.T) {
	items := []int{1, 2, 3}
	page, next := cutPage(items, maxPageSize+10, "nope")
	assert.Equal(t, items, page)
	assert.Empty(t, next)
	page, next = cutPage(items, 1, "9")
	assert.Empty(t, page)
	assert.Empty(t, next)
	page, next = cutPage(items, 1, "-1")
	assert.Equal(t, []int{1}, page)
	assert.Equal(t, "1", next)
}

func TestStoredAlarmCodec(t *testing.T) {
	assert.Equal(t, StoredAlarm{}, AlarmDocument(nil, "ns"))
	var state alarmState
	require.NoError(t, json.Unmarshal([]byte(`"raised"`), &state))
	require.Error(t, state.UnmarshalJSON([]byte(`"`)))
	require.NoError(t, json.Unmarshal([]byte("1"), &state))
	require.NoError(t, json.Unmarshal([]byte("2"), &state))
	require.NoError(t, json.Unmarshal([]byte("0"), &state))
	require.Error(t, state.UnmarshalJSON([]byte("{")))
	raw, err := alarmState(rules.Raised).MarshalJSON()
	require.NoError(t, err)
	assert.Equal(t, `"raised"`, string(raw))

	var when storedTime
	require.NoError(t, when.UnmarshalJSON([]byte("null")))
	require.NoError(t, when.UnmarshalJSON([]byte(`""`)))
	require.Error(t, when.UnmarshalJSON([]byte(`"nope"`)))
	require.Error(t, when.UnmarshalJSON([]byte("{")))
	require.NoError(t, when.UnmarshalJSON([]byte(`{"seconds":0,"nanos":0}`)))
	require.NoError(t, when.UnmarshalJSON([]byte(`{"seconds":10,"nanos":1}`)))
	require.Error(t, when.UnmarshalJSON([]byte(`"`)))
	empty, err := storedTime{}.MarshalJSON()
	require.NoError(t, err)
	assert.Equal(t, "null", string(empty))
	stamped, err := storedTime(time.Unix(10, 0).UTC()).MarshalJSON()
	require.NoError(t, err)
	assert.NotEmpty(t, stamped)

	doc := AlarmDocument(&rules.Alarm{
		Rule: "r", Device: "d", Title: "t", State: rules.Acked, Count: 2,
		RaisedAt: time.Unix(10, 0).UTC(),
	}, "ns")
	assert.Equal(t, "r_d", doc.Key)
	assert.Equal(t, rules.Acked, doc.Alarm().State)
	assert.Equal(t, rpbState(doc.Proto().GetState()), "ACKED")
	doc.State = alarmState(rules.Cleared)
	assert.Equal(t, "CLEARED", doc.Proto().GetState().String())
	doc.State = alarmState(rules.Raised)
	assert.NotNil(t, doc.Proto().GetRaisedAt())
	doc.RaisedAt = storedTime{}
	assert.Nil(t, doc.Proto().GetRaisedAt())
}

func rpbState(v interface{ String() string }) string { return v.String() }

func TestSessionsErrorsAndAccountMove(t *testing.T) {
	ctrl := newSessionsControllerForTest(&stubSessions{getErr: errors.New("get"), getActivityErr: errors.New("act"), revokeErr: errors.New("rev")})
	ctx := ctxWithAccount("acc", "sid")
	_, err := ctrl.Get(ctx, connect.NewRequest(&pb.ListRequest{}))
	assert.Error(t, err)
	_, err = ctrl.GetActivity(ctx, connect.NewRequest(&pb.EmptyMessage{}))
	assert.Error(t, err)
	_, err = ctrl.Revoke(ctx, connect.NewRequest(&sesspb.Session{Id: "sid"}))
	assert.Error(t, err)

	current := newSessionsControllerForTest(&stubSessions{getSessions: []*sesspb.Session{{Id: "other"}}})
	res, err := current.Get(ctx, connect.NewRequest(&pb.ListRequest{PageSize: 1}))
	require.NoError(t, err)
	assert.Nil(t, res.Msg.Sessions[0].Current)

	repo := flexRepo{moveErr: errors.New("move")}
	acc := &AccountsController{InfinimeshBaseController: InfinimeshBaseController{log: zap.NewNop()}, ica_repo: repo}
	_, err = acc.Move(accCtx("who"), connect.NewRequest(&pb.MoveRequest{Uuid: "u", Namespace: "ns"}))
	assert.EqualError(t, err, "move")
	repo.moveErr = nil
	acc.ica_repo = repo
	resMove, err := acc.Move(accCtx("who"), connect.NewRequest(&pb.MoveRequest{Uuid: "u", Namespace: "ns"}))
	require.NoError(t, err)
	assert.NotNil(t, resMove)
}

func accCtx(id string) context.Context {
	return context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, id)
}

func TestAccountAndDeviceNodeHelpers(t *testing.T) {
	acc := NewBlankAccountDocument("a")
	assert.Equal(t, driver.NewDocumentID(schema.ACCOUNTS_COL, "a"), acc.ID())
	acc.SetAccessLevel(access.Level_READ)
	acc.SetAccessLevel(access.Level_ADMIN)
	assert.Equal(t, access.Level_ADMIN, acc.GetAccess().Level)
	blank := &Account{Account: &accpb.Account{}}
	assert.Equal(t, access.Level_NONE, blank.GetAccess().Level)
	fromPB := NewAccountFromPB(&accpb.Account{Uuid: "b"})
	assert.Equal(t, "b", fromPB.GetUuid())

	ns := NewBlankNamespaceDocument("n")
	assert.Equal(t, driver.NewDocumentID(schema.NAMESPACES_COL, "n"), ns.ID())
	ns.SetAccessLevel(access.Level_READ)
	ns.SetAccessLevel(access.Level_MGMT)
	assert.Equal(t, access.Level_MGMT, ns.GetAccess().Level)

	dev := NewDeviceFromPB(&devpb.Device{Uuid: "d"})
	assert.Equal(t, driver.NewDocumentID(schema.DEVICES_COL, "d"), dev.ID())
	dev.SetAccessLevel(access.Level_READ)
	dev.SetAccessLevel(access.Level_ADMIN)
	assert.Equal(t, access.Level_ADMIN, dev.GetAccess().Level)
	assert.Equal(t, access.Level_NONE, NewBlankDeviceDocument("z").GetAccess().Level)

	plug := NewBlankPluginDocument("p")
	assert.Equal(t, driver.NewDocumentID(schema.PLUGINS_COL, "p"), plug.ID())
	plug.SetAccessLevel(access.Level_READ)
	plug.SetAccessLevel(access.Level_ADMIN)
	assert.Equal(t, access.Level_ADMIN, plug.GetAccess().Level)
	assert.Nil(t, (&Plugin{Plugin: &plg.Plugin{}}).GetAccess())
	rn := &ruleNode{key: "r"}
	assert.Equal(t, "r", rn.GetUuid())
	assert.Equal(t, driver.NewDocumentID(schema.RULES_COL, "r"), rn.ID())
	rn.SetAccessLevel(access.Level_ROOT)
	assert.Nil(t, rn.GetAccess())
}
