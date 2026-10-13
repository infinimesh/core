package auth

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt/v4"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	infinimesh "github.com/infinimesh/core/pkg/shared"
	pb "github.com/infinimesh/proto/node"
	accpb "github.com/infinimesh/proto/node/accounts"
	"github.com/infinimesh/proto/node/nodeconnect"
	"github.com/infinimesh/proto/shadow"
)

type rootProbe struct {
	nodeconnect.UnimplementedAccountsServiceHandler
	root any
}

func (p *rootProbe) Get(ctx context.Context, _ *connect.Request[accpb.Account]) (*connect.Response[accpb.Account], error) {
	p.root = ctx.Value(infinimesh.InfinimeshRootCtxKey)
	return connect.NewResponse(&accpb.Account{}), nil
}

type addonProbe struct {
	nodeconnect.UnimplementedShadowServiceHandler
	plugin any
}

func (p *addonProbe) AssignOTA(ctx context.Context, _ *connect.Request[shadow.AssignOTARequest]) (*connect.Response[shadow.AssignOTAResponse], error) {
	p.plugin = ctx.Value(infinimesh.InfinimeshPluginCtxKey)
	return connect.NewResponse(&shadow.AssignOTAResponse{}), nil
}

func signClaims(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("secret"))
	require.NoError(t, err)
	return raw
}

func TestGRPCAndConnectKeepRootAndAddonClaims(t *testing.T) {
	accountsProbe := &rootProbe{}
	addon := &addonProbe{}
	router := mux.NewRouter()
	opts := connect.WithInterceptors(NewAuthInterceptor(zap.NewNop(), nil, nil, []byte("secret")))
	accountsPath, accountsHandler := nodeconnect.NewAccountsServiceHandler(accountsProbe, opts)
	shadowPath, shadowHandler := nodeconnect.NewShadowServiceHandler(addon, opts)
	router.PathPrefix(accountsPath).Handler(accountsHandler)
	router.PathPrefix(shadowPath).Handler(shadowHandler)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &http.Server{Handler: h2c.NewHandler(router, &http2.Server{}), ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(lis)
	t.Cleanup(func() { _ = srv.Close() })

	rootToken := signClaims(t, jwt.MapClaims{
		infinimesh.INFINIMESH_ACCOUNT_CLAIM:   "acc",
		infinimesh.INFINIMESH_ROOT_CLAIM:      true,
		infinimesh.INFINIMESH_NOSESSION_CLAIM: true,
	})
	rootString := signClaims(t, jwt.MapClaims{
		infinimesh.INFINIMESH_ACCOUNT_CLAIM:   "acc",
		infinimesh.INFINIMESH_ROOT_CLAIM:      "true",
		infinimesh.INFINIMESH_NOSESSION_CLAIM: true,
	})
	addonToken := signClaims(t, jwt.MapClaims{
		infinimesh.INFINIMESH_PLUGIN_CLAIM:       "plug-1",
		infinimesh.INFINIMESH_OTA_PROVIDER_CLAIM: "hawkbit",
		infinimesh.INFINIMESH_JTI_CLAIM:          "jti-1",
		infinimesh.INFINIMESH_NOSESSION_CLAIM:    true,
	})

	conn, err := grpc.Dial(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	grpcCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+rootToken)
	_, err = pb.NewAccountsServiceClient(conn).Get(grpcCtx, &accpb.Account{})
	require.NoError(t, err)
	require.Equal(t, true, accountsProbe.root)

	accountsProbe.root = nil
	grpcCtx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+rootString)
	_, err = pb.NewAccountsServiceClient(conn).Get(grpcCtx, &accpb.Account{})
	require.NoError(t, err)
	require.Equal(t, true, accountsProbe.root)

	grpcCtx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+addonToken)
	_, err = pb.NewShadowServiceClient(conn).AssignOTA(grpcCtx, &shadow.AssignOTARequest{})
	require.NoError(t, err)
	require.Equal(t, "plug-1", addon.plugin)

	accountsProbe.root = nil
	addon.plugin = nil
	httpClient := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLS: func(network, addr string, _ *tls.Config) (net.Conn, error) {
			return net.Dial(network, addr)
		},
	}}
	base := "http://" + lis.Addr().String()
	accounts := nodeconnect.NewAccountsServiceClient(httpClient, base)
	req := connect.NewRequest(&accpb.Account{})
	req.Header().Set("Authorization", "Bearer "+rootToken)
	_, err = accounts.Get(ctx, req)
	require.NoError(t, err)
	require.Equal(t, true, accountsProbe.root)

	shadowClient := nodeconnect.NewShadowServiceClient(httpClient, base, connect.WithGRPC())
	ota := connect.NewRequest(&shadow.AssignOTARequest{})
	ota.Header().Set("Authorization", "Bearer "+addonToken)
	_, err = shadowClient.AssignOTA(ctx, ota)
	require.NoError(t, err)
	require.Equal(t, "plug-1", addon.plugin)
}
