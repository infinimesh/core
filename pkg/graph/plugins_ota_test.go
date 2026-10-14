package graph

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/arangodb/go-driver"
	"github.com/golang-jwt/jwt/v4"
	driver_mocks "github.com/infinimesh/core/mocks/github.com/arangodb/go-driver"
	"github.com/infinimesh/core/pkg/graph/schema"
	inf "github.com/infinimesh/core/pkg/shared"
	pb "github.com/infinimesh/proto/plugins"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestCreateOTAPluginReturnsTokenOnce(t *testing.T) {
	col := driver_mocks.NewMockCollection(t)
	col.On("CreateDocument", mock.Anything, mock.MatchedBy(func(doc Plugin) bool {
		return doc.GetToken() == "" && doc.GetProvider() == "hawkbit" && doc.GetKind() == pb.PluginKind_OTA
	})).Return(driver.DocumentMeta{ID: driver.NewDocumentID(schema.PLUGINS_COL, "plug-1")}, nil)
	col.On("UpdateDocument", mock.Anything, "plug-1", mock.MatchedBy(func(patch map[string]interface{}) bool {
		_, stored := patch["token"]
		id, _ := patch["token_id"].(string)
		return !stored && id != ""
	})).Return(driver.DocumentMeta{}, nil)
	ctrl := &PluginsController{log: zap.NewNop(), col: col, signingKey: []byte("secret")}
	ctx := context.WithValue(context.Background(), inf.InfinimeshRootCtxKey, true)

	_, err := ctrl.Create(ctx, connect.NewRequest(&pb.Plugin{Title: "Hawk", Kind: pb.PluginKind_OTA}))
	require.Error(t, err)

	res, err := ctrl.Create(ctx, connect.NewRequest(&pb.Plugin{Title: "Hawk", Kind: pb.PluginKind_OTA, Provider: "hawkbit"}))
	require.NoError(t, err)
	require.NotEmpty(t, res.Msg.GetToken())
	require.NotEmpty(t, res.Msg.GetTokenId())
	parsed, err := jwt.Parse(res.Msg.GetToken(), func(token *jwt.Token) (interface{}, error) {
		return []byte("secret"), nil
	})
	require.NoError(t, err)
	claims := parsed.Claims.(jwt.MapClaims)
	require.Equal(t, "plug-1", claims[inf.INFINIMESH_PLUGIN_CLAIM])
	require.Equal(t, "hawkbit", claims[inf.INFINIMESH_OTA_PROVIDER_CLAIM])
	require.Equal(t, res.Msg.GetTokenId(), claims[inf.INFINIMESH_JTI_CLAIM])
}

type seenPluginAudit struct {
	rows []string
}

func (s *seenPluginAudit) Record(_ context.Context, action, _, account, _, namespace string) {
	s.rows = append(s.rows, action+" "+account+" "+namespace)
}

