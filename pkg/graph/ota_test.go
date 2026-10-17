package graph

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/arangodb/go-driver"
	driver_mocks "github.com/infinimesh/core/v3/mocks/github.com/arangodb/go-driver"
	inf "github.com/infinimesh/core/v3/pkg/shared"
	nspb "github.com/infinimesh/proto/node/namespaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestValidOTAConfig(t *testing.T) {
	assert.NoError(t, ValidOTAConfig(nil, 0))
	assert.NoError(t, ValidOTAConfig(&nspb.OTAConfig{}, 0))
	assert.NoError(t, ValidOTAConfig(&nspb.OTAConfig{Provider: "none"}, 0))
	assert.NoError(t, ValidOTAConfig(&nspb.OTAConfig{Provider: "hawkbit"}, 0))
	assert.NoError(t, ValidOTAConfig(&nspb.OTAConfig{Provider: "mender"}, 1))
	assert.NoError(t, ValidOTAConfig(&nspb.OTAConfig{Provider: "webhook", WebhookUrl: "https://ota.example/hook", Enabled: true}, 1))
	assert.EqualError(t, ValidOTAConfig(&nspb.OTAConfig{Provider: "webhook"}, 1), "webhook_url is required when provider is webhook")
	assert.EqualError(t, ValidOTAConfig(&nspb.OTAConfig{Provider: "hawkBit", Enabled: true}, 1), "ota provider must be none, hawkbit, mender, or webhook")
	assert.EqualError(t, ValidOTAConfig(&nspb.OTAConfig{Enabled: true}, 0), "ota cannot be enabled until an OTA addon is registered")
	assert.False(t, OTAActive(&nspb.OTAConfig{Enabled: true}))
	assert.True(t, OTAActive(&nspb.OTAConfig{Enabled: true, AddonRegistered: true}))
}

func TestNamespaceConfigIgnoresAccount(t *testing.T) {
	col := driver_mocks.NewMockCollection(t)
	col.On("ReadDocument", mock.Anything, "plant", mock.Anything).Run(func(args mock.Arguments) {
		doc := args.Get(2).(*nspb.Namespace)
		doc.Ota = &nspb.OTAConfig{Enabled: true, Provider: "hawkbit"}
	}).Return(driver.DocumentMeta{ID: driver.NewDocumentID("Namespaces", "plant")}, nil)
	ctrl := &NamespacesController{log: zap.NewNop(), col: col}
	ns, err := ctrl.Config(context.Background(), "plant")
	require.NoError(t, err)
	require.Equal(t, "plant", ns.GetUuid())
	require.Equal(t, "hawkbit", ns.GetOta().GetProvider())
	require.True(t, ns.GetOta().GetEnabled())
}

type fakePluginSource struct {
	plug OTAPlugin
	ok   bool
}

func (f fakePluginSource) OTAPlugin(context.Context, string) (OTAPlugin, bool) {
	return f.plug, f.ok
}

func TestAddonReadsWebhookURL(t *testing.T) {
	col := driver_mocks.NewMockCollection(t)
	col.On("ReadDocument", mock.Anything, "plant", mock.Anything).Run(func(args mock.Arguments) {
		doc := args.Get(2).(*nspb.Namespace)
		doc.Title = "Plant"
		doc.Ota = &nspb.OTAConfig{Enabled: true, Provider: "webhook", WebhookUrl: "http://host:18092/ota", BackendUrl: "https://hidden.example"}
	}).Return(driver.DocumentMeta{ID: driver.NewDocumentID("Namespaces", "plant")}, nil)
	ctrl := &NamespacesController{log: zap.NewNop(), col: col}
	ctrl.SetPlugins(fakePluginSource{ok: true, plug: OTAPlugin{ID: "plug", Provider: "webhook", TokenID: "jti-1", Public: true}})
	ctx := context.WithValue(context.Background(), inf.InfinimeshPluginCtxKey, "plug")
	ctx = context.WithValue(ctx, inf.InfinimeshOTAProviderCtxKey, "webhook")
	ctx = context.WithValue(ctx, inf.InfinimeshJTICtxKey, "jti-1")
	res, err := ctrl.Get(ctx, connect.NewRequest(&nspb.Namespace{Uuid: "plant"}))
	require.NoError(t, err)
	require.Equal(t, "plant", res.Msg.GetUuid())
	require.Empty(t, res.Msg.GetTitle())
	require.Nil(t, res.Msg.GetConfig())
	require.Equal(t, "webhook", res.Msg.GetOta().GetProvider())
	require.Equal(t, "http://host:18092/ota", res.Msg.GetOta().GetWebhookUrl())
	require.Empty(t, res.Msg.GetOta().GetBackendUrl())
	require.False(t, res.Msg.GetOta().GetEnabled())

	other := context.WithValue(context.Background(), inf.InfinimeshPluginCtxKey, "plug")
	other = context.WithValue(other, inf.InfinimeshOTAProviderCtxKey, "hawkbit")
	other = context.WithValue(other, inf.InfinimeshJTICtxKey, "jti-1")
	_, err = ctrl.Get(other, connect.NewRequest(&nspb.Namespace{Uuid: "plant"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.ErrorContains(t, err, "ota provider does not match the namespace")

	scoped := &NamespacesController{log: zap.NewNop()}
	scoped.SetPlugins(fakePluginSource{ok: true, plug: OTAPlugin{Provider: "webhook", TokenID: "jti-1", Namespace: "plant"}})
	ctx = context.WithValue(context.Background(), inf.InfinimeshPluginCtxKey, "plug")
	ctx = context.WithValue(ctx, inf.InfinimeshOTAProviderCtxKey, "webhook")
	ctx = context.WithValue(ctx, inf.InfinimeshJTICtxKey, "jti-1")
	_, err = scoped.Get(ctx, connect.NewRequest(&nspb.Namespace{Uuid: "other"}))
	require.Error(t, err)
	assert.ErrorContains(t, err, "ota provider does not match the namespace")

	bad := &NamespacesController{log: zap.NewNop()}
	bad.SetPlugins(fakePluginSource{ok: true, plug: OTAPlugin{Provider: "webhook", TokenID: "other"}})
	_, err = bad.Get(ctx, connect.NewRequest(&nspb.Namespace{Uuid: "plant"}))
	require.Error(t, err)
	assert.ErrorContains(t, err, "ota provider requires the addon token")
}

func TestKeepOTABackend(t *testing.T) {
	next := &nspb.OTAConfig{Enabled: false, Provider: "none"}
	KeepOTABackend(next, &nspb.OTAConfig{BackendUrl: "https://old.example", Backend: "hawkbit"})
	assert.Equal(t, "https://old.example", next.GetBackendUrl())
	assert.Equal(t, "hawkbit", next.GetBackend())
	assert.False(t, next.GetEnabled())
}
