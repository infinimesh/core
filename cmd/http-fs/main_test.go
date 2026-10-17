package main

import (
	"context"
	"testing"

	"github.com/arangodb/go-driver"
	"github.com/go-redis/redis/v8"
	"github.com/infinimesh/core/v3/pkg/graph"
	"github.com/infinimesh/proto/node/access"
	"go.uber.org/zap"
)

type stubRepo struct {
	level   access.Level
	account string
	ns      string
}

func (s *stubRepo) GetEdgeCol(context.Context, string) driver.Collection { return nil }
func (s *stubRepo) CheckLink(context.Context, driver.Collection, graph.InfinimeshGraphNode, graph.InfinimeshGraphNode) bool {
	return false
}
func (s *stubRepo) Link(context.Context, *zap.Logger, driver.Collection, graph.InfinimeshGraphNode, graph.InfinimeshGraphNode, access.Level, access.Role) error {
	return nil
}
func (s *stubRepo) LinkUntil(context.Context, *zap.Logger, driver.Collection, graph.InfinimeshGraphNode, graph.InfinimeshGraphNode, access.Level, access.Role, int64) error {
	return nil
}
func (s *stubRepo) Move(context.Context, graph.InfinimeshController, graph.InfinimeshGraphNode, driver.Collection, string) error {
	return nil
}
func (s *stubRepo) AccessLevel(_ context.Context, requestor graph.InfinimeshGraphNode, node graph.InfinimeshGraphNode) (bool, access.Level) {
	s.account = requestor.GetUuid()
	s.ns = node.GetUuid()
	return true, s.level
}
func (s *stubRepo) AccessLevelAndGet(context.Context, *zap.Logger, *graph.Account, graph.InfinimeshGraphNode) error {
	return nil
}
func (s *stubRepo) ListQuery(context.Context, *zap.Logger, graph.InfinimeshGraphNode, string) (driver.Cursor, error) {
	return nil, nil
}
func (s *stubRepo) ListOwnedDeep(context.Context, *zap.Logger, graph.InfinimeshGraphNode) (*access.Nodes, error) {
	return nil, nil
}
func (s *stubRepo) DeleteRecursive(context.Context, *zap.Logger, graph.InfinimeshGraphNode) error {
	return nil
}
func (s *stubRepo) Toggle(context.Context, graph.InfinimeshGraphNode, string) error { return nil }
func (s *stubRepo) EnsureRootExists(*zap.Logger, *redis.Client, string) error       { return nil }

func TestGraphAccessLevel(t *testing.T) {
	repo := &stubRepo{level: access.Level_ADMIN}
	level, err := (graphAccess{repo: repo}).Level(context.Background(), "acct", "plant")
	if err != nil {
		t.Fatal(err)
	}
	if level != access.Level_ADMIN || repo.account != "acct" || repo.ns != "plant" {
		t.Fatalf("%v %s %s", level, repo.account, repo.ns)
	}
}
