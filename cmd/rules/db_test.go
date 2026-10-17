package main

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/arangodb/go-driver"
	"github.com/go-redis/redis/v8"
	driver_mocks "github.com/infinimesh/core/v3/mocks/github.com/arangodb/go-driver"
	"github.com/infinimesh/core/v3/pkg/graph"
	"github.com/infinimesh/core/v3/pkg/graph/schema"
	"github.com/infinimesh/core/v3/pkg/rules"
	rpb "github.com/infinimesh/proto/node/rules"
	pb "github.com/infinimesh/proto/shadow"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

type seqCursor struct {
	rows     []any
	i        int
	readErr  error
	errAt    int
	closeErr error
}

func (c *seqCursor) Close() error  { return c.closeErr }
func (c *seqCursor) HasMore() bool { return c.i < len(c.rows) }
func (c *seqCursor) ReadDocument(_ context.Context, result interface{}) (driver.DocumentMeta, error) {
	if c.readErr != nil && c.i == c.errAt {
		return driver.DocumentMeta{}, c.readErr
	}
	if c.i >= len(c.rows) {
		return driver.DocumentMeta{}, driver.NoMoreDocumentsError{}
	}
	switch dst := result.(type) {
	case *string:
		*dst = c.rows[c.i].(string)
	case *rpb.Rule:
		proto.Reset(dst)
		proto.Merge(dst, c.rows[c.i].(*rpb.Rule))
	case *graph.StoredAlarm:
		*dst = c.rows[c.i].(graph.StoredAlarm)
	case *graph.NamespaceDevice:
		*dst = c.rows[c.i].(graph.NamespaceDevice)
	default:
		return driver.DocumentMeta{}, errors.New("type")
	}
	c.i++
	return driver.DocumentMeta{}, nil
}
func (c *seqCursor) RetryReadDocument(ctx context.Context, result interface{}) (driver.DocumentMeta, error) {
	return c.ReadDocument(ctx, result)
}
func (c *seqCursor) Count() int64                       { return int64(len(c.rows)) }
func (c *seqCursor) Statistics() driver.QueryStatistics { return nil }
func (c *seqCursor) Extra() driver.QueryExtra           { return nil }

func TestNamespacesRulesAndAlarms(t *testing.T) {
	ctx := context.Background()
	db := driver_mocks.NewMockDatabase(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("query")).Once()
	if _, err := namespaces(ctx, db); err == nil {
		t.Fatal("query")
	}
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&seqCursor{readErr: errors.New("read"), errAt: 0}, nil).Once()
	if _, err := namespaces(ctx, db); err == nil {
		t.Fatal("read")
	}
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&seqCursor{rows: []any{"plant", ""}}, nil).Once()
	got, err := namespaces(ctx, db)
	if err != nil || len(got) != 1 || got[0] != "plant" {
		t.Fatalf("%v %v", got, err)
	}

	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("query")).Once()
	if _, err := loadRules(ctx, db, "plant"); err == nil {
		t.Fatal("rules query")
	}
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&seqCursor{readErr: errors.New("read"), errAt: 0}, nil).Once()
	if _, err := loadRules(ctx, db, "plant"); err == nil {
		t.Fatal("rules read")
	}
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&seqCursor{rows: []any{
		&rpb.Rule{Name: "bad", When: "1+2"},
		&rpb.Rule{Uuid: "id", Name: "hot", When: "true"},
	}}, nil).Once()
	rulesGot, err := loadRules(ctx, db, "plant")
	if err != nil || len(rulesGot) != 1 || rulesGot[0].ID != "id" {
		t.Fatalf("%v %v", rulesGot, err)
	}

	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("query")).Once()
	if _, err := loadAlarms(ctx, db, "plant"); err == nil {
		t.Fatal("alarms query")
	}
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&seqCursor{readErr: errors.New("read"), errAt: 0}, nil).Once()
	if _, err := loadAlarms(ctx, db, "plant"); err == nil {
		t.Fatal("alarms read")
	}
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&seqCursor{rows: []any{
		graph.AlarmDocument(&rules.Alarm{Rule: "id", Device: "dev", State: rules.Raised, Count: 2}, "plant"),
	}}, nil).Once()
	alarms, err := loadAlarms(ctx, db, "plant")
	if err != nil || len(alarms) != 1 || alarms[0].Rule != "id" || alarms[0].Count != 2 {
		t.Fatalf("%v %v", alarms, err)
	}
}

