package main

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	inf "github.com/infinimesh/core/pkg/shared"
	"github.com/infinimesh/proto/node/access"
	devpb "github.com/infinimesh/proto/node/devices"
	nspb "github.com/infinimesh/proto/node/namespaces"
	"github.com/infinimesh/proto/shadow"
)

type stubShadowClient struct {
	shadow.ShadowServiceClient
	got     *shadow.PatchManyRequest
	patched *shadow.Shadow
	res     *shadow.PatchManyResponse
	err     error
}

func (s *stubShadowClient) Patch(_ context.Context, in *shadow.Shadow, _ ...grpc.CallOption) (*shadow.Shadow, error) {
	s.patched = in
	return in, nil
}

func (s *stubShadowClient) PatchMany(_ context.Context, in *shadow.PatchManyRequest, _ ...grpc.CallOption) (*shadow.PatchManyResponse, error) {
	s.got = in
	if s.err != nil {
		return nil, s.err
	}
	if s.res != nil {
		return s.res, nil
	}
	results := make([]*shadow.DevicePatchResult, 0, len(in.GetDevices()))
	for _, id := range in.GetDevices() {
		results = append(results, &shadow.DevicePatchResult{Device: id})
	}
	return &shadow.PatchManyResponse{Updated: int32(len(results)), Results: results}, nil
}

type recordAudit struct{ rows []string }

func (r *recordAudit) Record(_ context.Context, action, device, _, _ string) {
	r.rows = append(r.rows, action+":"+device)
}

func nsPtr(s string) *string { return &s }

func TestAssignOTANamespaceTagAuditsEachDevice(t *testing.T) {
	client := &stubShadowClient{}
	audit := &recordAudit{}
	devices := &fakeScopeDevices{devices: []*devpb.Device{
		{Uuid: "pump", Tags: []string{"line-a"}, Access: &access.Access{Level: access.Level_MGMT, Namespace: nsPtr("plant")}},
		{Uuid: "valve", Tags: []string{"line-b"}, Access: &access.Access{Level: access.Level_MGMT, Namespace: nsPtr("plant")}},
		{Uuid: "meter", Tags: []string{"line-a"}, Access: &access.Access{Level: access.Level_READ, Namespace: nsPtr("plant")}},
	}}
	namespaces := &fakeScopeNamespaces{byID: map[string]*nspb.Namespace{
		"plant": {Uuid: "plant", Ota: &nspb.OTAConfig{Enabled: true, AddonRegistered: true, Provider: "none"}},
	}}
	api := &ShadowAPI{client: client, devices: devices, namespaces: namespaces, auditor: audit}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "acc")

	res, err := api.AssignOTA(ctx, connect.NewRequest(&shadow.AssignOTARequest{
		Namespace: "plant", Tag: "line-a", Version: "1.4.0",
	}))
	require.NoError(t, err)
	require.Equal(t, []string{"pump"}, client.got.GetDevices())
	assert.Equal(t, "1.4.0", client.got.GetDesired().GetData().GetFields()["ota"].GetStructValue().GetFields()["version"].GetStringValue())
	assert.Equal(t, int32(1), res.Msg.GetUpdated())
	assert.Equal(t, []string{"ota_assign:pump"}, audit.rows)
}

func TestAssignOTARequiresTheOwningProvider(t *testing.T) {
	client := &stubShadowClient{}
	devices := &fakeScopeDevices{devices: []*devpb.Device{
		{Uuid: "pump", Access: &access.Access{Level: access.Level_MGMT, Namespace: nsPtr("plant")}},
	}}
	namespaces := &fakeScopeNamespaces{byID: map[string]*nspb.Namespace{
		"plant": {Uuid: "plant", Ota: &nspb.OTAConfig{Enabled: true, AddonRegistered: true, Provider: "hawkbit"}},
	}}
	api := &ShadowAPI{client: client, devices: devices, namespaces: namespaces}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "acc")

	res, err := api.AssignOTA(ctx, connect.NewRequest(&shadow.AssignOTARequest{Devices: []string{"pump", "other"}, Version: "1.4.0"}))
	require.NoError(t, err)
	assert.Nil(t, client.got)
	require.Len(t, res.Msg.GetResults(), 2)
	assert.Equal(t, "pump", res.Msg.Results[0].GetDevice())
	assert.Equal(t, "ota provider does not match the namespace", res.Msg.Results[0].GetError())
	assert.Equal(t, "other", res.Msg.Results[1].GetDevice())
	assert.Equal(t, "device is outside of token scope or not allowed", res.Msg.Results[1].GetError())

	req := connect.NewRequest(&shadow.AssignOTARequest{Devices: []string{"pump"}, Version: "1.4.0"})
	req.Header().Set("x-infinimesh-ota-provider", "hawkbit")
	res, err = api.AssignOTA(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, []string{"pump"}, client.got.GetDevices())
	assert.Equal(t, int32(1), res.Msg.GetUpdated())
}

func TestAssignOTADisabledUntilAddon(t *testing.T) {
	client := &stubShadowClient{}
	devices := &fakeScopeDevices{devices: []*devpb.Device{
		{Uuid: "pump", Access: &access.Access{Level: access.Level_MGMT, Namespace: nsPtr("plant")}},
	}}
	namespaces := &fakeScopeNamespaces{byID: map[string]*nspb.Namespace{
		"plant": {Uuid: "plant", Ota: &nspb.OTAConfig{Enabled: true}},
	}}
	api := &ShadowAPI{client: client, devices: devices, namespaces: namespaces}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "acc")
	res, err := api.AssignOTA(ctx, connect.NewRequest(&shadow.AssignOTARequest{Namespace: "plant", Version: "1.4.0"}))
	require.NoError(t, err)
	assert.Nil(t, client.got)
	require.Len(t, res.Msg.GetResults(), 1)
	assert.Equal(t, "ota is not enabled", res.Msg.Results[0].GetError())
}

func TestPatchDesiredOTAUsesTheSameGate(t *testing.T) {
	client := &stubShadowClient{}
	devices := &fakeScopeDevices{devices: []*devpb.Device{
		{Uuid: "pump", Access: &access.Access{Level: access.Level_MGMT, Namespace: nsPtr("plant")}},
	}}
	namespaces := &fakeScopeNamespaces{byID: map[string]*nspb.Namespace{
		"plant": {Uuid: "plant", Ota: &nspb.OTAConfig{Enabled: true, AddonRegistered: true}},
	}}
	api := &ShadowAPI{log: zap.NewNop(), client: client, devices: devices, namespaces: namespaces}
	ctx := context.WithValue(context.Background(), inf.InfinimeshDevicesCtxKey, map[string]access.Level{
		"pump": access.Level_MGMT,
	})
	_, err := api.Patch(ctx, connect.NewRequest(&shadow.Shadow{
		Device:  "pump",
		Desired: otaDesired("9", time.Now().UTC()),
	}))
	require.NoError(t, err)
	assert.Equal(t, "pump", client.patched.GetDevice())
	assert.Nil(t, client.got)
}
