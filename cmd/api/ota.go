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

const (
	auditOTAAssign  = "ota_assign"
	auditOTARequest = "ota_request"
)

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

func reportedOTAOnly(st *shadow.State) bool {
	if st == nil || st.GetData() == nil {
		return false
	}
	fields := st.GetData().GetFields()
	if len(fields) != 1 {
		return false
	}
	ota := fields["ota"]
	return ota != nil && ota.GetStructValue() != nil
}

func addonMayPatch(body *shadow.Shadow) bool {
	if body == nil || body.GetConnection() != nil {
		return false
	}
	desired := body.GetDesired()
	reported := body.GetReported()
	if desired == nil && reported == nil {
		return false
	}
	if desired != nil && !stateHasOTA(desired) {
		return false
	}
	if desired != nil && desiredRequestsVersion(desired) {
		return false
	}
	if reported != nil && !reportedOTAOnly(reported) {
		return false
	}
	return true
}

func addonPatchError(body *shadow.Shadow) error {
	if body != nil && body.GetConnection() == nil && body.GetReported() != nil && !reportedOTAOnly(body.GetReported()) {
		return apierr.Code(connect.CodePermissionDenied, "reported patch may contain only ota")
	}
	if !addonMayPatch(body) {
		return apierr.Code(connect.CodePermissionDenied, "addon token can write desired ota only")
	}
	return nil
}

func desiredRequestsVersion(st *shadow.State) bool {
	ota := st.GetData().GetFields()["ota"].GetStructValue()
	if ota == nil {
		return false
	}
	_, ok := ota.GetFields()["requested_version"]
	return ok
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

func (s *ShadowAPI) allowAccountOTARequest(ctx context.Context, dev *devpb.Device, cache map[string]*nspb.Namespace) error {
	_, plugin, _, _ := callerIDs(ctx)
	if plugin != "" {
		return apierr.Code(connect.CodePermissionDenied, "ota request needs an account token")
	}
	ns, err := s.namespaceDoc(ctx, dev.GetAccess().GetNamespace(), cache)
	if err != nil {
		return err
	}
	if !graph.OTAActive(ns.GetOta()) {
		return apierr.Code(connect.CodeFailedPrecondition, "ota is not enabled")
	}
	return nil
}

func (s *ShadowAPI) selectAccountOTA(ctx context.Context, ns, tag string, explicit []string) ([]*devpb.Device, []*shadow.DevicePatchResult, error) {
	account, plugin, _, _ := callerIDs(ctx)
	if account == "" || plugin != "" {
		if account == "" && plugin == "" {
			return nil, nil, apierr.Code(connect.CodeUnauthenticated, "Invalid token format: no requestor ID")
		}
		return nil, nil, apierr.Code(connect.CodePermissionDenied, "ota request needs an account token")
	}
	devs, err := s.manageableDevices(ctx, ns, tag)
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
		if err := s.allowAccountOTARequest(ctx, dev, cache); err != nil {
			denied = append(denied, &shadow.DevicePatchResult{Device: id, Error: connectMessage(err)})
			continue
		}
		allowed = append(allowed, dev)
	}
	return allowed, denied, nil
}

func (s *ShadowAPI) patchDesiredOTA(ctx context.Context, ns, tag string, explicit []string, desired *shadow.State) (*shadow.PatchManyResponse, error) {
	allowed, denied, err := s.selectOTA(ctx, ns, tag, explicit)
	if err != nil {
		return nil, err
	}
	return s.storeDesiredOTA(ctx, allowed, denied, desired, auditOTAAssign)
}

func (s *ShadowAPI) storeDesiredOTA(ctx context.Context, allowed []*devpb.Device, denied []*shadow.DevicePatchResult, desired *shadow.State, action string) (*shadow.PatchManyResponse, error) {
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
	s.auditOTA(ctx, action, nsOf, patched.GetResults())
	return resp, nil
}

func (s *ShadowAPI) auditOTA(ctx context.Context, action string, nsOf map[string]string, results []*shadow.DevicePatchResult) {
	if s.auditor == nil {
		return
	}
	account, plugin, _, jti := callerIDs(ctx)
	if account == "" {
		account = plugin
	}
	for _, res := range results {
		if res.GetError() == "" && res.GetDevice() != "" {
			s.auditor.Record(ctx, action, res.GetDevice(), account, jti, nsOf[res.GetDevice()])
		}
	}
}

func otaDocument(fields map[string]any) *shadow.State {
	data, err := structpb.NewStruct(map[string]any{"ota": fields})
	if err != nil {
		return &shadow.State{}
	}
	return &shadow.State{Data: data}
}

func otaDesired(version string, now time.Time) *shadow.State {
	return otaDocument(map[string]any{
		"version":    version,
		"updated_at": now.UTC().Format(time.RFC3339),
	})
}

func otaRequest(version string, now time.Time) *shadow.State {
	return otaDocument(map[string]any{
		"requested_version": version,
		"updated_at":        now.UTC().Format(time.RFC3339),
	})
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

func (s *ShadowAPI) RequestOTA(ctx context.Context, request *connect.Request[shadow.AssignOTARequest]) (*connect.Response[shadow.AssignOTAResponse], error) {
	msg := request.Msg
	if strings.TrimSpace(msg.GetVersion()) == "" {
		return nil, apierr.Code(connect.CodeInvalidArgument, "version is required")
	}
	if msg.GetNamespace() == "" && msg.GetTag() == "" && len(msg.GetDevices()) == 0 {
		return nil, apierr.Code(connect.CodeInvalidArgument, "namespace, tag, or devices is required")
	}
	allowed, denied, err := s.selectAccountOTA(ctx, msg.GetNamespace(), msg.GetTag(), msg.GetDevices())
	if err != nil {
		return nil, err
	}
	patched, err := s.storeDesiredOTA(ctx, allowed, denied, otaRequest(msg.GetVersion(), time.Now().UTC()), auditOTARequest)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&shadow.AssignOTAResponse{Updated: patched.GetUpdated(), Results: patched.GetResults()}), nil
}
