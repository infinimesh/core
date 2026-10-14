package shadow_test

import (
	"context"
	"sync"
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

// holdStream blocks in the first Send so the replay queue can fill.
type holdStream struct {
	recStream
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *holdStream) Send(sh *pb.Shadow) error {
	h.once.Do(func() { close(h.entered) })
	<-h.release
	return h.recStream.Send(sh)
}

func TestFullQueueClosesTheStream(t *testing.T) {
	ps := pubsub.New(8)
	svc := shadow.NewShadowServiceServer(zap.NewNop(), nil, ps)
	const id = "pump"
	const topic = "mqtt.incoming/" + id
	st := &holdStream{
		recStream: recStream{
			ctx:  context.Background(),
			ch:   make(chan *pb.Shadow, 1),
			stop: make(chan struct{}),
		},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.StreamShadow(&pb.StreamShadowRequest{Devices: []string{id}, OnlyDelta: true}, st)
	}()

	stopPrime := make(chan struct{})
	defer close(stopPrime)
	go func() {
		var i uint64
		for {
			select {
			case <-stopPrime:
				return
			case <-st.entered:
				return
			default:
			}
			i++
			ps.TryPub(&pb.Shadow{Device: id, Sequence: i, EventId: i}, topic)
			select {
			case <-stopPrime:
				return
			case <-st.entered:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	select {
	case <-st.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not accept a live update")
	}

	// Pub blocks in the bus once the drainer has stopped, which is when the
	// replay queue is full. A timed burst is dropped by the bus before then.
	for n := uint64(1); n <= 4096; n++ {
		ack := make(chan struct{})
		go func() {
			ps.Pub(&pb.Shadow{Device: id, Sequence: 100_000 + n, EventId: 100_000 + n}, topic)
			close(ack)
		}()
		select {
		case <-ack:
		case <-time.After(time.Second):
			close(st.release)
			select {
			case err := <-errCh:
				require.ErrorContains(t, err, "shadow stream fell behind")
			case <-time.After(2 * time.Second):
				t.Fatal("a full queue left the stream open")
			}
			return
		}
	}
	t.Fatal("published 4096 updates without filling the queue")
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
