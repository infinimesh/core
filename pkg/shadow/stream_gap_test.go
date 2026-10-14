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

func TestResumePastTheWindowReturnsAnError(t *testing.T) {
	ps := pubsub.New(8)
	svc := shadow.NewShadowServiceServer(zap.NewNop(), nil, ps)
	ctx := context.Background()
	const id = "pump"
	for i := uint64(1); i <= 258; i++ {
		_, err := svc.PublishDeviceScope(ctx, &pb.Shadow{
			Device:   id,
			Sequence: i,
			EventId:  i,
			Event:    pb.ShadowEvent_OTA_STATUS_CHANGED,
		})
		require.NoError(t, err)
	}

	st := &recStream{ctx: ctx, ch: make(chan *pb.Shadow, 8), stop: make(chan struct{})}
	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.StreamShadow(&pb.StreamShadowRequest{
			Devices:     []string{id},
			ResumeAfter: 1,
			OnlyDelta:   true,
		}, st)
	}()
	select {
	case err := <-errCh:
		require.ErrorContains(t, err, "resume window exceeded")
	case sh := <-st.ch:
		t.Fatalf("resumed past the window with sequence %d", sh.GetSequence())
	case <-time.After(time.Second):
		t.Fatal("resume past the window left the stream open")
	}
}

func TestResumeAtTheWindowEdgeReplays(t *testing.T) {
	ps := pubsub.New(8)
	svc := shadow.NewShadowServiceServer(zap.NewNop(), nil, ps)
	ctx := context.Background()
	const id = "pump"
	for i := uint64(1); i <= 258; i++ {
		_, err := svc.PublishDeviceScope(ctx, &pb.Shadow{
			Device:   id,
			Sequence: i,
			EventId:  i,
			Event:    pb.ShadowEvent_OTA_STATUS_CHANGED,
		})
		require.NoError(t, err)
	}

	st := &recStream{ctx: ctx, ch: make(chan *pb.Shadow, 8), stop: make(chan struct{})}
	go func() {
		_ = svc.StreamShadow(&pb.StreamShadowRequest{
			Devices:     []string{id},
			ResumeAfter: 2,
			OnlyDelta:   true,
		}, st)
	}()
	waitShadow(t, st.ch, func(sh *pb.Shadow) bool { return sh.GetSequence() == 3 })
}

func TestDeviceListDoesNotAdvancePastReplay(t *testing.T) {
	ps := pubsub.New(8)
	svc := shadow.NewShadowServiceServer(zap.NewNop(), nil, ps)
	ctx := context.Background()
	const id = "pump"
	for i := 0; i < 10; i++ {
		_, err := svc.PublishDeviceScope(ctx, &pb.Shadow{
			Device:    id,
			Namespace: "plant",
			Event:     pb.ShadowEvent_OTA_STATUS_CHANGED,
		})
		require.NoError(t, err)
	}

	st := &recStream{ctx: ctx, ch: make(chan *pb.Shadow, 32), stop: make(chan struct{})}
	go func() {
		_ = svc.StreamShadow(&pb.StreamShadowRequest{
			Namespace: "plant",
			Catalog: []*pb.DeviceMeta{{
				Device: id, Namespace: "plant", Title: "Pump",
			}},
			ResumeAfter: 5,
			OnlyDelta:   true,
		}, st)
	}()

	var seqs []uint64
	deadline := time.After(2 * time.Second)
	for {
		select {
		case sh := <-st.ch:
			seqs = append(seqs, sh.GetSequence())
			if sh.GetSequence() == 6 {
				for _, seq := range seqs {
					if seq > 6 {
						t.Fatalf("sequence %d arrived before replayed 6: %v", seq, seqs)
					}
				}
				return
			}
		case <-deadline:
			t.Fatalf("replay did not arrive: %v", seqs)
		}
	}
}

