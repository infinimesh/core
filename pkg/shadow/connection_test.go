package shadow_test

import (
	"context"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	mq "github.com/infinimesh/core/v3/pkg/mqtt/pubsub"
	"github.com/infinimesh/core/v3/pkg/pubsub"
	"github.com/infinimesh/core/v3/pkg/shadow"
	pb "github.com/infinimesh/proto/shadow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

func TestIncomingConnectionReachesStream(t *testing.T) {
	ps := pubsub.New(8)
	svc := shadow.NewShadowServiceServer(zap.NewNop(), nil, ps)
	shadow.Inbound = svc.Accept
	t.Cleanup(func() { shadow.Inbound = nil })

	st := &recStream{
		ctx:  context.Background(),
		ch:   make(chan *pb.Shadow, 8),
		stop: make(chan struct{}),
	}
	done := make(chan struct{})
	go func() {
		_ = svc.StreamShadow(&pb.StreamShadowRequest{
			Devices:   []string{"dev"},
			OnlyDelta: true,
		}, st)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	var got *pb.Shadow
	for got == nil && time.Now().Before(deadline) {
		mq.Deliver(ps, "mqtt.incoming", &pb.Shadow{
			Device:     "dev",
			Connection: &pb.ConnectionState{Connected: true, ClientId: "c1"},
		})
		select {
		case sh := <-st.ch:
			if sh.GetDevice() == "dev" && sh.GetConnection().GetConnected() {
				got = sh
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
	if got == nil || got.GetConnection().GetClientId() != "c1" || got.GetEventId() == 0 {
		t.Fatalf("%+v", got)
	}

	close(st.stop)
	mq.Deliver(ps, "mqtt.incoming", &pb.Shadow{Device: "dev"})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not return")
	}
}

func TestStreamShadowSyncSendsConnection(t *testing.T) {
	f := newShadowServiceServerFixture(t)
	f.mocks.srv.EXPECT().Context().Return(f.data.ctx)

	mget := redis.NewSliceCmd(f.data.ctx)
	mget.SetVal([]interface{}{nil, nil, f.data.sample_connection})
	f.mocks.rdb.EXPECT().MGet(
		f.data.ctx,
		shadow.Key(f.data.uuid, pb.StateKey_REPORTED),
		shadow.Key(f.data.uuid, pb.StateKey_DESIRED),
		shadow.Key(f.data.uuid, pb.StateKey_CONNECTION),
	).Return(mget)

	var got *pb.Shadow
	f.mocks.srv.EXPECT().Send(mock.MatchedBy(func(sh *pb.Shadow) bool {
		if got == nil {
			got = proto.Clone(sh).(*pb.Shadow)
		}
		return true
	})).Return(nil).Once()
	f.mocks.srv.EXPECT().Send(mock.Anything).Return(assert.AnError).Once()
	f.mocks.ps.EXPECT().AddSub(mock.MatchedBy(func(ch chan interface{}) bool {
		ch <- &pb.Shadow{Device: f.data.uuid}
		return true
	}), "mqtt.incoming/"+f.data.uuid, "mqtt.outgoing/"+f.data.uuid).Return()
	f.mocks.ps.EXPECT().Unsub(mock.MatchedBy(func(ch chan interface{}) bool {
		close(ch)
		return true
	})).Return()

	err := f.service.StreamShadow(&pb.StreamShadowRequest{
		Devices:   []string{f.data.uuid},
		Sync:      true,
		OnlyDelta: true,
	}, f.mocks.srv)
	assert.NoError(t, err)
	assert.True(t, got.GetConnection().GetConnected())
	assert.Equal(t, "device1", got.GetConnection().GetClientId())
}
