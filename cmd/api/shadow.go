/*
Copyright © 2021-2023 Infinite Devices GmbH, Nikita Ivanovski info@slnt-opp.xyz

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
package main

// TO BE DEPRECATED AND MOVED TO pkg/state

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/infinimesh/proto/node/access"

	"connectrpc.com/connect"

	"go.uber.org/zap"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/status"

	"github.com/infinimesh/core/pkg/apierr"
	"github.com/infinimesh/core/pkg/graph"
	inf "github.com/infinimesh/core/pkg/shared"
	pb "github.com/infinimesh/proto/node"
	devpb "github.com/infinimesh/proto/node/devices"
	nspb "github.com/infinimesh/proto/node/namespaces"
	"github.com/infinimesh/proto/shadow"
)

// ShadowAPI data strcuture
type scopeDevices interface {
	List(context.Context, *connect.Request[pb.QueryRequest]) (*connect.Response[devpb.Devices], error)
	ListForAddon(ctx context.Context, namespace, tag string) ([]*devpb.Device, error)
	DeviceNamespace(ctx context.Context, id string) (string, error)
	Present(ctx context.Context, id string) (bool, error)
}

type scopeNamespaces interface {
	List(context.Context, *connect.Request[pb.ListRequest]) (*connect.Response[nspb.Namespaces], error)
	Get(context.Context, *connect.Request[nspb.Namespace]) (*connect.Response[nspb.Namespace], error)
	Config(context.Context, string) (*nspb.Namespace, error)
}

type ShadowAPI struct {
	pb.UnimplementedShadowServiceServer

	log        *zap.Logger
	client     shadow.ShadowServiceClient
	devices    scopeDevices
	namespaces scopeNamespaces
	plugins    otaPlugins
	auditor    otaAuditor
}

func (s *ShadowAPI) SetRegistry(devices scopeDevices, namespaces scopeNamespaces) {
	s.devices = devices
	s.namespaces = namespaces
}

func NewShadowAPI(log *zap.Logger, client shadow.ShadowServiceClient) *ShadowAPI {
	return &ShadowAPI{
		log: log.Named("ShadowAPI"), client: client,
	}
}

func (s *ShadowAPI) Get(ctx context.Context, _ *connect.Request[shadow.GetRequest]) (response *connect.Response[shadow.GetResponse], err error) {
	log := s.log.Named("Get")

	devices_scope, ok := ctx.Value(inf.InfinimeshDevicesCtxKey).(map[string]access.Level)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("requested device is outside of token scope or not allowed to post"))
	}
	log.Debug("Scope", zap.Any("devices", devices_scope))

	var pool []string
	for device := range devices_scope {
		pool = append(pool, device)
	}

	res, err := s.client.Get(ctx, &shadow.GetRequest{Devices: pool, Pool: pool})
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(res), nil
}

// Patch - is a method to update the current state of the device
func (s *ShadowAPI) Patch(ctx context.Context, request *connect.Request[shadow.Shadow]) (response *connect.Response[shadow.Shadow], err error) {
	log := s.log.Named("PatchDesiredState")
	body := request.Msg

	var otaNamespace string
	if devices_scope, ok := ctx.Value(inf.InfinimeshDevicesCtxKey).(map[string]access.Level); ok {
		log.Debug("Scope", zap.Any("devices", devices_scope))
		if s.devices != nil {
			present, presentErr := s.devices.Present(ctx, body.GetDevice())
			if presentErr != nil {
				return nil, presentErr
			}
			if !present {
				return nil, apierr.New(connect.CodeUnauthenticated, apierr.ReasonTokenRevoked, "token is revoked")
			}
		}
		if err = authorizeShadowWrite(ctx, devices_scope, body.GetDevice(), body.GetReported() != nil, body.GetDesired() != nil); err != nil {
			return nil, err
		}
		if stateHasOTA(body.GetDesired()) {
			otaNamespace, err = s.authorizeDeviceTokenOTA(ctx, body.GetDevice())
			if err != nil {
				return nil, err
			}
		}
	} else if _, plugin, _, _ := callerIDs(ctx); plugin != "" {
		if err = addonPatchError(body); err != nil {
			return nil, err
		}
		otaNamespace, err = s.authorizeDeviceTokenOTA(ctx, body.GetDevice())
		if err != nil {
			return nil, err
		}
	} else {
		dev, ns, lookupErr := s.managedDevice(ctx, body.GetDevice())
		if lookupErr != nil {
			return nil, lookupErr
		}
		if stateHasOTA(body.GetDesired()) {
			if err = s.allowDeviceOTA(ctx, dev, map[string]*nspb.Namespace{}); err != nil {
				return nil, err
			}
			otaNamespace = ns
		}
	}

	res, err := s.client.Patch(ctx, body)
	if err != nil {
		return nil, asConnect(err)
	}
	if stateHasOTA(body.GetDesired()) {
		s.auditOTA(ctx, auditOTAAssign, map[string]string{body.GetDevice(): otaNamespace}, []*shadow.DevicePatchResult{{Device: body.GetDevice()}})
	}

	return connect.NewResponse(res), nil
}

func (s *ShadowAPI) Remove(ctx context.Context, request *connect.Request[shadow.RemoveRequest]) (response *connect.Response[shadow.Shadow], err error) {
	log := s.log.Named("RemoveStateKey")
	req := request.Msg

	devices_scope, ok := ctx.Value(inf.InfinimeshDevicesCtxKey).(map[string]access.Level)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("requested device is outside of token scope or not allowed to post"))
	}
	log.Debug("Scope", zap.Any("devices", devices_scope))

	reported := req.GetStateKey() != shadow.StateKey_DESIRED
	desired := req.GetStateKey() == shadow.StateKey_DESIRED
	if err = authorizeShadowWrite(ctx, devices_scope, req.GetDevice(), reported, desired); err != nil {
		return nil, err
	}

	res, err := s.client.Remove(ctx, req)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(res), nil
}

// StreamShadow is a method to get the stream for a device
func (s *ShadowAPI) prepareStream(ctx context.Context, req *shadow.StreamShadowRequest) error {
	if pool, ok := ctx.Value(inf.InfinimeshDevicesCtxKey).(map[string]access.Level); ok {
		devices := make([]string, 0, len(pool))
		for id := range pool {
			devices = append(devices, id)
		}
		slices.Sort(devices)
		req.Devices = devices
		req.Namespace = ""
		req.Namespaces = nil
		req.Account = ""
		req.Catalog = nil
		return nil
	}

	if _, plugin, _, _ := callerIDs(ctx); plugin != "" {
		return s.prepareAddonStream(ctx, req)
	}

	account, _ := ctx.Value(inf.InfinimeshAccountCtxKey).(string)
	if account == "" {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("requested device is outside of token scope or not allowed to post"))
	}
	if s.devices == nil || s.namespaces == nil {
		return connect.NewError(connect.CodeUnavailable, errors.New("device registry is not available"))
	}
	if pinned, ok := ctx.Value(inf.InfinimeshNamespaceCtxKey).(string); ok && pinned != "" {
		req.Namespace = pinned
	}

	ns := req.GetNamespace()
	var namespaces []string
	if ns != "" {
		if _, err := s.namespaces.Get(ctx, connect.NewRequest(&nspb.Namespace{Uuid: ns})); err != nil {
			return asConnect(err)
		}
		namespaces = []string{ns}
	} else {
		listed, err := s.namespaces.List(ctx, connect.NewRequest(&pb.ListRequest{}))
		if err != nil {
			return asConnect(err)
		}
		for _, item := range listed.Msg.GetNamespaces() {
			if id := item.GetUuid(); id != "" {
				namespaces = append(namespaces, id)
			}
		}
		slices.Sort(namespaces)
	}

	listed, err := s.devices.List(ctx, connect.NewRequest(&pb.QueryRequest{Namespace: ns}))
	if err != nil {
		return asConnect(err)
	}
	byID := map[string]*devpb.Device{}
	for _, dev := range listed.Msg.GetDevices() {
		if dev.GetUuid() != "" {
			byID[dev.GetUuid()] = dev
		}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	catalog := make([]*shadow.DeviceMeta, 0, len(ids))
	for _, id := range ids {
		dev := byID[id]
		namespace := ns
		if namespace == "" {
			namespace = dev.GetAccess().GetNamespace()
		}
		catalog = append(catalog, &shadow.DeviceMeta{
			Device:    id,
			Title:     dev.GetTitle(),
			Namespace: namespace,
			Tags:      append([]string(nil), dev.GetTags()...),
		})
	}
	req.Account = account
	req.Namespace = ns
	req.Namespaces = namespaces
	req.Devices = ids
	req.Catalog = catalog
	return nil
}

func (s *ShadowAPI) prepareAddonStream(ctx context.Context, req *shadow.StreamShadowRequest) error {
	_, plugin, provider, jti := callerIDs(ctx)
	if s.plugins == nil || s.devices == nil || s.namespaces == nil {
		return apierr.Code(connect.CodeUnavailable, "device registry is not available")
	}
	loaded, ok := s.plugins.OTAPlugin(ctx, plugin)
	if !ok || loaded.TokenID != jti || loaded.Provider != provider || !graph.ValidAddonProvider(loaded.Provider) {
		return apierr.Code(connect.CodePermissionDenied, "ota provider requires the addon token")
	}
	ns := req.GetNamespace()
	if !loaded.Public {
		if ns != "" && ns != loaded.Namespace {
			return apierr.Code(connect.CodePermissionDenied, "ota provider does not match the namespace")
		}
		ns = loaded.Namespace
	}
	if ns == "" {
		return apierr.Code(connect.CodeInvalidArgument, "namespace is required")
	}
	doc, err := s.namespaces.Config(ctx, ns)
	if err != nil {
		return err
	}
	got := strings.TrimSpace(doc.GetOta().GetProvider())
	if got == "" {
		got = "none"
	}
	if got != loaded.Provider || !graph.OTAActive(doc.GetOta()) {
		return apierr.Code(connect.CodePermissionDenied, "ota provider does not match the namespace")
	}
	devs, err := s.devices.ListForAddon(ctx, ns, "")
	if err != nil {
		return err
	}
	slices.SortFunc(devs, func(a, b *devpb.Device) int {
		return strings.Compare(a.GetUuid(), b.GetUuid())
	})
	ids := make([]string, 0, len(devs))
	catalog := make([]*shadow.DeviceMeta, 0, len(devs))
	for _, dev := range devs {
		if dev.GetUuid() == "" {
			continue
		}
		ids = append(ids, dev.GetUuid())
		catalog = append(catalog, &shadow.DeviceMeta{
			Device:    dev.GetUuid(),
			Title:     dev.GetTitle(),
			Namespace: ns,
			Tags:      append([]string(nil), dev.GetTags()...),
		})
	}
	req.Account = ""
	req.Namespace = ns
	req.Namespaces = []string{ns}
	req.Devices = ids
	req.Catalog = catalog
	return nil
}

func authorizeShadowWrite(ctx context.Context, scope map[string]access.Level, device string, reported, desired bool) error {
	lvl, ok := scope[device]
	if !ok {
		return apierr.New(connect.CodeUnauthenticated, apierr.ReasonDeviceOutOfScope, "requested device is outside of token scope")
	}
	post, _ := ctx.Value(inf.InfinimeshPostCtxKey).(bool)
	if reported && !(post || lvl >= access.Level_MGMT) {
		return apierr.New(connect.CodePermissionDenied, apierr.ReasonReportedWrite, "reported state requires write access")
	}
	if desired && lvl < access.Level_MGMT {
		return apierr.New(connect.CodePermissionDenied, apierr.ReasonDesiredManage, "desired state requires manage access")
	}
	return nil
}

func asConnect(err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	if st, ok := status.FromError(err); ok {
		for _, detail := range st.Details() {
			info, ok := detail.(*errdetails.ErrorInfo)
			if ok && info.GetDomain() == apierr.Domain && info.GetReason() != "" {
				return apierr.New(connect.Code(st.Code()), info.GetReason(), st.Message())
			}
		}
		return apierr.Code(connect.Code(st.Code()), st.Message())
	}
	return err
}

func (s *ShadowAPI) PatchMany(ctx context.Context, request *connect.Request[shadow.PatchManyRequest]) (*connect.Response[shadow.PatchManyResponse], error) {
	msg := request.Msg
	if msg.GetDesired() == nil {
		return nil, apierr.Code(connect.CodeInvalidArgument, "desired state is required")
	}
	if stateHasOTA(msg.GetDesired()) {
		patched, err := s.patchDesiredOTA(ctx, msg.GetNamespace(), msg.GetTag(), msg.GetDevices(), msg.GetDesired())
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(patched), nil
	}
	if _, plugin, _, _ := callerIDs(ctx); plugin != "" {
		return nil, apierr.Code(connect.CodePermissionDenied, "addon token can write desired ota only")
	}
	devs, err := s.manageableDevices(ctx, msg.GetNamespace(), msg.GetTag())
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, dev := range devs {
		ids = append(ids, dev.GetUuid())
	}
	if len(msg.GetDevices()) > 0 {
		ids = msg.GetDevices()
	}
	if len(ids) == 0 {
		return connect.NewResponse(&shadow.PatchManyResponse{}), nil
	}
	res, err := s.client.PatchMany(ctx, &shadow.PatchManyRequest{Devices: ids, Desired: msg.GetDesired()})
	if err != nil {
		return nil, asConnect(err)
	}
	return connect.NewResponse(res), nil
}

func (s *ShadowAPI) manageableDevices(ctx context.Context, ns, tag string) ([]*devpb.Device, error) {
	if s.devices == nil {
		return nil, apierr.Code(connect.CodeUnavailable, "device registry is not available")
	}
	listed, err := s.devices.List(ctx, connect.NewRequest(&pb.QueryRequest{Namespace: ns}))
	if err != nil {
		return nil, err
	}
	var out []*devpb.Device
	for _, dev := range listed.Msg.GetDevices() {
		if ns != "" && dev.GetAccess().GetNamespace() != ns {
			continue
		}
		if tag != "" && !slices.Contains(dev.GetTags(), tag) {
			continue
		}
		if dev.GetAccess().GetLevel() < access.Level_MGMT {
			continue
		}
		out = append(out, dev)
	}
	return out, nil
}

func (s *ShadowAPI) StreamShadow(ctx context.Context, request *connect.Request[shadow.StreamShadowRequest], srv *connect.ServerStream[shadow.Shadow]) (err error) {
	log := s.log.Named("StreamReportedStateChanges")
	req := request.Msg
	if err = s.prepareStream(ctx, req); err != nil {
		return err
	}

	log.Debug("Stream API Method: Streaming started", zap.Strings("devices", req.GetDevices()))

	c, err := s.client.StreamShadow(ctx, req)
	if err != nil {
		log.Warn("Stream API Method: Failed to start the Stream", zap.Error(err))
		return connect.NewError(connect.CodeAborted, errors.New("failed to start the Stream"))
	}

	return s.copyShadowStream(ctx, c.Recv, srv.Send)
}

func (s *ShadowAPI) copyShadowStream(ctx context.Context, recv func() (*shadow.Shadow, error), send func(*shadow.Shadow) error) error {
	log := s.log.Named("StreamReportedStateChanges")
	for {
		msg, err := recv()
		if err != nil {
			log.Info("Error receiving message, closing stream", zap.Error(err))
			return err
		}
		if err = s.addonTokenCurrent(ctx); err != nil {
			return err
		}
		if err = send(msg); err != nil {
			log.Info("Error sending message, closing stream", zap.Error(err))
			return err
		}
	}
}

func (s *ShadowAPI) addonTokenCurrent(ctx context.Context) error {
	_, plugin, _, jti := callerIDs(ctx)
	if plugin == "" {
		return nil
	}
	if s.plugins == nil {
		return apierr.New(connect.CodeUnauthenticated, apierr.ReasonTokenRevoked, "token is revoked")
	}
	loaded, ok := s.plugins.OTAPlugin(ctx, plugin)
	if !ok || loaded.TokenID != jti {
		return apierr.New(connect.CodeUnauthenticated, apierr.ReasonTokenRevoked, "token is revoked")
	}
	return nil
}

func (s *ShadowAPI) StreamShadowSync(ctx context.Context, request *connect.Request[shadow.StreamShadowRequest], srv *connect.ServerStream[shadow.Shadow]) (err error) {
	request.Msg.Sync = true
	return s.StreamShadow(ctx, request, srv)
}
