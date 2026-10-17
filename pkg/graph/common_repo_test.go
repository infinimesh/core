package graph

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arangodb/go-driver"
	driver_mocks "github.com/infinimesh/core/v3/mocks/github.com/arangodb/go-driver"
	"github.com/infinimesh/core/v3/pkg/credentials"
	"github.com/infinimesh/core/v3/pkg/graph/schema"
	inf "github.com/infinimesh/core/v3/pkg/shared"
	"github.com/infinimesh/proto/node/access"
	accpb "github.com/infinimesh/proto/node/accounts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

func TestBaseControllerAccessorsAndBlankDocument(t *testing.T) {
	db := driver_mocks.NewMockDatabase(t)
	c := &InfinimeshBaseController{log: zap.NewNop(), db: db}
	assert.Equal(t, db, c._DB())
	assert.NotNil(t, c._log())
	meta := NewBlankDocument(schema.ACCOUNTS_COL, "k")
	assert.Equal(t, "k", meta.Key)
	col, key := SplitDocID("Accounts/k")
	assert.Equal(t, "Accounts", col)
	assert.Equal(t, "k", key)
	col, key = SplitDocID("nonsplit")
	assert.Empty(t, col)
	assert.Empty(t, key)
	repo := NewInfinimeshCommonActionsRepo(db)
	require.NotNil(t, repo)
}

func TestGetEdgeColAndCheckLink(t *testing.T) {
	db := driver_mocks.NewMockDatabase(t)
	edge := driver_mocks.NewMockCollection(t)
	db.On("Graph", mock.Anything, schema.PERMISSIONS_GRAPH.Name).Return(stubGraph{col: edge}, nil)
	repo := NewInfinimeshCommonActionsRepo(db)
	assert.Equal(t, edge, repo.GetEdgeCol(context.Background(), schema.ACC2NS))

	edge.On("DocumentExists", mock.Anything, "a-b").Return(true, nil).Once()
	edge.On("DocumentExists", mock.Anything, "a-b").Return(false, errors.New("no")).Once()
	from := NewBlankAccountDocument("a")
	to := NewBlankNamespaceDocument("b")
	assert.True(t, repo.CheckLink(context.Background(), edge, from, to))
	assert.False(t, repo.CheckLink(context.Background(), edge, from, to))
}

