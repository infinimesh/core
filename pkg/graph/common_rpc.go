package graph

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/infinimesh/proto/node"
	devpb "github.com/infinimesh/proto/node/devices"
	shadowpb "github.com/infinimesh/proto/shadow"

	inf "github.com/infinimesh/core/pkg/shared"
)

func (ctrl *DevicesController) Move(ctx context.Context, msg *connect.Request[node.MoveRequest]) (*connect.Response[node.EmptyMessage], error) {
	req := msg.Msg
	loaded := NewBlankDeviceDocument(req.GetUuid())
	if requestor, ok := ctx.Value(inf.InfinimeshAccountCtxKey).(string); ok && requestor != "" {
		if err := ctrl.ica_repo.AccessLevelAndGet(ctx, ctrl.log.Named("Move"), NewBlankAccountDocument(requestor), loaded); err != nil {
			loaded = NewBlankDeviceDocument(req.GetUuid())
		}
	}
	old := loaded.GetAccess().GetNamespace()
	snapshot := &devpb.Device{
		Uuid:  req.GetUuid(),
		Title: loaded.GetTitle(),
		Tags:  append([]string(nil), loaded.GetTags()...),
	}

	if err := ctrl.ica_repo.Move(ctx, ctrl, NewBlankDeviceDocument(req.GetUuid()), ctrl.ns2dev, req.GetNamespace()); err != nil {
		return nil, err
	}
	if old != "" && old != req.GetNamespace() {
		ctrl.publishScope(ctx, shadowpb.ShadowEvent_DEVICE_REMOVED, snapshot, old, "")
	}
	ctrl.publishScope(ctx, shadowpb.ShadowEvent_DEVICE_ADDED, snapshot, req.GetNamespace(), "")
	return connect.NewResponse(&node.EmptyMessage{}), nil
}

func (ctrl *AccountsController) Move(ctx context.Context, _req *connect.Request[node.MoveRequest]) (*connect.Response[node.EmptyMessage], error) {
	req := _req.Msg
	obj := NewBlankAccountDocument(req.GetUuid())

	return connect.NewResponse(&node.EmptyMessage{}), ctrl.ica_repo.Move(ctx, ctrl, obj, ctrl.ns2acc, req.GetNamespace())
}

func StatusFromString(code connect.Code, format string, args ...any) *connect.Error {
	return connect.NewError(code, fmt.Errorf(format, args...))
}
