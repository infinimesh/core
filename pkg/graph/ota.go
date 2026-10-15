package graph

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/arangodb/go-driver"
	"github.com/infinimesh/core/pkg/apierr"
	"github.com/infinimesh/core/pkg/graph/schema"
	inf "github.com/infinimesh/core/pkg/shared"
	nspb "github.com/infinimesh/proto/node/namespaces"
)

func ValidOTAConfig(cfg *nspb.OTAConfig, otaAddons int) error {
	if cfg == nil {
		return nil
	}
	switch strings.TrimSpace(cfg.GetProvider()) {
	case "", "none", "hawkbit", "mender":
	case "webhook":
		if strings.TrimSpace(cfg.GetWebhookUrl()) == "" {
			return errOTAWebhook
		}
	default:
		return errOTAProvider
	}
	if cfg.GetEnabled() && otaAddons < 1 {
		return errOTAAddon
	}
	return nil
}

type otaConfigError string

func (e otaConfigError) Error() string { return string(e) }

const (
	errOTAProvider otaConfigError = "ota provider must be none, hawkbit, mender, or webhook"
	errOTAWebhook  otaConfigError = "webhook_url is required when provider is webhook"
	errOTAAddon    otaConfigError = "ota cannot be enabled until an OTA addon is registered"
)

func OTAActive(cfg *nspb.OTAConfig) bool {
	return cfg != nil && cfg.GetEnabled() && cfg.GetAddonRegistered()
}

func ValidAddonProvider(provider string) bool {
	switch provider {
	case "hawkbit", "mender", "webhook":
		return true
	default:
		return false
	}
}

func KeepOTABackend(next, prev *nspb.OTAConfig) {
	if next == nil || prev == nil {
		return
	}
	if next.BackendUrl == "" {
		next.BackendUrl = prev.BackendUrl
	}
	if next.Backend == "" {
		next.Backend = prev.Backend
	}
}

type otaPluginSource interface {
	OTAPlugin(ctx context.Context, id string) (OTAPlugin, bool)
}

func (c *NamespacesController) addonNamespace(ctx context.Context, id string) (*connect.Response[nspb.Namespace], error) {
	provider, _ := ctx.Value(inf.InfinimeshOTAProviderCtxKey).(string)
	if provider != "webhook" {
		return nil, apierr.Code(connect.CodePermissionDenied, "ota provider does not match the namespace")
	}
	pluginID, _ := ctx.Value(inf.InfinimeshPluginCtxKey).(string)
	jti, _ := ctx.Value(inf.InfinimeshJTICtxKey).(string)
	if c == nil || c.plugins == nil {
		return nil, apierr.Code(connect.CodePermissionDenied, "ota provider requires the addon token")
	}
	loaded, ok := c.plugins.OTAPlugin(ctx, pluginID)
	if !ok || loaded.TokenID != jti || loaded.Provider != "webhook" {
		return nil, apierr.Code(connect.CodePermissionDenied, "ota provider requires the addon token")
	}
	if !loaded.Public && loaded.Namespace != id {
		return nil, apierr.Code(connect.CodePermissionDenied, "ota provider does not match the namespace")
	}
	doc, err := c.Config(ctx, id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&nspb.Namespace{
		Uuid: doc.GetUuid(),
		Ota: &nspb.OTAConfig{
			Provider:   doc.GetOta().GetProvider(),
			WebhookUrl: doc.GetOta().GetWebhookUrl(),
		},
	}), nil
}

func (c *NamespacesController) countOTAAddons(ctx context.Context, namespace string) (int, error) {
	if c == nil || c.db == nil || namespace == "" {
		return 0, nil
	}
	cr, err := c.db.Query(ctx, `FOR p IN @@col
FILTER (p.kind == "OTA" OR p.kind == 2) AND (p.public == true OR p.namespace == @ns)
COLLECT WITH COUNT INTO n
RETURN n`, map[string]interface{}{
		"@col": schema.PLUGINS_COL, "ns": namespace,
	})
	if err != nil {
		return 0, err
	}
	defer cr.Close()
	var n int
	_, err = cr.ReadDocument(ctx, &n)
	if driver.IsNoMoreDocuments(err) {
		return 0, nil
	}
	return n, err
}

func (c *NamespacesController) markOTAAddon(ctx context.Context, ns *nspb.Namespace) {
	if ns == nil {
		return
	}
	n, err := c.countOTAAddons(ctx, ns.GetUuid())
	if err != nil || ns.Ota == nil && n == 0 {
		return
	}
	if ns.Ota == nil {
		ns.Ota = &nspb.OTAConfig{}
	}
	ns.Ota.AddonRegistered = n > 0
}
