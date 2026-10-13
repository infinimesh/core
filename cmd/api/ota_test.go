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

	"github.com/infinimesh/core/pkg/graph"
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

type recordAudit struct {
	rows []string
	ns   []string
}

func (r *recordAudit) Record(_ context.Context, action, device, _, _, namespace string) {
	r.rows = append(r.rows, action+":"+device)
	r.ns = append(r.ns, namespace)
}

type fakeOTAPlugins struct {
	plug graph.OTAPlugin
	ok   bool
}

func (f *fakeOTAPlugins) OTAPlugin(context.Context, string) (graph.OTAPlugin, bool) {
	return f.plug, f.ok
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
	assert.Equal(t, []string{"plant"}, audit.ns)
	assert.Equal(t, 0, devices.addonCalls)
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
	assert.Equal(t, "ota provider requires the addon token", res.Msg.Results[0].GetError())
	assert.Equal(t, "other", res.Msg.Results[1].GetDevice())
	assert.Equal(t, "device is outside of token scope or not allowed", res.Msg.Results[1].GetError())

	req := connect.NewRequest(&shadow.AssignOTARequest{Devices: []string{"pump"}, Version: "1.4.0"})
	req.Header().Set("x-infinimesh-ota-provider", "hawkbit")
	res, err = api.AssignOTA(ctx, req)
	require.NoError(t, err)
	assert.Nil(t, client.got)
	require.Len(t, res.Msg.GetResults(), 1)
	assert.Equal(t, "ota provider requires the addon token", res.Msg.Results[0].GetError())
}

func TestAssignOTAPluginToken(t *testing.T) {
	client := &stubShadowClient{}
	audit := &recordAudit{}
	devices := &fakeScopeDevices{panicOnList: true, devices: []*devpb.Device{
		{Uuid: "pump", Tags: []string{"line-a"}, Access: &access.Access{Level: access.Level_MGMT, Namespace: nsPtr("plant")}},
		{Uuid: "other", Access: &access.Access{Level: access.Level_MGMT, Namespace: nsPtr("other")}},
	}}
	namespaces := &fakeScopeNamespaces{byID: map[string]*nspb.Namespace{
		"plant": {Uuid: "plant", Ota: &nspb.OTAConfig{Enabled: true, AddonRegistered: true, Provider: "hawkbit"}},
		"other": {Uuid: "other", Ota: &nspb.OTAConfig{Enabled: true, AddonRegistered: true, Provider: "hawkbit"}},
	}}
	api := &ShadowAPI{
		client: client, devices: devices, namespaces: namespaces, auditor: audit,
		plugins: &fakeOTAPlugins{ok: true, plug: graph.OTAPlugin{ID: "plug", Provider: "hawkbit", TokenID: "jti-1", Namespace: "plant"}},
	}
	ctx := context.WithValue(context.Background(), inf.InfinimeshPluginCtxKey, "plug")
	ctx = context.WithValue(ctx, inf.InfinimeshOTAProviderCtxKey, "hawkbit")
	ctx = context.WithValue(ctx, inf.InfinimeshJTICtxKey, "jti-1")

	res, err := api.AssignOTA(ctx, connect.NewRequest(&shadow.AssignOTARequest{Devices: []string{"pump", "other"}, Version: "1.4.0"}))
	require.NoError(t, err)
	assert.Equal(t, []string{"pump"}, client.got.GetDevices())
	assert.Equal(t, []string{"ota_assign:pump"}, audit.rows)
	assert.Equal(t, []string{"plant"}, audit.ns)
	require.Len(t, res.Msg.GetResults(), 2)
	assert.Equal(t, "other", res.Msg.Results[0].GetDevice())
	assert.Equal(t, "device is outside of token scope or not allowed", res.Msg.Results[0].GetError())
	assert.Equal(t, "pump", res.Msg.Results[1].GetDevice())
	assert.Empty(t, res.Msg.Results[1].GetError())
	assert.Greater(t, devices.addonCalls, 0)
	assert.Greater(t, namespaces.configCalls, 0)
}

