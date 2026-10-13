package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/infinimesh/core/pkg/apierr"
	"github.com/infinimesh/core/pkg/graph"
	inf "github.com/infinimesh/core/pkg/shared"
	devpb "github.com/infinimesh/proto/node/devices"
	nspb "github.com/infinimesh/proto/node/namespaces"
	"github.com/infinimesh/proto/shadow"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/structpb"
)

const otaProviderHeader = "x-infinimesh-ota-provider"

const auditOTAAssign = "ota_assign"

type otaAuditor interface {
	Record(ctx context.Context, action, device, account, tokenID string)
}

func (s *ShadowAPI) SetAuditor(a otaAuditor) { s.auditor = a }

func providerHeader(ctx context.Context, h http.Header) string {
	if h != nil {
		if v := strings.TrimSpace(h.Get(otaProviderHeader)); v != "" {
			return v
		}
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(otaProviderHeader)
	if len(vals) == 0 {
		return ""
	}
	return strings.TrimSpace(vals[0])
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
	res, err := s.namespaces.Get(ctx, connect.NewRequest(&nspb.Namespace{Uuid: id}))
	if err != nil {
		return nil, err
	}
	cache[id] = res.Msg
	return res.Msg, nil
}

func (s *ShadowAPI) allowDeviceOTA(ctx context.Context, header string, dev *devpb.Device, cache map[string]*nspb.Namespace) error {
	ns, err := s.namespaceDoc(ctx, dev.GetAccess().GetNamespace(), cache)
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
	if provider != "none" && header != provider {
		return apierr.Code(connect.CodePermissionDenied, "ota provider does not match the namespace")
	}
	return nil
}

func (s *ShadowAPI) authorizeDesiredOTA(ctx context.Context, header, device string) error {
	devs, err := s.manageableDevices(ctx, "", "")
	if err != nil {
		return err
	}
	for _, dev := range devs {
		if dev.GetUuid() != device {
			continue
		}
		return s.allowDeviceOTA(ctx, header, dev, map[string]*nspb.Namespace{})
	}
	return apierr.Code(connect.CodePermissionDenied, "device is outside of token scope or not allowed")
}

func (s *ShadowAPI) selectOTA(ctx context.Context, header, ns, tag string, explicit []string) ([]*devpb.Device, []*shadow.DevicePatchResult, error) {
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
		if err := s.allowDeviceOTA(ctx, header, dev, cache); err != nil {
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

func (s *ShadowAPI) patchDesiredOTA(ctx context.Context, header string, ns, tag string, explicit []string, desired *shadow.State, audit bool) (*shadow.PatchManyResponse, error) {
	allowed, denied, err := s.selectOTA(ctx, header, ns, tag, explicit)
	if err != nil {
		return nil, err
	}
	resp := &shadow.PatchManyResponse{Results: denied}
	if len(allowed) == 0 {
		return resp, nil
	}
	ids := make([]string, 0, len(allowed))
	for _, dev := range allowed {
		ids = append(ids, dev.GetUuid())
	}
	patched, err := s.client.PatchMany(ctx, &shadow.PatchManyRequest{Devices: ids, Desired: desired})
	if err != nil {
		return nil, asConnect(err)
	}
	resp.Updated = patched.GetUpdated()
	resp.Results = append(resp.Results, patched.GetResults()...)
	if audit {
		s.auditAssign(ctx, patched.GetResults())
	}
	return resp, nil
}

func (s *ShadowAPI) auditAssign(ctx context.Context, results []*shadow.DevicePatchResult) {
	if s.auditor == nil {
		return
	}
	account, _ := ctx.Value(inf.InfinimeshAccountCtxKey).(string)
	jti, _ := ctx.Value(inf.InfinimeshJTICtxKey).(string)
	for _, res := range results {
		if res.GetError() == "" && res.GetDevice() != "" {
			s.auditor.Record(ctx, auditOTAAssign, res.GetDevice(), account, jti)
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
	patched, err := s.patchDesiredOTA(ctx, providerHeader(ctx, request.Header()), msg.GetNamespace(), msg.GetTag(), msg.GetDevices(), otaDesired(msg.GetVersion(), time.Now().UTC()), true)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&shadow.AssignOTAResponse{Updated: patched.GetUpdated(), Results: patched.GetResults()}), nil
}
