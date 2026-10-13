package graph

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/arangodb/go-driver"
	arangohttp "github.com/arangodb/go-driver/http"
	driver_mocks "github.com/infinimesh/core/mocks/github.com/arangodb/go-driver"
	"github.com/infinimesh/core/pkg/graph/schema"
	"github.com/infinimesh/core/pkg/rules"
	inf "github.com/infinimesh/core/pkg/shared"
	"github.com/infinimesh/proto/node/access"
	rpb "github.com/infinimesh/proto/node/rules"
	"github.com/stretchr/testify/mock"
	"go.uber.org/zap"
)

func TestAlarmRaiseAckClearRaiseAgain(t *testing.T) {
	col := newJSONCollection(t, schema.ALARMS_COL)
	ctrl := &RulesController{
		InfinimeshBaseController: InfinimeshBaseController{log: zap.NewNop()},
		alarms:                   col,
		ica:                      fakeICA{ok: true, level: access.Level_ADMIN},
	}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "acc")
	ruleID, dev := "rule-uuid", "dev-1"
	key := AlarmKey(ruleID, dev)
	now := time.Unix(100, 0).UTC()
	raised := AlarmDocument(&rules.Alarm{
		Rule: ruleID, Device: dev, Title: "Hot", State: rules.Raised, Count: 1, RaisedAt: now, FiredAt: now,
	}, "ns")
	if _, err := col.ReplaceDocument(ctx, key, raised); err != nil {
		t.Fatal(err)
	}

	ack, err := ctrl.AckAlarm(ctx, connect.NewRequest(&rpb.AlarmRef{Rule: ruleID, Device: dev}))
	if err != nil {
		t.Fatal(err)
	}
	if ack.Msg.GetState() != rpb.AlarmState_ACKED || ack.Msg.GetTitle() != "Hot" || ack.Msg.GetRule() != ruleID {
		t.Fatalf("%+v", ack.Msg)
	}
	cleared, err := ctrl.ClearAlarm(ctx, connect.NewRequest(&rpb.AlarmRef{Rule: ruleID, Device: dev}))
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Msg.GetState() != rpb.AlarmState_CLEARED || cleared.Msg.GetClearedAt() == nil {
		t.Fatalf("%+v", cleared.Msg)
	}

	var saved StoredAlarm
	if _, err := col.ReadDocument(ctx, key, &saved); err != nil {
		t.Fatal(err)
	}
	store, err := rules.OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	store.Replace([]*rules.Alarm{saved.Alarm()})
	ev, err := store.Step(&rules.Rule{ID: ruleID, Name: "Hot"}, dev, true, true, false, true, now.Add(time.Minute))
	if err != nil || ev == nil || ev.Alarm.Count != 2 || ev.Alarm.State != rules.Raised || ev.Alarm.Rule != ruleID || ev.Alarm.Title != "Hot" {
		t.Fatalf("%+v %v", ev, err)
	}
	if _, err := col.ReplaceDocument(ctx, key, AlarmDocument(&ev.Alarm, "ns")); err != nil {
		t.Fatal(err)
	}
	again, err := ctrl.AckAlarm(ctx, connect.NewRequest(&rpb.AlarmRef{Rule: ruleID, Device: dev}))
	if err != nil {
		t.Fatal(err)
	}
	if again.Msg.GetState() != rpb.AlarmState_ACKED || again.Msg.GetCount() != 2 || again.Msg.GetAckedAt() == nil {
		t.Fatalf("%+v", again.Msg)
	}
}

