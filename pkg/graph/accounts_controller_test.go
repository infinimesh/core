package graph

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	inf "github.com/infinimesh/core/pkg/shared"
	pb "github.com/infinimesh/proto/node"
	"github.com/infinimesh/proto/node/access"
	accpb "github.com/infinimesh/proto/node/accounts"
)

func TestAccountsController_Token_RequiresAuth(t *testing.T) {
	ctrl := &AccountsController{
		InfinimeshBaseController: InfinimeshBaseController{
			log: zap.NewNop(),
		},
	}
	_, err := ctrl.Token(context.Background(), connect.NewRequest(&pb.TokenRequest{}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAccountsController_Get_AllowsRead(t *testing.T) {
	ctrl := &AccountsController{
		InfinimeshBaseController: InfinimeshBaseController{
			log: zap.NewExample(),
		},
		ica_repo: fakeICARepo{accessLevel: access.Level_READ, accessOK: true},
	}

	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "requestor")
	resp, err := ctrl.Get(ctx, connect.NewRequest(&accpb.Account{Uuid: "target"}))
	assert.NoError(t, err)
	assert.Equal(t, "target", resp.Msg.Uuid)
}

func TestAccountsController_Get_DeniesLowAccess(t *testing.T) {
	ctrl := &AccountsController{
		InfinimeshBaseController: InfinimeshBaseController{
			log: zap.NewExample(),
		},
		ica_repo: fakeICARepo{accessLevel: access.Level_NONE, accessOK: true},
	}

	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "requestor")
	_, err := ctrl.Get(ctx, connect.NewRequest(&accpb.Account{Uuid: "target"}))
	assert.Error(t, err)
}
