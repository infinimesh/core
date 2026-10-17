package graph

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/arangodb/go-driver"
	driver_mocks "github.com/infinimesh/core/v3/mocks/github.com/arangodb/go-driver"
	"github.com/infinimesh/core/v3/pkg/credentials"
	"github.com/infinimesh/core/v3/pkg/graph/schema"
	inf "github.com/infinimesh/core/v3/pkg/shared"
	pb "github.com/infinimesh/proto/node"
	"github.com/infinimesh/proto/node/access"
	accpb "github.com/infinimesh/proto/node/accounts"
	nspb "github.com/infinimesh/proto/node/namespaces"
	sesspb "github.com/infinimesh/proto/node/sessions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type tokSess struct{ err error }

func (s tokSess) New(int64, string) *sesspb.Session       { return &sesspb.Session{Id: "sid"} }
func (s tokSess) Store(string, *sesspb.Session) error     { return s.err }
func (s tokSess) Check(string, string) error              { return nil }
func (s tokSess) LogActivity(string, string, int64) error { return nil }
func (s tokSess) Get(string) ([]*sesspb.Session, error)   { return nil, nil }
func (s tokSess) GetActivity(string) (map[string]*timestamppb.Timestamp, error) {
	return nil, nil
}
func (s tokSess) Revoke(string, string) error { return nil }

func newAcc(repo flexRepo) *AccountsController {
	return &AccountsController{
		InfinimeshBaseController: InfinimeshBaseController{log: zap.NewNop()},
		ica_repo:                 repo,
		SIGNING_KEY:              []byte("secret"),
		sessions:                 tokSess{},
	}
}

