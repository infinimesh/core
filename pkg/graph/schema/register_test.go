package schema

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	driver "github.com/arangodb/go-driver"
	driver_mocks "github.com/infinimesh/core/v3/mocks/github.com/arangodb/go-driver"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func fatalLog() *zap.Logger {
	return zap.New(zapcore.NewNopCore(), zap.WithFatalHook(zapcore.WriteThenPanic))
}

type stubGraph struct {
	col       driver.Collection
	exists    func(string) (bool, error)
	createErr error
	created   []string
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

func (g stubGraph) EdgeCollection(context.Context, string) (driver.Collection, driver.VertexConstraints, error) {
	return g.col, driver.VertexConstraints{}, nil
}
func (g stubGraph) EdgeCollectionExists(_ context.Context, name string) (bool, error) {
	if g.exists != nil {
		return g.exists(name)
	}
	return false, nil
}
func (stubGraph) EdgeCollections(context.Context) ([]driver.Collection, []driver.VertexConstraints, error) {
	return nil, nil, nil
}
func (g *stubGraph) CreateEdgeCollection(_ context.Context, name string, _ driver.VertexConstraints) (driver.Collection, error) {
	g.created = append(g.created, name)
	if g.createErr != nil {
		return nil, g.createErr
	}
	return g.col, nil
}
func (g *stubGraph) CreateEdgeCollectionWithOptions(ctx context.Context, name string, c driver.VertexConstraints, _ driver.CreateEdgeCollectionOptions) (driver.Collection, error) {
	return g.CreateEdgeCollection(ctx, name, c)
}
func (stubGraph) SetVertexConstraints(context.Context, string, driver.VertexConstraints) error {
	return nil
}
func (g stubGraph) VertexCollection(context.Context, string) (driver.Collection, error) {
	return g.col, nil
}
func (stubGraph) VertexCollectionExists(context.Context, string) (bool, error) {
	return true, nil
}
func (stubGraph) VertexCollections(context.Context) ([]driver.Collection, error) {
	return nil, nil
}
func (g stubGraph) CreateVertexCollection(context.Context, string) (driver.Collection, error) {
	return g.col, nil
}
func (g stubGraph) CreateVertexCollectionWithOptions(context.Context, string, driver.CreateVertexCollectionOptions) (driver.Collection, error) {
	return g.col, nil
}

func TestCheckAndRegisterCollections(t *testing.T) {
	db := driver_mocks.NewMockDatabase(t)
	col := driver_mocks.NewMockCollection(t)
	db.On("CollectionExists", mock.Anything, "A").Return(true, nil)
	db.On("CollectionExists", mock.Anything, "B").Return(false, nil)
	db.On("CreateCollection", mock.Anything, "B", mock.Anything).Return(col, nil)
	CheckAndRegisterCollections(zap.NewNop(), db, []string{"A", "B"})

	bad := driver_mocks.NewMockDatabase(t)
	bad.On("CollectionExists", mock.Anything, "A").Return(false, errors.New("down"))
	require.Panics(t, func() { CheckAndRegisterCollections(fatalLog(), bad, []string{"A"}) })

	createFail := driver_mocks.NewMockDatabase(t)
	createFail.On("CollectionExists", mock.Anything, "A").Return(false, nil)
	createFail.On("CreateCollection", mock.Anything, "A", mock.Anything).Return(nil, errors.New("no"))
	require.Panics(t, func() { CheckAndRegisterCollections(fatalLog(), createFail, []string{"A"}) })
}

func TestCheckAndRegisterGraph(t *testing.T) {
	def := InfinimeshGraphSchema{Name: "Permissions", Edges: [][]string{{"Accounts", "Namespaces"}, {"Accounts", "Devices"}}}

	missing := driver_mocks.NewMockDatabase(t)
	missing.On("GraphExists", mock.Anything, def.Name).Return(false, errors.New("down"))
	require.Panics(t, func() { CheckAndRegisterGraph(fatalLog(), missing, def) })

	g := &stubGraph{exists: func(name string) (bool, error) {
		if name == "Accounts2Namespaces" {
			return false, nil
		}
		return false, errors.New("missing")
	}}
	exists := driver_mocks.NewMockDatabase(t)
	exists.On("GraphExists", mock.Anything, def.Name).Return(true, nil)
	exists.On("Graph", mock.Anything, def.Name).Return(g, nil)
	CheckAndRegisterGraph(zap.NewNop(), exists, def)
	require.Equal(t, []string{"Accounts2Namespaces", "Accounts2Devices"}, g.created)

	skip := &stubGraph{exists: func(string) (bool, error) { return true, nil }}
	okDB := driver_mocks.NewMockDatabase(t)
	okDB.On("GraphExists", mock.Anything, def.Name).Return(true, nil)
	okDB.On("Graph", mock.Anything, def.Name).Return(skip, nil)
	CheckAndRegisterGraph(zap.NewNop(), okDB, def)
	require.Empty(t, skip.created)

	boom := &stubGraph{createErr: errors.New("edge"), exists: func(string) (bool, error) { return false, nil }}
	edgeFail := driver_mocks.NewMockDatabase(t)
	edgeFail.On("GraphExists", mock.Anything, "G").Return(true, nil)
	edgeFail.On("Graph", mock.Anything, "G").Return(boom, nil)
	require.Panics(t, func() {
		CheckAndRegisterGraph(fatalLog(), edgeFail, InfinimeshGraphSchema{Name: "G", Edges: [][]string{{"A", "B"}}})
	})

	fresh := driver_mocks.NewMockDatabase(t)
	fresh.On("GraphExists", mock.Anything, "New").Return(false, nil)
	fresh.On("CreateGraph", mock.Anything, "New", mock.Anything).Return(skip, nil)
	CheckAndRegisterGraph(zap.NewNop(), fresh, InfinimeshGraphSchema{Name: "New", Edges: [][]string{{"A", "B"}}})

	freshFail := driver_mocks.NewMockDatabase(t)
	freshFail.On("GraphExists", mock.Anything, "New").Return(false, nil)
	freshFail.On("CreateGraph", mock.Anything, "New", mock.Anything).Return(nil, errors.New("graph"))
	require.Panics(t, func() {
		CheckAndRegisterGraph(fatalLog(), freshFail, InfinimeshGraphSchema{Name: "New", Edges: [][]string{{"A", "B"}}})
	})
}

func TestInitDBRejectsBadEndpoint(t *testing.T) {
	require.Panics(t, func() {
		InitDB(fatalLog(), "%", "not a user", "pw", false)
	})
	require.Panics(t, func() {
		InitDB(fatalLog(), "localhost:1", "%zz", "pw", true)
	})
}

func arangoHost(t *testing.T, h http.Handler) (string, string) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	return u.Host, "root:pw"
}

func arangoErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(`{"error":true,"code":` + itoa(code) + `,"errorNum":1,"errorMessage":"` + msg + `"}`))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [4]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func TestInitDBHTTPPaths(t *testing.T) {
	host, cred := arangoHost(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arangoErr(w, 500, "down")
	}))
	require.Panics(t, func() { InitDB(fatalLog(), host, cred, "pw", false) })

	host, cred = arangoHost(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	require.Nil(t, InitDB(zap.NewNop(), host, cred, "pw", true))

	var seen atomic.Int32
	host, cred = arangoHost(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/_api/database") {
			arangoErr(w, 500, "create")
			return
		}
		arangoErr(w, 404, "missing")
	}))
	require.Panics(t, func() { InitDB(fatalLog(), host, cred, "pw", false) })

	host, cred = arangoHost(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/_api/database"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		case strings.Contains(r.URL.Path, "/_api/gharial/"):
			arangoErr(w, 500, "graph")
		case strings.Contains(r.URL.Path, "/_api/collection/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			n := seen.Add(1)
			if n == 1 {
				arangoErr(w, 404, "missing")
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	require.Panics(t, func() { InitDB(fatalLog(), host, cred, "pw", false) })

	graphBody := []byte(`{"graph":{"name":"Permissions","edgeDefinitions":[` +
		`{"collection":"Accounts2Namespaces","from":["Accounts"],"to":["Namespaces"]},` +
		`{"collection":"Accounts2Devices","from":["Accounts"],"to":["Devices"]},` +
		`{"collection":"Namespaces2Accounts","from":["Namespaces"],"to":["Accounts"]},` +
		`{"collection":"Namespaces2Devices","from":["Namespaces"],"to":["Devices"]},` +
		`{"collection":"Namespaces2Plugins","from":["Namespaces"],"to":["Plugins"]},` +
		`{"collection":"Namespaces2Rules","from":["Namespaces"],"to":["Rules"]},` +
		`{"collection":"Accounts2Credentials","from":["Accounts"],"to":["Credentials"]}` +
		`]}}`)
	var current atomic.Int32
	host, cred = arangoHost(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/_api/database"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		case strings.Contains(r.URL.Path, "/_api/gharial/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(graphBody)
		case strings.Contains(r.URL.Path, "/_api/collection/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			if current.Add(1) == 1 {
				arangoErr(w, 404, "missing")
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	require.NotNil(t, InitDB(zap.NewNop(), host, cred, "pw", false))
}
