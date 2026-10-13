package graph

import (
	"context"
	"testing"

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