func TestQuietNamespaceKeepsItsReplayWindow(t *testing.T) {
	ps := pubsub.New(8)
	svc := shadow.NewShadowServiceServer(zap.NewNop(), nil, ps)
	ctx := context.Background()
	for i := 0; i < 300; i++ {
		_, err := svc.PublishDeviceScope(ctx, &pb.Shadow{
			Device:    "busy",
			Namespace: "busy",
			Event:     pb.ShadowEvent_OTA_STATUS_CHANGED,
		})
		require.NoError(t, err)
	}
	_, err := svc.PublishDeviceScope(ctx, &pb.Shadow{
		Device:    "quiet",
		Namespace: "quiet",
		Event:     pb.ShadowEvent_OTA_STATUS_CHANGED,
	})
	require.NoError(t, err)

	st := &recStream{ctx: ctx, ch: make(chan *pb.Shadow, 8), stop: make(chan struct{})}
	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.StreamShadow(&pb.StreamShadowRequest{
			Namespace: "quiet",
			Catalog: []*pb.DeviceMeta{{
				Device: "quiet", Namespace: "quiet", Title: "Quiet",
			}},
			ResumeAfter: 1,
			OnlyDelta:   true,
		}, st)
	}()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case err := <-errCh:
			t.Fatalf("quiet namespace: %v", err)
		case sh := <-st.ch:
			if sh.GetDevice() == "quiet" && sh.GetEvent() == pb.ShadowEvent_OTA_STATUS_CHANGED {
				return
			}
		case <-deadline:
			t.Fatal("quiet namespace did not replay its update")
		}
	}
}

func TestResumeKeepsTheUpdateThatLandsBeforeSubscribe(t *testing.T) {
	ps := pubsub.New(8)
	rdb := redis_mocks.NewMockCmdable(t)
	svc := shadow.NewShadowServiceServer(zap.NewNop(), rdb, ps)
	const id = "pump"
	ctx := context.Background()

	_, err := svc.PublishDeviceScope(ctx, &pb.Shadow{
		Device:   id,
		Sequence: 3,
		EventId:  3,
		Event:    pb.ShadowEvent_OTA_STATUS_CHANGED,
	})
	require.NoError(t, err)

	mget := redis.NewSliceCmd(ctx)
	mget.SetVal([]interface{}{nil, nil, nil})
	rdb.EXPECT().MGet(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Run(func(context.Context, ...string) {
		mq.Deliver(ps, "mqtt.incoming/"+id, &pb.Shadow{
			Device:   id,
			Sequence: 3,
			EventId:  3,
			Event:    pb.ShadowEvent_OTA_STATUS_CHANGED,
		})
		mq.Deliver(ps, "mqtt.incoming/"+id, &pb.Shadow{
			Device:   id,
			Sequence: 77,
			EventId:  77,
			Event:    pb.ShadowEvent_OTA_STATUS_CHANGED,
		})
	}).Return(mget)

	st := &recStream{
		ctx:  ctx,
		ch:   make(chan *pb.Shadow, 8),
		stop: make(chan struct{}),
	}
	done := make(chan struct{})
	go func() {
		_ = svc.StreamShadow(&pb.StreamShadowRequest{
			Devices:     []string{id},
			ResumeAfter: 1,
			Sync:        true,
			OnlyDelta:   true,
		}, st)
		close(done)
	}()

	counts := map[uint64]int{}
	deadline := time.After(2 * time.Second)
	for counts[77] == 0 {
		select {
		case sh := <-st.ch:
			if sh.GetSequence() > 0 {
				counts[sh.GetSequence()]++
			}
		case <-deadline:
			t.Fatalf("missed the update published before subscribe: %v", counts)
		}
	}
	time.Sleep(50 * time.Millisecond)
	for {
		select {
		case sh := <-st.ch:
			if sh.GetSequence() > 0 {
				counts[sh.GetSequence()]++
			}
		default:
			goto stopped
		}
	}
stopped:
	require.Equal(t, 1, counts[3])
	require.Equal(t, 1, counts[77])

	close(st.stop)
	mq.Deliver(ps, "mqtt.incoming/"+id, &pb.Shadow{Device: id, Sequence: 78, EventId: 78})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not return")
	}
}
