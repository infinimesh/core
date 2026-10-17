package graph

import (
	"context"

	"github.com/arangodb/go-driver"
	"github.com/go-redis/redis/v8"
	"github.com/infinimesh/proto/node/access"
	accpb "github.com/infinimesh/proto/node/accounts"
	devpb "github.com/infinimesh/proto/node/devices"
	nspb "github.com/infinimesh/proto/node/namespaces"
	"go.uber.org/zap"
)

type scriptCursor struct {
	fn []func(any) (driver.DocumentMeta, error)
	i  int
}

func (c *scriptCursor) Close() error  { return nil }
func (c *scriptCursor) HasMore() bool { return c != nil && c.i < len(c.fn) }
func (c *scriptCursor) ReadDocument(_ context.Context, result any) (driver.DocumentMeta, error) {
	if c == nil || c.i >= len(c.fn) {
		return driver.DocumentMeta{}, driver.NoMoreDocumentsError{}
	}
	fn := c.fn[c.i]
	c.i++
	if fn == nil {
		return driver.DocumentMeta{Key: "k", ID: driver.NewDocumentID("C", "k")}, nil
	}
	return fn(result)
}
func (c *scriptCursor) RetryReadDocument(ctx context.Context, result any) (driver.DocumentMeta, error) {
	return c.ReadDocument(ctx, result)
}
func (c *scriptCursor) Count() int64                       { return 0 }
func (c *scriptCursor) Statistics() driver.QueryStatistics { return nil }
func (c *scriptCursor) Extra() driver.QueryExtra           { return nil }

func docMeta(col, key string) driver.DocumentMeta {
	return driver.DocumentMeta{Key: key, ID: driver.NewDocumentID(col, key)}
}

func oneRead(fill func(any)) *scriptCursor {
	return &scriptCursor{fn: []func(any) (driver.DocumentMeta, error){
		func(result any) (driver.DocumentMeta, error) {
			if fill != nil {
				fill(result)
			}
			return docMeta("C", "k"), nil
		},
	}}
}

func readErr(err error) *scriptCursor {
	return &scriptCursor{fn: []func(any) (driver.DocumentMeta, error){
		func(any) (driver.DocumentMeta, error) { return driver.DocumentMeta{}, err },
	}}
}

type stubGraph struct {
	col  driver.Collection
	vert func(string) driver.Collection
	edge func(string) driver.Collection
}

func (stubGraph) Name() string                 { return "g" }
func (stubGraph) Remove(context.Context) error { return nil }
func (stubGraph) RemoveWithOpts(context.Context, *driver.RemoveGraphOptions) error {
	return nil
}
func (stubGraph) IsSmart() bool          { return false }
func (stubGraph) IsSatellite() bool      { return false }
func (stubGraph) IsDisjoint() bool       { return false }
func (stubGraph) ID() string             { return "id" }
func (stubGraph) Key() driver.DocumentID { return "" }
func (stubGraph) Rev() string            { return "" }
func (stubGraph) EdgeDefinitions() []driver.EdgeDefinition {
	return nil
}
func (stubGraph) SmartGraphAttribute() string { return "" }
func (stubGraph) MinReplicationFactor() int   { return 0 }
func (stubGraph) NumberOfShards() int         { return 0 }
func (stubGraph) OrphanCollections() []string { return nil }
func (stubGraph) ReplicationFactor() int      { return 0 }
func (stubGraph) WriteConcern() int           { return 0 }
func (g stubGraph) EdgeCollection(_ context.Context, name string) (driver.Collection, driver.VertexConstraints, error) {
	if g.edge != nil {
		return g.edge(name), driver.VertexConstraints{}, nil
	}
	return g.col, driver.VertexConstraints{}, nil
}
func (stubGraph) EdgeCollectionExists(context.Context, string) (bool, error) {
	return true, nil
}
func (stubGraph) EdgeCollections(context.Context) ([]driver.Collection, []driver.VertexConstraints, error) {
	return nil, nil, nil
}
func (g stubGraph) CreateEdgeCollection(context.Context, string, driver.VertexConstraints) (driver.Collection, error) {
	return g.col, nil
}
func (g stubGraph) CreateEdgeCollectionWithOptions(context.Context, string, driver.VertexConstraints, driver.CreateEdgeCollectionOptions) (driver.Collection, error) {
	return g.col, nil
}
func (stubGraph) SetVertexConstraints(context.Context, string, driver.VertexConstraints) error {
	return nil
}
func (g stubGraph) VertexCollection(_ context.Context, name string) (driver.Collection, error) {
	if g.vert != nil {
		return g.vert(name), nil
	}
	return g.col, nil
}
func (stubGraph) VertexCollectionExists(context.Context, string) (bool, error) { return true, nil }
func (stubGraph) VertexCollections(context.Context) ([]driver.Collection, error) {
	return nil, nil
}
func (g stubGraph) CreateVertexCollection(context.Context, string) (driver.Collection, error) {
	return g.col, nil
}
func (g stubGraph) CreateVertexCollectionWithOptions(context.Context, string, driver.CreateVertexCollectionOptions) (driver.Collection, error) {
	return g.col, nil
}

