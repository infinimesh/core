package graph

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/arangodb/go-driver"
	driver_mocks "github.com/infinimesh/core/mocks/github.com/arangodb/go-driver"
	"github.com/infinimesh/core/pkg/graph/schema"
	"github.com/infinimesh/core/pkg/rules"
	inf "github.com/infinimesh/core/pkg/shared"
	"github.com/infinimesh/proto/node/access"
	apb "github.com/infinimesh/proto/node/audit"
	gpb "github.com/infinimesh/proto/node/grants"
	rpb "github.com/infinimesh/proto/node/rules"
	plg "github.com/infinimesh/proto/plugins"
	shadowpb "github.com/infinimesh/proto/shadow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

func TestGrants(t *testing.T) {
	db := driver_mocks.NewMockDatabase(t)
	col := driver_mocks.NewMockCollection(t)
	consent := driver_mocks.NewMockCollection(t)
	devCol := driver_mocks.NewMockCollection(t)
	nsCol := driver_mocks.NewMockCollection(t)
	edge := driver_mocks.NewMockCollection(t)
	db.On("Collection", mock.Anything, schema.DEVICES_COL).Return(devCol, nil)
	db.On("Collection", mock.Anything, schema.NAMESPACES_COL).Return(nsCol, nil)
	devCol.On("DocumentExists", mock.Anything, "dev").Return(true, nil)
	devCol.On("DocumentExists", mock.Anything, mock.Anything).Return(false, nil)
	nsCol.On("DocumentExists", mock.Anything, "ns").Return(true, nil)
	nsCol.On("DocumentExists", mock.Anything, "missing").Return(false, errors.New("no"))
	ctrl := &GrantsController{
		InfinimeshBaseController: InfinimeshBaseController{log: zap.NewNop(), db: db},
		col:                      col, consent: consent, ica: flexRepo{edge: edge, level: access.Level_ADMIN},
	}
	ctx := accCtx("root")
	_, err := ctrl.Create(ctx, connect.NewRequest(&gpb.Grant{}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = ctrl.Create(ctx, connect.NewRequest(&gpb.Grant{ToAccount: "a", Node: "dev", ExpiresAt: time.Now().Unix() + 100, Level: access.Level_ADMIN}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = ctrl.Create(ctx, connect.NewRequest(&gpb.Grant{ToAccount: "a", Node: "missing", ExpiresAt: time.Now().Unix() + 100, Level: access.Level_READ}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	ctrl.ica = flexRepo{edge: edge, getErr: errors.New("no"), level: access.Level_ADMIN}
	_, err = ctrl.Create(ctx, connect.NewRequest(&gpb.Grant{ToAccount: "a", Node: "dev", ExpiresAt: time.Now().Unix() + 100, Level: access.Level_READ}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.ica = flexRepo{edge: edge, level: access.Level_READ}
	_, err = ctrl.Create(ctx, connect.NewRequest(&gpb.Grant{ToAccount: "a", Node: "ns", ExpiresAt: time.Now().Unix() + 100, Level: access.Level_MGMT}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.ica = flexRepo{edge: edge, level: access.Level_ADMIN, linkUntilErr: errors.New("link")}
	_, err = ctrl.Create(ctx, connect.NewRequest(&gpb.Grant{ToAccount: "a", Node: "dev", ExpiresAt: time.Now().Unix() + 100, Level: access.Level_READ}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	ctrl.ica = flexRepo{edge: edge, level: access.Level_ADMIN}
	col.On("CreateDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("store")).Once()
	_, err = ctrl.Create(ctx, connect.NewRequest(&gpb.Grant{ToAccount: "a", Node: "ns", ExpiresAt: time.Now().Unix() + 100, Level: access.Level_READ, Scope: "s"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	col.On("CreateDocument", mock.Anything, mock.Anything).Return(docMeta(schema.GRANTS_COL, "g"), nil).Once()
	consent.On("CreateDocument", mock.Anything, mock.Anything).Return(docMeta(schema.CONSENT_COL, "c"), nil).Once()
	created, err := ctrl.Create(ctx, connect.NewRequest(&gpb.Grant{ToAccount: "a", Node: "dev", ExpiresAt: time.Now().Unix() + 100, Level: access.Level_READ, Scope: "s"}))
	require.NoError(t, err)
	assert.NotEmpty(t, created.Msg.GetId())

	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q")).Once()
	_, err = ctrl.List(ctx, connect.NewRequest(&gpb.ListGrantsRequest{}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(readErr(errors.New("row")), nil).Once()
	_, err = ctrl.List(ctx, connect.NewRequest(&gpb.ListGrantsRequest{}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&scriptCursor{fn: []func(any) (driver.DocumentMeta, error){
		func(result any) (driver.DocumentMeta, error) {
			g := result.(*gpb.Grant)
			g.Node = "other"
			g.ToAccount = "a"
			return docMeta(schema.GRANTS_COL, "skip"), nil
		},
		func(result any) (driver.DocumentMeta, error) {
			g := result.(*gpb.Grant)
			g.Node = "dev"
			g.ToAccount = "nobody"
			g.FromAccount = "else"
			return docMeta(schema.GRANTS_COL, "skip2"), nil
		},
		func(result any) (driver.DocumentMeta, error) {
			g := result.(*gpb.Grant)
			g.Node = "dev"
			g.ToAccount = "a"
			return docMeta(schema.GRANTS_COL, "keep"), nil
		},
	}}, nil).Once()
	listed, err := ctrl.List(ctx, connect.NewRequest(&gpb.ListGrantsRequest{Node: "dev", Account: "a", PageSize: 10}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.Grants, 1)

	col.On("ReadDocument", mock.Anything, "missing", mock.Anything).Return(driver.DocumentMeta{}, errors.New("no")).Once()
	_, err = ctrl.Revoke(ctx, connect.NewRequest(&gpb.Grant{Id: "missing"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	col.On("ReadDocument", mock.Anything, "g", mock.Anything).Run(func(args mock.Arguments) {
		g := args.Get(2).(*gpb.Grant)
		g.Id = "g"
		g.FromAccount = "other"
		g.Node = "dev"
	}).Return(docMeta(schema.GRANTS_COL, "g"), nil).Once()
	_, err = ctrl.Revoke(ctx, connect.NewRequest(&gpb.Grant{Id: "g"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	col.On("ReadDocument", mock.Anything, "g", mock.Anything).Run(func(args mock.Arguments) {
		g := args.Get(2).(*gpb.Grant)
		g.Id = "g"
		g.FromAccount = "root"
		g.ToAccount = "a"
		g.Node = "missing"
	}).Return(docMeta(schema.GRANTS_COL, "g"), nil).Once()
	col.On("ReplaceDocument", mock.Anything, mock.Anything, mock.Anything).Return(docMeta(schema.GRANTS_COL, "g"), nil)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&scriptCursor{}, nil)
	revoked, err := ctrl.Revoke(ctx, connect.NewRequest(&gpb.Grant{Id: "g"}))
	require.NoError(t, err)
	assert.True(t, revoked.Msg.GetRevoked())
	col.On("ReadDocument", mock.Anything, "g2", mock.Anything).Run(func(args mock.Arguments) {
		g := args.Get(2).(*gpb.Grant)
		g.Id = "g2"
		g.FromAccount = "root"
		g.ToAccount = "a"
		g.Node = "dev"
	}).Return(docMeta(schema.GRANTS_COL, "g2"), nil).Once()
	_, err = ctrl.Revoke(ctx, connect.NewRequest(&gpb.Grant{Id: "g2"}))
	require.NoError(t, err)

	ctrl.ica = flexRepo{edge: edge, ok: false, level: access.Level_NONE}
	_, err = ctrl.ListConsent(ctx, connect.NewRequest(&gpb.ListConsentRequest{Account: "other"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.ica = flexRepo{edge: edge, ok: true, level: access.Level_ADMIN}
	errDB := driver_mocks.NewMockDatabase(t)
	ctrl.db = errDB
	errDB.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("consent")).Once()
	_, err = ctrl.ListConsent(ctx, connect.NewRequest(&gpb.ListConsentRequest{Account: "other"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	ctrl.db = db
	listedC, err := ctrl.ListConsent(ctx, connect.NewRequest(&gpb.ListConsentRequest{}))
	require.NoError(t, err)
	assert.Empty(t, listedC.Msg.Consents)

	ctrl.Expire(ctx)
	ctrl.ica = flexRepo{edge: nil}
	ctrl.Expire(ctx)
}

func TestRulesControllerBranches(t *testing.T) {
	col := driver_mocks.NewMockCollection(t)
	alarms := driver_mocks.NewMockCollection(t)
	db := driver_mocks.NewMockDatabase(t)
	ctrl := &RulesController{
		InfinimeshBaseController: InfinimeshBaseController{log: zap.NewNop(), db: db},
		col:                      col, alarms: alarms, ica: flexRepo{ok: false}, ns2r: col,
	}
	var nilCtrl *RulesController
	nilCtrl.SetAuditor(&auditCapture{})
	ctrl.SetAuditor(&auditCapture{})
	ctx := accCtx("root")
	_, err := ctrl.Create(ctx, connect.NewRequest(&rpb.Rule{}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = ctrl.Create(ctx, connect.NewRequest(&rpb.Rule{Name: "hot", When: "true", Namespace: "ns"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.ica = flexRepo{ok: true, level: access.Level_ADMIN}
	col.On("CreateDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("store")).Once()
	_, err = ctrl.Create(ctx, connect.NewRequest(&rpb.Rule{Name: "hot", When: "true", Namespace: "ns"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	col.On("CreateDocument", mock.Anything, mock.Anything).Return(docMeta(schema.RULES_COL, "rule"), nil).Once()
	created, err := ctrl.Create(ctx, connect.NewRequest(&rpb.Rule{Name: "hot", When: "true", Namespace: "ns"}))
	require.NoError(t, err)
	assert.Equal(t, "rule", created.Msg.GetUuid())

	col.On("ReadDocument", mock.Anything, "missing", mock.Anything).Return(driver.DocumentMeta{}, errors.New("no")).Once()
	_, err = ctrl.Get(ctx, connect.NewRequest(&rpb.Rule{Uuid: "missing"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	col.On("ReadDocument", mock.Anything, "rule", mock.Anything).Run(func(args mock.Arguments) {
		r := args.Get(2).(*rpb.Rule)
		r.Namespace = "ns"
		r.Name = "hot"
		r.When = "true"
	}).Return(docMeta(schema.RULES_COL, "rule"), nil)
	got, err := ctrl.Get(ctx, connect.NewRequest(&rpb.Rule{Uuid: "rule"}))
	require.NoError(t, err)
	assert.Equal(t, "rule", got.Msg.GetUuid())
	_, err = ctrl.Update(ctx, connect.NewRequest(&rpb.Rule{Uuid: "rule", When: "??"}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	col.On("ReplaceDocument", mock.Anything, "rule", mock.Anything).Return(driver.DocumentMeta{}, errors.New("rep")).Once()
	_, err = ctrl.Update(ctx, connect.NewRequest(&rpb.Rule{Uuid: "rule", Name: "hot", When: "true"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	col.On("ReplaceDocument", mock.Anything, "rule", mock.Anything).Return(docMeta(schema.RULES_COL, "rule"), nil).Once()
	updated, err := ctrl.Update(ctx, connect.NewRequest(&rpb.Rule{Uuid: "rule", Name: "hot", When: "true"}))
	require.NoError(t, err)
	assert.Equal(t, "ns", updated.Msg.GetNamespace())

	_, err = ctrl.List(ctx, connect.NewRequest(&rpb.ListRulesRequest{}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	ctrl.ica = flexRepo{ok: false}
	_, err = ctrl.List(ctx, connect.NewRequest(&rpb.ListRulesRequest{Namespace: "ns"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.ica = flexRepo{ok: true, level: access.Level_READ}
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q")).Once()
	_, err = ctrl.List(ctx, connect.NewRequest(&rpb.ListRulesRequest{Namespace: "ns"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(readErr(errors.New("row")), nil).Once()
	_, err = ctrl.List(ctx, connect.NewRequest(&rpb.ListRulesRequest{Namespace: "ns"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		r := result.(*rpb.Rule)
		r.Name = "hot"
	}), nil).Once()
	listed, err := ctrl.List(ctx, connect.NewRequest(&rpb.ListRulesRequest{Namespace: "ns", PageSize: 1}))
	require.NoError(t, err)
	assert.Equal(t, "hot", listed.Msg.Rules[0].GetName())

	ctrl.ica = flexRepo{ok: true, level: access.Level_ADMIN}
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("alarms")).Once()
	_, err = ctrl.Delete(ctx, connect.NewRequest(&rpb.Rule{Uuid: "rule"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&scriptCursor{}, nil).Once()
	col.On("RemoveDocument", mock.Anything, "rule").Return(driver.DocumentMeta{}, errors.New("rm")).Once()
	_, err = ctrl.Delete(ctx, connect.NewRequest(&rpb.Rule{Uuid: "rule"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&scriptCursor{}, nil).Once()
	col.On("RemoveDocument", mock.Anything, "rule").Return(docMeta(schema.RULES_COL, "rule"), nil).Once()
	_, err = ctrl.Delete(ctx, connect.NewRequest(&rpb.Rule{Uuid: "rule"}))
	require.NoError(t, err)

	_, err = ctrl.ListAlarms(ctx, connect.NewRequest(&rpb.ListAlarmsRequest{}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q")).Once()
	_, err = ctrl.ListAlarms(ctx, connect.NewRequest(&rpb.ListAlarmsRequest{Namespace: "ns"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(readErr(errors.New("row")), nil).Once()
	_, err = ctrl.ListAlarms(ctx, connect.NewRequest(&rpb.ListAlarmsRequest{Namespace: "ns"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&scriptCursor{fn: []func(any) (driver.DocumentMeta, error){
		func(result any) (driver.DocumentMeta, error) {
			doc := result.(*StoredAlarm)
			*doc = AlarmDocument(&rules.Alarm{Device: "other", State: rules.Raised}, "ns")
			return docMeta(schema.ALARMS_COL, "a"), nil
		},
		func(result any) (driver.DocumentMeta, error) {
			doc := result.(*StoredAlarm)
			doc.Device = "dev"
			doc.State = alarmState("cleared")
			doc.Namespace = "ns"
			return docMeta(schema.ALARMS_COL, "b"), nil
		},
		func(result any) (driver.DocumentMeta, error) {
			doc := result.(*StoredAlarm)
			doc.Device = "dev"
			doc.State = alarmState("raised")
			doc.Namespace = "ns"
			return docMeta(schema.ALARMS_COL, "c"), nil
		},
	}}, nil).Once()
	alarmsListed, err := ctrl.ListAlarms(ctx, connect.NewRequest(&rpb.ListAlarmsRequest{Namespace: "ns", Device: "dev"}))
	require.NoError(t, err)
	assert.NotEmpty(t, alarmsListed.Msg.Alarms)
	assert.False(t, alarmVisible(rpb.AlarmState_ACKED, false, rpb.AlarmState_RAISED))
	assert.True(t, alarmVisible(rpb.AlarmState_CLEARED, false, rpb.AlarmState_CLEARED))
	assert.True(t, alarmVisible(rpb.AlarmState_RAISED, true, rpb.AlarmState_CLEARED))

	alarms.On("ReadDocument", mock.Anything, mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("no")).Once()
	_, err = ctrl.AckAlarm(ctx, connect.NewRequest(&rpb.AlarmRef{Rule: "r", Device: "d"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	alarms.On("ReadDocument", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		doc := args.Get(2).(*StoredAlarm)
		doc.State = alarmState("cleared")
		doc.Namespace = "ns"
		doc.Device = "d"
	}).Return(docMeta(schema.ALARMS_COL, "k"), nil).Once()
	_, err = ctrl.AckAlarm(ctx, connect.NewRequest(&rpb.AlarmRef{Rule: "r", Device: "d"}))
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	alarms.On("ReadDocument", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		doc := args.Get(2).(*StoredAlarm)
		doc.State = alarmState("raised")
		doc.Namespace = "ns"
		doc.Device = "d"
	}).Return(docMeta(schema.ALARMS_COL, "k"), nil)
	alarms.On("ReplaceDocument", mock.Anything, mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("rep")).Once()
	_, err = ctrl.ClearAlarm(ctx, connect.NewRequest(&rpb.AlarmRef{Rule: "r", Device: "d"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	ctrl.ica = flexRepo{ok: false}
	_, err = ctrl.AckAlarm(ctx, connect.NewRequest(&rpb.AlarmRef{Rule: "r", Device: "d"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

type capShadow struct {
	shadows []*shadowpb.Shadow
	err     error
}

func (c capShadow) Get(context.Context, *shadowpb.GetRequest, ...grpc.CallOption) (*shadowpb.GetResponse, error) {
	if c.err != nil {
		return nil, c.err
	}
	return &shadowpb.GetResponse{Shadows: c.shadows}, nil
}

func TestAuditBranches(t *testing.T) {
	col := driver_mocks.NewMockCollection(t)
	db := driver_mocks.NewMockDatabase(t)
	ctrl := &AuditController{
		InfinimeshBaseController: InfinimeshBaseController{log: zap.NewNop(), db: db},
		col:                      col, ica: flexRepo{ok: true, level: access.Level_ADMIN},
	}
	ctrl.Record(context.Background(), "", "d", "a", "", "")
	ctrl.RecordRead(context.Background(), "", "a", "")
	ctrl.RecordRead(context.Background(), "d", "", "")
	col.On("CreateDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("write")).Once()
	ctrl.Record(context.Background(), "read", "d", "a", "t", "ns")
	var bare *AuditController
	bare.Record(context.Background(), "read", "d", "a", "", "")
	empty := &AuditController{}
	empty.Record(context.Background(), "read", "d", "", "", "")

	_, err := ctrl.List(accCtx("root"), connect.NewRequest(&apb.ListAuditRequest{}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	ctrl.ica = flexRepo{ok: false}
	_, err = ctrl.List(accCtx("root"), connect.NewRequest(&apb.ListAuditRequest{Namespace: "ns"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	_, err = ctrl.List(accCtx("root"), connect.NewRequest(&apb.ListAuditRequest{Device: "d"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	_, err = ctrl.List(accCtx("root"), connect.NewRequest(&apb.ListAuditRequest{Account: "other"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.ica = flexRepo{ok: true, level: access.Level_ADMIN}
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q")).Once()
	_, err = ctrl.List(accCtx("root"), connect.NewRequest(&apb.ListAuditRequest{Account: "root", Device: "d", Namespace: "ns"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(readErr(errors.New("row")), nil).Once()
	_, err = ctrl.List(accCtx("root"), connect.NewRequest(&apb.ListAuditRequest{Device: "d"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	when := storedTime(time.Unix(10, 0).UTC())
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		rec := result.(*storedAudit)
		rec.ID = "1"
		rec.At = when
		rec.Action = "read"
	}), nil).Once()
	listed, err := ctrl.List(accCtx("root"), connect.NewRequest(&apb.ListAuditRequest{Device: "d", PageSize: 1}))
	require.NoError(t, err)
	assert.Equal(t, "1", listed.Msg.Records[0].GetId())
	assert.NotNil(t, listed.Msg.Records[0].GetAt())
	assert.Nil(t, storedAudit{}.Proto().GetAt())

	ctrl.ica = flexRepo{getErr: errors.New("no")}
	_, err = ctrl.Export(accCtx("root"), connect.NewRequest(&apb.ExportRequest{Device: "d"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.ica = flexRepo{level: access.Level_ADMIN, accessFn: func(node InfinimeshGraphNode) {
		dev := node.(*Device)
		dev.Access = &access.Access{Level: access.Level_READ}
		dev.Title = "Pump"
	}}
	col.On("CreateDocument", mock.Anything, mock.Anything).Return(docMeta(schema.AUDIT_COL, "a"), nil)
	ctrl.shadow = capShadow{err: errors.New("shadow")}
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q")).Once()
	exported, err := ctrl.Export(accCtx("root"), connect.NewRequest(&apb.ExportRequest{Device: "d"}))
	require.NoError(t, err)
	assert.Equal(t, "Pump", exported.Msg.GetDevice().GetTitle())
	ctrl.shadow = capShadow{shadows: []*shadowpb.Shadow{{Device: "d"}}}
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&scriptCursor{fn: []func(any) (driver.DocumentMeta, error){
		func(result any) (driver.DocumentMeta, error) {
			p := result.(*string)
			*p = ""
			return docMeta(schema.AUDIT_COL, "empty"), nil
		},
		func(result any) (driver.DocumentMeta, error) {
			p := result.(*string)
			*p = "rec"
			return docMeta(schema.AUDIT_COL, "rec"), nil
		},
	}}, nil).Once()
	exported, err = ctrl.Export(accCtx("root"), connect.NewRequest(&apb.ExportRequest{Device: "d"}))
	require.NoError(t, err)
	assert.Contains(t, exported.Msg.GetHistory(), "audit:rec")
}

func TestPluginsBranches(t *testing.T) {
	assert.Equal(t, "Title cannot be empty", ValidatePluginDocument(&plg.Plugin{}))
	assert.Equal(t, "Kind can't be Unknown", ValidatePluginDocument(&plg.Plugin{Title: "t"}))
	assert.Equal(t, "Kind is set to Embedded, but no conf provided", ValidatePluginDocument(&plg.Plugin{Title: "t", Kind: plg.PluginKind_EMBEDDED}))
	assert.Empty(t, ValidatePluginDocument(&plg.Plugin{Title: "t", Kind: plg.PluginKind_EMBEDDED, EmbeddedConf: &plg.EmbededPluginConf{}}))
	assert.False(t, ValidateRoot(context.Background()))
	assert.False(t, ValidateRoot(context.WithValue(context.Background(), inf.InfinimeshRootCtxKey, "no")))
	assert.True(t, ValidateRoot(context.WithValue(context.Background(), inf.InfinimeshRootCtxKey, true)))

	var nilCtrl *PluginsController
	nilCtrl.SetOnRotate(func(string) {})
	nilCtrl.SetAuditor(nil)
	nilCtrl.SetSigningKey(nil)
	nilCtrl.SetDenylist(nil)
	nilCtrl.noteToken("x")
	_, ok := nilCtrl.OTAPlugin(context.Background(), "id")
	assert.False(t, ok)
	clearPluginToken(nil)
	assert.Empty(t, pluginNamespace(nil))
	ns := "plant"
	assert.Equal(t, "plant", pluginNamespace(&plg.Plugin{Namespace: &ns}))

	col := driver_mocks.NewMockCollection(t)
	db := driver_mocks.NewMockDatabase(t)
	ctrl := &PluginsController{log: zap.NewNop(), col: col, db: db, signingKey: []byte("secret"), ns_ctrl: &NamespacesController{log: zap.NewNop(), ica: flexRepo{level: access.Level_READ}}}
	_, ok = ctrl.OTAPlugin(context.Background(), "")
	assert.False(t, ok)
	col.On("ReadDocument", mock.Anything, "missing", mock.Anything).Return(driver.DocumentMeta{}, errors.New("no")).Once()
	_, ok = ctrl.OTAPlugin(context.Background(), "missing")
	assert.False(t, ok)
	col.On("ReadDocument", mock.Anything, "plug", mock.Anything).Run(func(args mock.Arguments) {
		p := args.Get(2).(*plg.Plugin)
		p.Kind = plg.PluginKind_EMBEDDED
	}).Return(docMeta(schema.PLUGINS_COL, "plug"), nil).Once()
	_, ok = ctrl.OTAPlugin(context.Background(), "plug")
	assert.False(t, ok)
	col.On("ReadDocument", mock.Anything, "ota", mock.Anything).Run(func(args mock.Arguments) {
		p := args.Get(2).(*plg.Plugin)
		p.Kind = plg.PluginKind_OTA
		p.Provider = "webhook"
		p.Namespace = &ns
		p.TokenId = "jti"
		p.Public = true
	}).Return(docMeta(schema.PLUGINS_COL, "ota"), nil).Once()
	loaded, ok := ctrl.OTAPlugin(context.Background(), "ota")
	assert.True(t, ok)
	assert.Equal(t, "webhook", loaded.Provider)
	_, _, err := ctrl.issueOTAToken("id", "webhook")
	require.NoError(t, err)
	ctrl.signingKey = nil
	_, _, err = ctrl.issueOTAToken("id", "webhook")
	assert.Error(t, err)

	root := context.WithValue(context.Background(), inf.InfinimeshRootCtxKey, true)
	h := http.Header{}
	h.Set("Idempotency-Key", "plugins-cover")
	saveIdem(h, "plugins.create", &plg.Plugin{Uuid: "cached"})
	req := connect.NewRequest(&plg.Plugin{})
	req.Header().Set("Idempotency-Key", "plugins-cover")
	cached, err := ctrl.Create(root, req)
	require.NoError(t, err)
	assert.Equal(t, "cached", cached.Msg.GetUuid())
	_, err = ctrl.Create(context.Background(), connect.NewRequest(&plg.Plugin{Title: "t", Kind: plg.PluginKind_EMBEDDED, EmbeddedConf: &plg.EmbededPluginConf{}}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	col.On("CreateDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("col")).Once()
	_, err = ctrl.Create(root, connect.NewRequest(&plg.Plugin{Title: "t", Kind: plg.PluginKind_EMBEDDED, EmbeddedConf: &plg.EmbededPluginConf{}}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	ctrl.signingKey = []byte("secret")
	col.On("CreateDocument", mock.Anything, mock.Anything).Return(docMeta(schema.PLUGINS_COL, "made"), nil).Once()
	col.On("UpdateDocument", mock.Anything, "made", mock.Anything).Return(driver.DocumentMeta{}, errors.New("tok")).Once()
	_, err = ctrl.Create(root, connect.NewRequest(&plg.Plugin{Title: "OTA", Kind: plg.PluginKind_OTA, Provider: "hawkbit"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))

	col.On("ReadDocument", mock.Anything, "gone", mock.Anything).Return(driver.DocumentMeta{}, errors.New("no")).Once()
	_, err = ctrl.Get(root, connect.NewRequest(&plg.Plugin{Uuid: "gone"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	col.On("ReadDocument", mock.Anything, "pub", mock.Anything).Run(func(args mock.Arguments) {
		p := args.Get(2).(*plg.Plugin)
		p.Public = true
		p.Token = "secret"
	}).Return(docMeta(schema.PLUGINS_COL, "pub"), nil).Once()
	got, err := ctrl.Get(context.Background(), connect.NewRequest(&plg.Plugin{Uuid: "pub"}))
	require.NoError(t, err)
	assert.Empty(t, got.Msg.GetToken())
	col.On("ReadDocument", mock.Anything, "priv", mock.Anything).Return(docMeta(schema.PLUGINS_COL, "priv"), nil).Once()
	_, err = ctrl.Get(context.Background(), connect.NewRequest(&plg.Plugin{Uuid: "priv"}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	col.On("ReadDocument", mock.Anything, "priv", mock.Anything).Run(func(args mock.Arguments) {
		p := args.Get(2).(*plg.Plugin)
		p.Namespace = &ns
	}).Return(docMeta(schema.PLUGINS_COL, "priv"), nil).Once()
	got, err = ctrl.Get(accCtx("root"), connect.NewRequest(&plg.Plugin{Uuid: "priv"}))
	require.NoError(t, err)
	assert.Equal(t, "priv", got.Msg.GetUuid())
	ctrl.ns_ctrl = &NamespacesController{log: zap.NewNop(), ica: flexRepo{getErr: errors.New("ns")}}
	col.On("ReadDocument", mock.Anything, "priv", mock.Anything).Run(func(args mock.Arguments) {
		p := args.Get(2).(*plg.Plugin)
		p.Namespace = &ns
	}).Return(docMeta(schema.PLUGINS_COL, "priv"), nil).Once()
	_, err = ctrl.Get(accCtx("root"), connect.NewRequest(&plg.Plugin{Uuid: "priv"}))
	assert.Error(t, err)

	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q")).Once()
	_, err = ctrl.List(root, connect.NewRequest(&plg.ListRequest{}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(readErr(errors.New("row")), nil).Once()
	_, err = ctrl.List(root, connect.NewRequest(&plg.ListRequest{}))
	assert.Error(t, err)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		p := result.(*plg.Plugin)
		p.Title = "pub"
		p.Token = "hide"
	}), nil).Once()
	listed, err := ctrl.List(context.Background(), connect.NewRequest(&plg.ListRequest{PageSize: 1}))
	require.NoError(t, err)
	assert.Empty(t, listed.Msg.Pool[0].GetToken())
	ctrl.ica_repo = flexRepo{list: oneRead(func(result any) {
		p := result.(*plg.Plugin)
		p.Title = "ns"
	})}
	listed, err = ctrl.List(context.Background(), connect.NewRequest(&plg.ListRequest{Namespace: &ns}))
	require.NoError(t, err)
	assert.Equal(t, "ns", listed.Msg.Plugins[0].GetTitle())

	_, err = ctrl.Update(context.Background(), connect.NewRequest(&plg.Plugin{Title: "t", Kind: plg.PluginKind_EMBEDDED, EmbeddedConf: &plg.EmbededPluginConf{}}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	_, err = ctrl.Update(root, connect.NewRequest(&plg.Plugin{Title: "t", Kind: plg.PluginKind_OTA, Provider: "nope"}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = ctrl.Update(root, connect.NewRequest(&plg.Plugin{Title: "t", Kind: plg.PluginKind_EMBEDDED, EmbeddedConf: &plg.EmbededPluginConf{}}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	col.On("ReadDocument", mock.Anything, "ota", mock.Anything).Run(func(args mock.Arguments) {
		p := args.Get(2).(*plg.Plugin)
		p.Provider = "mender"
		p.TokenId = "old"
		p.Kind = plg.PluginKind_OTA
	}).Return(docMeta(schema.PLUGINS_COL, "ota"), nil).Once()
	col.On("ReplaceDocument", mock.Anything, "ota", mock.Anything).Return(driver.DocumentMeta{}, errors.New("rep")).Once()
	_, err = ctrl.Update(root, connect.NewRequest(&plg.Plugin{Uuid: "ota", Title: "OTA", Kind: plg.PluginKind_OTA, Provider: "hawkbit"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	seen := ""
	ctrl.SetOnRotate(func(id string) { seen = id })
	ctrl.SetAuditor(&auditCapture{})
	col.On("ReadDocument", mock.Anything, "ota", mock.Anything).Run(func(args mock.Arguments) {
		p := args.Get(2).(*plg.Plugin)
		p.Provider = "mender"
		p.TokenId = "old"
		p.Kind = plg.PluginKind_OTA
		p.Namespace = &ns
	}).Return(docMeta(schema.PLUGINS_COL, "ota"), nil).Once()
	col.On("ReplaceDocument", mock.Anything, "ota", mock.Anything).Return(docMeta(schema.PLUGINS_COL, "ota"), nil).Once()
	updated, err := ctrl.Update(root, connect.NewRequest(&plg.Plugin{Uuid: "ota", Title: "OTA", Kind: plg.PluginKind_OTA, Provider: "hawkbit"}))
	require.NoError(t, err)
	assert.NotEmpty(t, updated.Msg.GetToken())
	assert.Equal(t, "ota", seen)

	_, err = ctrl.Delete(context.Background(), connect.NewRequest(&plg.Plugin{Uuid: "ota"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	col.On("ReadDocument", mock.Anything, "ota", mock.Anything).Return(docMeta(schema.PLUGINS_COL, "ota"), nil).Once()
	col.On("RemoveDocument", mock.Anything, "ota").Return(driver.DocumentMeta{}, errors.New("rm")).Once()
	_, err = ctrl.Delete(root, connect.NewRequest(&plg.Plugin{Uuid: "ota"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	col.On("ReadDocument", mock.Anything, "ota", mock.Anything).Run(func(args mock.Arguments) {
		p := args.Get(2).(*plg.Plugin)
		p.Namespace = &ns
	}).Return(docMeta(schema.PLUGINS_COL, "ota"), nil).Once()
	col.On("RemoveDocument", mock.Anything, "ota").Return(docMeta(schema.PLUGINS_COL, "ota"), nil).Once()
	deleted, err := ctrl.Delete(root, connect.NewRequest(&plg.Plugin{Uuid: "ota"}))
	require.NoError(t, err)
	assert.Equal(t, "ota", deleted.Msg.GetUuid())
}