func TestLinkAndLinkUntil(t *testing.T) {
	db := driver_mocks.NewMockDatabase(t)
	repo := NewInfinimeshCommonActionsRepo(db)
	edge := driver_mocks.NewMockCollection(t)
	log := zap.NewNop()
	from := NewBlankAccountDocument("a")
	to := NewBlankNamespaceDocument("b")
	ctx := context.Background()

	edge.On("RemoveDocument", mock.Anything, "a-b").Return(docMeta(schema.ACC2NS, "a-b"), nil).Once()
	require.NoError(t, repo.Link(ctx, log, edge, from, to, access.Level_NONE, access.Role_UNSET))
	edge.On("RemoveDocument", mock.Anything, "a-b").Return(driver.DocumentMeta{}, errors.New("rm")).Once()
	require.Error(t, repo.Link(ctx, log, edge, from, to, access.Level_NONE, access.Role_UNSET))

	edge.On("UpdateDocument", mock.Anything, "a-b", mock.Anything).Return(docMeta(schema.ACC2NS, "a-b"), nil).Once()
	require.NoError(t, repo.Link(ctx, log, edge, from, to, access.Level_ADMIN, access.Role_OWNER))
	edge.On("UpdateDocument", mock.Anything, "a-b", mock.Anything).Return(driver.DocumentMeta{}, errors.New("upd")).Once()
	edge.On("CreateDocument", mock.Anything, mock.Anything).Return(docMeta(schema.ACC2NS, "a-b"), nil).Once()
	require.NoError(t, repo.Link(ctx, log, edge, from, to, access.Level_READ, access.Role_SHARED))
	edge.On("UpdateDocument", mock.Anything, "a-b", mock.Anything).Return(driver.DocumentMeta{}, errors.New("upd")).Once()
	edge.On("CreateDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("create")).Once()
	require.Error(t, repo.Link(ctx, log, edge, from, to, access.Level_READ, access.Role_SHARED))

	edge.On("RemoveDocument", mock.Anything, "a-b").Return(docMeta(schema.ACC2NS, "a-b"), nil).Once()
	require.NoError(t, repo.LinkUntil(ctx, log, edge, from, to, access.Level_NONE, access.Role_UNSET, 10))
	edge.On("RemoveDocument", mock.Anything, "a-b").Return(driver.DocumentMeta{}, errors.New("rm")).Once()
	require.Error(t, repo.LinkUntil(ctx, log, edge, from, to, access.Level_ADMIN, access.Role_OWNER, 0))
	edge.On("UpdateDocument", mock.Anything, "a-b", mock.Anything).Return(docMeta(schema.ACC2NS, "a-b"), nil).Once()
	require.NoError(t, repo.LinkUntil(ctx, log, edge, from, to, access.Level_READ, access.Role_SHARED, 50))
	edge.On("UpdateDocument", mock.Anything, "a-b", mock.Anything).Return(driver.DocumentMeta{}, errors.New("upd")).Once()
	edge.On("CreateDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("create")).Once()
	require.Error(t, repo.LinkUntil(ctx, log, edge, from, to, access.Level_READ, access.Role_SHARED, 50))
}

func TestAccessLevelAndGetAndList(t *testing.T) {
	log := zap.NewNop()
	ctx := context.Background()

	db := driver_mocks.NewMockDatabase(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q")).Once()
	repo := NewInfinimeshCommonActionsRepo(db)
	err := repo.AccessLevelAndGet(ctx, log, NewBlankAccountDocument("a"), NewBlankNamespaceDocument("n"))
	require.Error(t, err)

	db = driver_mocks.NewMockDatabase(t)
	cur := readErr(errors.New("read"))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(cur, nil)
	repo = NewInfinimeshCommonActionsRepo(db)
	require.Error(t, repo.AccessLevelAndGet(ctx, log, NewBlankAccountDocument("a"), NewBlankNamespaceDocument("n")))

	db = driver_mocks.NewMockDatabase(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		p := result.(*InfinimeshGraphNode)
		*p = nil
	}), nil)
	repo = NewInfinimeshCommonActionsRepo(db)
	require.EqualError(t, repo.AccessLevelAndGet(ctx, log, NewBlankAccountDocument("a"), NewBlankAccountDocument("a")), "node not found")

	db = driver_mocks.NewMockDatabase(t)
	ns := "root"
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		paintAccess(result, access.Level_READ, access.Role_UNSET, &ns)
	}), nil)
	repo = NewInfinimeshCommonActionsRepo(db)
	node := NewBlankAccountDocument("same")
	require.NoError(t, repo.AccessLevelAndGet(ctx, log, NewBlankAccountDocument("same"), node))
	assert.Equal(t, access.Level_ROOT, node.Access.Level)

	db = driver_mocks.NewMockDatabase(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("list")).Once()
	repo = NewInfinimeshCommonActionsRepo(db)
	_, err = repo.ListQuery(ctx, log, NewBlankAccountDocument("a"), schema.DEVICES_COL)
	require.Error(t, err)

	db = driver_mocks.NewMockDatabase(t)
	var seen string
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		seen = args.String(1)
	}).Return(&scriptCursor{}, nil)
	repo = NewInfinimeshCommonActionsRepo(db)
	filtered := WithNamespaceFilter(ctx, "plant")
	_, err = repo.ListQuery(filtered, log, NewBlankAccountDocument("a"), schema.DEVICES_COL)
	require.NoError(t, err)
	assert.Contains(t, seen, `path.vertices[-2]._key == "plant"`)
}