type flexRepo struct {
	level        access.Level
	role         access.Role
	ok           bool
	ns           string
	enabled      *bool
	getErr       error
	list         driver.Cursor
	listErr      error
	deep         *access.Nodes
	deepErr      error
	delErr       error
	toggleErr    error
	linkErr      error
	linkUntilErr error
	moveErr      error
	edge         driver.Collection
	accessFn     func(InfinimeshGraphNode)
}

func (f flexRepo) GetEdgeCol(context.Context, string) driver.Collection { return f.edge }
func (f flexRepo) CheckLink(context.Context, driver.Collection, InfinimeshGraphNode, InfinimeshGraphNode) bool {
	return false
}
func (f flexRepo) Link(context.Context, *zap.Logger, driver.Collection, InfinimeshGraphNode, InfinimeshGraphNode, access.Level, access.Role) error {
	return f.linkErr
}
func (f flexRepo) LinkUntil(context.Context, *zap.Logger, driver.Collection, InfinimeshGraphNode, InfinimeshGraphNode, access.Level, access.Role, int64) error {
	return f.linkUntilErr
}
func (f flexRepo) Move(context.Context, InfinimeshController, InfinimeshGraphNode, driver.Collection, string) error {
	return f.moveErr
}
func (f flexRepo) AccessLevel(context.Context, InfinimeshGraphNode, InfinimeshGraphNode) (bool, access.Level) {
	return f.ok, f.level
}
func (f flexRepo) AccessLevelAndGet(_ context.Context, _ *zap.Logger, _ *Account, node InfinimeshGraphNode) error {
	if f.getErr != nil {
		return f.getErr
	}
	if f.accessFn != nil {
		f.accessFn(node)
		return nil
	}
	ns := f.ns
	var nsp *string
	if ns != "" {
		nsp = &ns
	}
	acc := &access.Access{Level: f.level, Role: f.role, Namespace: nsp}
	switch n := node.(type) {
	case *Account:
		n.Access = acc
		if f.enabled != nil {
			n.Enabled = *f.enabled
		} else {
			n.Enabled = true
		}
	case *Namespace:
		n.Access = acc
	case *Device:
		n.Access = acc
	case *Plugin:
		n.SetAccessLevel(f.level)
	}
	return nil
}
func (f flexRepo) ListQuery(context.Context, *zap.Logger, InfinimeshGraphNode, string) (driver.Cursor, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.list == nil {
		return &scriptCursor{}, nil
	}
	return f.list, nil
}
func (f flexRepo) ListOwnedDeep(context.Context, *zap.Logger, InfinimeshGraphNode) (*access.Nodes, error) {
	if f.deep == nil && f.deepErr == nil {
		return &access.Nodes{}, nil
	}
	return f.deep, f.deepErr
}
func (f flexRepo) DeleteRecursive(context.Context, *zap.Logger, InfinimeshGraphNode) error {
	return f.delErr
}
func (f flexRepo) Toggle(context.Context, InfinimeshGraphNode, string) error { return f.toggleErr }
func (f flexRepo) EnsureRootExists(*zap.Logger, *redis.Client, string) error { return nil }

func paintAccess(result any, level access.Level, role access.Role, ns *string) {
	p, ok := result.(*InfinimeshGraphNode)
	if !ok || p == nil || *p == nil {
		return
	}
	acc := &access.Access{Level: level, Role: role, Namespace: ns}
	switch n := (*p).(type) {
	case *Account:
		if n.Account == nil {
			n.Account = &accpb.Account{}
		}
		n.Access = acc
		n.Enabled = true
	case *Namespace:
		if n.Namespace == nil {
			n.Namespace = &nspb.Namespace{}
		}
		n.Access = acc
	case *Device:
		if n.Device == nil {
			n.Device = &devpb.Device{}
		}
		n.Access = acc
	}
}
