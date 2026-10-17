package graph

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/arangodb/go-driver"
	driver_mocks "github.com/infinimesh/core/v3/mocks/github.com/arangodb/go-driver"
	inf "github.com/infinimesh/core/v3/pkg/shared"
	"github.com/infinimesh/proto/node/access"
	apb "github.com/infinimesh/proto/node/audit"
	"github.com/stretchr/testify/mock"
	"go.uber.org/zap"
)

func TestCreateAndTokenAuditListedByDeviceAndAccount(t *testing.T) {
	docs := &memAudit{}
	ctrl := &AuditController{
		InfinimeshBaseController: InfinimeshBaseController{log: zap.NewNop()},
		col:                      docs,
		ica:                      fakeICA{ok: true, level: access.Level_READ},
	}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "acc1")
	ctrl.Record(ctx, auditCreate, "dev1", "acc1", "", "plant")
	ctrl.Record(ctx, auditToken, "dev1", "acc1", "jti-1", "")
	if len(docs.rows) != 2 {
		t.Fatalf("%+v", docs.rows)
	}

	db := driver_mocks.NewMockDatabase(t)
	ctrl.db = db
	for _, q := range []struct{ device, account string }{{"dev1", ""}, {"", "acc1"}} {
		rows := append([]storedAudit(nil), docs.rows...)
		db.On("Query", mock.Anything, mock.MatchedBy(func(s string) bool {
			return strings.Contains(s, "a.device") && strings.Contains(s, "a.account")
		}), mock.MatchedBy(func(b map[string]interface{}) bool {
			return b["device"] == q.device && b["account"] == q.account
		})).Return(&auditCursor{rows: rows, device: q.device, account: q.account}, nil).Once()
	}

	byDevice, err := ctrl.List(ctx, connect.NewRequest(&apb.ListAuditRequest{Device: "dev1"}))
	if err != nil {
		t.Fatal(err)
	}
	byAccount, err := ctrl.List(ctx, connect.NewRequest(&apb.ListAuditRequest{Account: "acc1"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range [][]*apb.AuditRecord{byDevice.Msg.GetRecords(), byAccount.Msg.GetRecords()} {
		got := map[string]string{}
		for _, rec := range page {
			if rec.GetDevice() != "dev1" || rec.GetAccount() != "acc1" || rec.GetAt() == nil {
				t.Fatalf("%+v", rec)
			}
			got[rec.GetAction()] = rec.GetTokenId()
		}
		if _, ok := got[auditCreate]; !ok {
			t.Fatalf("%+v", page)
		}
		if got[auditToken] != "jti-1" {
			t.Fatalf("%+v", page)
		}
	}
}

type memAudit struct {
	rows []storedAudit
}

func (m *memAudit) CreateDocument(_ context.Context, document interface{}) (driver.DocumentMeta, error) {
	raw, err := json.Marshal(document)
	if err != nil {
		return driver.DocumentMeta{}, err
	}
	var row storedAudit
	if err := json.Unmarshal(raw, &row); err != nil {
		return driver.DocumentMeta{}, err
	}
	m.rows = append(m.rows, row)
	return driver.DocumentMeta{Key: row.ID}, nil
}

type auditCursor struct {
	rows      []storedAudit
	device    string
	account   string
	namespace string
	i         int
}

func (c *auditCursor) Close() error  { return nil }
func (c *auditCursor) HasMore() bool { return c.i < len(c.rows) }
func (c *auditCursor) ReadDocument(_ context.Context, result interface{}) (driver.DocumentMeta, error) {
	for c.i < len(c.rows) {
		row := c.rows[c.i]
		c.i++
		if c.device != "" && row.Device != c.device {
			continue
		}
		if c.account != "" && row.Account != c.account {
			continue
		}
		if c.namespace != "" && row.Namespace != c.namespace {
			continue
		}
		raw, err := json.Marshal(row)
		if err != nil {
			return driver.DocumentMeta{}, err
		}
		if err := json.Unmarshal(raw, result); err != nil {
			return driver.DocumentMeta{}, err
		}
		return driver.DocumentMeta{Key: row.ID}, nil
	}
	return driver.DocumentMeta{}, driver.NoMoreDocumentsError{}
}
func TestListAuditByNamespace(t *testing.T) {
	docs := &memAudit{}
	ctrl := &AuditController{
		InfinimeshBaseController: InfinimeshBaseController{log: zap.NewNop()},
		col:                      docs,
		ica:                      fakeICA{ok: true, level: access.Level_READ},
	}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "acc1")
	ctrl.Record(ctx, "ota_assign", "dev1", "acc1", "jti", "plant")
	ctrl.Record(ctx, "alarm_ack", "dev1", "acc1", "jti", "other")

	db := driver_mocks.NewMockDatabase(t)
	ctrl.db = db
	db.On("Query", mock.Anything, mock.MatchedBy(func(s string) bool {
		return strings.Contains(s, "a.namespace")
	}), mock.MatchedBy(func(b map[string]interface{}) bool {
		return b["namespace"] == "plant" && b["device"] == "" && b["account"] == ""
	})).Return(&auditCursor{rows: append([]storedAudit(nil), docs.rows...), namespace: "plant"}, nil)

	page, err := ctrl.List(ctx, connect.NewRequest(&apb.ListAuditRequest{Namespace: "plant"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Msg.GetRecords()) != 1 || page.Msg.Records[0].GetNamespace() != "plant" || page.Msg.Records[0].GetAction() != "ota_assign" {
		t.Fatalf("%+v", page.Msg.GetRecords())
	}

	denied := &AuditController{
		InfinimeshBaseController: InfinimeshBaseController{log: zap.NewNop()},
		ica:                      fakeICA{ok: false},
	}
	if _, err := denied.List(ctx, connect.NewRequest(&apb.ListAuditRequest{Namespace: "plant"})); err == nil {
		t.Fatal("expected permission denied")
	}
}

func (c *auditCursor) RetryReadDocument(ctx context.Context, result interface{}) (driver.DocumentMeta, error) {
	return c.ReadDocument(ctx, result)
}
func (c *auditCursor) Count() int64                       { return int64(len(c.rows)) }
func (c *auditCursor) Statistics() driver.QueryStatistics { return nil }
func (c *auditCursor) Extra() driver.QueryExtra           { return nil }
