package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/infinimesh/core/pkg/apierr"
	"github.com/infinimesh/core/pkg/graph"
	inf "github.com/infinimesh/core/pkg/shared"
	pb "github.com/infinimesh/proto/node"
	"github.com/infinimesh/proto/node/access"
	devpb "github.com/infinimesh/proto/node/devices"
	nspb "github.com/infinimesh/proto/node/namespaces"
	"github.com/infinimesh/proto/shadow"
)

type fakeScopeDevices struct {
	namespace   string
	devices     []*devpb.Device
	err         error
	listCalls   int
	addonCalls  int
	panicOnList bool
}

func (f *fakeScopeDevices) List(_ context.Context, req *connect.Request[pb.QueryRequest]) (*connect.Response[devpb.Devices], error) {
	f.listCalls++
	if f.panicOnList {
		panic("account list")
	}
	f.namespace = req.Msg.GetNamespace()
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&devpb.Devices{Devices: f.devices}), nil
}

func (f *fakeScopeDevices) ListForAddon(_ context.Context, namespace, tag string) ([]*devpb.Device, error) {
	f.addonCalls++
	f.namespace = namespace
	if f.err != nil {
		return nil, f.err
	}
	var out []*devpb.Device
	for _, dev := range f.devices {
		if namespace != "" && dev.GetAccess().GetNamespace() != namespace {
			continue
		}
		if tag != "" && !containsTag(dev.GetTags(), tag) {
			continue
		}
		out = append(out, dev)
	}
	return out, nil
}

func containsTag(tags []string, tag string) bool {
	for _, item := range tags {
		if item == tag {
			return true
		}
	}
	return false
}