func TestCreateAndDeletePluginWriteAudit(t *testing.T) {
	ns := "plant"
	col := driver_mocks.NewMockCollection(t)
	col.On("CreateDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{ID: driver.NewDocumentID(schema.PLUGINS_COL, "plug-1")}, nil)
	col.On("UpdateDocument", mock.Anything, "plug-1", mock.Anything).Return(driver.DocumentMeta{}, nil)
	col.On("ReadDocument", mock.Anything, "plug-1", mock.Anything).Run(func(args mock.Arguments) {
		plug := args.Get(2).(*pb.Plugin)
		plug.Namespace = &ns
	}).Return(driver.DocumentMeta{Key: "plug-1"}, nil)
	col.On("RemoveDocument", mock.Anything, "plug-1").Return(driver.DocumentMeta{}, nil)
	seen := &seenPluginAudit{}
	ctrl := &PluginsController{log: zap.NewNop(), col: col, signingKey: []byte("secret"), audit: seen}
	ctx := context.WithValue(context.Background(), inf.InfinimeshRootCtxKey, true)
	ctx = context.WithValue(ctx, inf.InfinimeshAccountCtxKey, "root")

	_, err := ctrl.Create(ctx, connect.NewRequest(&pb.Plugin{Title: "Hawk", Kind: pb.PluginKind_OTA, Provider: "hawkbit", Namespace: &ns}))
	require.NoError(t, err)
	_, err = ctrl.Delete(ctx, connect.NewRequest(&pb.Plugin{Uuid: "plug-1"}))
	require.NoError(t, err)
	require.Equal(t, []string{"plugin_create root plant", "plugin_delete root plant"}, seen.rows)
}

type recordedDeny struct {
	id  string
	ttl time.Duration
	err error
}

func (d *recordedDeny) Deny(_ context.Context, id string, ttl time.Duration) error {
	d.id = id
	d.ttl = ttl
	return d.err
}

func TestRotateTokenDeniesThePreviousJTI(t *testing.T) {
	ns := "plant"
	col := driver_mocks.NewMockCollection(t)
	col.On("ReadDocument", mock.Anything, "plug-1", mock.Anything).Run(func(args mock.Arguments) {
		plug := args.Get(2).(*pb.Plugin)
		plug.Kind = pb.PluginKind_OTA
		plug.Provider = "hawkbit"
		plug.TokenId = "old-jti"
		plug.Title = "Hawk"
		plug.Namespace = &ns
	}).Return(driver.DocumentMeta{ID: driver.NewDocumentID(schema.PLUGINS_COL, "plug-1")}, nil)
	col.On("UpdateDocument", mock.Anything, "plug-1", mock.MatchedBy(func(patch map[string]interface{}) bool {
		_, stored := patch["token"]
		id, _ := patch["token_id"].(string)
		return !stored && id != "" && id != "old-jti"
	})).Return(driver.DocumentMeta{}, nil)
	deny := &recordedDeny{}
	ctrl := &PluginsController{log: zap.NewNop(), col: col, signingKey: []byte("secret"), denylist: deny}
	ctx := context.WithValue(context.Background(), inf.InfinimeshRootCtxKey, true)

	res, err := ctrl.RotateToken(ctx, connect.NewRequest(&pb.Plugin{Uuid: "plug-1"}))
	require.NoError(t, err)
	require.Equal(t, "plug-1", res.Msg.GetUuid())
	require.Equal(t, pb.PluginKind_OTA, res.Msg.GetKind())
	require.Equal(t, "hawkbit", res.Msg.GetProvider())
	require.Equal(t, "plant", res.Msg.GetNamespace())
	require.NotEmpty(t, res.Msg.GetToken())
	require.NotEqual(t, "old-jti", res.Msg.GetTokenId())
	parsed, err := jwt.Parse(res.Msg.GetToken(), func(token *jwt.Token) (interface{}, error) {
		return []byte("secret"), nil
	})
	require.NoError(t, err)
	claims := parsed.Claims.(jwt.MapClaims)
	require.Equal(t, "plug-1", claims[inf.INFINIMESH_PLUGIN_CLAIM])
	require.Equal(t, "hawkbit", claims[inf.INFINIMESH_OTA_PROVIDER_CLAIM])
	require.Equal(t, res.Msg.GetTokenId(), claims[inf.INFINIMESH_JTI_CLAIM])
	require.Equal(t, "old-jti", deny.id)
	require.GreaterOrEqual(t, deny.ttl, 365*24*time.Hour)
}

func TestRotateTokenStillReturnsTheBearerWhenDenyFails(t *testing.T) {
	col := driver_mocks.NewMockCollection(t)
	col.On("ReadDocument", mock.Anything, "plug-1", mock.Anything).Run(func(args mock.Arguments) {
		plug := args.Get(2).(*pb.Plugin)
		plug.Kind = pb.PluginKind_OTA
		plug.Provider = "mender"
		plug.TokenId = "old-jti"
	}).Return(driver.DocumentMeta{ID: driver.NewDocumentID(schema.PLUGINS_COL, "plug-1")}, nil)
	col.On("UpdateDocument", mock.Anything, "plug-1", mock.Anything).Return(driver.DocumentMeta{}, nil)
	ctrl := &PluginsController{
		log: zap.NewNop(), col: col, signingKey: []byte("secret"),
		denylist: &recordedDeny{err: errors.New("redis down")},
	}
	ctx := context.WithValue(context.Background(), inf.InfinimeshRootCtxKey, true)
	res, err := ctrl.RotateToken(ctx, connect.NewRequest(&pb.Plugin{Uuid: "plug-1"}))
	require.NoError(t, err)
	require.NotEmpty(t, res.Msg.GetToken())
	require.NotEqual(t, "old-jti", res.Msg.GetTokenId())
}

func TestRotateTokenRejectsNonRootMissingAndEmbedded(t *testing.T) {
	col := driver_mocks.NewMockCollection(t)
	ctrl := &PluginsController{log: zap.NewNop(), col: col, signingKey: []byte("secret")}
	_, err := ctrl.RotateToken(context.Background(), connect.NewRequest(&pb.Plugin{Uuid: "plug-1"}))
	require.EqualError(t, err, "permission_denied: Not enough access rights to rotate the plugin token")

	ctx := context.WithValue(context.Background(), inf.InfinimeshRootCtxKey, true)
	_, err = ctrl.RotateToken(ctx, connect.NewRequest(&pb.Plugin{}))
	require.EqualError(t, err, "invalid_argument: No Plugin UUID has been provided")

	col.On("ReadDocument", mock.Anything, "missing", mock.Anything).Return(driver.DocumentMeta{}, errors.New("missing"))
	_, err = ctrl.RotateToken(ctx, connect.NewRequest(&pb.Plugin{Uuid: "missing"}))
	require.EqualError(t, err, "not_found: Plugin not found")

	col.On("ReadDocument", mock.Anything, "emb", mock.Anything).Run(func(args mock.Arguments) {
		plug := args.Get(2).(*pb.Plugin)
		plug.Kind = pb.PluginKind_EMBEDDED
		plug.Title = "Page"
	}).Return(driver.DocumentMeta{ID: driver.NewDocumentID(schema.PLUGINS_COL, "emb")}, nil)
	_, err = ctrl.RotateToken(ctx, connect.NewRequest(&pb.Plugin{Uuid: "emb"}))
	require.EqualError(t, err, "invalid_argument: only an OTA plugin has an addon token")
}

func TestGetClearsPluginToken(t *testing.T) {
	col := driver_mocks.NewMockCollection(t)
	col.On("ReadDocument", mock.Anything, "plug-1", mock.Anything).Run(func(args mock.Arguments) {
		plug := args.Get(2).(*pb.Plugin)
		plug.Token = "secret"
		plug.TokenId = "jti"
		plug.Public = true
		plug.Kind = pb.PluginKind_OTA
		plug.Title = "Hawk"
	}).Return(driver.DocumentMeta{Key: "plug-1"}, nil)
	ctrl := &PluginsController{log: zap.NewNop(), col: col}
	res, err := ctrl.Get(context.Background(), connect.NewRequest(&pb.Plugin{Uuid: "plug-1"}))
	require.NoError(t, err)
	require.Empty(t, res.Msg.GetToken())
	require.Equal(t, "jti", res.Msg.GetTokenId())
}
