package graph

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/arangodb/go-driver"
	driver_mocks "github.com/infinimesh/core/v3/mocks/github.com/arangodb/go-driver"
	"github.com/infinimesh/core/v3/pkg/graph/schema"
	"github.com/infinimesh/proto/node/access"
	devpb "github.com/infinimesh/proto/node/devices"
	nspb "github.com/infinimesh/proto/node/namespaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type memClaims struct {
	err error
	ns  string
}

func (m memClaims) Put(context.Context, string, string, time.Duration) error { return m.err }
func (m memClaims) Take(context.Context, string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.ns, nil
}

func TestFleetCreateClaimAndCertificates(t *testing.T) {
	ctrl := newDev(flexRepo{ok: true, level: access.Level_ADMIN})
	_, err := ctrl.BulkCreate(accCtx("root"), connect.NewRequest(&devpb.BulkCreateRequest{}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{ok: false}
	_, err = ctrl.BulkCreate(accCtx("root"), connect.NewRequest(&devpb.BulkCreateRequest{Namespace: "ns", Devices: []*devpb.Device{{Title: "a"}}}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	db := driver_mocks.NewMockDatabase(t)
	nsCol := driver_mocks.NewMockCollection(t)
	devCol := driver_mocks.NewMockCollection(t)
	ctrl.db = db
	ctrl.col = devCol
	ctrl.ns2dev = devCol
	ctrl.ica_repo = flexRepo{ok: true, level: access.Level_ADMIN}
	db.On("Collection", mock.Anything, schema.NAMESPACES_COL).Return(nil, errors.New("col")).Once()
	_, err = ctrl.BulkCreate(accCtx("root"), connect.NewRequest(&devpb.BulkCreateRequest{Namespace: "ns", Devices: []*devpb.Device{{Title: "a"}}}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))

	db.On("Collection", mock.Anything, schema.NAMESPACES_COL).Return(nsCol, nil)
	nsCol.On("ReadDocument", mock.Anything, "ns", mock.Anything).Return(driver.DocumentMeta{}, errors.New("read")).Once()
	err = ctrl.allowCreate(accCtx("root"), "ns", 1)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	nsCol.On("ReadDocument", mock.Anything, "ns", mock.Anything).Run(func(args mock.Arguments) {
		doc := args.Get(2).(*nspb.Namespace)
		doc.Quota = &nspb.Quota{MaxDevices: 1}
	}).Return(docMeta(schema.NAMESPACES_COL, "ns"), nil)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		n := result.(*int)
		*n = 1
	}), nil).Once()
	err = ctrl.allowCreate(context.Background(), "ns", 1)
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("count")).Once()
	err = ctrl.allowCreate(context.Background(), "ns", 1)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(ctrl.requireNamespace(context.Background(), "ns", access.Level_READ)))

	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		n := result.(*int)
		*n = 0
	}), nil)
	_, err = ctrl.createInNamespace(context.Background(), "ns", &devpb.Device{Certificate: &devpb.Certificate{PemData: "nope"}})
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	devCol.On("CreateDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("dev")).Once()
	_, err = ctrl.createInNamespace(context.Background(), "ns", nil)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{ok: true, level: access.Level_ADMIN, linkErr: errors.New("link")}
	devCol.On("CreateDocument", mock.Anything, mock.Anything).Return(docMeta(schema.DEVICES_COL, "d1"), nil).Once()
	devCol.On("RemoveDocument", mock.Anything, "d1").Return(docMeta(schema.DEVICES_COL, "d1"), nil).Once()
	_, err = ctrl.createInNamespace(accCtx("root"), "ns", &devpb.Device{Title: "Pump"})
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))

	ctrl.ica_repo = flexRepo{ok: true, level: access.Level_ADMIN}
	ctrl.scope = &recordScope{}
	devCol.On("CreateDocument", mock.Anything, mock.Anything).Return(docMeta(schema.DEVICES_COL, "d1"), nil).Once()
	made, err := ctrl.BulkCreate(accCtx("root"), connect.NewRequest(&devpb.BulkCreateRequest{Namespace: "ns", Devices: []*devpb.Device{{Title: "Pump"}}}))
	require.NoError(t, err)
	assert.Equal(t, "d1", made.Msg.Devices[0].GetUuid())

	_, err = ctrl.IssueClaim(accCtx("root"), connect.NewRequest(&devpb.ClaimIssueRequest{}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = ctrl.IssueClaim(accCtx("root"), connect.NewRequest(&devpb.ClaimIssueRequest{Namespace: "ns", ExpiresAt: time.Now().Add(time.Hour).Unix()}))
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	ctrl.SetClaimTokens(memClaims{err: errors.New("redis")})
	_, err = ctrl.IssueClaim(accCtx("root"), connect.NewRequest(&devpb.ClaimIssueRequest{Namespace: "ns", ExpiresAt: time.Now().Add(time.Hour).Unix()}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	ctrl.SetClaimTokens(memClaims{ns: "ns"})
	issued, err := ctrl.IssueClaim(accCtx("root"), connect.NewRequest(&devpb.ClaimIssueRequest{Namespace: "ns", ExpiresAt: time.Now().Add(time.Hour).Unix()}))
	require.NoError(t, err)
	assert.NotEmpty(t, issued.Msg.GetToken())

	_, err = ctrl.Claim(context.Background(), connect.NewRequest(&devpb.ClaimRequest{}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	ctrl.SetClaimTokens(memClaims{err: errors.New("gone")})
	_, err = ctrl.Claim(context.Background(), connect.NewRequest(&devpb.ClaimRequest{Token: "t", Device: &devpb.Device{Title: "a"}}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.SetClaimTokens(memClaims{ns: "ns"})
	ctrl.ica_repo = flexRepo{ok: false}
	_, err = ctrl.Claim(accCtx("root"), connect.NewRequest(&devpb.ClaimRequest{Token: "t", Device: &devpb.Device{Title: "a"}}))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{ok: true, level: access.Level_ADMIN}
	devCol.On("CreateDocument", mock.Anything, mock.Anything).Return(docMeta(schema.DEVICES_COL, "d2"), nil).Once()
	claimed, err := ctrl.Claim(context.Background(), connect.NewRequest(&devpb.ClaimRequest{Token: "t", Device: &devpb.Device{Title: "a"}}))
	require.NoError(t, err)
	assert.Equal(t, "d2", claimed.Msg.GetDevice().GetUuid())

	_, err = ctrl.RotateCertificate(accCtx("root"), connect.NewRequest(&devpb.RotateCertificateRequest{}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{getErr: errors.New("gone")}
	_, err = ctrl.RotateCertificate(accCtx("root"), connect.NewRequest(&devpb.RotateCertificateRequest{Uuid: "d"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{level: access.Level_READ}
	_, err = ctrl.deviceForAdmin(accCtx("root"), "d")
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	ctrl.ica_repo = flexRepo{level: access.Level_ADMIN, accessFn: func(node InfinimeshGraphNode) {
		dev := node.(*Device)
		dev.Access = &access.Access{Level: access.Level_ADMIN}
		dev.Certificate = &devpb.Certificate{Fingerprint: []byte{1}, RevokedFingerprints: [][]byte{{9}}}
	}}
	_, err = ctrl.RotateCertificate(accCtx("root"), connect.NewRequest(&devpb.RotateCertificateRequest{Uuid: "d", Certificate: &devpb.Certificate{PemData: "nope"}}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	pemData := testCertPEM(t)
	next := &devpb.Certificate{PemData: pemData}
	require.NoError(t, sha256Fingerprint(next))
	devCol.On("ReplaceDocument", mock.Anything, "d", mock.Anything).Return(driver.DocumentMeta{}, errors.New("rep")).Once()
	_, err = ctrl.RotateCertificate(accCtx("root"), connect.NewRequest(&devpb.RotateCertificateRequest{Uuid: "d", Certificate: next}))
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	devCol.On("ReplaceDocument", mock.Anything, "d", mock.Anything).Return(docMeta(schema.DEVICES_COL, "d"), nil)
	rotated, err := ctrl.RotateCertificate(accCtx("root"), connect.NewRequest(&devpb.RotateCertificateRequest{Uuid: "d", Certificate: next}))
	require.NoError(t, err)
	assert.NotEmpty(t, rotated.Msg.GetCertificate().GetRolloverFingerprint())

	same := &devpb.Certificate{PemData: pemData, Fingerprint: append([]byte(nil), next.Fingerprint...)}
	ctrl.ica_repo = flexRepo{level: access.Level_ADMIN, accessFn: func(node InfinimeshGraphNode) {
		dev := node.(*Device)
		dev.Access = &access.Access{Level: access.Level_ADMIN}
		dev.Certificate = &devpb.Certificate{Fingerprint: append([]byte(nil), next.Fingerprint...), RolloverFingerprint: []byte{2}}
	}}
	rotated, err = ctrl.RotateCertificate(accCtx("root"), connect.NewRequest(&devpb.RotateCertificateRequest{Uuid: "d", Certificate: same}))
	require.NoError(t, err)
	assert.Equal(t, next.Fingerprint, rotated.Msg.GetCertificate().GetFingerprint())

	revoked, err := ctrl.RevokeCertificate(accCtx("root"), connect.NewRequest(&devpb.RevokeCertificateRequest{Uuid: "d"}))
	require.NoError(t, err)
	assert.True(t, revoked.Msg.GetCertificate().GetRevoked())
	fp := append([]byte(nil), next.Fingerprint...)
	revoked, err = ctrl.RevokeCertificate(accCtx("root"), connect.NewRequest(&devpb.RevokeCertificateRequest{Uuid: "d", Fingerprint: fp}))
	require.NoError(t, err)
	assert.Empty(t, revoked.Msg.GetCertificate().GetFingerprint())
	ctrl.ica_repo = flexRepo{level: access.Level_ADMIN, accessFn: func(node InfinimeshGraphNode) {
		dev := node.(*Device)
		dev.Access = &access.Access{Level: access.Level_ADMIN}
	}}
	revoked, err = ctrl.RevokeCertificate(accCtx("root"), connect.NewRequest(&devpb.RevokeCertificateRequest{Uuid: "d", Fingerprint: []byte{3}}))
	require.NoError(t, err)
	assert.NotEmpty(t, revoked.Msg.GetCertificate().GetRevokedFingerprints())
}

func TestListNamespaceDevicesErrors(t *testing.T) {
	db := driver_mocks.NewMockDatabase(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q")).Once()
	_, err := ListNamespaceDevices(context.Background(), db, "ns")
	assert.Error(t, err)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(readErr(errors.New("row")), nil).Once()
	_, err = ListNamespaceDevices(context.Background(), db, "ns")
	assert.Error(t, err)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(&scriptCursor{fn: []func(any) (driver.DocumentMeta, error){
		func(result any) (driver.DocumentMeta, error) {
			row := result.(*NamespaceDevice)
			row.Device = ""
			return docMeta("Devices", "x"), nil
		},
		func(result any) (driver.DocumentMeta, error) {
			row := result.(*NamespaceDevice)
			row.Device = "d"
			row.Title = "Pump"
			return docMeta("Devices", "d"), nil
		},
	}}, nil).Once()
	rows, err := ListNamespaceDevices(context.Background(), db, "ns")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "Pump", rows[0].Title)

	ctrl := newDev(flexRepo{})
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(oneRead(func(result any) {
		ns := result.(*nspb.Namespace)
		ns.Quota = &nspb.Quota{DeviceMessagesPerSecond: 3, NamespaceMessagesPerSecond: 4}
		ns.Uuid = "ns"
	}), nil).Once()
	ctrl.db = db
	dps, nps, id := ctrl.MessageRates(context.Background(), "d")
	assert.Equal(t, 3.0, dps)
	assert.Equal(t, 4.0, nps)
	assert.Equal(t, "k", id)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(readErr(errors.New("row")), nil).Once()
	_, _, id = ctrl.MessageRates(context.Background(), "d")
	assert.Empty(t, id)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q")).Once()
	ctrl.attachNamespace(context.Background(), &devpb.Device{Uuid: "d"})
}