func (f *fakeScopeDevices) Present(_ context.Context, id string) (bool, error) {
	for _, dev := range f.devices {
		if dev.GetUuid() == id {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeScopeDevices) DeviceNamespace(_ context.Context, id string) (string, error) {
	for _, dev := range f.devices {
		if dev.GetUuid() == id {
			return dev.GetAccess().GetNamespace(), nil
		}
	}
	return "", connect.NewError(connect.CodeNotFound, errors.New("device not found"))
}

type fakeScopeNamespaces struct {
	got         string
	getErr      error
	items       []*nspb.Namespace
	byID        map[string]*nspb.Namespace
	configCalls int
}

func (f *fakeScopeNamespaces) Get(ctx context.Context, req *connect.Request[nspb.Namespace]) (*connect.Response[nspb.Namespace], error) {
	if _, ok := ctx.Value(inf.InfinimeshAccountCtxKey).(string); !ok {
		panic("namespace get without account")
	}
	f.got = req.Msg.GetUuid()
	if f.getErr != nil {
		return nil, f.getErr
	}
	if ns, ok := f.byID[req.Msg.GetUuid()]; ok {
		return connect.NewResponse(ns), nil
	}
	return connect.NewResponse(&nspb.Namespace{Uuid: req.Msg.GetUuid()}), nil
}

func (f *fakeScopeNamespaces) Config(_ context.Context, id string) (*nspb.Namespace, error) {
	f.configCalls++
	if f.getErr != nil {
		return nil, f.getErr
	}
	if ns, ok := f.byID[id]; ok {
		return ns, nil
	}
	return &nspb.Namespace{Uuid: id}, nil
}

func (f *fakeScopeNamespaces) List(context.Context, *connect.Request[pb.ListRequest]) (*connect.Response[nspb.Namespaces], error) {
	return connect.NewResponse(&nspb.Namespaces{Namespaces: f.items}), nil
}

func TestPrepareStreamDeviceTokenClearsScope(t *testing.T) {
	api := &ShadowAPI{}
	ctx := context.WithValue(context.Background(), inf.InfinimeshDevicesCtxKey, map[string]access.Level{
		"b": access.Level_READ,
		"a": access.Level_ADMIN,
	})
	req := &shadow.StreamShadowRequest{
		Devices:    []string{"evil"},
		Namespace:  "plant",
		Namespaces: []string{"plant"},
		Account:    "acc",
		Catalog:    []*shadow.DeviceMeta{{Device: "evil", Title: "no"}},
	}
	assert.NoError(t, api.prepareStream(ctx, req))
	assert.Equal(t, []string{"a", "b"}, req.Devices)
	assert.Empty(t, req.Namespace)
	assert.Empty(t, req.Namespaces)
	assert.Empty(t, req.Account)
	assert.Empty(t, req.Catalog)
}

func TestPrepareStreamAccountNamespace(t *testing.T) {
	devices := &fakeScopeDevices{devices: []*devpb.Device{
		{Uuid: "b", Title: "Valve", Tags: []string{"steam"}},
		{Uuid: "a", Title: "Pump", Tags: []string{"water"}},
	}}
	namespaces := &fakeScopeNamespaces{}
	api := &ShadowAPI{devices: devices, namespaces: namespaces}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "acc")
	req := &shadow.StreamShadowRequest{
		Namespace:  "plant",
		Account:    "other",
		Devices:    []string{"evil"},
		Namespaces: []string{"other"},
		OnlyDelta:  true,
		Include:    []shadow.StateKey{shadow.StateKey_CONNECTION},
	}
	assert.NoError(t, api.prepareStream(ctx, req))
	assert.True(t, req.GetOnlyDelta())
	assert.Equal(t, []shadow.StateKey{shadow.StateKey_CONNECTION}, req.GetInclude())
	assert.Equal(t, "plant", namespaces.got)
	assert.Equal(t, "plant", devices.namespace)
	assert.Equal(t, "acc", req.Account)
	assert.Equal(t, []string{"plant"}, req.Namespaces)
	assert.Equal(t, []string{"a", "b"}, req.Devices)
	assert.Equal(t, "Pump", req.Catalog[0].GetTitle())
	assert.Equal(t, "plant", req.Catalog[0].GetNamespace())
	assert.Equal(t, []string{"water"}, req.Catalog[0].GetTags())
}

func TestPrepareStreamAccountAllNamespaces(t *testing.T) {
	devices := &fakeScopeDevices{devices: []*devpb.Device{
		{Uuid: "a", Title: "Pump", Access: &access.Access{Namespace: strPtr("plant")}},
	}}
	namespaces := &fakeScopeNamespaces{items: []*nspb.Namespace{{Uuid: "z"}, {Uuid: "plant"}}}
	api := &ShadowAPI{devices: devices, namespaces: namespaces}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "acc")
	req := &shadow.StreamShadowRequest{}
	assert.NoError(t, api.prepareStream(ctx, req))
	assert.Equal(t, "", devices.namespace)
	assert.Equal(t, []string{"plant", "z"}, req.Namespaces)
	assert.Equal(t, "plant", req.Catalog[0].GetNamespace())
	assert.Equal(t, "acc", req.Account)
}

func TestPrepareStreamNamespaceDenied(t *testing.T) {
	api := &ShadowAPI{
		devices:    &fakeScopeDevices{},
		namespaces: &fakeScopeNamespaces{getErr: status.Error(codes.PermissionDenied, "Not enough Access Rights")},
	}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "acc")
	err := api.prepareStream(ctx, &shadow.StreamShadowRequest{Namespace: "secret"})
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	api.namespaces = &fakeScopeNamespaces{getErr: status.Error(codes.NotFound, "Namespace not found or not enough Access Rights")}
	err = api.prepareStream(ctx, &shadow.StreamShadowRequest{Namespace: "missing"})
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestPrepareStreamStreamTokenPinsNamespace(t *testing.T) {
	devices := &fakeScopeDevices{devices: []*devpb.Device{{Uuid: "a", Title: "Pump"}}}
	namespaces := &fakeScopeNamespaces{}
	api := &ShadowAPI{devices: devices, namespaces: namespaces}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "acc")
	ctx = context.WithValue(ctx, inf.InfinimeshNamespaceCtxKey, "plant")
	req := &shadow.StreamShadowRequest{Namespace: "other"}
	assert.NoError(t, api.prepareStream(ctx, req))
	assert.Equal(t, "plant", namespaces.got)
	assert.Equal(t, "plant", devices.namespace)
	assert.Equal(t, "plant", req.Namespace)
	assert.Equal(t, []string{"plant"}, req.Namespaces)
}

