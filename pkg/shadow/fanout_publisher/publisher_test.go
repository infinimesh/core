package fanoutpublisher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/infinimesh/core/v3/pkg/pubsub"
	"github.com/infinimesh/core/v3/pkg/shadow/fanout"
	pb "github.com/infinimesh/proto/shadow"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

type stubFanout struct {
	err error
	n   int
}

func (s *stubFanout) Publish(ctx context.Context, message proto.Message, args ...string) error {
	s.n++
	return s.err
}

func (s *stubFanout) Subscribe(ctx context.Context, client string, args ...string) (<-chan amqp.Delivery, error) {
	return nil, nil
}

func TestPublishLoop(t *testing.T) {
	messages := make(chan interface{}, 3)
	f := &stubFanout{err: errors.New("publish")}
	messages <- "bad"
	messages <- &pb.Shadow{Device: "dev"}
	close(messages)
	publishLoop(zap.NewNop(), f, messages)
	if f.n != 1 {
		t.Fatal(f.n)
	}
	f.err = nil
	more := make(chan interface{}, 1)
	more <- &pb.Shadow{Device: "ok"}
	close(more)
	publishLoop(zap.NewNop(), f, more)
	if f.n != 2 {
		t.Fatal(f.n)
	}
}

func TestSetup(t *testing.T) {
	origSetup := setupFanout
	origExit := publishLoopExit
	t.Cleanup(func() {
		setupFanout = origSetup
		publishLoopExit = origExit
	})
	setupFanout = func(*zap.Logger, *amqp.Connection, string, int) (fanout.IFanout, error) {
		return nil, errors.New("fanout")
	}
	failed := pubsub.New(1)
	if err := Setup(zap.NewNop(), nil, failed); err == nil {
		t.Fatal("expected error")
	}
	failed.Shutdown()

	exited := make(chan struct{})
	publishLoopExit = func() { close(exited) }
	setupFanout = func(*zap.Logger, *amqp.Connection, string, int) (fanout.IFanout, error) {
		return &stubFanout{}, nil
	}
	ps := pubsub.New(1)
	if err := Setup(zap.NewNop(), nil, ps); err != nil {
		t.Fatal(err)
	}
	ps.Shutdown()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("loop did not exit")
	}
}