func TestLegacyProtoAlarmCanClear(t *testing.T) {
	col := newJSONCollection(t, schema.ALARMS_COL)
	ctrl := &RulesController{
		InfinimeshBaseController: InfinimeshBaseController{log: zap.NewNop()},
		alarms:                   col,
		ica:                      fakeICA{ok: true, level: access.Level_ADMIN},
	}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "acc")
	key := AlarmKey("hot", "dev-1")
	legacy := struct {
		Rule      string         `json:"rule"`
		Device    string         `json:"device"`
		Title     string         `json:"title"`
		State     int            `json:"state"`
		AckedAt   map[string]int `json:"acked_at"`
		Namespace string         `json:"namespace"`
		Count     int            `json:"count"`
	}{
		Rule: "hot", Device: "dev-1", Title: "hot", State: int(rpb.AlarmState_ACKED),
		AckedAt: map[string]int{"seconds": 30}, Namespace: "ns", Count: 1,
	}
	if _, err := col.ReplaceDocument(ctx, key, legacy); err != nil {
		t.Fatal(err)
	}
	cleared, err := ctrl.ClearAlarm(ctx, connect.NewRequest(&rpb.AlarmRef{Rule: "hot", Device: "dev-1"}))
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Msg.GetState() != rpb.AlarmState_CLEARED || cleared.Msg.GetAckedAt().AsTime().Unix() != 30 {
		t.Fatalf("%+v", cleared.Msg)
	}
}

func TestDeleteRuleRemovesAlarms(t *testing.T) {
	db := driver_mocks.NewMockDatabase(t)
	col := driver_mocks.NewMockCollection(t)
	id := "rule-uuid"
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "acc")
	col.On("ReadDocument", mock.Anything, id, mock.Anything).Run(func(args mock.Arguments) {
		rule := args.Get(2).(*rpb.Rule)
		rule.Name = "Hot"
		rule.Namespace = "ns"
	}).Return(driver.DocumentMeta{Key: id}, nil)
	col.On("RemoveDocument", mock.Anything, id).Return(driver.DocumentMeta{}, nil)
	db.On("Query", mock.Anything, mock.MatchedBy(func(q string) bool {
		return strings.Contains(q, "REMOVE a IN") && strings.Contains(q, "a.rule")
	}), mock.MatchedBy(func(b map[string]interface{}) bool {
		return b["rule"] == id && b["name"] == "Hot" && b["@col"] == schema.ALARMS_COL
	})).Return(doneCursor{}, nil).Once()
	ctrl := &RulesController{
		InfinimeshBaseController: InfinimeshBaseController{log: zap.NewNop(), db: db},
		col:                      col,
		ica:                      fakeICA{ok: true, level: access.Level_ADMIN},
	}
	if _, err := ctrl.Delete(ctx, connect.NewRequest(&rpb.Rule{Uuid: id})); err != nil {
		t.Fatal(err)
	}
}

type doneCursor struct{}

func (doneCursor) Close() error  { return nil }
func (doneCursor) HasMore() bool { return false }
func (doneCursor) ReadDocument(context.Context, interface{}) (driver.DocumentMeta, error) {
	return driver.DocumentMeta{}, driver.NoMoreDocumentsError{}
}
func (doneCursor) RetryReadDocument(context.Context, interface{}) (driver.DocumentMeta, error) {
	return driver.DocumentMeta{}, driver.NoMoreDocumentsError{}
}
func (doneCursor) Count() int64                       { return 0 }
func (doneCursor) Statistics() driver.QueryStatistics { return nil }
func (doneCursor) Extra() driver.QueryExtra           { return nil }

func newJSONCollection(t *testing.T, name string) driver.Collection {
	t.Helper()
	docs := map[string][]byte{}
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := path.Base(r.URL.Path)
		switch r.Method {
		case http.MethodGet:
			mu.Lock()
			body, ok := docs[key]
			mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			docs[key] = body
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"_key":%q,"_id":%q,"_rev":"1"}`, key, name+"/"+key)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	conn, err := arangohttp.NewConnection(arangohttp.ConnectionConfig{Endpoints: []string{srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	client, err := driver.NewClient(driver.ClientConfig{Connection: conn})
	if err != nil {
		t.Fatal(err)
	}
	db, err := client.Database(driver.WithSkipExistCheck(context.Background(), true), "infinimesh")
	if err != nil {
		t.Fatal(err)
	}
	col, err := db.Collection(driver.WithSkipExistCheck(context.Background(), true), name)
	if err != nil {
		t.Fatal(err)
	}
	return col
}
