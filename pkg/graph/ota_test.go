package graph

import (
	"context"
	"testing"

	"github.com/arangodb/go-driver"
	driver_mocks "github.com/infinimesh/core/mocks/github.com/arangodb/go-driver"
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

func TestKeepOTABackend(t *testing.T) {
	next := &nspb.OTAConfig{Enabled: false, Provider: "none"}
	KeepOTABackend(next, &nspb.OTAConfig{BackendUrl: "https://old.example", Backend: "hawkbit"})
	assert.Equal(t, "https://old.example", next.GetBackendUrl())
	assert.Equal(t, "hawkbit", next.GetBackend())
	assert.False(t, next.GetEnabled())
}