func TestAccountsTokenBranches(t *testing.T) {
	id := "self"
	ctrl := newAcc(flexRepo{ok: true, level: access.Level_ROOT, role: access.Role_OWNER})
	_, err := ctrl.Token(accCtx("self"), connect.NewRequest(&pb.TokenRequest{Uuid: &id}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	other := "other"
	ctrl.ica_repo = flexRepo{getErr: errors.New("missing")}
	_, err = ctrl.Token(accCtx("root"), connect.NewRequest(&pb.TokenRequest{Uuid: &other}))
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	ctrl.ica_repo = flexRepo{level: access.Level_READ, role: access.Role_UNSET}
	_, err = ctrl.Token(accCtx("root"), connect.NewRequest(&pb.TokenRequest{Uuid: &other}))
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	off := false
	ctrl.ica_repo = flexRepo{level: access.Level_ROOT, role: access.Role_OWNER, enabled: &off}
	_, err = ctrl.Token(accCtx("root"), connect.NewRequest(&pb.TokenRequest{Uuid: &other}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	ctrl.sessions = tokSess{err: errors.New("redis")}
	ctrl.ica_repo = flexRepo{level: access.Level_ROOT, role: access.Role_OWNER}
	_, err = ctrl.Token(accCtx("root"), connect.NewRequest(&pb.TokenRequest{Uuid: &other}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))

	ctrl.sessions = tokSess{}
	inf := true
	res, err := ctrl.Token(accCtx("root"), connect.NewRequest(&pb.TokenRequest{Uuid: &other, Infinite: &inf, ExpiresAt: time.Now().Add(time.Hour).Unix()}))
	require.NoError(t, err)
	assert.NotEmpty(t, res.Msg.GetToken())

	ctrl.ica_repo = flexRepo{ok: false, level: access.Level_NONE, role: access.Role_OWNER}
	_, err = ctrl.Token(accCtx("root"), connect.NewRequest(&pb.TokenRequest{Uuid: &other, Infinite: &inf}))
	require.NoError(t, err)

	col := driver_mocks.NewMockCollection(t)
	db := driver_mocks.NewMockDatabase(t)
	col.On("Database").Return(db).Maybe()
	ctrl.col = col
	_, err = ctrl.Token(context.Background(), connect.NewRequest(&pb.TokenRequest{Auth: &accpb.Credentials{Type: "nope"}}))
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	_, err = ctrl.Accessibles(context.Background(), connect.NewRequest(&nspb.Namespace{}))
	assert.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
	_, err = ctrl.DeleteCredentials(context.Background(), connect.NewRequest(&pb.DeleteCredentialsRequest{}))
	assert.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
	_, err = ctrl.DelCredentials(context.Background(), connect.NewRequest(&pb.DeleteCredentialsRequest{}))
	assert.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
}

func TestAccountsGetListUpdateToggle(t *testing.T) {
	ctrl := newAcc(flexRepo{getErr: errors.New("gone")})
	_, err := ctrl.Get(accCtx("me"), connect.NewRequest(&accpb.Account{Uuid: "me"}))
	assert.Error(t, err)
	ctrl.ica_repo = flexRepo{level: access.Level_NONE}
	_, err = ctrl.Get(accCtx("me"), connect.NewRequest(&accpb.Account{Uuid: "target"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{level: access.Level_READ}
	res, err := ctrl.Get(accCtx("me"), connect.NewRequest(&accpb.Account{Uuid: "me"}))
	require.NoError(t, err)
	assert.Equal(t, "me", res.Msg.GetUuid())

	_, err = ctrl.List(context.Background(), connect.NewRequest(&pb.ListRequest{}))
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{listErr: errors.New("q")}
	_, err = ctrl.List(accCtx("me"), connect.NewRequest(&pb.ListRequest{}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{list: readErr(errors.New("row"))}
	_, err = ctrl.List(accCtx("me"), connect.NewRequest(&pb.ListRequest{}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{list: oneRead(func(result any) {
		acc := result.(*accpb.Account)
		acc.Title = "t"
	})}
	listed, err := ctrl.List(accCtx("me"), connect.NewRequest(&pb.ListRequest{PageSize: 1}))
	require.NoError(t, err)
	assert.Equal(t, "k", listed.Msg.Accounts[0].GetUuid())

	root := context.WithValue(accCtx("root"), inf.InfinimeshRootCtxKey, true)
	_, err = ctrl.List(root, connect.NewRequest(&pb.ListRequest{}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	db := driver_mocks.NewMockDatabase(t)
	ctrl.db = db
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q")).Once()
	_, err = ctrl.List(root, connect.NewRequest(&pb.ListRequest{}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(readErr(errors.New("row")), nil).Once()
	_, err = ctrl.List(root, connect.NewRequest(&pb.ListRequest{}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		acc := result.(*accpb.Account)
		acc.Title = "root"
	}), nil).Once()
	listed, err = ctrl.List(root, connect.NewRequest(&pb.ListRequest{}))
	require.NoError(t, err)
	assert.Equal(t, "k", listed.Msg.Accounts[0].GetUuid())

	ctrl.ica_repo = flexRepo{getErr: errors.New("no")}
	_, err = ctrl.Update(accCtx("root"), connect.NewRequest(&accpb.Account{Uuid: "a", DefaultNamespace: "ns"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{level: access.Level_ADMIN, ok: false}
	_, err = ctrl.Update(accCtx("root"), connect.NewRequest(&accpb.Account{Uuid: "a", DefaultNamespace: "ns"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	col := driver_mocks.NewMockCollection(t)
	ctrl.col = col
	ctrl.ica_repo = flexRepo{level: access.Level_ADMIN, ok: true, accessFn: func(node InfinimeshGraphNode) {
		a := node.(*Account)
		a.Access = &access.Access{Level: access.Level_ADMIN}
		a.DefaultNamespace = "same"
	}}
	col.On("UpdateDocument", mock.Anything, "a", mock.Anything).Return(driver.DocumentMeta{}, errors.New("upd")).Once()
	_, err = ctrl.Update(accCtx("root"), connect.NewRequest(&accpb.Account{Uuid: "a", DefaultNamespace: "same"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	col.On("UpdateDocument", mock.Anything, "a", mock.Anything).Return(docMeta(schema.ACCOUNTS_COL, "a"), nil).Once()
	updated, err := ctrl.Update(accCtx("root"), connect.NewRequest(&accpb.Account{Uuid: "a", DefaultNamespace: "same", Title: "n"}))
	require.NoError(t, err)
	assert.Equal(t, "n", updated.Msg.GetTitle())

	ctrl.ica_repo = flexRepo{level: access.Level_READ}
	_, err = ctrl.Toggle(accCtx("root"), connect.NewRequest(&accpb.Account{Uuid: "a"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{level: access.Level_MGMT, toggleErr: errors.New("t")}
	_, err = ctrl.Toggle(accCtx("root"), connect.NewRequest(&accpb.Account{Uuid: "a"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{level: access.Level_MGMT}
	toggled, err := ctrl.Toggle(accCtx("root"), connect.NewRequest(&accpb.Account{Uuid: "a"}))
	require.NoError(t, err)
	assert.Equal(t, "a", toggled.Msg.GetUuid())
}

func TestAccountsDeleteAndCredentials(t *testing.T) {
	ctrl := newAcc(flexRepo{getErr: errors.New("gone")})
	_, err := ctrl.Deletables(accCtx("root"), connect.NewRequest(&accpb.Account{Uuid: "a"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{level: access.Level_READ, role: access.Role_UNSET}
	_, err = ctrl.Deletables(accCtx("root"), connect.NewRequest(&accpb.Account{Uuid: "a"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{level: access.Level_ROOT, deepErr: errors.New("deep")}
	_, err = ctrl.Deletables(accCtx("root"), connect.NewRequest(&accpb.Account{Uuid: "a"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{level: access.Level_ADMIN, role: access.Role_OWNER, deep: &access.Nodes{}}
	_, err = ctrl.Deletables(accCtx("root"), connect.NewRequest(&accpb.Account{Uuid: "a"}))
	require.NoError(t, err)

	ctrl.ica_repo = flexRepo{getErr: errors.New("gone")}
	_, err = ctrl.Delete(accCtx("root"), connect.NewRequest(&accpb.Account{Uuid: "a"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{level: access.Level_READ}
	_, err = ctrl.Delete(accCtx("root"), connect.NewRequest(&accpb.Account{Uuid: "a"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{level: access.Level_ADMIN, delErr: errors.New("del")}
	_, err = ctrl.Delete(accCtx("root"), connect.NewRequest(&accpb.Account{Uuid: "a"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{role: access.Role_OWNER, level: access.Level_READ}
	deleted, err := ctrl.Delete(accCtx("root"), connect.NewRequest(&accpb.Account{Uuid: "a"}))
	require.NoError(t, err)
	assert.NotNil(t, deleted)

	ctrl.ica_repo = flexRepo{getErr: errors.New("gone")}
	_, err = ctrl.GetCredentials(accCtx("root"), connect.NewRequest(&pb.GetCredentialsRequest{Uuid: "a"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{level: access.Level_READ}
	_, err = ctrl.GetCredentials(accCtx("root"), connect.NewRequest(&pb.GetCredentialsRequest{Uuid: "a"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	db := driver_mocks.NewMockDatabase(t)
	ctrl.db = db
	ctrl.ica_repo = flexRepo{level: access.Level_ROOT}
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q")).Once()
	_, err = ctrl.GetCredentials(accCtx("root"), connect.NewRequest(&pb.GetCredentialsRequest{Uuid: "a"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&scriptCursor{fn: []func(any) (driver.DocumentMeta, error){
		func(result any) (driver.DocumentMeta, error) {
			row := result.(*credentials.ListCredentialsResponse)
			row.Type = "other"
			return docMeta("Credentials", "c"), nil
		},
		func(result any) (driver.DocumentMeta, error) {
			row := result.(*credentials.ListCredentialsResponse)
			row.Type = "standard"
			row.D = map[string]interface{}{"username": "root"}
			return docMeta("Credentials", "s"), nil
		},
	}}, nil).Once()
	got, err := ctrl.GetCredentials(accCtx("root"), connect.NewRequest(&pb.GetCredentialsRequest{Uuid: "a"}))
	require.NoError(t, err)
	require.Len(t, got.Msg.Credentials, 1)
	assert.Equal(t, "standard", got.Msg.Credentials[0].GetType())

	db.On("Collection", mock.Anything, schema.CREDENTIALS_EDGE_COL).Return(driver_mocks.NewMockCollection(t), nil)
	_, err = ctrl.SetCredentials(accCtx("root"), connect.NewRequest(&pb.SetCredentialsRequest{Uuid: "a"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{level: access.Level_READ}
	_, err = ctrl.SetCredentials(accCtx("root"), connect.NewRequest(&pb.SetCredentialsRequest{
		Uuid: "a", Credentials: &accpb.Credentials{Type: "standard", Data: []string{"u", "p"}},
	}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestAccountsCreateAndSetCredentials(t *testing.T) {
	ctrl := newAcc(flexRepo{ok: false})
	h := http.Header{}
	h.Set("Idempotency-Key", "accounts-cover-create")
	saveIdem(h, "accounts.create", &accpb.CreateResponse{Account: &accpb.Account{Uuid: "cached"}})
	req := connect.NewRequest(&accpb.CreateRequest{})
	req.Header().Set("Idempotency-Key", "accounts-cover-create")
	cached, err := ctrl.Create(accCtx("root"), req)
	require.NoError(t, err)
	assert.Equal(t, "cached", cached.Msg.GetAccount().GetUuid())

	_, err = ctrl.Create(accCtx("root"), connect.NewRequest(&accpb.CreateRequest{Account: &accpb.Account{}}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	col := driver_mocks.NewMockCollection(t)
	edge := driver_mocks.NewMockCollection(t)
	cred := driver_mocks.NewMockCollection(t)
	db := driver_mocks.NewMockDatabase(t)
	ctrl.col = col
	ctrl.cred = cred
	ctrl.ns2acc = edge
	ctrl.db = db
	ctrl.ica_repo = flexRepo{ok: true, level: access.Level_ADMIN, linkErr: errors.New("link")}
	col.On("CreateDocument", mock.Anything, mock.Anything).Return(docMeta(schema.ACCOUNTS_COL, "new"), nil).Once()
	col.On("RemoveDocument", mock.Anything, "new").Return(docMeta(schema.ACCOUNTS_COL, "new"), nil).Once()
	_, err = ctrl.Create(accCtx("root"), connect.NewRequest(&accpb.CreateRequest{Account: &accpb.Account{Title: "t"}, Namespace: "ns"}))
	assert.Error(t, err)

	ctrl.ica_repo = flexRepo{ok: true, level: access.Level_ADMIN}
	col.On("CreateDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("col")).Once()
	_, err = ctrl.Create(accCtx("root"), connect.NewRequest(&accpb.CreateRequest{Account: &accpb.Account{Title: "t"}}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))

	db.On("Collection", mock.Anything, schema.CREDENTIALS_EDGE_COL).Return(edge, nil)
	col.On("CreateDocument", mock.Anything, mock.Anything).Return(docMeta(schema.ACCOUNTS_COL, "new"), nil).Once()
	col.On("RemoveDocument", mock.Anything, "new").Return(docMeta(schema.ACCOUNTS_COL, "new"), nil).Once()
	_, err = ctrl.Create(accCtx("root"), connect.NewRequest(&accpb.CreateRequest{Account: &accpb.Account{Title: "t"}, Credentials: &accpb.Credentials{Type: "nope"}}))
	assert.Error(t, err)

	edge.On("ReadDocument", mock.Anything, mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("missing")).Once()
	cred.On("CreateDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("cred")).Once()
	col.On("CreateDocument", mock.Anything, mock.Anything).Return(docMeta(schema.ACCOUNTS_COL, "new"), nil).Once()
	col.On("RemoveDocument", mock.Anything, "new").Return(docMeta(schema.ACCOUNTS_COL, "new"), nil).Once()
	_, err = ctrl.Create(accCtx("root"), connect.NewRequest(&accpb.CreateRequest{
		Account: &accpb.Account{Title: "t"}, Credentials: &accpb.Credentials{Type: "standard", Data: []string{"user", "pass"}},
	}))
	assert.Error(t, err)

	edge.On("ReadDocument", mock.Anything, mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("missing")).Once()
	cred.On("CreateDocument", mock.Anything, mock.Anything).Return(docMeta(schema.CREDENTIALS_COL, "c1"), nil).Once()
	edge.On("CreateDocument", mock.Anything, mock.Anything).Return(docMeta(schema.ACC2CRED, "standard-new"), nil).Once()
	col.On("CreateDocument", mock.Anything, mock.Anything).Return(docMeta(schema.ACCOUNTS_COL, "new"), nil).Once()
	created, err := ctrl.Create(accCtx("root"), connect.NewRequest(&accpb.CreateRequest{
		Account: &accpb.Account{Title: "t", DefaultNamespace: "ns"}, Namespace: "ns",
		Credentials: &accpb.Credentials{Type: "standard", Data: []string{"user", "pass"}},
	}))
	require.NoError(t, err)
	assert.Equal(t, "new", created.Msg.GetAccount().GetUuid())

	ctrl.ica_repo = flexRepo{level: access.Level_ROOT}
	edge.On("ReadDocument", mock.Anything, "standard-a", mock.Anything).Run(func(args mock.Arguments) {
		link := args.Get(2).(*credentials.Link)
		link.To = driver.NewDocumentID(schema.CREDENTIALS_COL, "old")
	}).Return(docMeta(schema.ACC2CRED, "standard-a"), nil).Once()
	cred.On("UpdateDocument", mock.Anything, "old", mock.Anything).Return(docMeta(schema.CREDENTIALS_COL, "old"), nil).Once()
	set, err := ctrl.SetCredentials(accCtx("root"), connect.NewRequest(&pb.SetCredentialsRequest{
		Uuid: "a", Credentials: &accpb.Credentials{Type: "standard", Data: []string{"user", "pass"}},
	}))
	require.NoError(t, err)
	assert.NotNil(t, set)

	edge.On("ReadDocument", mock.Anything, "standard-a", mock.Anything).Return(driver.DocumentMeta{}, errors.New("missing")).Once()
	cred.On("CreateDocument", mock.Anything, mock.Anything).Return(docMeta(schema.CREDENTIALS_COL, "c2"), nil).Once()
	edge.On("CreateDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("edge")).Once()
	cred.On("RemoveDocument", mock.Anything, "c2").Return(docMeta(schema.CREDENTIALS_COL, "c2"), nil).Once()
	_, err = ctrl.SetCredentials(accCtx("root"), connect.NewRequest(&pb.SetCredentialsRequest{
		Uuid: "a", Credentials: &accpb.Credentials{Type: "standard", Data: []string{"user", "pass"}},
	}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))

	edge.On("ReadDocument", mock.Anything, "standard-a", mock.Anything).Run(func(args mock.Arguments) {
		link := args.Get(2).(*credentials.Link)
		link.To = driver.NewDocumentID(schema.CREDENTIALS_COL, "old")
	}).Return(docMeta(schema.ACC2CRED, "standard-a"), nil).Once()
	cred.On("UpdateDocument", mock.Anything, "old", mock.Anything).Return(driver.DocumentMeta{}, errors.New("upd")).Once()
	_, err = ctrl.SetCredentials(accCtx("root"), connect.NewRequest(&pb.SetCredentialsRequest{
		Uuid: "a", Credentials: &accpb.Credentials{Type: "standard", Data: []string{"user", "pass"}},
	}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	accCol := driver_mocks.NewMockCollection(t)
	ctrl.col = accCol
	accCol.On("Database").Return(db)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("find")).Once()
	_, ok := ctrl.Authorize(context.Background(), "standard", "user", "pass")
	assert.False(t, ok)
	_, ok = ctrl.Authorize(context.Background(), "unknown")
	assert.False(t, ok)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(readErr(errors.New("read")), nil).Once()
	std := credentials.Credentials(&credentials.StandardCredentials{})
	_, ok = Authorisable(context.Background(), &std, db)
	assert.False(t, ok)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		a := result.(*Account)
		if a.Account == nil {
			a.Account = &accpb.Account{}
		}
		a.Uuid = "root"
		a.Enabled = true
	}), nil).Once()
	found, ok := Authorisable(context.Background(), &std, db)
	assert.True(t, ok)
	assert.Equal(t, "root", found.Uuid)
}
