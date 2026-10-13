/*
Copyright © 2021-2023 Infinite Devices GmbH, Nikita Ivanovski info@slnt-opp.xyz

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package graph

import (
	"context"

	"connectrpc.com/connect"
	"github.com/arangodb/go-driver"
	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
	"github.com/infinimesh/core/pkg/apierr"
	inf "github.com/infinimesh/core/pkg/shared"
	"go.uber.org/zap"

	"github.com/infinimesh/core/pkg/graph/schema"
	"github.com/infinimesh/proto/node/access"
	"github.com/infinimesh/proto/node/namespaces"
	pb "github.com/infinimesh/proto/plugins"
)

type Plugin struct {
	*pb.Plugin
	driver.DocumentMeta
}

func (o *Plugin) ID() driver.DocumentID {
	return o.DocumentMeta.ID
}

func (o *Plugin) SetAccessLevel(level access.Level) {
	if o.Access == nil {
		o.Access = &access.Access{
			Level: level,
		}
		return
	}
	o.Access.Level = level
}

func NewBlankPluginDocument(key string) *Plugin {
	return &Plugin{
		Plugin: &pb.Plugin{
			Uuid: key,
		},
		DocumentMeta: NewBlankDocument(schema.PLUGINS_COL, key),
	}
}

type PluginsController struct {
	pb.UnimplementedPluginsServiceServer
	log *zap.Logger

	col     driver.Collection // Plugins Collection
	ns_ctrl *NamespacesController

	ica_repo InfinimeshCommonActionsRepo

	db         driver.Database
	signingKey []byte
	audit      pluginAuditor
}

type pluginAuditor interface {
	Record(ctx context.Context, action, device, account, tokenID, namespace string)
}

const (
	auditPluginCreate = "plugin_create"
	auditPluginDelete = "plugin_delete"
)

func (c *PluginsController) SetAuditor(a pluginAuditor) {
	if c != nil {
		c.audit = a
	}
}

func (c *PluginsController) auditPlugin(ctx context.Context, action, namespace string) {
	if c == nil || c.audit == nil {
		return
	}
	account, _ := ctx.Value(inf.InfinimeshAccountCtxKey).(string)
	jti, _ := ctx.Value(inf.InfinimeshJTICtxKey).(string)
	c.audit.Record(ctx, action, "", account, jti, namespace)
}

func pluginNamespace(p *pb.Plugin) string {
	if p == nil || p.Namespace == nil {
		return ""
	}
	return *p.Namespace
}

func (c *PluginsController) SetSigningKey(key []byte) {
	if c != nil {
		c.signingKey = key
	}
}

type OTAPlugin struct {
	ID        string
	Provider  string
	Namespace string
	TokenID   string
	Public    bool
}

func (c *PluginsController) OTAPlugin(ctx context.Context, id string) (OTAPlugin, bool) {
	if c == nil || c.col == nil || id == "" {
		return OTAPlugin{}, false
	}
	var plug pb.Plugin
	meta, err := c.col.ReadDocument(ctx, id, &plug)
	if err != nil || plug.GetKind() != pb.PluginKind_OTA {
		return OTAPlugin{}, false
	}
	ns := ""
	if plug.Namespace != nil {
		ns = *plug.Namespace
	}
	return OTAPlugin{
		ID: meta.ID.Key(), Provider: plug.GetProvider(), Namespace: ns,
		TokenID: plug.GetTokenId(), Public: plug.GetPublic(),
	}, true
}

func (c *PluginsController) issueOTAToken(id, provider string) (string, string, error) {
	if len(c.signingKey) == 0 {
		return "", "", apierr.Code(connect.CodeInternal, "signing key is not configured")
	}
	jti := uuid.NewString()
	claims := jwt.MapClaims{
		inf.INFINIMESH_PLUGIN_CLAIM:       id,
		inf.INFINIMESH_OTA_PROVIDER_CLAIM: provider,
		inf.INFINIMESH_JTI_CLAIM:          jti,
		inf.INFINIMESH_NOSESSION_CLAIM:    true,
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(c.signingKey)
	if err != nil {
		return "", "", apierr.Code(connect.CodeInternal, "could not issue the addon token")
	}
	return signed, jti, nil
}

func clearPluginToken(p *pb.Plugin) {
	if p != nil {
		p.Token = ""
	}
}

func NewPluginsController(log *zap.Logger, db driver.Database) *PluginsController {
	ctx := context.TODO()
	col, _ := db.Collection(ctx, schema.PLUGINS_COL)
	return &PluginsController{
		log: log.Named("PluginsController"), col: col, db: db,
		ns_ctrl:  NewNamespacesController(log, db),
		ica_repo: NewInfinimeshCommonActionsRepo(db),
	}
}

func ValidateRoot(ctx context.Context) bool {
	rootV := ctx.Value(inf.InfinimeshRootCtxKey)
	if rootV == nil {
		return false
	}

	root, ok := rootV.(bool)
	return ok && root
}

func ValidatePluginDocument(p *pb.Plugin) string {
	if p.Title == "" {
		return "Title cannot be empty"
	}

	if p.Kind == pb.PluginKind_UNKNOWN {
		return "Kind can't be Unknown"
	} else if p.Kind == pb.PluginKind_EMBEDDED && p.EmbeddedConf == nil {
		return "Kind is set to Embedded, but no conf provided"
	}

	return ""
}

func (c *PluginsController) Create(ctx context.Context, req *connect.Request[pb.Plugin]) (*connect.Response[pb.Plugin], error) {
	var cached pb.Plugin
	if loadIdem(req.Header(), "plugins.create", &cached) {
		return connect.NewResponse(&cached), nil
	}
	log := c.log.Named("Create")
	plug := req.Msg

	if !ValidateRoot(ctx) {
		return nil, apierr.Code(connect.CodePermissionDenied, "Not enough access rights to create Plugin")
	}

	log.Debug("Creating", zap.Any("plugin", plug))
	msg := ValidatePluginDocument(plug)
	if msg != "" {
		return nil, apierr.Code(connect.CodeInvalidArgument, msg)
	}
	if plug.GetKind() == pb.PluginKind_OTA && !ValidAddonProvider(plug.GetProvider()) {
		return nil, apierr.Code(connect.CodeInvalidArgument, "ota provider must be hawkbit, mender, or webhook")
	}
	clearPluginToken(plug)

	plugin := Plugin{Plugin: plug}
	meta, err := c.col.CreateDocument(ctx, plugin)
	if err != nil {
		return nil, apierr.Codef(connect.CodeInternal, "Error while creating Plugin in DB: %v", err)
	}
	plugin.Uuid = meta.ID.Key()
	plugin.DocumentMeta = meta
	if plug.GetKind() == pb.PluginKind_OTA {
		signed, jti, err := c.issueOTAToken(plugin.Uuid, plug.GetProvider())
		if err != nil {
			return nil, err
		}
		plugin.TokenId = jti
		if _, err := c.col.UpdateDocument(ctx, plugin.Uuid, map[string]interface{}{"token_id": jti}); err != nil {
			return nil, apierr.Code(connect.CodeInternal, "could not store the addon token")
		}
		plugin.Token = signed
	}

	log.Debug("Created", zap.String("plugin", plugin.Uuid))
	c.auditPlugin(ctx, auditPluginCreate, pluginNamespace(plugin.Plugin))
	resp := connect.NewResponse(plugin.Plugin)
	saveIdem(req.Header(), "plugins.create", resp.Msg)
	return resp, nil
}

const listAllPluginsQuery = `FOR plug IN @@plugins RETURN plug`
const listAllPublicPluginsQuery = `
FOR plug IN @@plugins
FILTER plug.public
    RETURN plug
`

func (c *PluginsController) Get(ctx context.Context, req *connect.Request[pb.Plugin]) (*connect.Response[pb.Plugin], error) {
	log := c.log.Named("Get")
	plug := req.Msg

	meta, err := c.col.ReadDocument(ctx, plug.Uuid, plug)
	if err != nil {
		log.Warn("Couldn't get plugin", zap.Error(err))
		return nil, apierr.Code(connect.CodeNotFound, "Plugin not found")
	}
	plug.Uuid = meta.ID.Key()
	clearPluginToken(plug)

	if plug.Public || ValidateRoot(ctx) {
		return connect.NewResponse(plug), nil
	}

	if plug.Namespace == nil || *plug.Namespace == "" {
		return nil, apierr.Code(connect.CodeInvalidArgument, "Namespace is not given")
	}

	ns, err := c.ns_ctrl.Get(ctx, connect.NewRequest(&namespaces.Namespace{Uuid: *plug.Namespace}))
	if err != nil {
		return nil, err
	}

	if ns.Msg.Access.Level < access.Level_READ {
		return nil, apierr.Code(connect.CodePermissionDenied, "Not enough Access")
	}

	return connect.NewResponse(plug), nil
}

func (c *PluginsController) List(ctx context.Context, req *connect.Request[pb.ListRequest]) (*connect.Response[pb.Plugins], error) {
	log := c.log.Named("List")
	r := req.Msg

	var cr driver.Cursor
	var err error

	if ValidateRoot(ctx) {
		cr, err = c.db.Query(ctx, listAllPluginsQuery, map[string]interface{}{
			"@plugins": schema.PLUGINS_COL,
		})

	} else if r.Namespace != nil && *r.Namespace != "" {
		cr, err = c.ica_repo.ListQuery(WithDepth(ctx, 1), log, NewBlankNamespaceDocument(*r.Namespace), schema.PLUGINS_COL)
	} else {
		cr, err = c.db.Query(ctx, listAllPublicPluginsQuery, map[string]interface{}{
			"@plugins": schema.PLUGINS_COL,
		})
	}

	if err != nil {
		return nil, apierr.Codef(connect.CodeInternal, "Error getting Plugins from DB: %v", err)
	}
	defer cr.Close()

	var res []*pb.Plugin
	for {
		var plug pb.Plugin
		meta, err := cr.ReadDocument(ctx, &plug)
		if driver.IsNoMoreDocuments(err) {
			break
		} else if err != nil {
			return nil, err
		}
		plug.Uuid = meta.ID.Key()
		clearPluginToken(&plug)

		log.Debug("Got document", zap.Any("plugin", &plug))
		res = append(res, &plug)
	}

	page, next := cutPage(res, r.GetPageSize(), r.GetPageToken())
	return connect.NewResponse(&pb.Plugins{
		Pool:          page,
		Plugins:       page,
		NextPageToken: next,
	}), nil
}

func (c *PluginsController) Update(ctx context.Context, req *connect.Request[pb.Plugin]) (*connect.Response[pb.Plugin], error) {
	log := c.log.Named("Update")
	plug := req.Msg

	if !ValidateRoot(ctx) {
		return nil, apierr.Code(connect.CodePermissionDenied, "Not enough access rights to update Plugin")
	}

	msg := ValidatePluginDocument(plug)
	if msg != "" {
		return nil, apierr.Code(connect.CodeInvalidArgument, msg)
	}
	if plug.GetKind() == pb.PluginKind_OTA && !ValidAddonProvider(plug.GetProvider()) {
		return nil, apierr.Code(connect.CodeInvalidArgument, "ota provider must be hawkbit, mender, or webhook")
	}
	clearPluginToken(plug)

	if plug.Uuid == "" {
		return nil, apierr.Code(connect.CodeInvalidArgument, "No Plugin UUID has been provided")
	}
	var cur pb.Plugin
	if _, err := c.col.ReadDocument(ctx, plug.Uuid, &cur); err == nil {
		plug.TokenId = cur.GetTokenId()
	}
	if plug.GetKind() == pb.PluginKind_OTA && (plug.GetTokenId() == "" || plug.GetProvider() != cur.GetProvider()) {
		signed, jti, err := c.issueOTAToken(plug.Uuid, plug.GetProvider())
		if err != nil {
			return nil, err
		}
		plug.TokenId = jti
		plug.Token = signed
	}
	stored := plug.GetToken()
	clearPluginToken(plug)

	plugin := Plugin{Plugin: plug}
	_, err := c.col.ReplaceDocument(ctx, plug.Uuid, plugin)
	if err != nil {
		return nil, apierr.Codef(connect.CodeInternal, "Error while updating Plugin in DB: %v", err)
	}

	log.Debug("Updated", zap.Any("plugin", plugin))
	plugin.Token = stored
	return connect.NewResponse(plugin.Plugin), nil
}

func (c *PluginsController) Delete(ctx context.Context, req *connect.Request[pb.Plugin]) (*connect.Response[pb.Plugin], error) {
	plug := req.Msg
	if !ValidateRoot(ctx) {
		return nil, StatusFromString(connect.CodePermissionDenied, "Not enough access rights to delete this Plugin")
	}

	var cur pb.Plugin
	_, _ = c.col.ReadDocument(ctx, plug.Uuid, &cur)
	_, err := c.col.RemoveDocument(ctx, plug.Uuid)
	if err != nil {
		return nil, StatusFromString(connect.CodeInternal, "Error while deleting Plugin: %v", err)
	}
	c.auditPlugin(ctx, auditPluginDelete, pluginNamespace(&cur))

	return connect.NewResponse(plug), nil
}
