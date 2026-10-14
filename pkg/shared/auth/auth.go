/*
Copyright © 2021-2023 Infinite Devices GmbH

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package auth

import (
	"context"
	"errors"
	"github.com/infinimesh/core/pkg/apierr"
	"strings"

	"connectrpc.com/connect"
	"github.com/go-redis/redis/v8"
	"github.com/golang-jwt/jwt/v4"
	"go.uber.org/zap"
	"google.golang.org/grpc/metadata"

	"github.com/infinimesh/proto/node/access"
	"github.com/infinimesh/proto/node/nodeconnect"
	"github.com/infinimesh/proto/pairing/pairingconnect"

	"github.com/infinimesh/core/pkg/sessions"
	infinimesh "github.com/infinimesh/core/pkg/shared"
)

type AuthInterceptor interface {
	connect.Interceptor

	MakeToken(string) (string, error)
	ConnectStandardAuthMiddleware(context.Context, []byte, string) (context.Context, bool, error)
	ConnectDeviceAuthMiddleware(context.Context, []byte, string) (context.Context, bool, error)
	ConnectShadowStreamMiddleware(context.Context, []byte, string) (context.Context, bool, error)

	SetSessionsHandler(sessions.SessionsHandler)
}

type interceptor struct {
	log         *zap.Logger
	rdb         *redis.Client
	jwt         JWTHandler
	sessions    sessions.SessionsHandler
	signing_key []byte
}

type middleware func(context.Context, []byte, string) (context.Context, bool, error)

func NewAuthInterceptor(log *zap.Logger, _rdb *redis.Client, _jwth JWTHandler, signing_key []byte) *interceptor {
	jwth := _jwth
	if jwth == nil {
		jwth = defaultJWTHandler{}
	}

	sessions := sessions.NewSessionsHandlerModule(_rdb).Handler()

	return &interceptor{
		log:         log.Named("AuthInterceptor"),
		rdb:         _rdb,
		jwt:         jwth,
		sessions:    sessions,
		signing_key: signing_key,
	}
}

func (i *interceptor) MakeToken(account string) (string, error) {
	claims := jwt.MapClaims{}
	claims[infinimesh.INFINIMESH_ACCOUNT_CLAIM] = account
	claims[infinimesh.INFINIMESH_ROOT_CLAIM] = true
	claims[infinimesh.INFINIMESH_NOSESSION_CLAIM] = true

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(i.signing_key)
}

func SelectMiddleware(i *interceptor, procedure string) middleware {
	var middleware middleware

	switch {
	case procedure == nodeconnect.DevicesServiceGetByTokenProcedure:
		middleware = i.ConnectDeviceAuthMiddleware
	case procedure == nodeconnect.DevicesServiceClaimProcedure:
		middleware = i.ConnectBlankMiddleware
	case procedure == nodeconnect.ShadowServiceAssignOTAProcedure:
		middleware = i.ConnectAssignOTAMiddleware
	case procedure == nodeconnect.ShadowServiceRequestOTAProcedure:
		middleware = i.ConnectRequestOTAMiddleware
	case procedure == nodeconnect.ShadowServicePatchManyProcedure:
		middleware = i.ConnectAccountOrPluginMiddleware
	case procedure == nodeconnect.ShadowServicePatchProcedure:
		middleware = i.ConnectShadowPatchMiddleware
	case strings.HasPrefix(procedure, "/"+nodeconnect.ShadowServiceName):
		middleware = i.ConnectDeviceAuthMiddleware
	default:
		middleware = i.ConnectStandardAuthMiddleware
	}

	return middleware
}

func (i *interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return connect.UnaryFunc(func(
		ctx context.Context,
		req connect.AnyRequest,
	) (connect.AnyResponse, error) {
		procedure := req.Spec().Procedure
		header := req.Header().Get("Authorization")

		segments := strings.Split(header, " ")
		if len(segments) != 2 {
			segments = []string{"", ""}
		}

		i.log.Debug("Authorization Header", zap.String("header", header))

		middleware := SelectMiddleware(i, procedure)

		ctx, log_activity, err := middleware(ctx, i.signing_key, segments[1])
		if procedure != nodeconnect.AccountsServiceTokenProcedure && procedure != nodeconnect.DevicesServiceClaimProcedure && err != nil {
			return nil, err
		}

		if log_activity {
			go i.connectHandleLogActivity(ctx)
		}

		return next(ctx, req)
	})
}

func (i *interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	i.log.Debug("WrapStreamingClient")
	return next
}
func (i *interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	i.log.Debug("Setup Wrap Streaming Handler")
	return func(ctx context.Context, shc connect.StreamingHandlerConn) error {
		procedure := shc.Spec().Procedure
		header := shc.RequestHeader().Get("Authorization")

		segments := strings.Split(header, " ")
		if len(segments) != 2 {
			segments = []string{"", ""}
		}

		var middleware middleware

		switch {
		case procedure == nodeconnect.ShadowServiceStreamShadowProcedure || procedure == nodeconnect.ShadowServiceStreamShadowSyncProcedure:
			middleware = i.ConnectShadowStreamMiddleware
		case strings.HasPrefix(procedure, "/"+nodeconnect.ShadowServiceName):
			middleware = i.ConnectDeviceAuthMiddleware
		case procedure == pairingconnect.PairingServiceConnectProcedure:
			middleware = i.ConnectBlankMiddleware
		default:
			middleware = i.ConnectStandardAuthMiddleware
		}
		i.log.Debug("Authorization Header", zap.String("header", header))

		ctx, log_activity, err := middleware(ctx, i.signing_key, segments[1])
		if err != nil {
			return err
		}

		if log_activity {
			go i.connectHandleLogActivity(ctx)
		}

		return next(ctx, shc)
	}
}

func (i *interceptor) ConnectAssignOTAMiddleware(ctx context.Context, signingKey []byte, tokenString string) (context.Context, bool, error) {
	token, err := connectValidateToken(i.jwt, signingKey, tokenString)
	if err != nil {
		return ctx, false, apierr.Code(connect.CodeUnauthenticated, "Invalid token format")
	}
	if token[infinimesh.INFINIMESH_DEVICES_CLAIM] != nil {
		if _, ok := token[infinimesh.INFINIMESH_ACCOUNT_CLAIM].(string); !ok {
			if id, _ := token[infinimesh.INFINIMESH_PLUGIN_CLAIM].(string); id == "" {
				return ctx, false, apierr.Code(connect.CodeUnauthenticated, "bulk assign needs an account or addon token")
			}
		}
	}
	if id, _ := token[infinimesh.INFINIMESH_PLUGIN_CLAIM].(string); id != "" {
		return i.pluginAuth(ctx, token)
	}
	return i.ConnectStandardAuthMiddleware(ctx, signingKey, tokenString)
}

func (i *interceptor) ConnectRequestOTAMiddleware(ctx context.Context, signingKey []byte, tokenString string) (context.Context, bool, error) {
	token, err := connectValidateToken(i.jwt, signingKey, tokenString)
	if err != nil {
		return ctx, false, apierr.Code(connect.CodeUnauthenticated, "Invalid token format")
	}
	if id, _ := token[infinimesh.INFINIMESH_PLUGIN_CLAIM].(string); id != "" {
		return ctx, false, apierr.Code(connect.CodePermissionDenied, "ota request needs an account token")
	}
	if token[infinimesh.INFINIMESH_DEVICES_CLAIM] != nil {
		if _, ok := token[infinimesh.INFINIMESH_ACCOUNT_CLAIM].(string); !ok {
			return ctx, false, apierr.Code(connect.CodeUnauthenticated, "ota request needs an account token")
		}
	}
	return i.ConnectStandardAuthMiddleware(ctx, signingKey, tokenString)
}

func (i *interceptor) ConnectAccountOrPluginMiddleware(ctx context.Context, signingKey []byte, tokenString string) (context.Context, bool, error) {
	token, err := connectValidateToken(i.jwt, signingKey, tokenString)
	if err != nil {
		return ctx, false, apierr.Code(connect.CodeUnauthenticated, "Invalid token format")
	}
	if id, _ := token[infinimesh.INFINIMESH_PLUGIN_CLAIM].(string); id != "" {
		return i.pluginAuth(ctx, token)
	}
	return i.ConnectStandardAuthMiddleware(ctx, signingKey, tokenString)
}

func (i *interceptor) ConnectShadowPatchMiddleware(ctx context.Context, signingKey []byte, tokenString string) (context.Context, bool, error) {
	token, err := connectValidateToken(i.jwt, signingKey, tokenString)
	if err != nil {
		return ctx, false, apierr.Code(connect.CodeUnauthenticated, "Invalid token format")
	}
	if token[infinimesh.INFINIMESH_DEVICES_CLAIM] != nil {
		return i.ConnectDeviceAuthMiddleware(ctx, signingKey, tokenString)
	}
	if id, _ := token[infinimesh.INFINIMESH_PLUGIN_CLAIM].(string); id != "" {
		return i.pluginAuth(ctx, token)
	}
	return i.ConnectStandardAuthMiddleware(ctx, signingKey, tokenString)
}

func (i *interceptor) pluginAuth(ctx context.Context, token jwt.MapClaims) (context.Context, bool, error) {
	if stream, _ := token[infinimesh.INFINIMESH_STREAM_CLAIM].(bool); stream {
		return ctx, false, apierr.Code(connect.CodePermissionDenied, "streaming token is read-only")
	}
	id, _ := token[infinimesh.INFINIMESH_PLUGIN_CLAIM].(string)
	provider, _ := token[infinimesh.INFINIMESH_OTA_PROVIDER_CLAIM].(string)
	jti, _ := token[infinimesh.INFINIMESH_JTI_CLAIM].(string)
	switch provider {
	case "hawkbit", "mender", "webhook":
	default:
		return ctx, false, apierr.Code(connect.CodeUnauthenticated, "Invalid token format")
	}
	if id == "" || jti == "" {
		return ctx, false, apierr.Code(connect.CodeUnauthenticated, "Invalid token format")
	}
	if i.rdb != nil {
		denied, err := NewRedisDenylist(i.rdb).Denied(ctx, jti)
		if err != nil {
			return ctx, false, apierr.Code(connect.CodeInternal, "Failed to check token revocation")
		}
		if denied {
			return ctx, false, apierr.New(connect.CodeUnauthenticated, apierr.ReasonTokenRevoked, "token is revoked")
		}
	}
	ctx = context.WithValue(ctx, infinimesh.InfinimeshPluginCtxKey, id)
	ctx = context.WithValue(ctx, infinimesh.InfinimeshOTAProviderCtxKey, provider)
	ctx = context.WithValue(ctx, infinimesh.InfinimeshJTICtxKey, jti)
	return ctx, false, nil
}

func (i *interceptor) ConnectStandardAuthMiddleware(_ctx context.Context, signingKey []byte, tokenString string) (ctx context.Context, log_activity bool, err error) {
	ctx = _ctx

	log := i.log.Named("StandardAuthMiddleware")

	token, err := connectValidateToken(i.jwt, signingKey, tokenString)
	if err != nil {
		err = apierr.Code(connect.CodeUnauthenticated, "Invalid token format")
		return
	}
	log.Debug("Validated token", zap.Any("claims", token))

	if stream, _ := token[infinimesh.INFINIMESH_STREAM_CLAIM].(bool); stream {
		err = apierr.Code(connect.CodePermissionDenied, "streaming token is read-only")
		return
	}

	account := token[infinimesh.INFINIMESH_ACCOUNT_CLAIM]
	if account == nil {
		err = apierr.Code(connect.CodeUnauthenticated, "Invalid token format: no requestor ID")
		return
	}
	uuid, ok := account.(string)
	if !ok {
		err = apierr.Code(connect.CodeUnauthenticated, "Invalid token format: requestor ID isn't string")
		return
	}

	if token[infinimesh.INFINIMESH_NOSESSION_CLAIM] == nil {
		log_activity = true
		session := token[infinimesh.INFINIMESH_SESSION_CLAIM]
		if session == nil {
			err = apierr.Code(connect.CodeUnauthenticated, "Invalid token format: no session ID")
			return
		}
		sid, ok := session.(string)
		if !ok {
			err = apierr.Code(connect.CodeUnauthenticated, "Invalid token format: session ID isn't string")
			return
		}

		// Check if session is valid
		if err = i.sessions.Check(uuid, sid); err != nil {
			i.log.Debug("Session check failed", zap.Any("error", err))
			err = apierr.Code(connect.CodeUnauthenticated, "Session is expired, revoked or invalid")
			return
		}

		ctx = context.WithValue(ctx, infinimesh.InfinimeshSessionCtxKey, sid)
	}

	var exp int64
	if token["exp"] != nil {
		exp = int64(token["exp"].(float64))
	}

	ctx = context.WithValue(ctx, infinimesh.InfinimeshAccountCtxKey, uuid)
	ctx = context.WithValue(ctx, infinimesh.ContextKey("exp"), exp)

	ctx = metadata.AppendToOutgoingContext(ctx, infinimesh.INFINIMESH_ACCOUNT_CLAIM, uuid)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tokenString)
	if jti, _ := token[infinimesh.INFINIMESH_JTI_CLAIM].(string); jti != "" {
		ctx = context.WithValue(ctx, infinimesh.InfinimeshJTICtxKey, jti)
		ctx = metadata.AppendToOutgoingContext(ctx, infinimesh.INFINIMESH_JTI_CLAIM, jti)
	}

	if root, ok := claimBool(token[infinimesh.INFINIMESH_ROOT_CLAIM]); ok {
		ctx = context.WithValue(ctx, infinimesh.InfinimeshRootCtxKey, root)
	}

	return
}

func (i *interceptor) ConnectBlankMiddleware(_ctx context.Context, signingKey []byte, tokenString string) (ctx context.Context, log_activity bool, err error) {
	return _ctx, false, nil
}

func (i *interceptor) ConnectShadowStreamMiddleware(ctx context.Context, signingKey []byte, tokenString string) (context.Context, bool, error) {
	token, err := connectValidateToken(i.jwt, signingKey, tokenString)
	if err != nil {
		return ctx, false, apierr.Code(connect.CodeUnauthenticated, "Invalid token format")
	}
	if token[infinimesh.INFINIMESH_DEVICES_CLAIM] != nil {
		return i.ConnectDeviceAuthMiddleware(ctx, signingKey, tokenString)
	}
	if id, _ := token[infinimesh.INFINIMESH_PLUGIN_CLAIM].(string); id != "" {
		return i.pluginAuth(ctx, token)
	}
	if stream, _ := token[infinimesh.INFINIMESH_STREAM_CLAIM].(bool); stream {
		return i.ConnectStreamTokenMiddleware(ctx, signingKey, tokenString)
	}
	return i.ConnectStandardAuthMiddleware(ctx, signingKey, tokenString)
}

func (i *interceptor) ConnectStreamTokenMiddleware(_ctx context.Context, signingKey []byte, tokenString string) (ctx context.Context, log_activity bool, err error) {
	token, err := connectValidateToken(i.jwt, signingKey, tokenString)
	if err != nil {
		return _ctx, false, apierr.Code(connect.CodeUnauthenticated, "Invalid token format")
	}
	account, ok := token[infinimesh.INFINIMESH_ACCOUNT_CLAIM].(string)
	if !ok || account == "" {
		return _ctx, false, apierr.Code(connect.CodeUnauthenticated, "Invalid token format: no requestor ID")
	}
	ctx = context.WithValue(_ctx, infinimesh.InfinimeshAccountCtxKey, account)
	ctx = context.WithValue(ctx, infinimesh.InfinimeshStreamCtxKey, true)
	if ns, ok := token[infinimesh.INFINIMESH_NAMESPACE_CLAIM].(string); ok && ns != "" {
		ctx = context.WithValue(ctx, infinimesh.InfinimeshNamespaceCtxKey, ns)
	}
	ctx = metadata.AppendToOutgoingContext(ctx, infinimesh.INFINIMESH_ACCOUNT_CLAIM, account)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tokenString)
	return ctx, false, nil
}

func (i *interceptor) ConnectDeviceAuthMiddleware(_ctx context.Context, signingKey []byte, tokenString string) (ctx context.Context, log_activity bool, err error) {
	log_activity = false
	token, err := connectValidateToken(i.jwt, signingKey, tokenString)
	if err != nil {
		return
	}
	log := i.log.Named("DeviceAuthMiddleware")
	log.Debug("Validated token", zap.Any("claims", token))

	if stream, _ := token[infinimesh.INFINIMESH_STREAM_CLAIM].(bool); stream {
		err = apierr.Code(connect.CodePermissionDenied, "streaming token is read-only")
		return
	}

	devices := token[infinimesh.INFINIMESH_DEVICES_CLAIM]
	if devices == nil {
		err = apierr.Code(connect.CodeUnauthenticated, "Invalid token format: no devices scope")
		return
	}

	ipool, ok := devices.(map[string]any)
	if !ok {
		err = apierr.Code(connect.CodeUnauthenticated, "Invalid token format: devices scope isn't a map")
		return
	}

	pool := make(map[string]access.Level, len(ipool))
	for key, value := range ipool {
		val, ok := value.(float64)
		if !ok {
			err = apierr.Codef(connect.CodeUnauthenticated, "Invalid token format: element %v is not a number", value)
			return
		}
		pool[key] = access.Level(val)
	}
	ctx = _ctx
	if jti, _ := token[infinimesh.INFINIMESH_JTI_CLAIM].(string); jti != "" {
		if i.rdb != nil {
			denied, derr := NewRedisDenylist(i.rdb).Denied(_ctx, jti)
			if derr != nil {
				err = apierr.Code(connect.CodeInternal, "Failed to check token revocation")
				return
			}
			if denied {
				err = apierr.New(connect.CodeUnauthenticated, apierr.ReasonTokenRevoked, "token is revoked")
				return
			}
		}
		ctx = context.WithValue(ctx, infinimesh.InfinimeshJTICtxKey, jti)
		ctx = metadata.AppendToOutgoingContext(ctx, infinimesh.INFINIMESH_JTI_CLAIM, jti)
	}
	if post, _ := token[infinimesh.INFINIMESH_POST_CLAIM].(bool); post {
		ctx = context.WithValue(ctx, infinimesh.InfinimeshPostCtxKey, true)
	}
	ctx = context.WithValue(ctx, infinimesh.InfinimeshDevicesCtxKey, pool)

	return
}

func claimBool(v any) (bool, bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		switch strings.ToLower(t) {
		case "true", "1":
			return true, true
		case "false", "0":
			return false, true
		}
	case float64:
		if t == 1 {
			return true, true
		}
		if t == 0 {
			return false, true
		}
	}
	return false, false
}

func connectValidateToken(jwth JWTHandler, signing_key []byte, tokenString string) (jwt.MapClaims, error) {
	token, err := jwth.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, apierr.Codef(connect.CodeUnauthenticated, "Unexpected signing method: %v", t.Header["alg"])
		}
		return signing_key, nil
	})

	if err != nil {
		return nil, err
	}

	if !token.Valid {
		return nil, errors.New("invalid token")
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok {
		return claims, nil
	}

	return nil, apierr.Code(connect.CodeUnauthenticated, "Cannot Validate Token")
}

func (i *interceptor) connectHandleLogActivity(ctx context.Context) {
	sid_ctx := ctx.Value(infinimesh.InfinimeshSessionCtxKey)
	if sid_ctx == nil {
		return
	}

	sid := sid_ctx.(string)
	req := ctx.Value(infinimesh.InfinimeshAccountCtxKey).(string)
	exp := ctx.Value(infinimesh.ContextKey("exp")).(int64)

	if err := i.sessions.LogActivity(req, sid, exp); err != nil {
		i.log.Warn("Error logging activity", zap.Any("error", err))
	}
}

func (i *interceptor) SetSessionsHandler(sessions sessions.SessionsHandler) {
	i.sessions = sessions
}
