package main

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	inf "github.com/infinimesh/core/pkg/shared"
	pb "github.com/infinimesh/proto/node"
	"github.com/infinimesh/proto/node/access"
	devpb "github.com/infinimesh/proto/node/devices"
	nspb "github.com/infinimesh/proto/node/namespaces"
	"github.com/infinimesh/proto/shadow"
)

type fakeScopeDevices struct {
	namespace string
	devices   []*devpb.Device
	err       error
}

func (f *fakeScopeDevices) List(_ context.Context, req *connect.Request[pb.QueryRequest]) (*connect.Response[devpb.Devices], error) {
	f.namespace = req.Msg.GetNamespace()
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&devpb.Devices{Devices: f.devices}), nil
}

type fakeScopeNamespaces struct {
	got    string
	getErr error
	items  []*nspb.Namespace
}

func (f *fakeScopeNamespaces) Get(_ context.Context, req *connect.Request[nspb.Namespace]) (*connect.Response[nspb.Namespace], error) {
	f.got = req.Msg.GetUuid()
	if f.getErr != nil {
		return nil, f.getErr
	}
	return connect.NewResponse(&nspb.Namespace{Uuid: req.Msg.GetUuid()}), nil
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

func TestPrepareStreamRegistryUnavailable(t *testing.T) {
	api := &ShadowAPI{}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "acc")
	err := api.prepareStream(ctx, &shadow.StreamShadowRequest{Namespace: "plant", Devices: []string{"evil"}})
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

func strPtr(s string) *string { return &s }
