package graph

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/arangodb/go-driver"
	"github.com/google/uuid"
	"github.com/infinimesh/core/pkg/apierr"
	"github.com/infinimesh/core/pkg/graph/schema"
	inf "github.com/infinimesh/core/pkg/shared"
	"github.com/infinimesh/proto/node/access"
	gpb "github.com/infinimesh/proto/node/grants"
	"go.uber.org/zap"
)

type GrantsController struct {
	InfinimeshBaseController
	col     driver.Collection
	consent driver.Collection
	ica     InfinimeshCommonActionsRepo
}

func NewGrantsController(log *zap.Logger, db driver.Database, ica InfinimeshCommonActionsRepo) *GrantsController {
	ctx := context.TODO()
	col, _ := db.Collection(ctx, schema.GRANTS_COL)
	consent, _ := db.Collection(ctx, schema.CONSENT_COL)
	return &GrantsController{
		InfinimeshBaseController: InfinimeshBaseController{log: log.Named("GrantsController"), db: db},
		col:                      col, consent: consent, ica: ica,
	}
}

func (c *GrantsController) Create(ctx context.Context, req *connect.Request[gpb.Grant]) (*connect.Response[gpb.Grant], error) {
	in := req.Msg
	requestor, _ := ctx.Value(inf.InfinimeshAccountCtxKey).(string)
	if in.GetToAccount() == "" || in.GetNode() == "" || in.GetExpiresAt() <= time.Now().Unix() {
		return nil, apierr.Code(connect.CodeInvalidArgument, "to_account, node, and a future expires_at are required")
	}
	if in.GetLevel() >= access.Level_ADMIN || in.GetLevel() <= access.Level_NONE {
		return nil, apierr.Code(connect.CodeInvalidArgument, "level must be read or manage")
	}
	node := c.node(in.GetNode())
	if node == nil {
		return nil, apierr.Code(connect.CodeInvalidArgument, "node was not found")
	}
	if err := c.ica.AccessLevelAndGet(ctx, c.log, NewBlankAccountDocument(requestor), node); err != nil || node.GetAccess().GetLevel() < access.Level_ADMIN {
		return nil, apierr.Code(connect.CodePermissionDenied, "Not enough Access Rights")
	}
	edgeName := schema.ACC2NS
	if _, ok := node.(*Device); ok {
		edgeName = schema.ACC2DEV
	}
	edge := c.ica.GetEdgeCol(ctx, edgeName)
	to := NewBlankAccountDocument(in.GetToAccount())
	if err := c.ica.LinkUntil(ctx, c.log, edge, to, node, in.GetLevel(), access.Role_SHARED, in.GetExpiresAt()); err != nil {
		return nil, apierr.Code(connect.CodeInternal, "could not create the access edge")
	}
	in.Id = uuid.NewString()
	in.FromAccount = requestor
	if _, err := c.col.CreateDocument(ctx, in); err != nil {
		return nil, apierr.Code(connect.CodeInternal, "could not store the grant")
	}
	consent := &gpb.Consent{
		Id: uuid.NewString(), Grantor: requestor, Grantee: in.GetToAccount(),
		Scope: in.GetScope(), At: time.Now().Unix(), GrantId: in.GetId(),
	}
	_, _ = c.consent.CreateDocument(ctx, consent)
	return connect.NewResponse(in), nil
}

func (c *GrantsController) List(ctx context.Context, req *connect.Request[gpb.ListGrantsRequest]) (*connect.Response[gpb.Grants], error) {
	requestor, _ := ctx.Value(inf.InfinimeshAccountCtxKey).(string)
	cr, err := c.db.Query(ctx, `FOR g IN @@col
FILTER g.from_account == @me OR g.to_account == @me
RETURN g`, map[string]interface{}{"@col": schema.GRANTS_COL, "me": requestor})
	if err != nil {
		return nil, apierr.Code(connect.CodeInternal, "could not list grants")
	}
	defer cr.Close()
	var all []*gpb.Grant
	for {
		var g gpb.Grant
		_, err := cr.ReadDocument(ctx, &g)
		if driver.IsNoMoreDocuments(err) {
			break
		}
		if err != nil {
			return nil, apierr.Code(connect.CodeInternal, "could not list grants")
		}
		if req.Msg.GetNode() != "" && g.GetNode() != req.Msg.GetNode() {
			continue
		}
		if req.Msg.GetAccount() != "" && g.GetToAccount() != req.Msg.GetAccount() && g.GetFromAccount() != req.Msg.GetAccount() {
			continue
		}
		all = append(all, &g)
	}
	page, next := cutPage(all, req.Msg.GetPageSize(), req.Msg.GetPageToken())
	return connect.NewResponse(&gpb.Grants{Grants: page, NextPageToken: next}), nil
}

