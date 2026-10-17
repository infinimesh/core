package graph

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/arangodb/go-driver"
	driver_mocks "github.com/infinimesh/core/v3/mocks/github.com/arangodb/go-driver"
	"github.com/infinimesh/core/v3/pkg/graph/schema"
	inf "github.com/infinimesh/core/v3/pkg/shared"
	pb "github.com/infinimesh/proto/node"
	"github.com/infinimesh/proto/node/access"
	accpb "github.com/infinimesh/proto/node/accounts"
	nspb "github.com/infinimesh/proto/node/namespaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/structpb"
)

func newNS(repo flexRepo) *NamespacesController {
	return &NamespacesController{log: zap.NewNop(), ica: repo}
}

type plugSrc struct {
	p  OTAPlugin
	ok bool
}

func (p plugSrc) OTAPlugin(context.Context, string) (OTAPlugin, bool) { return p.p, p.ok }

func TestNamespacesCreateUpdateList(t *testing.T) {
	ctrl := newNS(flexRepo{})
	h := http.Header{}
	h.Set("Idempotency-Key", "ns-cover-create")
	saveIdem(h, "namespaces.create", &nspb.Namespace{Uuid: "cached"})
	req := connect.NewRequest(&nspb.Namespace{})
	req.Header().Set("Idempotency-Key", "ns-cover-create")
	cached, err := ctrl.Create(accCtx("root"), req)
	require.NoError(t, err)
	assert.Equal(t, "cached", cached.Msg.GetUuid())

	_, err = ctrl.Create(accCtx("root"), connect.NewRequest(&nspb.Namespace{}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	col := driver_mocks.NewMockCollection(t)
	ctrl.col = col
	ctrl.acc2ns = col
	col.On("CreateDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("col")).Once()
	_, err = ctrl.Create(accCtx("root"), connect.NewRequest(&nspb.Namespace{Title: "Plant", Uuid: "ignore"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))

	ctrl.ica = flexRepo{linkErr: errors.New("edge")}
	col.On("CreateDocument", mock.Anything, mock.Anything).Return(docMeta(schema.NAMESPACES_COL, "ns"), nil).Once()
	col.On("RemoveDocument", mock.Anything, "ns").Return(docMeta(schema.NAMESPACES_COL, "ns"), nil).Once()
	_, err = ctrl.Create(accCtx("root"), connect.NewRequest(&nspb.Namespace{Title: "Plant"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))

	ctrl.ica = flexRepo{}
	col.On("CreateDocument", mock.Anything, mock.Anything).Return(docMeta(schema.NAMESPACES_COL, "ns"), nil).Once()
	created, err := ctrl.Create(accCtx("root"), connect.NewRequest(&nspb.Namespace{Title: "Plant"}))
	require.NoError(t, err)
	assert.Equal(t, "ns", created.Msg.GetUuid())

	var nilCtrl *NamespacesController
	nilCtrl.SetPlugins(plugSrc{})
	ctrl.SetPlugins(plugSrc{ok: true})

	_, err = ctrl.Get(context.Background(), connect.NewRequest(&nspb.Namespace{Uuid: "ns"}))
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	ctrl.ica = flexRepo{getErr: errors.New("gone")}
	_, err = ctrl.Get(accCtx("root"), connect.NewRequest(&nspb.Namespace{Uuid: "ns"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	_, err = ctrl.Config(context.Background(), "")
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	col.On("ReadDocument", mock.Anything, "missing", mock.Anything).Return(driver.DocumentMeta{}, errors.New("no")).Once()
	_, err = ctrl.Config(context.Background(), "missing")
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	ctrl.ica = flexRepo{getErr: errors.New("gone")}
	_, err = ctrl.Update(accCtx("root"), connect.NewRequest(&nspb.Namespace{Uuid: "ns", Title: "n"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	ctrl.ica = flexRepo{level: access.Level_READ}
	_, err = ctrl.Update(accCtx("root"), connect.NewRequest(&nspb.Namespace{Uuid: "ns", Title: "n"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	db := driver_mocks.NewMockDatabase(t)
	ctrl.db = db
	ctrl.ica = flexRepo{level: access.Level_ADMIN}
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("addons")).Once()
	_, err = ctrl.Update(accCtx("root"), connect.NewRequest(&nspb.Namespace{Uuid: "ns", Ota: &nspb.OTAConfig{Provider: "hawkbit"}}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))

	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		n := result.(*int)
		*n = 0
	}), nil).Once()
	_, err = ctrl.Update(accCtx("root"), connect.NewRequest(&nspb.Namespace{Uuid: "ns", Ota: &nspb.OTAConfig{Provider: "nope"}}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	cfg, _ := structpb.NewStruct(map[string]any{"k": "v"})
	plug := "embed"
	col.On("ReplaceDocument", mock.Anything, "ns", mock.Anything).Return(driver.DocumentMeta{}, errors.New("rep")).Once()
	_, err = ctrl.Update(accCtx("root"), connect.NewRequest(&nspb.Namespace{
		Uuid: "ns", Title: "Next", Plugin: &plug, Config: cfg, Quota: &nspb.Quota{MaxDevices: 2},
	}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))

	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(func(context.Context, string, map[string]interface{}) driver.Cursor {
		return oneRead(func(result any) {
			n := result.(*int)
			*n = 1
		})
	}, nil)
	col.On("ReplaceDocument", mock.Anything, "ns", mock.Anything).Return(docMeta(schema.NAMESPACES_COL, "ns"), nil).Once()
	updated, err := ctrl.Update(accCtx("root"), connect.NewRequest(&nspb.Namespace{
		Uuid: "ns", Ota: &nspb.OTAConfig{Provider: "webhook", WebhookUrl: "https://ota.example", Enabled: true, Backend: "", BackendUrl: ""},
	}))
	require.NoError(t, err)
	assert.True(t, updated.Msg.GetOta().GetAddonRegistered())

	ctrl.ica = flexRepo{listErr: errors.New("list")}
	_, err = ctrl.List(accCtx("root"), connect.NewRequest(&pb.ListRequest{}))
	assert.Error(t, err)
	ctrl.ica = flexRepo{list: readErr(errors.New("row"))}
	_, err = ctrl.List(accCtx("root"), connect.NewRequest(&pb.ListRequest{}))
	assert.Error(t, err)
	ctrl.ica = flexRepo{list: oneRead(func(result any) {
		ns := result.(*nspb.Namespace)
		ns.Title = "Plant"
	})}
	listed, err := ctrl.List(accCtx("root"), connect.NewRequest(&pb.ListRequest{PageSize: 1}))
	require.NoError(t, err)
	assert.Equal(t, "k", listed.Msg.Namespaces[0].GetUuid())
	_, err = ctrl.Accessibles(context.Background(), connect.NewRequest(&nspb.Namespace{}))
	assert.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
}

func TestNamespacesJoinsAndDelete(t *testing.T) {
	ctrl := newNS(flexRepo{getErr: errors.New("gone")})
	_, err := ctrl.Joins(accCtx("root"), connect.NewRequest(&nspb.Namespace{Uuid: "ns"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	ctrl.ica = flexRepo{level: access.Level_READ}
	_, err = ctrl.Joins(accCtx("root"), connect.NewRequest(&nspb.Namespace{Uuid: "ns"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	db := driver_mocks.NewMockDatabase(t)
	ctrl.db = db
	ctrl.ica = flexRepo{level: access.Level_ADMIN}
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q")).Once()
	_, err = ctrl.Joins(accCtx("root"), connect.NewRequest(&nspb.Namespace{Uuid: "ns"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(readErr(errors.New("row")), nil).Once()
	_, err = ctrl.Joins(accCtx("root"), connect.NewRequest(&nspb.Namespace{Uuid: "ns"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		acc := result.(*accpb.Account)
		acc.Uuid = "member"
	}), nil).Once()
	joined, err := ctrl.Joins(accCtx("root"), connect.NewRequest(&nspb.Namespace{Uuid: "ns"}))
	require.NoError(t, err)
	assert.Equal(t, "member", joined.Msg.Accounts[0].GetUuid())

	ctrl.ica = flexRepo{getErr: errors.New("gone")}
	_, err = ctrl.Join(accCtx("root"), connect.NewRequest(&pb.JoinRequest{Namespace: "ns", Account: "a"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	ctrl.ica = flexRepo{level: access.Level_ADMIN, role: access.Role_UNSET}
	_, err = ctrl.Join(accCtx("root"), connect.NewRequest(&pb.JoinRequest{Namespace: "ns", Account: "a"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	accs := driver_mocks.NewMockCollection(t)
	ctrl.accs = accs
	ctrl.acc2ns = accs
	ctrl.ica = flexRepo{level: access.Level_ROOT, role: access.Role_UNSET}
	accs.On("ReadDocument", mock.Anything, "missing", mock.Anything).Return(driver.DocumentMeta{}, errors.New("no")).Once()
	_, err = ctrl.Join(accCtx("root"), connect.NewRequest(&pb.JoinRequest{Namespace: "ns", Account: "missing", Access: access.Level_READ}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	accs.On("ReadDocument", mock.Anything, "a", mock.Anything).Return(docMeta(schema.ACCOUNTS_COL, "a"), nil)
	ctrl.ica = flexRepo{level: access.Level_ADMIN, role: access.Role_OWNER}
	_, err = ctrl.Join(accCtx("root"), connect.NewRequest(&pb.JoinRequest{Namespace: "ns", Account: "a", Access: access.Level_ROOT}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	ctrl.ica = flexRepo{level: access.Level_ADMIN, role: access.Role_OWNER, linkErr: errors.New("link")}
	_, err = ctrl.Join(accCtx("root"), connect.NewRequest(&pb.JoinRequest{Namespace: "ns", Account: "a", Access: access.Level_READ}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))

	ctrl.ica = flexRepo{level: access.Level_ADMIN, role: access.Role_OWNER}
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&scriptCursor{}, nil).Once()
	joined, err = ctrl.Join(accCtx("root"), connect.NewRequest(&pb.JoinRequest{Namespace: "ns", Account: "a", Access: access.Level_READ}))
	require.NoError(t, err)
	assert.Empty(t, joined.Msg.Accounts)

	ctrl.ica = flexRepo{getErr: errors.New("gone")}
	_, err = ctrl.Deletables(accCtx("root"), connect.NewRequest(&nspb.Namespace{Uuid: "ns"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	ctrl.ica = flexRepo{level: access.Level_ADMIN, role: access.Role_UNSET}
	_, err = ctrl.Deletables(accCtx("root"), connect.NewRequest(&nspb.Namespace{Uuid: "ns"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.ica = flexRepo{level: access.Level_ROOT, deepErr: errors.New("deep")}
	_, err = ctrl.Deletables(accCtx("root"), connect.NewRequest(&nspb.Namespace{Uuid: "ns"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	ctrl.ica = flexRepo{role: access.Role_OWNER, level: access.Level_READ, deep: &access.Nodes{}}
	_, err = ctrl.Deletables(accCtx("root"), connect.NewRequest(&nspb.Namespace{Uuid: "ns"}))
	require.NoError(t, err)

	ctrl.ica = flexRepo{getErr: errors.New("gone")}
	_, err = ctrl.Delete(accCtx("root"), connect.NewRequest(&nspb.Namespace{Uuid: "ns"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	ctrl.ica = flexRepo{level: access.Level_READ, role: access.Role_UNSET}
	_, err = ctrl.Delete(accCtx("root"), connect.NewRequest(&nspb.Namespace{Uuid: "ns"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.ica = flexRepo{role: access.Role_OWNER, delErr: errors.New("del")}
	_, err = ctrl.Delete(accCtx("root"), connect.NewRequest(&nspb.Namespace{Uuid: "ns"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	ctrl.ica = flexRepo{level: access.Level_ROOT}
	deleted, err := ctrl.Delete(accCtx("root"), connect.NewRequest(&nspb.Namespace{Uuid: "ns"}))
	require.NoError(t, err)
	assert.NotNil(t, deleted)
}

func TestOTAAddonPaths(t *testing.T) {
	var ctrl *NamespacesController
	n, err := ctrl.countOTAAddons(context.Background(), "ns")
	require.NoError(t, err)
	assert.Zero(t, n)
	ctrl = &NamespacesController{log: zap.NewNop()}
	n, err = ctrl.countOTAAddons(context.Background(), "")
	require.NoError(t, err)
	assert.Zero(t, n)
	ctrl.markOTAAddon(context.Background(), nil)

	db := driver_mocks.NewMockDatabase(t)
	ctrl.db = db
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q")).Once()
	_, err = ctrl.countOTAAddons(context.Background(), "ns")
	assert.Error(t, err)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&scriptCursor{}, nil).Once()
	n, err = ctrl.countOTAAddons(context.Background(), "ns")
	require.NoError(t, err)
	assert.Zero(t, n)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(readErr(errors.New("row")), nil).Once()
	_, err = ctrl.countOTAAddons(context.Background(), "ns")
	assert.Error(t, err)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		p := result.(*int)
		*p = 2
	}), nil)
	ns := &nspb.Namespace{Uuid: "ns"}
	ctrl.markOTAAddon(context.Background(), ns)
	assert.True(t, ns.Ota.GetAddonRegistered())
	ns.Ota = nil
	ctrl.markOTAAddon(context.Background(), &nspb.Namespace{Uuid: ""})

	ctx := context.WithValue(context.Background(), inf.InfinimeshOTAProviderCtxKey, "hawkbit")
	_, err = ctrl.addonNamespace(ctx, "ns")
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctx = context.WithValue(context.Background(), inf.InfinimeshOTAProviderCtxKey, "webhook")
	_, err = ctrl.addonNamespace(ctx, "ns")
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.plugins = plugSrc{ok: false}
	ctx = context.WithValue(ctx, inf.InfinimeshPluginCtxKey, "plug")
	ctx = context.WithValue(ctx, inf.InfinimeshJTICtxKey, "jti")
	_, err = ctrl.addonNamespace(ctx, "ns")
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.plugins = plugSrc{ok: true, p: OTAPlugin{TokenID: "jti", Provider: "webhook", Namespace: "other"}}
	_, err = ctrl.addonNamespace(ctx, "ns")
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	col := driver_mocks.NewMockCollection(t)
	ctrl.col = col
	col.On("ReadDocument", mock.Anything, "ns", mock.Anything).Return(driver.DocumentMeta{}, errors.New("no")).Once()
	ctrl.plugins = plugSrc{ok: true, p: OTAPlugin{TokenID: "jti", Provider: "webhook", Public: true}}
	_, err = ctrl.addonNamespace(ctx, "ns")
	assert.Error(t, err)

	col.On("ReadDocument", mock.Anything, "ns", mock.Anything).Run(func(args mock.Arguments) {
		doc := args.Get(2).(*nspb.Namespace)
		doc.Ota = &nspb.OTAConfig{Provider: "webhook", WebhookUrl: "https://ota.example"}
	}).Return(docMeta(schema.NAMESPACES_COL, "ns"), nil).Once()
	res, err := ctrl.addonNamespace(ctx, "ns")
	require.NoError(t, err)
	assert.Equal(t, "https://ota.example", res.Msg.GetOta().GetWebhookUrl())

	pluginCtx := context.WithValue(accCtx("root"), inf.InfinimeshPluginCtxKey, "plug")
	_, err = ctrl.Get(pluginCtx, connect.NewRequest(&nspb.Namespace{Uuid: "ns"}))
	assert.Error(t, err)
}
