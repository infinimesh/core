package graph

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/arangodb/go-driver"
	driver_mocks "github.com/infinimesh/core/mocks/github.com/arangodb/go-driver"
	inf "github.com/infinimesh/core/pkg/shared"
	pb "github.com/infinimesh/proto/node"
	accpb "github.com/infinimesh/proto/node/accounts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestRootListsEveryAccount(t *testing.T) {
	db := driver_mocks.NewMockDatabase(t)
	cur := driver_mocks.NewMockCursor(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(cur, nil)
	cur.On("Close").Return(nil)
	cur.On("ReadDocument", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		acc := args.Get(1).(*accpb.Account)
		acc.Uuid = "root"
		acc.Title = "infinimesh"
	}).Return(driver.DocumentMeta{Key: "root"}, nil).Once()
	cur.On("ReadDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, driver.NoMoreDocumentsError{}).Once()

	ctrl := &AccountsController{InfinimeshBaseController: InfinimeshBaseController{log: zap.NewNop(), db: db}}
	ctx := context.WithValue(context.Background(), inf.InfinimeshAccountCtxKey, "root")
	ctx = context.WithValue(ctx, inf.InfinimeshRootCtxKey, true)
	res, err := ctrl.List(ctx, connect.NewRequest(&pb.ListRequest{}))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetAccounts(), 1)
	assert.Equal(t, "root", res.Msg.GetAccounts()[0].GetUuid())
}
