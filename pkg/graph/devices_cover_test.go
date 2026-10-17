package graph

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/arangodb/go-driver"
	"github.com/golang-jwt/jwt/v4"
	driver_mocks "github.com/infinimesh/core/mocks/github.com/arangodb/go-driver"
	"github.com/infinimesh/core/pkg/graph/schema"
	inf "github.com/infinimesh/core/pkg/shared"
	pb "github.com/infinimesh/proto/node"
	"github.com/infinimesh/proto/node/access"
	devpb "github.com/infinimesh/proto/node/devices"
	nspb "github.com/infinimesh/proto/node/namespaces"
	shadowpb "github.com/infinimesh/proto/shadow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newDev(repo flexRepo) *DevicesController {
	return &DevicesController{
		InfinimeshBaseController: InfinimeshBaseController{log: zap.NewNop()},
		ica_repo:                 repo,
		SIGNING_KEY:              []byte("secret"),
	}
}

func testCertPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestDeviceReadsListsAndTokens(t *testing.T) {
	ctrl := newDev(flexRepo{level: access.Level_ADMIN, ns: "plant"})
	scope := &recordScope{}
	ctrl.scope = scope
	ctrl.audit = &auditCapture{}
	col := driver_mocks.NewMockCollection(t)
	ctrl.col = col
	col.On("ReplaceDocument", mock.Anything, "d", mock.Anything).Return(docMeta(schema.DEVICES_COL, "d"), nil).Once()
	updated, err := ctrl.Update(accCtx("root"), connect.NewRequest(&devpb.Device{Uuid: "d", Title: "Pump", Tags: []string{"a"}}))
	require.NoError(t, err)
	assert.Equal(t, "Pump", updated.Msg.GetTitle())
	assert.NotEmpty(t, scope.ev)

	col.On("ReplaceDocument", mock.Anything, "d", mock.Anything).Return(driver.DocumentMeta{}, errors.New("rep")).Once()
	_, err = ctrl.Update(accCtx("root"), connect.NewRequest(&devpb.Device{Uuid: "d", Title: "Pump"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	_, err = ctrl.Update(accCtx("root"), connect.NewRequest(&devpb.Device{Uuid: "d"}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{level: access.Level_READ, ns: "plant"}
	_, err = ctrl.Update(accCtx("root"), connect.NewRequest(&devpb.Device{Uuid: "d", Title: "Pump"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{getErr: errors.New("gone")}
	col.On("ReadDocument", mock.Anything, "d", mock.Anything).Return(driver.DocumentMeta{}, errors.New("no")).Once()
	_, err = ctrl.Update(accCtx("root"), connect.NewRequest(&devpb.Device{Uuid: "d", Title: "Pump"}))
	assert.Error(t, err)

	ctrl.ica_repo = flexRepo{level: access.Level_ADMIN}
	col.On("ReplaceDocument", mock.Anything, "d", mock.Anything).Return(driver.DocumentMeta{}, errors.New("cfg")).Once()
	_, err = ctrl.PatchConfig(accCtx("root"), connect.NewRequest(&devpb.Device{Uuid: "d"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{level: access.Level_READ}
	_, err = ctrl.PatchConfig(accCtx("root"), connect.NewRequest(&devpb.Device{Uuid: "d"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	ctrl.ica_repo = flexRepo{level: access.Level_MGMT, toggleErr: errors.New("t")}
	_, err = ctrl.Toggle(accCtx("root"), connect.NewRequest(&devpb.Device{Uuid: "d"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{level: access.Level_READ}
	_, err = ctrl.Toggle(accCtx("root"), connect.NewRequest(&devpb.Device{Uuid: "d"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{level: access.Level_MGMT}
	toggled, err := ctrl.Toggle(accCtx("root"), connect.NewRequest(&devpb.Device{Uuid: "d"}))
	require.NoError(t, err)
	assert.Equal(t, "d", toggled.Msg.GetUuid())

	ctrl.ica_repo = flexRepo{level: access.Level_READ}
	_, err = ctrl.ToggleBasic(accCtx("root"), connect.NewRequest(&devpb.Device{Uuid: "d"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{level: access.Level_ADMIN, toggleErr: errors.New("t")}
	_, err = ctrl.ToggleBasic(accCtx("root"), connect.NewRequest(&devpb.Device{Uuid: "d"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{level: access.Level_ADMIN}
	_, err = ctrl.ToggleBasic(accCtx("root"), connect.NewRequest(&devpb.Device{Uuid: "d"}))
	require.NoError(t, err)

	_, err = ctrl.List(context.Background(), connect.NewRequest(&pb.QueryRequest{}))
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{listErr: errors.New("q")}
	_, err = ctrl.List(accCtx("root"), connect.NewRequest(&pb.QueryRequest{Namespace: "plant"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{list: readErr(errors.New("row"))}
	_, err = ctrl.List(accCtx("root"), connect.NewRequest(&pb.QueryRequest{}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{list: &scriptCursor{fn: []func(any) (driver.DocumentMeta, error){
		func(result any) (driver.DocumentMeta, error) {
			dev := result.(*devpb.Device)
			dev.Access = &access.Access{Level: access.Level_READ}
			dev.Certificate = &devpb.Certificate{PemData: "pem"}
			return docMeta(schema.DEVICES_COL, "low"), nil
		},
		func(result any) (driver.DocumentMeta, error) {
			dev := result.(*devpb.Device)
			dev.Access = &access.Access{Level: access.Level_ADMIN}
			dev.Certificate = &devpb.Certificate{PemData: "pem"}
			return docMeta(schema.DEVICES_COL, "high"), nil
		},
	}}}
	listed, err := ctrl.List(accCtx("root"), connect.NewRequest(&pb.QueryRequest{PageSize: 10}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.Devices, 2)
	assert.Nil(t, listed.Msg.Devices[0].Certificate)
	assert.NotNil(t, listed.Msg.Devices[1].Certificate)

	_, err = ctrl.GetByToken(context.Background(), connect.NewRequest(&devpb.Device{Uuid: "d"}))
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	scoped := context.WithValue(context.Background(), inf.InfinimeshDevicesCtxKey, map[string]access.Level{"other": access.Level_READ})
	_, err = ctrl.GetByToken(scoped, connect.NewRequest(&devpb.Device{Uuid: "d"}))
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	scoped = context.WithValue(context.Background(), inf.InfinimeshDevicesCtxKey, map[string]access.Level{"d": access.Level_READ})
	col.On("ReadDocument", mock.Anything, "d", mock.Anything).Return(driver.DocumentMeta{}, errors.New("no")).Once()
	_, err = ctrl.GetByToken(scoped, connect.NewRequest(&devpb.Device{Uuid: "d"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	db := driver_mocks.NewMockDatabase(t)
	ctrl.db = db
	col.On("ReadDocument", mock.Anything, "d", mock.Anything).Run(func(args mock.Arguments) {
		dev := args.Get(2).(*devpb.Device)
		dev.Title = "Pump"
	}).Return(docMeta(schema.DEVICES_COL, "d"), nil).Once()
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		ns := result.(*nspb.Namespace)
		ns.Quota = &nspb.Quota{DeviceMessagesPerSecond: 1, NamespaceMessagesPerSecond: 2}
	}), nil).Once()
	byTok, err := ctrl.GetByToken(scoped, connect.NewRequest(&devpb.Device{Uuid: "d"}))
	require.NoError(t, err)
	assert.Equal(t, "k", byTok.Msg.GetAccess().GetNamespace())
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("rates")).Once()
	dps, nps, nsID := ctrl.MessageRates(context.Background(), "d")
	assert.Equal(t, 0.0, dps)
	assert.Equal(t, 0.0, nps)
	assert.Empty(t, nsID)

	present, err := ctrl.Present(context.Background(), "")
	assert.False(t, present)
	assert.NoError(t, err)
	col.On("ReadDocument", mock.Anything, "d", mock.Anything).Return(docMeta(schema.DEVICES_COL, "d"), nil).Once()
	present, err = ctrl.Present(context.Background(), "d")
	assert.True(t, present)
	assert.NoError(t, err)
	col.On("ReadDocument", mock.Anything, "gone", mock.Anything).Return(driver.DocumentMeta{}, driver.ArangoError{HasError: true, Code: 404}).Once()
	present, err = ctrl.Present(context.Background(), "gone")
	assert.False(t, present)
	assert.NoError(t, err)
	col.On("ReadDocument", mock.Anything, "bad", mock.Anything).Return(driver.DocumentMeta{}, errors.New("db")).Once()
	_, err = ctrl.Present(context.Background(), "bad")
	assert.Error(t, err)

	_, err = ctrl.DeviceNamespace(context.Background(), "")
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q")).Once()
	_, err = ctrl.DeviceNamespace(context.Background(), "d")
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		p := result.(*string)
		*p = ""
	}), nil).Once()
	_, err = ctrl.DeviceNamespace(context.Background(), "d")
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		p := result.(*string)
		*p = "plant"
	}), nil).Once()
	ns, err := ctrl.DeviceNamespace(context.Background(), "d")
	require.NoError(t, err)
	assert.Equal(t, "plant", ns)

	_, err = (&DevicesController{}).ListForAddon(context.Background(), "", "")
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q")).Once()
	_, err = ctrl.ListForAddon(context.Background(), "plant", "")
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(readErr(errors.New("row")), nil).Once()
	_, err = ctrl.ListForAddon(context.Background(), "plant", "a")
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&scriptCursor{fn: []func(any) (driver.DocumentMeta, error){
		func(result any) (driver.DocumentMeta, error) {
			dev := result.(*devpb.Device)
			dev.Uuid = "skip"
			dev.Tags = []string{"b"}
			return docMeta(schema.DEVICES_COL, "skip"), nil
		},
		func(result any) (driver.DocumentMeta, error) {
			dev := result.(*devpb.Device)
			dev.Uuid = "keep"
			dev.Tags = []string{"a"}
			return docMeta(schema.DEVICES_COL, "keep"), nil
		},
	}}, nil).Once()
	addon, err := ctrl.ListForAddon(context.Background(), "", "a")
	require.NoError(t, err)
	require.Len(t, addon, 1)
	assert.Equal(t, "keep", addon[0].GetUuid())
	assert.True(t, hasTag([]string{"a"}, "a"))
	assert.False(t, hasTag(nil, "a"))
	assert.False(t, fingerprintListed(nil, []byte{1}))
	assert.True(t, fingerprintListed([][]byte{{1}}, []byte{1}))

	scope.err = errors.New("pub")
	ctrl.publishTag(context.Background(), shadowpb.ShadowEvent_TAG_ADDED, &devpb.Device{Uuid: "d", Tags: []string{"a"}}, "a")
	ctrl.publishTag(context.Background(), shadowpb.ShadowEvent_TAG_ADDED, &devpb.Device{Uuid: "d"}, "")
	ctrl.publishTag(context.Background(), shadowpb.ShadowEvent_TAG_ADDED, nil, "a")
	ctrl.publishScope(context.Background(), shadowpb.ShadowEvent_DEVICE_ADDED, &devpb.Device{Uuid: "d"}, "", "")
	raw, err := ctrl._MakeToken(map[string]access.Level{"d": access.Level_READ}, 0, false)
	require.NoError(t, err)
	assert.NotEmpty(t, raw)
	stream, err := ctrl._MakeStreamToken("acc", "", 0)
	require.NoError(t, err)
	assert.NotEmpty(t, stream)
	assert.Empty(t, jwtID("not-a-jwt"))
	assert.Error(t, sha256Fingerprint(nil))
	assert.Error(t, sha256Fingerprint(&devpb.Certificate{PemData: "nope"}))
	assert.Error(t, sha256Fingerprint(&devpb.Certificate{PemData: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1}}))}))
}

func TestDeviceFingerprintJoinsAndMove(t *testing.T) {
	ctrl := newDev(flexRepo{level: access.Level_ADMIN, ns: "old"})
	db := driver_mocks.NewMockDatabase(t)
	ctrl.db = db
	ctrl.audit = &auditCapture{}
	scope := &recordScope{}
	ctrl.scope = scope
	pemData := testCertPEM(t)
	cert := &devpb.Certificate{PemData: pemData}
	require.NoError(t, sha256Fingerprint(cert))

	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q")).Once()
	_, err := ctrl.GetByFingerprint(accCtx("root"), connect.NewRequest(&devpb.GetByFingerprintRequest{Fingerprint: cert.Fingerprint}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&scriptCursor{}, nil).Once()
	_, err = ctrl.GetByFingerprint(accCtx("root"), connect.NewRequest(&devpb.GetByFingerprintRequest{Fingerprint: cert.Fingerprint}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(readErr(errors.New("row")), nil).Once()
	_, err = ctrl.GetByFingerprint(accCtx("root"), connect.NewRequest(&devpb.GetByFingerprintRequest{Fingerprint: cert.Fingerprint}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		dev := result.(*devpb.Device)
		dev.Certificate = &devpb.Certificate{Revoked: true, Fingerprint: cert.Fingerprint}
	}), nil).Once()
	_, err = ctrl.GetByFingerprint(accCtx("root"), connect.NewRequest(&devpb.GetByFingerprintRequest{Fingerprint: cert.Fingerprint}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		dev := result.(*devpb.Device)
		dev.Certificate = &devpb.Certificate{Fingerprint: []byte{9}}
	}), nil).Once()
	_, err = ctrl.GetByFingerprint(accCtx("root"), connect.NewRequest(&devpb.GetByFingerprintRequest{Fingerprint: cert.Fingerprint}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		dev := result.(*devpb.Device)
		dev.Title = "Pump"
		dev.Certificate = &devpb.Certificate{Fingerprint: append([]byte(nil), cert.Fingerprint...)}
	}), nil).Once()
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&scriptCursor{}, nil).Once()
	found, err := ctrl.GetByFingerprint(accCtx("root"), connect.NewRequest(&devpb.GetByFingerprintRequest{Fingerprint: cert.Fingerprint}))
	require.NoError(t, err)
	assert.NotEmpty(t, found.Msg.GetToken())

	ctrl.ica_repo = flexRepo{level: access.Level_READ}
	_, err = ctrl.Joins(accCtx("root"), connect.NewRequest(&devpb.Device{Uuid: "d"}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{level: access.Level_ADMIN}
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("joins")).Once()
	_, err = ctrl.Joins(accCtx("root"), connect.NewRequest(&devpb.Device{Uuid: "d"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(readErr(errors.New("row")), nil).Once()
	_, err = ctrl.Joins(accCtx("root"), connect.NewRequest(&devpb.Device{Uuid: "d"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		node := result.(*access.Node)
		node.Node = "Accounts/a"
	}), nil).Once()
	joins, err := ctrl.Joins(accCtx("root"), connect.NewRequest(&devpb.Device{Uuid: "d"}))
	require.NoError(t, err)
	assert.Equal(t, "Accounts/a", joins.Msg.Nodes[0].GetNode())

	ctrl.ica_repo = flexRepo{getErr: errors.New("gone")}
	_, err = ctrl.Join(accCtx("root"), connect.NewRequest(&pb.JoinGeneralRequest{Node: "d", Join: "Accounts/a", Access: access.Level_READ}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{level: access.Level_READ, role: access.Role_UNSET}
	_, err = ctrl.Join(accCtx("root"), connect.NewRequest(&pb.JoinGeneralRequest{Node: "d", Join: "Accounts/a", Access: access.Level_READ}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{level: access.Level_ADMIN, role: access.Role_OWNER}
	_, err = ctrl.Join(accCtx("root"), connect.NewRequest(&pb.JoinGeneralRequest{Node: "d", Join: "Nope/a", Access: access.Level_READ}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = ctrl.Join(accCtx("root"), connect.NewRequest(&pb.JoinGeneralRequest{Node: "d", Join: "Accounts/a", Access: access.Level_ADMIN}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	jr := &joinErrRepo{flexRepo: flexRepo{level: access.Level_ADMIN, role: access.Role_OWNER}}
	ctrl.ica_repo = jr
	_, err = ctrl.Join(accCtx("root"), connect.NewRequest(&pb.JoinGeneralRequest{Node: "d", Join: "Accounts/a", Access: access.Level_READ}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	col := driver_mocks.NewMockCollection(t)
	ctrl.col = col
	ctrl.ns2dev = col
	ns := "old"
	ctrl.ica_repo = flexRepo{level: access.Level_ADMIN, ns: ns}
	col.On("ReadDocument", mock.Anything, "d", mock.Anything).Return(docMeta(schema.DEVICES_COL, "d"), nil).Maybe()
	col.On("RemoveDocument", mock.Anything, "d").Return(docMeta(schema.DEVICES_COL, "d"), nil).Once()
	deleted, err := ctrl.delete(accCtx("root"), connect.NewRequest(&devpb.Device{Uuid: "d"}), false)
	require.NoError(t, err)
	assert.NotNil(t, deleted)
	col.On("ReadDocument", mock.Anything, "hidden", mock.Anything).Return(docMeta(schema.DEVICES_COL, "hidden"), nil).Once()
	err = ctrl.deviceReadErr(accCtx("root"), "hidden")
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	ctrl.ica_repo = flexRepo{getErr: errors.New("load"), ns: "old"}
	_, err = ctrl.Move(context.Background(), connect.NewRequest(&pb.MoveRequest{Uuid: "d", Namespace: "next"}))
	require.NoError(t, err)
	ctrl.ica_repo = flexRepo{level: access.Level_ADMIN, ns: "old", moveErr: nil}
	moved, err := ctrl.Move(accCtx("root"), connect.NewRequest(&pb.MoveRequest{Uuid: "d", Namespace: "next"}))
	require.NoError(t, err)
	assert.NotNil(t, moved)

	deny := &memDeny{}
	ctrl.SetDenylist(deny)
	_, err = ctrl.RevokeDevicesToken(context.Background(), connect.NewRequest(&pb.RevokeDeviceTokenRequest{}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"exp": float64(time.Now().Add(time.Hour).Unix()), inf.INFINIMESH_JTI_CLAIM: "jti"}).SignedString(ctrl.SIGNING_KEY)
	require.NoError(t, err)
	_, err = ctrl.RevokeDevicesToken(context.Background(), connect.NewRequest(&pb.RevokeDeviceTokenRequest{Token: "bad"}))
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	revoked, err := ctrl.RevokeDevicesToken(context.Background(), connect.NewRequest(&pb.RevokeDeviceTokenRequest{Token: token}))
	require.NoError(t, err)
	assert.NotNil(t, revoked)
	deny.err = errors.New("deny")
	_, err = ctrl.RevokeDevicesToken(context.Background(), connect.NewRequest(&pb.RevokeDeviceTokenRequest{Id: "jti"}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
}

type joinErrRepo struct {
	flexRepo
	n int
}

func (j *joinErrRepo) AccessLevelAndGet(ctx context.Context, log *zap.Logger, account *Account, node InfinimeshGraphNode) error {
	j.n++
	if j.n > 1 {
		return errors.New("object")
	}
	return j.flexRepo.AccessLevelAndGet(ctx, log, account, node)
}