func TestAssignOTAMenderTokenRefusedOnHawkbit(t *testing.T) {
	client := &stubShadowClient{}
	devices := &fakeScopeDevices{panicOnList: true, devices: []*devpb.Device{
		{Uuid: "pump", Access: &access.Access{Level: access.Level_MGMT, Namespace: nsPtr("plant")}},
	}}
	namespaces := &fakeScopeNamespaces{byID: map[string]*nspb.Namespace{
		"plant": {Uuid: "plant", Ota: &nspb.OTAConfig{Enabled: true, AddonRegistered: true, Provider: "hawkbit"}},
	}}
	api := &ShadowAPI{
		client: client, devices: devices, namespaces: namespaces,
		plugins: &fakeOTAPlugins{ok: true, plug: graph.OTAPlugin{ID: "plug", Provider: "mender", TokenID: "jti-1", Namespace: "plant"}},
	}
	ctx := context.WithValue(context.Background(), inf.InfinimeshPluginCtxKey, "plug")
	ctx = context.WithValue(ctx, inf.InfinimeshOTAProviderCtxKey, "mender")
	ctx = context.WithValue(ctx, inf.InfinimeshJTICtxKey, "jti-1")

	res, err := api.AssignOTA(ctx, connect.NewRequest(&shadow.AssignOTARequest{Devices: []string{"pump"}, Version: "1.4.0"}))
	require.NoError(t, err)
	assert.Nil(t, client.got)
	require.Len(t, res.Msg.GetResults(), 1)
	assert.Equal(t, "pump", res.Msg.Results[0].GetDevice())
	assert.Equal(t, "ota provider requires the addon token", res.Msg.Results[0].GetError())
	assert.Greater(t, namespaces.configCalls, 0)
	assert.Equal(t, 0, devices.listCalls)
}