func TestPrepareStreamAddonNamespace(t *testing.T) {
	devices := &fakeScopeDevices{panicOnList: true, devices: []*devpb.Device{
		{Uuid: "pump", Title: "Pump", Tags: []string{"canary"}, Access: &access.Access{Namespace: strPtr("plant")}},
		{Uuid: "other", Access: &access.Access{Namespace: strPtr("lab")}},
	}}
	namespaces := &fakeScopeNamespaces{byID: map[string]*nspb.Namespace{
		"plant": {Uuid: "plant", Ota: &nspb.OTAConfig{Enabled: true, AddonRegistered: true, Provider: "hawkbit"}},
		"lab":   {Uuid: "lab", Ota: &nspb.OTAConfig{Enabled: true, AddonRegistered: true, Provider: "mender"}},
	}}
	api := &ShadowAPI{
		devices: devices, namespaces: namespaces,
		plugins: &fakeOTAPlugins{ok: true, plug: graph.OTAPlugin{ID: "plug", Provider: "hawkbit", TokenID: "jti-1", Namespace: "plant"}},
	}
	ctx := context.WithValue(context.Background(), inf.InfinimeshPluginCtxKey, "plug")
	ctx = context.WithValue(ctx, inf.InfinimeshOTAProviderCtxKey, "hawkbit")
	ctx = context.WithValue(ctx, inf.InfinimeshJTICtxKey, "jti-1")

	req := &shadow.StreamShadowRequest{}
	assert.NoError(t, api.prepareStream(ctx, req))
	assert.Equal(t, "plant", req.GetNamespace())
	assert.Equal(t, []string{"plant"}, req.GetNamespaces())
	assert.Equal(t, []string{"pump"}, req.GetDevices())
	assert.Empty(t, req.GetAccount())
	assert.Equal(t, 0, devices.listCalls)
	assert.Equal(t, 1, devices.addonCalls)

	err := api.prepareStream(ctx, &shadow.StreamShadowRequest{Namespace: "lab"})
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.ErrorContains(t, err, "ota provider does not match the namespace")
	assert.Equal(t, 0, devices.listCalls)
}

func TestPrepareStreamRegistryUnavailable(t *testing.T) {
	api := &ShadowAPI{}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "acc")
	err := api.prepareStream(ctx, &shadow.StreamShadowRequest{Namespace: "plant", Devices: []string{"evil"}})
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

func TestOpenAddonStreamStopsWhenTheTokenRotates(t *testing.T) {
	plugs := &fakeOTAPlugins{ok: true, plug: graph.OTAPlugin{ID: "plug", Provider: "hawkbit", TokenID: "jti-1", Namespace: "plant"}}
	api := &ShadowAPI{log: zap.NewNop(), plugins: plugs}
	ctx := context.WithValue(context.Background(), inf.InfinimeshPluginCtxKey, "plug")
	ctx = context.WithValue(ctx, inf.InfinimeshJTICtxKey, "jti-1")
	n := 0
	var sent []uint64
	err := api.copyShadowStream(ctx, func() (*shadow.Shadow, error) {
		n++
		if n == 2 {
			plugs.plug.TokenID = "jti-2"
			api.PluginTokenChanged("plug")
		}
		return &shadow.Shadow{Device: "pump", Sequence: uint64(n)}, nil
	}, func(sh *shadow.Shadow) error {
		sent = append(sent, sh.GetSequence())
		return nil
	})
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	assert.ErrorContains(t, err, "token is revoked")
	assert.Equal(t, []uint64{1}, sent)

	ctx = context.WithValue(context.Background(), inf.InfinimeshPluginCtxKey, "plug")
	ctx = context.WithValue(ctx, inf.InfinimeshJTICtxKey, "jti-2")
	err = api.copyShadowStream(ctx, func() (*shadow.Shadow, error) {
		return &shadow.Shadow{Device: "pump", Sequence: 9}, nil
	}, func(sh *shadow.Shadow) error {
		sent = append(sent, sh.GetSequence())
		return errors.New("stop")
	})
	assert.EqualError(t, err, "stop")
	assert.Equal(t, []uint64{1, 9}, sent)
}