func TestListOwnedDeepAccessLevelToggle(t *testing.T) {
	log := zap.NewNop()
	ctx := context.Background()
	from := NewBlankAccountDocument("a")

	db := driver_mocks.NewMockDatabase(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q"))
	repo := NewInfinimeshCommonActionsRepo(db)
	_, err := repo.ListOwnedDeep(ctx, log, from)
	require.Error(t, err)

	db = driver_mocks.NewMockDatabase(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(readErr(errors.New("bad")), nil)
	repo = NewInfinimeshCommonActionsRepo(db)
	_, err = repo.ListOwnedDeep(ctx, log, from)
	require.Error(t, err)

	db = driver_mocks.NewMockDatabase(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		n := result.(*access.Node)
		n.Node = "Devices/d"
	}), nil)
	repo = NewInfinimeshCommonActionsRepo(db)
	nodes, err := repo.ListOwnedDeep(ctx, log, from)
	require.NoError(t, err)
	require.Len(t, nodes.Nodes, 1)

	same, lvl := repo.AccessLevel(ctx, from, from)
	assert.True(t, same)
	assert.Equal(t, access.Level_ROOT, lvl)

	db = driver_mocks.NewMockDatabase(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q"))
	repo = NewInfinimeshCommonActionsRepo(db)
	ok, lvl := repo.AccessLevel(ctx, from, NewBlankNamespaceDocument("n"))
	assert.False(t, ok)
	assert.Equal(t, access.Level(0), lvl)

	db = driver_mocks.NewMockDatabase(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&scriptCursor{fn: []func(any) (driver.DocumentMeta, error){
		func(any) (driver.DocumentMeta, error) { return driver.DocumentMeta{}, errors.New("skip") },
		func(result any) (driver.DocumentMeta, error) {
			p := result.(*access.Level)
			*p = access.Level_READ
			return docMeta("C", "1"), nil
		},
		func(result any) (driver.DocumentMeta, error) {
			p := result.(*access.Level)
			*p = access.Level_ADMIN
			return docMeta("C", "2"), nil
		},
	}}, nil)
	repo = NewInfinimeshCommonActionsRepo(db)
	ok, lvl = repo.AccessLevel(ctx, from, NewBlankNamespaceDocument("n"))
	assert.True(t, ok)
	assert.Equal(t, access.Level_ADMIN, lvl)

	db = driver_mocks.NewMockDatabase(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&scriptCursor{}, nil)
	repo = NewInfinimeshCommonActionsRepo(db)
	ok, _ = repo.AccessLevel(ctx, from, NewBlankNamespaceDocument("n"))
	assert.False(t, ok)

	db = driver_mocks.NewMockDatabase(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("t")).Once()
	repo = NewInfinimeshCommonActionsRepo(db)
	require.Error(t, repo.Toggle(ctx, from, "enabled"))

	db = driver_mocks.NewMockDatabase(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(readErr(errors.New("r")), nil)
	repo = NewInfinimeshCommonActionsRepo(db)
	require.Error(t, repo.Toggle(ctx, from, "enabled"))

	db = driver_mocks.NewMockDatabase(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(nil), nil)
	repo = NewInfinimeshCommonActionsRepo(db)
	require.NoError(t, repo.Toggle(ctx, NewBlankAccountDocument("a"), "enabled"))
}

func TestMoveRepo(t *testing.T) {
	log := zap.NewNop()
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "owner")
	ctrl := &AccountsController{InfinimeshBaseController: InfinimeshBaseController{log: log}}
	edge := driver_mocks.NewMockCollection(t)
	obj := NewBlankAccountDocument("acc")
	nsName := "old"

	db := driver_mocks.NewMockDatabase(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q"))
	repo := NewInfinimeshCommonActionsRepo(db)
	require.Error(t, repo.Move(ctx, ctrl, obj, edge, "new"))

	db = driver_mocks.NewMockDatabase(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		paintAccess(result, access.Level_READ, access.Role_UNSET, &nsName)
	}), nil)
	repo = NewInfinimeshCommonActionsRepo(db)
	require.Error(t, repo.Move(ctx, ctrl, NewBlankAccountDocument("acc"), edge, "new"))

	db = driver_mocks.NewMockDatabase(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		paintAccess(result, access.Level_ROOT, access.Role_OWNER, nil)
	}), nil)
	repo = NewInfinimeshCommonActionsRepo(db)
	require.Error(t, repo.Move(ctx, ctrl, NewBlankAccountDocument("acc"), edge, "new"))

	db = driver_mocks.NewMockDatabase(t)
	n := 0
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(func(context.Context, string, map[string]interface{}) driver.Cursor {
		n++
		if n == 1 {
			return oneRead(func(result any) {
				paintAccess(result, access.Level_ROOT, access.Role_OWNER, &nsName)
			})
		}
		return readErr(errors.New("ns"))
	}, nil)
	repo = NewInfinimeshCommonActionsRepo(db)
	require.Error(t, repo.Move(ctx, ctrl, NewBlankAccountDocument("acc"), edge, "new"))

	db = driver_mocks.NewMockDatabase(t)
	n = 0
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(func(context.Context, string, map[string]interface{}) driver.Cursor {
		n++
		if n == 1 {
			return oneRead(func(result any) {
				paintAccess(result, access.Level_ROOT, access.Role_OWNER, &nsName)
			})
		}
		return oneRead(func(result any) {
			paintAccess(result, access.Level_READ, access.Role_UNSET, nil)
		})
	}, nil)
	repo = NewInfinimeshCommonActionsRepo(db)
	require.Error(t, repo.Move(ctx, ctrl, NewBlankAccountDocument("acc"), edge, "new"))

	db = driver_mocks.NewMockDatabase(t)
	n = 0
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(func(context.Context, string, map[string]interface{}) driver.Cursor {
		n++
		return oneRead(func(result any) {
			paintAccess(result, access.Level_ROOT, access.Role_OWNER, &nsName)
		})
	}, nil)
	edge.On("RemoveDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("unlink")).Once()
	repo = NewInfinimeshCommonActionsRepo(db)
	require.Error(t, repo.Move(ctx, ctrl, NewBlankAccountDocument("acc"), edge, "new"))

	db = driver_mocks.NewMockDatabase(t)
	n = 0
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(func(context.Context, string, map[string]interface{}) driver.Cursor {
		n++
		return oneRead(func(result any) {
			paintAccess(result, access.Level_ROOT, access.Role_OWNER, &nsName)
		})
	}, nil)
	edge.On("RemoveDocument", mock.Anything, mock.Anything).Return(docMeta(schema.NS2ACC, "e"), nil).Once()
	edge.On("UpdateDocument", mock.Anything, mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("link")).Once()
	edge.On("CreateDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("link")).Once()
	repo = NewInfinimeshCommonActionsRepo(db)
	require.Error(t, repo.Move(ctx, ctrl, NewBlankAccountDocument("acc"), edge, "new"))

	db = driver_mocks.NewMockDatabase(t)
	n = 0
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(func(context.Context, string, map[string]interface{}) driver.Cursor {
		n++
		return oneRead(func(result any) {
			paintAccess(result, access.Level_ADMIN, access.Role_OWNER, &nsName)
		})
	}, nil)
	edge.On("RemoveDocument", mock.Anything, mock.Anything).Return(docMeta(schema.NS2ACC, "e"), nil).Once()
	edge.On("UpdateDocument", mock.Anything, mock.Anything, mock.Anything).Return(docMeta(schema.NS2ACC, "e"), nil).Once()
	repo = NewInfinimeshCommonActionsRepo(db)
	require.NoError(t, repo.Move(ctx, ctrl, NewBlankAccountDocument("acc"), edge, "new"))
}

func TestDeleteRecursion(t *testing.T) {
	log := zap.NewNop()
	ctx := context.Background()
	db := driver_mocks.NewMockDatabase(t)
	repo := NewInfinimeshCommonActionsRepo(db)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("owned")).Once()
	require.Error(t, repo.DeleteRecursive(ctx, log, NewBlankAccountDocument("a")))

	col := driver_mocks.NewMockCollection(t)
	col.On("Database").Return(db).Maybe()
	db.On("Collection", mock.Anything, "Accounts").Return(col, nil)
	db.On("Collection", mock.Anything, "Namespaces").Return(col, nil)
	db.On("Collection", mock.Anything, "Devices").Return(nil, errors.New("missing")).Maybe()
	db.On("Collection", mock.Anything, "Credentials").Return(col, nil).Maybe()
	db.On("Collection", mock.Anything, schema.ACC2CRED).Return(col, nil).Maybe()

	require.EqualError(t, handleDeleteNodeInRecursion(ctx, log, db, schema.ACCOUNTS_COL+"/"+schema.ROOT_ACCOUNT_KEY, map[string]driver.Collection{}), "ERR_ROOT_OBJECT_CANNOT_BE_DELETED")
	require.EqualError(t, handleDeleteNodeInRecursion(ctx, log, db, schema.NAMESPACES_COL+"/"+schema.ROOT_NAMESPACE_KEY, map[string]driver.Collection{}), "ERR_ROOT_OBJECT_CANNOT_BE_DELETED")
	require.Error(t, handleDeleteNodeInRecursion(ctx, log, db, "Devices/d1", map[string]driver.Collection{}))

	cached := map[string]driver.Collection{schema.DEVICES_COL: col}
	col.On("RemoveDocument", mock.Anything, "d1").Return(docMeta(schema.DEVICES_COL, "d1"), nil).Once()
	require.NoError(t, handleDeleteNodeInRecursion(ctx, log, db, "Devices/d1", cached))

	col.On("RemoveDocument", mock.Anything, "d2").Return(driver.DocumentMeta{}, driver.ArangoError{HasError: true, Code: 404}).Once()
	require.NoError(t, handleDeleteNodeInRecursion(ctx, log, db, "Devices/d2", cached))

	col.On("RemoveDocument", mock.Anything, "d3").Return(driver.DocumentMeta{}, errors.New("boom")).Once()
	require.Error(t, handleDeleteNodeInRecursion(ctx, log, db, "Devices/d3", cached))

	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("creds")).Once()
	require.Error(t, handleDeleteNodeInRecursion(ctx, log, db, schema.ACCOUNTS_COL+"/user", map[string]driver.Collection{}))

	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		p := result.(*[]string)
		*p = []string{"Credentials/c1", schema.ACC2CRED + "/e1"}
	}), nil).Once()
	col.On("RemoveDocument", mock.Anything, "c1").Return(docMeta("Credentials", "c1"), nil).Once()
	col.On("RemoveDocument", mock.Anything, "e1").Return(docMeta(schema.ACC2CRED, "e1"), nil).Once()
	col.On("RemoveDocument", mock.Anything, "user").Return(docMeta(schema.ACCOUNTS_COL, "user"), nil).Once()
	require.NoError(t, handleDeleteNodeInRecursion(ctx, log, db, schema.ACCOUNTS_COL+"/user", map[string]driver.Collection{}))

	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&scriptCursor{fn: []func(any) (driver.DocumentMeta, error){
		func(result any) (driver.DocumentMeta, error) {
			n := result.(*access.Node)
			n.Node = schema.ACCOUNTS_COL + "/" + schema.ROOT_ACCOUNT_KEY
			return docMeta("C", "1"), nil
		},
		func(result any) (driver.DocumentMeta, error) {
			n := result.(*access.Node)
			n.Node = schema.NAMESPACES_COL + "/leaf"
			n.Edge = "Devices/edge"
			return docMeta("C", "2"), nil
		},
		func(result any) (driver.DocumentMeta, error) {
			_ = result.(*access.Node)
			return docMeta("C", "3"), nil
		},
	}}, nil)
	col.On("RemoveDocument", mock.Anything, "leaf").Return(driver.DocumentMeta{}, errors.New("nope")).Once()
	require.Error(t, repo.DeleteRecursive(ctx, log, NewBlankAccountDocument("a")))
}

