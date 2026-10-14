package shadow

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/infinimesh/core/pkg/pubsub"
	pb "github.com/infinimesh/proto/shadow"
	"go.uber.org/zap"
)

type blockSeq struct {
	n       atomic.Uint64
	entered chan struct{}
	release chan struct{}
}

func (b *blockSeq) Next(context.Context) uint64 {
	n := b.n.Add(1)
	if n == 1 {
		close(b.entered)
		<-b.release
	}
	return n
}

func TestParallelWritersDeliverInSequenceOrder(t *testing.T) {
	ps := pubsub.New(8)
	seq := &blockSeq{entered: make(chan struct{}), release: make(chan struct{})}
	svc := NewShadowServiceServer(zap.NewNop(), nil, ps)
	svc.SetSequencer(seq)
	ch := make(chan interface{}, 4)
	ps.AddSub(ch, lifecycleTopic)
	ctx := context.Background()
	go func() {
		_, _ = svc.PublishDeviceScope(ctx, &pb.Shadow{Device: "pump", Event: pb.ShadowEvent_DEVICE_ADDED})
	}()
	select {
	case <-seq.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first writer did not allocate a sequence")
	}
	second := make(chan struct{})
	go func() {
		_, _ = svc.PublishDeviceScope(ctx, &pb.Shadow{Device: "valve", Event: pb.ShadowEvent_DEVICE_ADDED})
		close(second)
	}()
	time.Sleep(50 * time.Millisecond)
	close(seq.release)
	var got []uint64
	deadline := time.After(2 * time.Second)
	for len(got) < 2 {
		select {
		case msg := <-ch:
			got = append(got, msg.(*pb.Shadow).GetSequence())
		case <-deadline:
			t.Fatalf("sequences %v", got)
		}
	}
	select {
	case <-second:
	case <-time.After(2 * time.Second):
		t.Fatal("second writer did not finish")
	}
	if got[0] >= got[1] {
		t.Fatalf("delivered out of order: %v", got)
	}
}