func (c *GrantsController) Revoke(ctx context.Context, req *connect.Request[gpb.Grant]) (*connect.Response[gpb.Grant], error) {
	var g gpb.Grant
	if _, err := c.col.ReadDocument(ctx, req.Msg.GetId(), &g); err != nil {
		return nil, apierr.Code(connect.CodeNotFound, "grant not found")
	}
	requestor, _ := ctx.Value(inf.InfinimeshAccountCtxKey).(string)
	if g.GetFromAccount() != requestor {
		return nil, apierr.Code(connect.CodePermissionDenied, "Not enough Access Rights")
	}
	node := c.node(g.GetNode())
	if node != nil {
		edgeName := schema.ACC2NS
		if _, ok := node.(*Device); ok {
			edgeName = schema.ACC2DEV
		}
		_ = c.ica.Link(ctx, c.log, c.ica.GetEdgeCol(ctx, edgeName), NewBlankAccountDocument(g.GetToAccount()), node, access.Level_NONE, access.Role_UNSET)
	}
	g.Revoked = true
	_, _ = c.col.ReplaceDocument(ctx, g.GetId(), &g)
	_, _ = c.db.Query(ctx, `FOR c IN @@col FILTER c.grant_id == @id UPDATE c WITH {revoked_at: @at} IN @@col`, map[string]interface{}{
		"@col": schema.CONSENT_COL, "id": g.GetId(), "at": time.Now().Unix(),
	})
	return connect.NewResponse(&g), nil
}

func (c *GrantsController) ListConsent(ctx context.Context, req *connect.Request[gpb.ListConsentRequest]) (*connect.Response[gpb.Consents], error) {
	requestor, _ := ctx.Value(inf.InfinimeshAccountCtxKey).(string)
	who := req.Msg.GetAccount()
	if who == "" {
		who = requestor
	}
	if who != requestor {
		ok, level := c.ica.AccessLevel(ctx, NewBlankAccountDocument(requestor), NewBlankAccountDocument(who))
		if !ok || level < access.Level_ADMIN {
			return nil, apierr.Code(connect.CodePermissionDenied, "Not enough Access Rights")
		}
	}
	cr, err := c.db.Query(ctx, `FOR c IN @@col FILTER c.grantor == @who OR c.grantee == @who RETURN c`, map[string]interface{}{
		"@col": schema.CONSENT_COL, "who": who,
	})
	if err != nil {
		return nil, apierr.Code(connect.CodeInternal, "could not list consent")
	}
	defer cr.Close()
	var all []*gpb.Consent
	for {
		var item gpb.Consent
		_, err := cr.ReadDocument(ctx, &item)
		if driver.IsNoMoreDocuments(err) {
			break
		}
		if err != nil {
			return nil, apierr.Code(connect.CodeInternal, "could not list consent")
		}
		all = append(all, &item)
	}
	page, next := cutPage(all, req.Msg.GetPageSize(), req.Msg.GetPageToken())
	return connect.NewResponse(&gpb.Consents{Consents: page, NextPageToken: next}), nil
}

func (c *GrantsController) Expire(ctx context.Context) {
	now := time.Now().Unix()
	for _, name := range []string{schema.ACC2NS, schema.NS2ACC, schema.ACC2DEV, schema.NS2DEV} {
		col := c.ica.GetEdgeCol(ctx, name)
		if col == nil {
			continue
		}
		cr, err := c.db.Query(ctx, `FOR e IN @@col FILTER e.expires_at > 0 AND e.expires_at <= @now REMOVE e IN @@col`, map[string]interface{}{
			"@col": name, "now": now,
		})
		if err == nil {
			cr.Close()
		}
	}
}

func (c *GrantsController) node(id string) InfinimeshGraphNode {
	ctx := context.Background()
	if col, err := c.db.Collection(ctx, schema.DEVICES_COL); err == nil {
		if ok, _ := col.DocumentExists(ctx, id); ok {
			return NewBlankDeviceDocument(id)
		}
	}
	if col, err := c.db.Collection(ctx, schema.NAMESPACES_COL); err == nil {
		if ok, _ := col.DocumentExists(ctx, id); ok {
			return NewBlankNamespaceDocument(id)
		}
	}
	return nil
}