func TestEnsureRootExists(t *testing.T) {
	ctx := context.TODO()
	log := zap.NewNop()
	hash, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	require.NoError(t, err)

	var accCol, nsCol, credCol, edgeCol, credEdge *driver_mocks.MockCollection

	newDB := func(t *testing.T) *driver_mocks.MockDatabase {
		t.Helper()
		db := driver_mocks.NewMockDatabase(t)
		accCol = driver_mocks.NewMockCollection(t)
		nsCol = driver_mocks.NewMockCollection(t)
		credCol = driver_mocks.NewMockCollection(t)
		edgeCol = driver_mocks.NewMockCollection(t)
		credEdge = driver_mocks.NewMockCollection(t)
		db.On("Collection", mock.Anything, schema.ACCOUNTS_COL).Return(accCol, nil).Maybe()
		db.On("Collection", mock.Anything, schema.NAMESPACES_COL).Return(nsCol, nil).Maybe()
		db.On("Collection", mock.Anything, schema.ACC2CRED).Return(credEdge, nil).Maybe()
		db.On("Collection", mock.Anything, schema.CREDENTIALS_EDGE_COL).Return(credEdge, nil).Maybe()
		db.On("Graph", mock.Anything, mock.Anything).Return(stubGraph{
			col: edgeCol,
			vert: func(name string) driver.Collection {
				if name == schema.CREDENTIALS_COL {
					return credCol
				}
				return accCol
			},
			edge: func(string) driver.Collection { return edgeCol },
		}, nil).Maybe()
		accCol.On("Database").Return(db).Maybe()
		credCol.On("Database").Return(db).Maybe()
		return db
	}

	db := newDB(t)
	accCol.On("DocumentExists", ctx, schema.ROOT_ACCOUNT_KEY).Return(false, errors.New("exists")).Once()
	require.Error(t, NewInfinimeshCommonActionsRepo(db).EnsureRootExists(log, nil, "pw"))

	db = newDB(t)
	accCol.On("DocumentExists", ctx, schema.ROOT_ACCOUNT_KEY).Return(false, nil).Once()
	accCol.On("CreateDocument", ctx, mock.Anything).Return(driver.DocumentMeta{}, errors.New("create")).Once()
	require.Error(t, NewInfinimeshCommonActionsRepo(db).EnsureRootExists(log, nil, "pw"))

	db = newDB(t)
	accCol.On("DocumentExists", ctx, schema.ROOT_ACCOUNT_KEY).Return(true, nil).Once()
	accCol.On("ReadDocument", ctx, schema.ROOT_ACCOUNT_KEY, mock.Anything).Return(driver.DocumentMeta{}, errors.New("read")).Once()
	require.Error(t, NewInfinimeshCommonActionsRepo(db).EnsureRootExists(log, nil, "pw"))

	db = newDB(t)
	accCol.On("DocumentExists", ctx, schema.ROOT_ACCOUNT_KEY).Return(true, nil).Once()
	accCol.On("ReadDocument", ctx, schema.ROOT_ACCOUNT_KEY, mock.Anything).Return(docMeta(schema.ACCOUNTS_COL, schema.ROOT_ACCOUNT_KEY), nil).Once()
	nsCol.On("DocumentExists", ctx, schema.ROOT_NAMESPACE_KEY).Return(false, nil).Once()
	nsCol.On("CreateDocument", ctx, mock.Anything).Return(driver.DocumentMeta{}, errors.New("ns")).Once()
	require.Error(t, NewInfinimeshCommonActionsRepo(db).EnsureRootExists(log, nil, "pw"))

	db = newDB(t)
	accCol.On("DocumentExists", ctx, schema.ROOT_ACCOUNT_KEY).Return(true, nil).Once()
	accCol.On("ReadDocument", ctx, schema.ROOT_ACCOUNT_KEY, mock.Anything).Return(docMeta(schema.ACCOUNTS_COL, schema.ROOT_ACCOUNT_KEY), nil).Once()
	nsCol.On("DocumentExists", ctx, schema.ROOT_NAMESPACE_KEY).Return(true, nil).Once()
	nsCol.On("ReadDocument", ctx, schema.ROOT_NAMESPACE_KEY, mock.Anything).Return(driver.DocumentMeta{}, errors.New("nsread")).Once()
	require.Error(t, NewInfinimeshCommonActionsRepo(db).EnsureRootExists(log, nil, "pw"))

	db = newDB(t)
	accCol.On("DocumentExists", ctx, schema.ROOT_ACCOUNT_KEY).Return(true, nil).Once()
	accCol.On("ReadDocument", ctx, schema.ROOT_ACCOUNT_KEY, mock.Anything).Return(docMeta(schema.ACCOUNTS_COL, schema.ROOT_ACCOUNT_KEY), nil).Once()
	nsCol.On("DocumentExists", ctx, schema.ROOT_NAMESPACE_KEY).Return(false, errors.New("probe")).Once()
	nsCol.On("CreateDocument", ctx, mock.Anything).Return(docMeta(schema.NAMESPACES_COL, schema.ROOT_NAMESPACE_KEY), nil).Once()
	nsCol.On("ReadDocument", ctx, schema.ROOT_NAMESPACE_KEY, mock.Anything).Return(docMeta(schema.NAMESPACES_COL, schema.ROOT_NAMESPACE_KEY), nil).Once()
	edgeCol.On("DocumentExists", mock.Anything, mock.Anything).Return(false, nil).Once()
	edgeCol.On("UpdateDocument", mock.Anything, mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("link")).Once()
	edgeCol.On("CreateDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("link")).Once()
	require.Error(t, NewInfinimeshCommonActionsRepo(db).EnsureRootExists(log, nil, "pw"))

	db = newDB(t)
	accCol.On("DocumentExists", ctx, schema.ROOT_ACCOUNT_KEY).Return(true, nil).Once()
	accCol.On("ReadDocument", ctx, schema.ROOT_ACCOUNT_KEY, mock.Anything).Return(docMeta(schema.ACCOUNTS_COL, schema.ROOT_ACCOUNT_KEY), nil).Once()
	nsCol.On("DocumentExists", ctx, schema.ROOT_NAMESPACE_KEY).Return(true, nil).Once()
	nsCol.On("ReadDocument", ctx, schema.ROOT_NAMESPACE_KEY, mock.Anything).Return(docMeta(schema.NAMESPACES_COL, schema.ROOT_NAMESPACE_KEY), nil).Once()
	edgeCol.On("DocumentExists", mock.Anything, mock.Anything).Return(true, nil).Once()
	credEdge.On("DocumentExists", mock.Anything, "standard-"+schema.ROOT_ACCOUNT_KEY).Return(false, nil).Once()
	credEdge.On("ReadDocument", mock.Anything, "standard-"+schema.ROOT_ACCOUNT_KEY, mock.Anything).Return(driver.DocumentMeta{}, errors.New("missing")).Once()
	credCol.On("CreateDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("cred")).Once()
	require.Error(t, NewInfinimeshCommonActionsRepo(db).EnsureRootExists(log, nil, "pw"))

	db = newDB(t)
	accCol.On("DocumentExists", ctx, schema.ROOT_ACCOUNT_KEY).Return(true, nil).Once()
	accCol.On("ReadDocument", ctx, schema.ROOT_ACCOUNT_KEY, mock.Anything).Return(docMeta(schema.ACCOUNTS_COL, schema.ROOT_ACCOUNT_KEY), nil).Once()
	nsCol.On("DocumentExists", ctx, schema.ROOT_NAMESPACE_KEY).Return(true, nil).Once()
	nsCol.On("ReadDocument", ctx, schema.ROOT_NAMESPACE_KEY, mock.Anything).Return(docMeta(schema.NAMESPACES_COL, schema.ROOT_NAMESPACE_KEY), nil).Once()
	edgeCol.On("DocumentExists", mock.Anything, mock.Anything).Return(true, nil).Once()
	credEdge.On("DocumentExists", mock.Anything, "standard-"+schema.ROOT_ACCOUNT_KEY).Return(true, nil).Once()
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("auth")).Once()
	require.EqualError(t, NewInfinimeshCommonActionsRepo(db).EnsureRootExists(log, nil, "pw"), "cannot authorize infinimesh")

	db = newDB(t)
	accCol.On("DocumentExists", ctx, schema.ROOT_ACCOUNT_KEY).Return(false, nil).Once()
	accCol.On("CreateDocument", ctx, mock.Anything).Return(docMeta(schema.ACCOUNTS_COL, schema.ROOT_ACCOUNT_KEY), nil).Once()
	accCol.On("ReadDocument", ctx, schema.ROOT_ACCOUNT_KEY, mock.Anything).Run(func(args mock.Arguments) {
		acc := args.Get(2).(*accpb.Account)
		acc.Enabled = true
		acc.Uuid = schema.ROOT_ACCOUNT_KEY
	}).Return(docMeta(schema.ACCOUNTS_COL, schema.ROOT_ACCOUNT_KEY), nil).Once()
	nsCol.On("DocumentExists", ctx, schema.ROOT_NAMESPACE_KEY).Return(true, nil).Once()
	nsCol.On("ReadDocument", ctx, schema.ROOT_NAMESPACE_KEY, mock.Anything).Return(docMeta(schema.NAMESPACES_COL, schema.ROOT_NAMESPACE_KEY), nil).Once()
	edgeCol.On("DocumentExists", mock.Anything, mock.Anything).Return(true, nil).Once()
	credEdge.On("DocumentExists", mock.Anything, "standard-"+schema.ROOT_ACCOUNT_KEY).Return(true, nil).Once()
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(func(_ context.Context, q string, _ map[string]interface{}) driver.Cursor {
		if strings.Contains(q, "username") {
			return oneRead(func(result any) {
				pp := result.(**credentials.StandardCredentials)
				(*pp).PasswordHash = string(hash)
			})
		}
		return oneRead(func(result any) {
			a := result.(*Account)
			if a.Account == nil {
				a.Account = &accpb.Account{}
			}
			a.Enabled = true
			a.Key = schema.ROOT_ACCOUNT_KEY
		})
	}, nil)
	require.NoError(t, NewInfinimeshCommonActionsRepo(db).EnsureRootExists(log, nil, "pw"))
}
