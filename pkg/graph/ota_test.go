package graph

import (
	"testing"

	nspb "github.com/infinimesh/proto/node/namespaces"
	"github.com/stretchr/testify/assert"
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

func TestKeepOTABackend(t *testing.T) {
	next := &nspb.OTAConfig{Enabled: false, Provider: "none"}
	KeepOTABackend(next, &nspb.OTAConfig{BackendUrl: "https://old.example", Backend: "hawkbit"})
	assert.Equal(t, "https://old.example", next.GetBackendUrl())
	assert.Equal(t, "hawkbit", next.GetBackend())
	assert.False(t, next.GetEnabled())
}
