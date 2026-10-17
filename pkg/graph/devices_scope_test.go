package graph

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	inf "github.com/infinimesh/core/v3/pkg/shared"
	pb "github.com/infinimesh/proto/node"
	"github.com/infinimesh/proto/node/access"
	shadowpb "github.com/infinimesh/proto/shadow"
)

type recordScope struct {
	ev  []*shadowpb.Shadow
	err error
}

func (r *recordScope) PublishDeviceScope(_ context.Context, in *shadowpb.Shadow, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if in != nil {
		r.ev = append(r.ev, proto.Clone(in).(*shadowpb.Shadow))
	}
	return &emptypb.Empty{}, r.err
}

func TestMovePublishesAddedNamespace(t *testing.T) {
	rec := &recordScope{}
	ctrl := &DevicesController{
		InfinimeshBaseController: InfinimeshBaseController{log: zap.NewNop()},
		ica_repo:                 fakeICA{},
		scope:                    rec,
	}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "acc")
	resp, err := ctrl.Move(ctx, connect.NewRequest(&pb.MoveRequest{Uuid: "dev", Namespace: "ns2"}))
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	if assert.Len(t, rec.ev, 1) {
		assert.Equal(t, shadowpb.ShadowEvent_DEVICE_ADDED, rec.ev[0].GetEvent())
		assert.Equal(t, "dev", rec.ev[0].GetDevice())
		assert.Equal(t, "ns2", rec.ev[0].GetNamespace())
	}
}

func TestMoveSkipsScopeWhenMoveFails(t *testing.T) {
	rec := &recordScope{}
	ctrl := &DevicesController{
		InfinimeshBaseController: InfinimeshBaseController{log: zap.NewNop()},
		ica_repo:                 fakeICA{moveErr: errors.New("no")},
		scope:                    rec,
	}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "acc")
	_, err := ctrl.Move(ctx, connect.NewRequest(&pb.MoveRequest{Uuid: "dev", Namespace: "ns2"}))
	assert.EqualError(t, err, "no")
	assert.Empty(t, rec.ev)
}

func TestJoinAccountPublishesShare(t *testing.T) {
	ns := "plant"
	rec := &recordScope{}
	ctrl := &DevicesController{
		InfinimeshBaseController: InfinimeshBaseController{log: zap.NewNop()},
		ica_repo: fakeICA{
			access: &access.Access{Level: access.Level_ADMIN, Namespace: &ns},
			title:  "Pump",
			tags:   []string{"water"},
		},
		scope: rec,
	}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "owner")
	_, err := ctrl.Join(ctx, connect.NewRequest(&pb.JoinGeneralRequest{
		Node:   "dev",
		Join:   "Accounts/acc2",
		Access: access.Level_READ,
	}))
	assert.NoError(t, err)
	if assert.Len(t, rec.ev, 1) {
		assert.Equal(t, shadowpb.ShadowEvent_DEVICE_ADDED, rec.ev[0].GetEvent())
		assert.Equal(t, "dev", rec.ev[0].GetDevice())
		assert.Equal(t, "acc2", rec.ev[0].GetAccount())
		assert.Equal(t, "plant", rec.ev[0].GetNamespace())
		assert.Equal(t, "Pump", rec.ev[0].GetTitle())
		assert.Equal(t, []string{"water"}, rec.ev[0].GetTags())
	}

	rec.ev = nil
	_, err = ctrl.Join(ctx, connect.NewRequest(&pb.JoinGeneralRequest{
		Node:   "dev",
		Join:   "Namespaces/other",
		Access: access.Level_READ,
	}))
	assert.NoError(t, err)
	assert.Empty(t, rec.ev)
}

func TestPublishScopeNilDoesNotPanic(t *testing.T) {
	ctrl := &DevicesController{
		InfinimeshBaseController: InfinimeshBaseController{log: zap.NewNop()},
		ica_repo:                 fakeICA{},
	}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "acc")
	resp, err := ctrl.Move(ctx, connect.NewRequest(&pb.MoveRequest{Uuid: "dev", Namespace: "ns2"}))
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	ctrl.publishScope(ctx, shadowpb.ShadowEvent_DEVICE_REMOVED, nil, "ns2", "")
}
