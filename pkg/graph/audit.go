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
	apb "github.com/infinimesh/proto/node/audit"
	"github.com/infinimesh/proto/shadow"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	auditRead   = "read"
	auditCreate = "create"
	auditToken  = "token"
)

type storedAudit struct {
	ID      string     `json:"id"`
	Account string     `json:"account,omitempty"`
	Device  string     `json:"device,omitempty"`
	TokenID string     `json:"token_id,omitempty"`
	At      storedTime `json:"at,omitempty"`
	Action  string     `json:"action,omitempty"`
}

func (s storedAudit) Proto() *apb.AuditRecord {
	out := &apb.AuditRecord{
		Id: s.ID, Account: s.Account, Device: s.Device, TokenId: s.TokenID, Action: s.Action,
	}
	if !time.Time(s.At).IsZero() {
		out.At = timestamppb.New(time.Time(s.At))
	}
	return out
}

type auditCol interface {
	CreateDocument(ctx context.Context, document interface{}) (driver.DocumentMeta, error)
}

type AuditController struct {
	InfinimeshBaseController
	col    auditCol
	ica    InfinimeshCommonActionsRepo
	shadow shadowGetter
}

type shadowGetter interface {
	Get(ctx context.Context, in *shadow.GetRequest, opts ...grpc.CallOption) (*shadow.GetResponse, error)
}

func (c *AuditController) SetShadow(g shadowGetter) { c.shadow = g }

func NewAuditController(log *zap.Logger, db driver.Database, ica InfinimeshCommonActionsRepo) *AuditController {
	col, _ := db.Collection(context.TODO(), schema.AUDIT_COL)
	return &AuditController{
		InfinimeshBaseController: InfinimeshBaseController{log: log.Named("AuditController"), db: db},
		col:                      col, ica: ica,
	}
}

func (c *AuditController) RecordRead(ctx context.Context, device, account, tokenID string) {
	if device == "" || (account == "" && tokenID == "") {
		return
	}
	c.Record(ctx, auditRead, device, account, tokenID)
}

func (c *AuditController) Record(ctx context.Context, action, device, account, tokenID string) {
	if c == nil || c.col == nil || action == "" || (device == "" && account == "" && tokenID == "") {
		return
	}
	rec := storedAudit{
		ID: uuid.NewString(), Account: account, Device: device, TokenID: tokenID,
		At: storedTime(time.Now().UTC()), Action: action,
	}
	if _, err := c.col.CreateDocument(ctx, rec); err != nil && c.log != nil {
		c.log.Warn("audit write failed", zap.String("device", device), zap.Error(err))
	}
}

func (c *AuditController) List(ctx context.Context, req *connect.Request[apb.ListAuditRequest]) (*connect.Response[apb.AuditRecords], error) {
	requestor, _ := ctx.Value(inf.InfinimeshAccountCtxKey).(string)
	if req.Msg.GetDevice() == "" && req.Msg.GetAccount() == "" {
		return nil, apierr.Code(connect.CodeInvalidArgument, "device or account is required")
	}
	if dev := req.Msg.GetDevice(); dev != "" {
		ok, level := c.ica.AccessLevel(ctx, NewBlankAccountDocument(requestor), NewBlankDeviceDocument(dev))
		if !ok || level < access.Level_READ {
			return nil, apierr.Code(connect.CodePermissionDenied, "Not enough Access Rights")
		}
	}
	if acc := req.Msg.GetAccount(); acc != "" && acc != requestor {
		ok, level := c.ica.AccessLevel(ctx, NewBlankAccountDocument(requestor), NewBlankAccountDocument(acc))
		if !ok || level < access.Level_ADMIN {
			return nil, apierr.Code(connect.CodePermissionDenied, "Not enough Access Rights")
		}
	}
	cr, err := c.db.Query(ctx, `FOR a IN @@col
FILTER (@device == "" OR a.device == @device) AND (@account == "" OR a.account == @account)
SORT a.at DESC
RETURN a`, map[string]interface{}{
		"@col": schema.AUDIT_COL, "device": req.Msg.GetDevice(), "account": req.Msg.GetAccount(),
	})
	if err != nil {
		return nil, apierr.Code(connect.CodeInternal, "could not list the audit log")
	}
	defer cr.Close()
	var all []*apb.AuditRecord
	for {
		var rec storedAudit
		_, err := cr.ReadDocument(ctx, &rec)
		if driver.IsNoMoreDocuments(err) {
			break
		}
		if err != nil {
			return nil, apierr.Code(connect.CodeInternal, "could not list the audit log")
		}
		all = append(all, rec.Proto())
	}
	page, next := cutPage(all, req.Msg.GetPageSize(), req.Msg.GetPageToken())
	return connect.NewResponse(&apb.AuditRecords{Records: page, NextPageToken: next}), nil
}

func (c *AuditController) Export(ctx context.Context, req *connect.Request[apb.ExportRequest]) (*connect.Response[apb.ExportResponse], error) {
	id := req.Msg.GetDevice()
	requestor, _ := ctx.Value(inf.InfinimeshAccountCtxKey).(string)
	acc := *NewBlankAccountDocument(requestor)
	dev := *NewBlankDeviceDocument(id)
	if err := c.ica.AccessLevelAndGet(ctx, c.log, &acc, &dev); err != nil || dev.Access.GetLevel() < access.Level_READ {
		return nil, apierr.Code(connect.CodePermissionDenied, "Not enough Access Rights")
	}
	c.RecordRead(ctx, id, requestor, "")
	sh := &shadow.Shadow{Device: id}
	if c.shadow != nil {
		if res, err := c.shadow.Get(ctx, &shadow.GetRequest{Devices: []string{id}, Device: id}); err == nil && len(res.GetShadows()) > 0 {
			sh = res.GetShadows()[0]
		}
	}
	history := []string{
		"redis:" + id + ":reported",
		"redis:" + id + ":desired",
		"redis:" + id + ":connection",
	}
	cr, err := c.db.Query(ctx, `FOR a IN @@col FILTER a.device == @device RETURN a.id`, map[string]interface{}{
		"@col": schema.AUDIT_COL, "device": id,
	})
	if err == nil {
		defer cr.Close()
		for {
			var recID string
			_, err := cr.ReadDocument(ctx, &recID)
			if driver.IsNoMoreDocuments(err) {
				break
			}
			if err == nil && recID != "" {
				history = append(history, "audit:"+recID)
			}
		}
	}
	return connect.NewResponse(&apb.ExportResponse{
		Device:  dev.Device,
		Shadow:  sh,
		History: history,
	}), nil
}