func TestIdleAddonStreamStopsWhenTheTokenRotates(t *testing.T) {
	plugs := &fakeOTAPlugins{ok: true, plug: graph.OTAPlugin{ID: "plug", TokenID: "jti-1"}}
	api := &ShadowAPI{log: zap.NewNop(), plugins: plugs}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = context.WithValue(ctx, inf.InfinimeshPluginCtxKey, "plug")
	ctx = context.WithValue(ctx, inf.InfinimeshJTICtxKey, "jti-1")
	started := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		errCh <- api.copyShadowStream(ctx, func() (*shadow.Shadow, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}, func(*shadow.Shadow) error { return nil })
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not start")
	}
	api.PluginTokenChanged("plug")
	select {
	case err := <-errCh:
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
		assert.ErrorContains(t, err, "token is revoked")
	case <-time.After(2 * time.Second):
		t.Fatal("idle stream stayed open after rotation")
	}
}

func TestAddonStreamCachesThePluginUntilRotation(t *testing.T) {
	plugs := &fakeOTAPlugins{ok: true, plug: graph.OTAPlugin{ID: "plug", TokenID: "jti-1"}}
	api := &ShadowAPI{plugins: plugs}
	ctx := context.WithValue(context.Background(), inf.InfinimeshPluginCtxKey, "plug")
	ctx = context.WithValue(ctx, inf.InfinimeshJTICtxKey, "jti-1")
	require.NoError(t, api.addonTokenCurrent(ctx))
	require.NoError(t, api.addonTokenCurrent(ctx))
	require.Equal(t, 1, plugs.calls)
	api.PluginTokenChanged("plug")
	plugs.plug.TokenID = "jti-2"
	err := api.addonTokenCurrent(ctx)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	assert.ErrorContains(t, err, "token is revoked")
	require.Equal(t, 2, plugs.calls)
}

func strPtr(s string) *string { return &s }

func TestAuthorizeShadowWrite(t *testing.T) {
	scope := map[string]access.Level{"dev": access.Level_READ}
	ctx := context.WithValue(context.Background(), inf.InfinimeshPostCtxKey, true)
	assert.NoError(t, authorizeShadowWrite(ctx, scope, "dev", true, false))
	err := authorizeShadowWrite(ctx, scope, "dev", false, true)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.EqualError(t, err, "permission_denied: desired state requires manage access")

	err = authorizeShadowWrite(context.Background(), scope, "dev", true, false)
	assert.EqualError(t, err, "permission_denied: reported state requires write access")

	mgmt := map[string]access.Level{"dev": access.Level_MGMT}
	assert.NoError(t, authorizeShadowWrite(context.Background(), mgmt, "dev", true, true))

	err = authorizeShadowWrite(ctx, scope, "other", true, false)
	assert.EqualError(t, err, "unauthenticated: requested device is outside of token scope")
}

func TestAsConnectKeepsErrorInfo(t *testing.T) {
	err := apierr.New(connect.CodePermissionDenied, apierr.ReasonReportedWrite, "reported state requires write access")
	got := asConnect(err)
	assert.Equal(t, err, got)
	got = asConnect(status.Error(codes.NotFound, "missing"))
	assert.EqualError(t, got, "not_found: missing")

	st, serr := status.New(codes.InvalidArgument, "no devices specified").WithDetails(&errdetails.ErrorInfo{
		Reason: apierr.ReasonInvalidArgument,
		Domain: apierr.Domain,
	})
	assert.NoError(t, serr)
	got = asConnect(st.Err())
	assert.EqualError(t, got, "invalid_argument: no devices specified")
	var ce *connect.Error
	assert.ErrorAs(t, got, &ce)
	found := false
	for _, detail := range ce.Details() {
		msg, err := detail.Value()
		assert.NoError(t, err)
		info, ok := msg.(*errdetails.ErrorInfo)
		if ok && info.GetReason() == apierr.ReasonInvalidArgument && info.GetDomain() == apierr.Domain {
			found = true
		}
	}
	assert.True(t, found)
}

func TestCopyShadowStreamKeepsAborted(t *testing.T) {
	api := &ShadowAPI{log: zap.NewNop()}
	err := api.copyShadowStream(context.Background(), func() (*shadow.Shadow, error) {
		return nil, status.Error(codes.Aborted, "shadow stream fell behind")
	}, func(*shadow.Shadow) error { return nil })
	assert.Equal(t, connect.CodeAborted, connect.CodeOf(err))
	assert.ErrorContains(t, err, "shadow stream fell behind")
}
