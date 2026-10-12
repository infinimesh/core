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

	"github.com/infinimesh/proto/node/access"

	"connectrpc.com/connect"

	"go.uber.org/zap"
	"google.golang.org/grpc/status"

	"github.com/infinimesh/core/pkg/apierr"
	inf "github.com/infinimesh/core/pkg/shared"
	pb "github.com/infinimesh/proto/node"
	devpb "github.com/infinimesh/proto/node/devices"
	nspb "github.com/infinimesh/proto/node/namespaces"
	"github.com/infinimesh/proto/shadow"
)

// ShadowAPI data strcuture
type scopeDevices interface {
	List(context.Context, *connect.Request[pb.QueryRequest]) (*connect.Response[devpb.Devices], error)
}

type scopeNamespaces interface {
	List(context.Context, *connect.Request[pb.ListRequest]) (*connect.Response[nspb.Namespaces], error)
	Get(context.Context, *connect.Request[nspb.Namespace]) (*connect.Response[nspb.Namespace], error)
}

type ShadowAPI struct {
	pb.UnimplementedShadowServiceServer

	log        *zap.Logger
	client     shadow.ShadowServiceClient
	devices    scopeDevices
	namespaces scopeNamespaces
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
	shadow := request.Msg

	devices_scope, ok := ctx.Value(inf.InfinimeshDevicesCtxKey).(map[string]access.Level)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("requested device is outside of token scope or not allowed to post"))
	}
	log.Debug("Scope", zap.Any("devices", devices_scope))

	if err = authorizeShadowWrite(ctx, devices_scope, shadow.GetDevice(), shadow.GetReported() != nil, shadow.GetDesired() != nil); err != nil {
		return nil, err
	}

	res, err := s.client.Patch(ctx, shadow)
	if err != nil {
		return nil, err
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
		return apierr.Code(connect.Code(st.Code()), st.Message())
	}
	return err
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

	for {
		msg, err := c.Recv()
		if err != nil {
			log.Info("Error receiving message, closing stream", zap.Error(err))
			return err
		}

		err = srv.Send(msg)
		if err != nil {
			log.Info("Error sending message, closing stream", zap.Error(err))
			return err
		}
	}
}

func (s *ShadowAPI) StreamShadowSync(ctx context.Context, request *connect.Request[shadow.StreamShadowRequest], srv *connect.ServerStream[shadow.Shadow]) (err error) {
	request.Msg.Sync = true
	return s.StreamShadow(ctx, request, srv)
}
