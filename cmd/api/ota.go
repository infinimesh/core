package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/infinimesh/core/pkg/apierr"
	"github.com/infinimesh/core/pkg/graph"
	inf "github.com/infinimesh/core/pkg/shared"
	"github.com/infinimesh/proto/node/access"
	devpb "github.com/infinimesh/proto/node/devices"
	nspb "github.com/infinimesh/proto/node/namespaces"
	"github.com/infinimesh/proto/shadow"
	"google.golang.org/protobuf/types/known/structpb"
)

const auditOTAAssign = "ota_assign"

type otaAuditor interface {
	Record(ctx context.Context, action, device, account, tokenID, namespace string)
}

type otaPlugins interface {
	OTAPlugin(ctx context.Context, id string) (graph.OTAPlugin, bool)
}

func (s *ShadowAPI) SetAuditor(a otaAuditor) { s.auditor = a }

func (s *ShadowAPI) SetPlugins(p otaPlugins) {
	if s != nil {
		s.plugins = p
	}
}

func callerIDs(ctx context.Context) (account, plugin, provider, jti string) {
	account, _ = ctx.Value(inf.InfinimeshAccountCtxKey).(string)
	plugin, _ = ctx.Value(inf.InfinimeshPluginCtxKey).(string)
	provider, _ = ctx.Value(inf.InfinimeshOTAProviderCtxKey).(string)
	jti, _ = ctx.Value(inf.InfinimeshJTICtxKey).(string)
	return
}

func stateHasOTA(st *shadow.State) bool {
	return st != nil && st.GetData() != nil && st.GetData().GetFields()["ota"] != nil
}

func (s *ShadowAPI) namespaceDoc(ctx context.Context, id string, cache map[string]*nspb.Namespace) (*nspb.Namespace, error) {
	if ns, ok := cache[id]; ok {
		return ns, nil
	}
	if s.namespaces == nil {
		return nil, apierr.Code(connect.CodeUnavailable, "namespace registry is not available")
	}
	if id == "" {
		return nil, apierr.Code(connect.CodeFailedPrecondition, "device has no namespace")
	}
	ns, err := s.namespaces.Config(ctx, id)
	if err != nil {
		return nil, err
	}
	cache[id] = ns
	return ns, nil
}

func (s *ShadowAPI) allowDeviceOTA(ctx context.Context, dev *devpb.Device, cache map[string]*nspb.Namespace) error {
	nsID := dev.GetAccess().GetNamespace()
	ns, err := s.namespaceDoc(ctx, nsID, cache)
	if err != nil {
		return err
	}
	if !graph.OTAActive(ns.GetOta()) {
		return apierr.Code(connect.CodeFailedPrecondition, "ota is not enabled")
	}
	provider := strings.TrimSpace(ns.GetOta().GetProvider())
	if provider == "" {
		provider = "none"
	}
	_, plugin, claim, jti := callerIDs(ctx)
	if provider == "none" {
		if plugin != "" {
			return apierr.Code(connect.CodePermissionDenied, "ota provider does not match the namespace")
		}
		return nil
	}
	if plugin == "" || claim != provider {
		return apierr.Code(connect.CodePermissionDenied, "ota provider requires the addon token")
	}
	if s.plugins == nil {
		return apierr.Code(connect.CodePermissionDenied, "ota provider requires the addon token")
	}
	loaded, ok := s.plugins.OTAPlugin(ctx, plugin)
	if !ok || loaded.TokenID != jti || loaded.Provider != provider {
		return apierr.Code(connect.CodePermissionDenied, "ota provider requires the addon token")
	}
	if !loaded.Public && loaded.Namespace != nsID {
		return apierr.Code(connect.CodePermissionDenied, "ota provider does not match the namespace")
	}
	return nil
}

func (s *ShadowAPI) authorizeDeviceTokenOTA(ctx context.Context, device string) (string, error) {
	if s.devices == nil {
		return "", apierr.Code(connect.CodeUnavailable, "device registry is not available")
	}
	nsID, err := s.devices.DeviceNamespace(ctx, device)
	if err != nil {
		return "", err
	}
	dev := &devpb.Device{Uuid: device, Access: &access.Access{Level: access.Level_MGMT, Namespace: &nsID}}
	if err = s.allowDeviceOTA(ctx, dev, map[string]*nspb.Namespace{}); err != nil {
		return "", err
	}
	return nsID, nil
}

func (s *ShadowAPI) managedDevice(ctx context.Context, device string) (*devpb.Device, string, error) {
	account, plugin, _, _ := callerIDs(ctx)
	if account == "" && plugin == "" {
		return nil, "", apierr.Code(connect.CodeUnauthenticated, "Invalid token format: no requestor ID")
	}
	if plugin != "" {
		return nil, "", apierr.Code(connect.CodePermissionDenied, "addon token can write desired ota only")
	}
	devs, err := s.manageableDevices(ctx, "", "")
	if err != nil {
		return nil, "", err
	}
	for _, dev := range devs {
		if dev.GetUuid() == device {
			return dev, dev.GetAccess().GetNamespace(), nil
		}
	}
	return nil, "", apierr.Code(connect.CodePermissionDenied, "device is outside of token scope or not allowed")
}

