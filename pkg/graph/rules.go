package graph

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/arangodb/go-driver"
	"github.com/google/uuid"
	"github.com/infinimesh/core/pkg/apierr"
	"github.com/infinimesh/core/pkg/graph/schema"
	"github.com/infinimesh/core/pkg/rules"
	inf "github.com/infinimesh/core/pkg/shared"
	pb "github.com/infinimesh/proto/node"
	"github.com/infinimesh/proto/node/access"
	rpb "github.com/infinimesh/proto/node/rules"
	"go.uber.org/zap"
)

type RulesController struct {
	InfinimeshBaseController
	col    driver.Collection
	alarms driver.Collection
	ica    InfinimeshCommonActionsRepo
	ns2r   driver.Collection
}

func NewRulesController(log *zap.Logger, db driver.Database, ica InfinimeshCommonActionsRepo) *RulesController {
	ctx := context.TODO()
	col, _ := db.Collection(ctx, schema.RULES_COL)
	alarms, _ := db.Collection(ctx, schema.ALARMS_COL)
	return &RulesController{
		InfinimeshBaseController: InfinimeshBaseController{log: log.Named("RulesController"), db: db},
		col:                      col, alarms: alarms, ica: ica, ns2r: ica.GetEdgeCol(ctx, schema.NS2RULE),
	}
}

type storedRule struct {
	*rpb.Rule
	driver.DocumentMeta
}

func (c *RulesController) Create(ctx context.Context, req *connect.Request[rpb.Rule]) (*connect.Response[rpb.Rule], error) {
	in := req.Msg
	if _, err := rules.FromProto(in); err != nil {
		return nil, apierr.Codef(connect.CodeInvalidArgument, "%v", err)
	}
	if err := c.requireNS(ctx, in.GetNamespace(), access.Level_ADMIN); err != nil {
		return nil, err
	}
	in.Uuid = uuid.NewString()
	meta, err := c.col.CreateDocument(ctx, storedRule{Rule: in, DocumentMeta: driver.DocumentMeta{Key: in.Uuid}})
	if err != nil {
		return nil, apierr.Code(connect.CodeInternal, "could not store the rule")
	}
	in.Uuid = meta.Key
	_ = c.ica.Link(ctx, c.log, c.ns2r, NewBlankNamespaceDocument(in.GetNamespace()), &ruleNode{key: in.Uuid}, access.Level_ADMIN, access.Role_OWNER)
	return connect.NewResponse(in), nil
}

func (c *RulesController) Update(ctx context.Context, req *connect.Request[rpb.Rule]) (*connect.Response[rpb.Rule], error) {
	cur, err := c.load(ctx, req.Msg.GetUuid(), access.Level_ADMIN)
	if err != nil {
		return nil, err
	}
	in := req.Msg
	in.Namespace = cur.GetNamespace()
	if _, err := rules.FromProto(in); err != nil {
		return nil, apierr.Codef(connect.CodeInvalidArgument, "%v", err)
	}
	if _, err := c.col.ReplaceDocument(ctx, cur.GetUuid(), storedRule{Rule: in, DocumentMeta: driver.DocumentMeta{Key: cur.GetUuid()}}); err != nil {
		return nil, apierr.Code(connect.CodeInternal, "could not update the rule")
	}
	return connect.NewResponse(in), nil
}

func (c *RulesController) Get(ctx context.Context, req *connect.Request[rpb.Rule]) (*connect.Response[rpb.Rule], error) {
	r, err := c.load(ctx, req.Msg.GetUuid(), access.Level_READ)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(r), nil
}

func (c *RulesController) List(ctx context.Context, req *connect.Request[rpb.ListRulesRequest]) (*connect.Response[rpb.Rules], error) {
	if req.Msg.GetNamespace() == "" {
		return nil, apierr.Code(connect.CodeInvalidArgument, "namespace is required")
	}
	if err := c.requireNS(ctx, req.Msg.GetNamespace(), access.Level_READ); err != nil {
		return nil, err
	}
	cr, err := c.db.Query(ctx, `FOR r IN @@col FILTER r.namespace == @ns RETURN r`, map[string]interface{}{
		"@col": schema.RULES_COL, "ns": req.Msg.GetNamespace(),
	})
	if err != nil {
		return nil, apierr.Code(connect.CodeInternal, "could not list rules")
	}
	defer cr.Close()
	var all []*rpb.Rule
	for {
		var r rpb.Rule
		_, err := cr.ReadDocument(ctx, &r)
		if driver.IsNoMoreDocuments(err) {
			break
		}
		if err != nil {
			return nil, apierr.Code(connect.CodeInternal, "could not list rules")
		}
		all = append(all, &r)
	}
	page, next := cutPage(all, req.Msg.GetPageSize(), req.Msg.GetPageToken())
	return connect.NewResponse(&rpb.Rules{Rules: page, NextPageToken: next}), nil
}

func (c *RulesController) Delete(ctx context.Context, req *connect.Request[rpb.Rule]) (*connect.Response[pb.DeleteResponse], error) {
	r, err := c.load(ctx, req.Msg.GetUuid(), access.Level_ADMIN)
	if err != nil {
		return nil, err
	}
	if err := c.deleteAlarms(ctx, r.GetUuid(), r.GetName()); err != nil {
		return nil, err
	}
	if _, err := c.col.RemoveDocument(ctx, r.GetUuid()); err != nil {
		return nil, apierr.Code(connect.CodeInternal, "could not delete the rule")
	}
	return connect.NewResponse(&pb.DeleteResponse{}), nil
}