func TestSaveAlarmsAndPatch(t *testing.T) {
	ctx := context.Background()
	db := driver_mocks.NewMockDatabase(t)
	db.On("Collection", mock.Anything, schema.ALARMS_COL).Return(nil, errors.New("col")).Once()
	if err := saveAlarms(ctx, db, nil, "plant", nil); err == nil {
		t.Fatal("collection")
	}

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	col := driver_mocks.NewMockCollection(t)
	db.On("Collection", mock.Anything, schema.ALARMS_COL).Return(col, nil)
	col.On("ReplaceDocument", mock.Anything, "hot_dev", mock.Anything).Return(driver.DocumentMeta{}, errors.New("missing")).Once()
	col.On("CreateDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("create")).Once()
	if err := saveAlarms(ctx, db, rdb, "plant", []*rules.Alarm{{Rule: "hot", Device: "dev"}}); err == nil {
		t.Fatal("create")
	}

	col.On("ReplaceDocument", mock.Anything, "hot_dev", mock.Anything).Return(driver.DocumentMeta{}, nil).Once()
	db.On("Query", mock.Anything, mock.MatchedBy(func(q string) bool { return true }), mock.Anything).Return(&seqCursor{}, nil).Once()
	list := []*rules.Alarm{{Rule: "hot", Device: "dev", Title: "Hot", State: rules.Raised}}
	if err := saveAlarms(ctx, db, rdb, "plant", list); err != nil {
		t.Fatal(err)
	}
	if !mr.Exists("rules:alarm:plant:hot:dev") {
		t.Fatal("redis")
	}

	col.On("ReplaceDocument", mock.Anything, "id_dev", mock.Anything).Return(driver.DocumentMeta{}, nil).Once()
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("remove")).Once()
	if err := saveAlarms(ctx, db, rdb, "plant", []*rules.Alarm{{Rule: "id", Device: "dev", Title: "hot"}}); err == nil {
		t.Fatal("remove")
	}

	db.On("Query", mock.Anything, mock.MatchedBy(func(q string) bool { return len(q) > 0 }), mock.MatchedBy(func(b map[string]interface{}) bool {
		kept, _ := b["rules"].([]string)
		return len(kept) == 1 && kept[0] == "a"
	})).Return(&seqCursor{closeErr: errors.New("close")}, nil).Once()
	if err := removeOtherAlarms(ctx, db, "plant", []*rules.Alarm{{Rule: "a"}, {Rule: "a"}, nil}); err == nil {
		t.Fatal("close")
	}
	db.On("Query", mock.Anything, mock.MatchedBy(func(q string) bool { return len(q) > 0 }), mock.MatchedBy(func(b map[string]interface{}) bool {
		_, ok := b["rules"]
		return !ok
	})).Return(&seqCursor{}, nil).Once()
	if err := removeOtherAlarms(ctx, db, "plant", nil); err != nil {
		t.Fatal(err)
	}

	mr.Close()
	col.On("ReplaceDocument", mock.Anything, "hot_dev", mock.Anything).Return(driver.DocumentMeta{}, nil).Once()
	if err := saveAlarms(ctx, db, rdb, "plant", []*rules.Alarm{{Rule: "hot", Device: "dev", Title: "hot"}}); err == nil {
		t.Fatal("redis down")
	}

	cl := &patchClient{err: errors.New("patch")}
	if err := (&desiredPatch{client: cl}).PatchDesired("dev", map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatal("struct")
	}
	if err := (&desiredPatch{client: cl}).PatchDesired("dev", map[string]any{"fan": "on"}); err == nil || cl.got.GetDevice() != "dev" {
		t.Fatal("patch")
	}
	cl.err = nil
	if err := (&desiredPatch{client: cl}).PatchDesired("dev", map[string]any{"fan": "off"}); err != nil || cl.got.GetDesired().GetData().AsMap()["fan"] != "off" {
		t.Fatal(err)
	}
}

type patchClient struct {
	pb.ShadowServiceClient
	err error
	got *pb.Shadow
}

func (p *patchClient) Patch(_ context.Context, in *pb.Shadow, _ ...grpc.CallOption) (*pb.Shadow, error) {
	p.got = in
	if p.err != nil {
		return nil, p.err
	}
	return &pb.Shadow{}, nil
}

func TestDBSource(t *testing.T) {
	ctx := context.Background()
	db := driver_mocks.NewMockDatabase(t)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	src := dbSource{db: db, rdb: rdb}
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&seqCursor{rows: []any{"plant"}}, nil).Once()
	names, err := src.Namespaces(ctx)
	if err != nil || len(names) != 1 {
		t.Fatal(err)
	}
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&seqCursor{rows: []any{&rpb.Rule{Name: "hot", When: "true"}}}, nil).Once()
	got, err := src.Rules(ctx, "plant")
	if err != nil || len(got) != 1 {
		t.Fatal(err)
	}
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("devices")).Once()
	if _, err := src.Devices(ctx, "plant"); err == nil {
		t.Fatal("devices")
	}
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&seqCursor{rows: []any{
		graph.NamespaceDevice{Device: "dev", Title: "Pump", Tags: []string{"water"}},
		graph.NamespaceDevice{},
	}}, nil).Once()
	devs, err := src.Devices(ctx, "plant")
	if err != nil || len(devs) != 1 || devs[0].GetNamespace() != "plant" || devs[0].GetTitle() != "Pump" || len(devs[0].GetTags()) != 1 {
		t.Fatalf("%v %v", devs, err)
	}
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&seqCursor{rows: []any{
		graph.AlarmDocument(&rules.Alarm{Rule: "hot", Device: "dev", State: rules.Raised}, "plant"),
	}}, nil).Once()
	alarms, err := src.LoadAlarms(ctx, "plant")
	if err != nil || len(alarms) != 1 {
		t.Fatal(err)
	}
	col := driver_mocks.NewMockCollection(t)
	db.On("Collection", mock.Anything, schema.ALARMS_COL).Return(col, nil)
	col.On("ReplaceDocument", mock.Anything, mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, nil)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&seqCursor{}, nil).Once()
	if err := src.SaveAlarms(ctx, "plant", []*rules.Alarm{{Rule: "hot", Device: "dev"}}); err != nil {
		t.Fatal(err)
	}
}