func (s *ShadowAPI) selectOTA(ctx context.Context, ns, tag string, explicit []string) ([]*devpb.Device, []*shadow.DevicePatchResult, error) {
	devs, err := s.devicesForOTA(ctx, ns, tag)
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[string]*devpb.Device, len(devs))
	for _, dev := range devs {
		byID[dev.GetUuid()] = dev
	}
	cache := map[string]*nspb.Namespace{}
	if len(explicit) == 0 {
		explicit = make([]string, 0, len(devs))
		for _, dev := range devs {
			explicit = append(explicit, dev.GetUuid())
		}
	}
	var allowed []*devpb.Device
	var denied []*shadow.DevicePatchResult
	for _, id := range explicit {
		dev, ok := byID[id]
		if !ok {
			denied = append(denied, &shadow.DevicePatchResult{Device: id, Error: "device is outside of token scope or not allowed"})
			continue
		}
		if err := s.allowDeviceOTA(ctx, dev, cache); err != nil {
			denied = append(denied, &shadow.DevicePatchResult{Device: id, Error: connectMessage(err)})
			continue
		}
		allowed = append(allowed, dev)
	}
	return allowed, denied, nil
}

func connectMessage(err error) string {
	if err == nil {
		return ""
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Message()
	}
	return err.Error()
}

func (s *ShadowAPI) devicesForOTA(ctx context.Context, ns, tag string) ([]*devpb.Device, error) {
	account, plugin, _, jti := callerIDs(ctx)
	if account == "" && plugin == "" {
		return nil, apierr.Code(connect.CodeUnauthenticated, "Invalid token format: no requestor ID")
	}
	if s.devices == nil {
		return nil, apierr.Code(connect.CodeUnavailable, "device registry is not available")
	}
	if plugin == "" {
		return s.manageableDevices(ctx, ns, tag)
	}
	if s.plugins == nil {
		return nil, apierr.Code(connect.CodePermissionDenied, "ota provider requires the addon token")
	}
	loaded, ok := s.plugins.OTAPlugin(ctx, plugin)
	if !ok || loaded.TokenID != jti || !graph.ValidAddonProvider(loaded.Provider) {
		return nil, apierr.Code(connect.CodePermissionDenied, "ota provider requires the addon token")
	}
	listNS := ns
	if !loaded.Public {
		if loaded.Namespace == "" || (ns != "" && ns != loaded.Namespace) {
			return nil, nil
		}
		listNS = loaded.Namespace
	}
	return s.devices.ListForAddon(ctx, listNS, tag)
}

func (s *ShadowAPI) patchDesiredOTA(ctx context.Context, ns, tag string, explicit []string, desired *shadow.State) (*shadow.PatchManyResponse, error) {
	allowed, denied, err := s.selectOTA(ctx, ns, tag, explicit)
	if err != nil {
		return nil, err
	}
	resp := &shadow.PatchManyResponse{Results: denied}
	if len(allowed) == 0 {
		return resp, nil
	}
	ids := make([]string, 0, len(allowed))
	nsOf := make(map[string]string, len(allowed))
	for _, dev := range allowed {
		ids = append(ids, dev.GetUuid())
		nsOf[dev.GetUuid()] = dev.GetAccess().GetNamespace()
	}
	patched, err := s.client.PatchMany(ctx, &shadow.PatchManyRequest{Devices: ids, Desired: desired})
	if err != nil {
		return nil, asConnect(err)
	}
	resp.Updated = patched.GetUpdated()
	resp.Results = append(resp.Results, patched.GetResults()...)
	s.auditAssign(ctx, nsOf, patched.GetResults())
	return resp, nil
}

func (s *ShadowAPI) auditAssign(ctx context.Context, nsOf map[string]string, results []*shadow.DevicePatchResult) {
	if s.auditor == nil {
		return
	}
	account, plugin, _, jti := callerIDs(ctx)
	if account == "" {
		account = plugin
	}
	for _, res := range results {
		if res.GetError() == "" && res.GetDevice() != "" {
			s.auditor.Record(ctx, auditOTAAssign, res.GetDevice(), account, jti, nsOf[res.GetDevice()])
		}
	}
}

func otaDesired(version string, now time.Time) *shadow.State {
	data, err := structpb.NewStruct(map[string]any{
		"ota": map[string]any{
			"version":    version,
			"updated_at": now.UTC().Format(time.RFC3339),
		},
	})
	if err != nil {
		return &shadow.State{}
	}
	return &shadow.State{Data: data}
}

func (s *ShadowAPI) AssignOTA(ctx context.Context, request *connect.Request[shadow.AssignOTARequest]) (*connect.Response[shadow.AssignOTAResponse], error) {
	msg := request.Msg
	if strings.TrimSpace(msg.GetVersion()) == "" {
		return nil, apierr.Code(connect.CodeInvalidArgument, "version is required")
	}
	if msg.GetNamespace() == "" && msg.GetTag() == "" && len(msg.GetDevices()) == 0 {
		return nil, apierr.Code(connect.CodeInvalidArgument, "namespace, tag, or devices is required")
	}
	patched, err := s.patchDesiredOTA(ctx, msg.GetNamespace(), msg.GetTag(), msg.GetDevices(), otaDesired(msg.GetVersion(), time.Now().UTC()))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&shadow.AssignOTAResponse{Updated: patched.GetUpdated(), Results: patched.GetResults()}), nil
}