func (c *RulesController) deleteAlarms(ctx context.Context, rule, name string) error {
	cr, err := c.db.Query(ctx, `FOR a IN @@col FILTER a.rule == @rule OR a.rule == @name REMOVE a IN @@col`, map[string]interface{}{
		"@col": schema.ALARMS_COL, "rule": rule, "name": name,
	})
	if err != nil {
		return apierr.Code(connect.CodeInternal, "could not delete the rule")
	}
	defer cr.Close()
	return nil
}

func (c *RulesController) ListAlarms(ctx context.Context, req *connect.Request[rpb.ListAlarmsRequest]) (*connect.Response[rpb.Alarms], error) {
	if req.Msg.GetNamespace() == "" {
		return nil, apierr.Code(connect.CodeInvalidArgument, "namespace is required")
	}
	if err := c.requireNS(ctx, req.Msg.GetNamespace(), access.Level_READ); err != nil {
		return nil, err
	}
	cr, err := c.db.Query(ctx, `FOR a IN @@col FILTER a.namespace == @ns RETURN a`, map[string]interface{}{
		"@col": schema.ALARMS_COL, "ns": req.Msg.GetNamespace(),
	})
	if err != nil {
		return nil, apierr.Code(connect.CodeInternal, "could not list alarms")
	}
	defer cr.Close()
	var all []*rpb.Alarm
	for {
		var doc StoredAlarm
		_, err := cr.ReadDocument(ctx, &doc)
		if driver.IsNoMoreDocuments(err) {
			break
		}
		if err != nil {
			return nil, apierr.Code(connect.CodeInternal, "could not list alarms")
		}
		a := doc.Proto()
		if req.Msg.GetDevice() != "" && a.GetDevice() != req.Msg.GetDevice() {
			continue
		}
		if !req.Msg.GetAnyState() && a.GetState() != req.Msg.GetState() {
			continue
		}
		all = append(all, a)
	}
	page, next := cutPage(all, req.Msg.GetPageSize(), req.Msg.GetPageToken())
	return connect.NewResponse(&rpb.Alarms{Alarms: page, NextPageToken: next}), nil
}

func (c *RulesController) AckAlarm(ctx context.Context, req *connect.Request[rpb.AlarmRef]) (*connect.Response[rpb.Alarm], error) {
	return c.setAlarm(ctx, req.Msg, rpb.AlarmState_ACKED)
}

func (c *RulesController) ClearAlarm(ctx context.Context, req *connect.Request[rpb.AlarmRef]) (*connect.Response[rpb.Alarm], error) {
	return c.setAlarm(ctx, req.Msg, rpb.AlarmState_CLEARED)
}

func (c *RulesController) setAlarm(ctx context.Context, ref *rpb.AlarmRef, state rpb.AlarmState) (*connect.Response[rpb.Alarm], error) {
	key := AlarmKey(ref.GetRule(), ref.GetDevice())
	var doc StoredAlarm
	if _, err := c.alarms.ReadDocument(ctx, key, &doc); err != nil {
		return nil, apierr.Code(connect.CodeNotFound, "alarm not found")
	}
	if err := c.requireNS(ctx, doc.Namespace, access.Level_MGMT); err != nil {
		return nil, err
	}
	if state == rpb.AlarmState_ACKED && string(doc.State) != rules.Raised {
		return nil, apierr.Code(connect.CodeFailedPrecondition, "alarm is not raised")
	}
	now := storedTime(time.Now().UTC())
	if state == rpb.AlarmState_ACKED {
		doc.State = alarmState(rules.Acked)
		doc.AckedAt = now
	} else {
		doc.State = alarmState(rules.Cleared)
		doc.ClearedAt = now
	}
	if _, err := c.alarms.ReplaceDocument(ctx, key, doc); err != nil {
		return nil, apierr.Code(connect.CodeInternal, "could not update the alarm")
	}
	return connect.NewResponse(doc.Proto()), nil
}

func (c *RulesController) load(ctx context.Context, id string, level access.Level) (*rpb.Rule, error) {
	var r rpb.Rule
	meta, err := c.col.ReadDocument(ctx, id, &r)
	if err != nil {
		return nil, apierr.Code(connect.CodeNotFound, "rule not found")
	}
	r.Uuid = meta.Key
	if err := c.requireNS(ctx, r.GetNamespace(), level); err != nil {
		return nil, err
	}
	return &r, nil
}

func (c *RulesController) requireNS(ctx context.Context, ns string, level access.Level) error {
	requestor, _ := ctx.Value(inf.InfinimeshAccountCtxKey).(string)
	ok, got := c.ica.AccessLevel(ctx, NewBlankAccountDocument(requestor), NewBlankNamespaceDocument(ns))
	if !ok || got < level {
		return apierr.Code(connect.CodePermissionDenied, "Not enough Access Rights")
	}
	return nil
}

type ruleNode struct{ key string }

func (r *ruleNode) GetUuid() string             { return r.key }
func (r *ruleNode) ID() driver.DocumentID       { return driver.NewDocumentID(schema.RULES_COL, r.key) }
func (r *ruleNode) SetAccessLevel(access.Level) {}
func (r *ruleNode) GetAccess() *access.Access   { return nil }