func TestPatchWithoutAccountDoesNotList(t *testing.T) {
	devices := &fakeScopeDevices{panicOnList: true, devices: []*devpb.Device{
		{Uuid: "pump", Access: &access.Access{Level: access.Level_MGMT, Namespace: nsPtr("plant")}},
	}}
	api := &ShadowAPI{log: zap.NewNop(), client: &stubShadowClient{}, devices: devices, namespaces: &fakeScopeNamespaces{}}
	_, err := api.Patch(context.Background(), connect.NewRequest(&shadow.Shadow{
		Device: "pump", Desired: otaDesired("9", time.Now().UTC()),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	assert.Equal(t, 0, devices.listCalls)
}

func TestAssignOTAWithoutAccountDoesNotList(t *testing.T) {
	devices := &fakeScopeDevices{panicOnList: true, devices: []*devpb.Device{
		{Uuid: "pump", Access: &access.Access{Level: access.Level_MGMT, Namespace: nsPtr("plant")}},
	}}
	api := &ShadowAPI{client: &stubShadowClient{}, devices: devices, namespaces: &fakeScopeNamespaces{}}
	_, err := api.AssignOTA(context.Background(), connect.NewRequest(&shadow.AssignOTARequest{Devices: []string{"pump"}, Version: "1.4.0"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	assert.Equal(t, 0, devices.listCalls)
	assert.Equal(t, 0, devices.addonCalls)
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
	assert.Equal(t, 0, devices.listCalls)
	assert.Greater(t, namespaces.configCalls, 0)
}

func TestPatchPluginTokenWritesOTA(t *testing.T) {
	client := &stubShadowClient{}
	audit := &recordAudit{}
	devices := &fakeScopeDevices{panicOnList: true, devices: []*devpb.Device{
		{Uuid: "pump", Access: &access.Access{Level: access.Level_MGMT, Namespace: nsPtr("plant")}},
	}}
	namespaces := &fakeScopeNamespaces{byID: map[string]*nspb.Namespace{
		"plant": {Uuid: "plant", Ota: &nspb.OTAConfig{Enabled: true, AddonRegistered: true, Provider: "hawkbit"}},
	}}
	api := &ShadowAPI{
		log: zap.NewNop(), client: client, devices: devices, namespaces: namespaces, auditor: audit,
		plugins: &fakeOTAPlugins{ok: true, plug: graph.OTAPlugin{ID: "plug", Provider: "hawkbit", TokenID: "jti-1", Namespace: "plant"}},
	}
	ctx := context.WithValue(context.Background(), inf.InfinimeshPluginCtxKey, "plug")
	ctx = context.WithValue(ctx, inf.InfinimeshOTAProviderCtxKey, "hawkbit")
	ctx = context.WithValue(ctx, inf.InfinimeshJTICtxKey, "jti-1")
	_, err := api.Patch(ctx, connect.NewRequest(&shadow.Shadow{
		Device:  "pump",
		Desired: otaDesired("9", time.Now().UTC()),
	}))
	require.NoError(t, err)
	assert.Equal(t, "pump", client.patched.GetDevice())
	assert.Equal(t, []string{"ota_assign:pump"}, audit.rows)
	assert.Equal(t, []string{"plant"}, audit.ns)
	assert.Greater(t, namespaces.configCalls, 0)
	assert.Equal(t, 0, devices.listCalls)
}

func TestPatchDeviceTokenNamedProviderDoesNotList(t *testing.T) {
	devices := &fakeScopeDevices{panicOnList: true, devices: []*devpb.Device{
		{Uuid: "pump", Access: &access.Access{Level: access.Level_MGMT, Namespace: nsPtr("plant")}},
	}}
	namespaces := &fakeScopeNamespaces{byID: map[string]*nspb.Namespace{
		"plant": {Uuid: "plant", Ota: &nspb.OTAConfig{Enabled: true, AddonRegistered: true, Provider: "hawkbit"}},
	}}
	api := &ShadowAPI{log: zap.NewNop(), client: &stubShadowClient{}, devices: devices, namespaces: namespaces}
	ctx := context.WithValue(context.Background(), inf.InfinimeshDevicesCtxKey, map[string]access.Level{
		"pump": access.Level_MGMT,
	})
	_, err := api.Patch(ctx, connect.NewRequest(&shadow.Shadow{
		Device:  "pump",
		Desired: otaDesired("9", time.Now().UTC()),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Equal(t, 0, devices.listCalls)
}

func TestPatchAccountTokenWritesOTA(t *testing.T) {
	client := &stubShadowClient{}
	audit := &recordAudit{}
	devices := &fakeScopeDevices{devices: []*devpb.Device{
		{Uuid: "pump", Access: &access.Access{Level: access.Level_MGMT, Namespace: nsPtr("plant")}},
	}}
	namespaces := &fakeScopeNamespaces{byID: map[string]*nspb.Namespace{
		"plant": {Uuid: "plant", Ota: &nspb.OTAConfig{Enabled: true, AddonRegistered: true, Provider: "none"}},
	}}
	api := &ShadowAPI{log: zap.NewNop(), client: client, devices: devices, namespaces: namespaces, auditor: audit}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "acc")
	ctx = context.WithValue(ctx, inf.InfinimeshJTICtxKey, "jti-acc")
	_, err := api.Patch(ctx, connect.NewRequest(&shadow.Shadow{
		Device:  "pump",
		Desired: otaDesired("9", time.Now().UTC()),
	}))
	require.NoError(t, err)
	assert.Equal(t, "pump", client.patched.GetDevice())
	assert.Equal(t, []string{"ota_assign:pump"}, audit.rows)
	assert.Equal(t, []string{"plant"}, audit.ns)
}

func TestPatchManyOTAAudits(t *testing.T) {
	client := &stubShadowClient{}
	audit := &recordAudit{}
	devices := &fakeScopeDevices{devices: []*devpb.Device{
		{Uuid: "pump", Access: &access.Access{Level: access.Level_MGMT, Namespace: nsPtr("plant")}},
	}}
	namespaces := &fakeScopeNamespaces{byID: map[string]*nspb.Namespace{
		"plant": {Uuid: "plant", Ota: &nspb.OTAConfig{Enabled: true, AddonRegistered: true, Provider: "none"}},
	}}
	api := &ShadowAPI{client: client, devices: devices, namespaces: namespaces, auditor: audit}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "acc")
	res, err := api.PatchMany(ctx, connect.NewRequest(&shadow.PatchManyRequest{
		Devices: []string{"pump"}, Desired: otaDesired("9", time.Now().UTC()),
	}))
	require.NoError(t, err)
	assert.Equal(t, int32(1), res.Msg.GetUpdated())
	assert.Equal(t, []string{"ota_assign:pump"}, audit.rows)
	assert.Equal(t, []string{"plant"}, audit.ns)
}
