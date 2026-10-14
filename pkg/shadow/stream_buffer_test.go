package shadow_test

import (
	"context"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	redis_mocks "github.com/infinimesh/core/mocks/github.com/go-redis/redis/v8"
	mq "github.com/infinimesh/core/pkg/mqtt/pubsub"
	"github.com/infinimesh/core/pkg/pubsub"
	"github.com/infinimesh/core/pkg/shadow"
	pb "github.com/infinimesh/proto/shadow"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestLiveUpdatesArriveOutOfOrder(t *testing.T) {
	ps := pubsub.New(8)
	svc := shadow.NewShadowServiceServer(zap.NewNop(), nil, ps)
	const id = "pump"
	st := &recStream{ctx: context.Background(), ch: make(chan *pb.Shadow, 8), stop: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		_ = svc.StreamShadow(&pb.StreamShadowRequest{Devices: []string{id}, OnlyDelta: true}, st)
		close(done)
	}()

	five := &pb.Shadow{Device: id, Sequence: 5, EventId: 5}
	deadline := time.Now().Add(2 * time.Second)
	for {
		mq.Deliver(ps, "mqtt.incoming/"+id, five)
		select {
		case sh := <-st.ch:
			if sh.GetEventId() == 5 {
				goto sawFive
			}
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("stream did not deliver sequence 5")
		}
	}
sawFive:
	mq.Deliver(ps, "mqtt.incoming/"+id, &pb.Shadow{Device: id, Sequence: 4, EventId: 4})
	waitShadow(t, st.ch, func(sh *pb.Shadow) bool { return sh.GetEventId() == 4 })
	close(st.stop)
	mq.Deliver(ps, "mqtt.incoming/"+id, &pb.Shadow{Device: id, Sequence: 6, EventId: 6})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not return")
	}
}

func TestReplayKeepsLiveTraffic(t *testing.T) {
	ps := pubsub.New(8)
	rdb := redis_mocks.NewMockCmdable(t)
	svc := shadow.NewShadowServiceServer(zap.NewNop(), rdb, ps)
	const id = "pump"
	ctx := context.Background()
	release := make(chan struct{})
	inGet := make(chan struct{})
	mget := redis.NewSliceCmd(ctx)
	mget.SetVal([]interface{}{nil, nil, nil})
	rdb.EXPECT().MGet(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Run(func(context.Context, ...string) {
		close(inGet)
		<-release
	}).Return(mget)

	st := &recStream{ctx: ctx, ch: make(chan *pb.Shadow, 64), stop: make(chan struct{})}
	go func() {
		_ = svc.StreamShadow(&pb.StreamShadowRequest{Devices: []string{id}, Sync: true, OnlyDelta: true}, st)
	}()
	<-inGet

	side := ps.Sub("mqtt.incoming/" + id)
	const n = 30
	const base = uint64(1_000_000)
	for i := uint64(1); i <= n; i++ {
		mq.Deliver(ps, "mqtt.incoming/"+id, &pb.Shadow{
			Device: id, Sequence: base + i, EventId: base + i,
		})
	}
	select {
	case <-side:
	case <-time.After(time.Second):
		t.Fatal("a full stream buffer stalled another subscriber")
	}
	close(release)

	got := map[uint64]int{}
	deadline := time.After(2 * time.Second)
	for got[base+n] == 0 {
		select {
		case sh := <-st.ch:
			if sh.GetEventId() > base {
				got[sh.GetEventId()]++
			}
		case <-deadline:
			t.Fatalf("live events during replay: got %d of %d", len(got), n)
		}
	}
	for i := uint64(1); i <= n; i++ {
		require.Equal(t, 1, got[base+i], "sequence %d", base+i)
	}
}
