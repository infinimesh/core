package pubsub

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/infinimesh/core/pkg/pubsub"
	"github.com/infinimesh/core/pkg/shadow"
	pb "github.com/infinimesh/proto/shadow"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

type fakeBroker struct {
	mu        sync.Mutex
	declares  int
	consumes  int
	pubs      int
	published chan struct{}
	msgs      chan amqp.Delivery
}

func (f *fakeBroker) QueueDeclare(name string, durable, autoDelete, exclusive, noWait bool, args amqp.Table) (amqp.Queue, error) {
	f.mu.Lock()
	f.declares++
	n := f.declares
	f.mu.Unlock()
	if n == 1 {
		return amqp.Queue{}, errors.New("declare")
	}
	return amqp.Queue{Name: name}, nil
}

func (f *fakeBroker) PublishWithContext(ctx context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error {
	f.mu.Lock()
	f.pubs++
	n := f.pubs
	f.mu.Unlock()
	if n == 1 {
		return errors.New("publish")
	}
	select {
	case f.published <- struct{}{}:
	default:
	}
	return nil
}

func (f *fakeBroker) Consume(queue, consumer string, autoAck, exclusive, noLocal, noWait bool, args amqp.Table) (<-chan amqp.Delivery, error) {
	f.mu.Lock()
	f.consumes++
	n := f.consumes
	f.mu.Unlock()
	if n == 1 {
		return nil, errors.New("consume")
	}
	return f.msgs, nil
}

func TestSetupDeclaresAndDelivers(t *testing.T) {
	origOpen := openChannel
	origRetry := retryWait
	origPubExit := publishExit
	origSubExit := subscribeExit
	t.Cleanup(func() {
		openChannel = origOpen
		retryWait = origRetry
		publishExit = origPubExit
		subscribeExit = origSubExit
		shadow.Dispatch = nil
		shadow.Inbound = nil
	})
	retryWait = 0
	shadow.Dispatch = nil
	shadow.Inbound = nil

	openChannel = func(*amqp.Connection) (brokerChannel, error) {
		return nil, errors.New("channel")
	}
	if _, err := Setup(zap.NewNop(), nil, "in", "out", 1); err == nil {
		t.Fatal("expected channel error")
	}

	msgs := make(chan amqp.Delivery, 2)
	broker := &fakeBroker{published: make(chan struct{}, 2), msgs: msgs}
	openChannel = func(*amqp.Connection) (brokerChannel, error) { return broker, nil }
	pubDone := make(chan struct{})
	subDone := make(chan struct{})
	publishExit = func() { close(pubDone) }
	subscribeExit = func() { close(subDone) }

	bus, err := Setup(zap.NewNop(), nil, "in", "out", 4)
	if err != nil {
		t.Fatal(err)
	}
	heard := bus.Sub("out")
	deadline := time.Now().Add(2 * time.Second)
	for {
		bus.Pub(&pb.Shadow{Device: "\xff"}, "in")
		bus.Pub(&pb.Shadow{Device: "dev"}, "in")
		select {
		case <-broker.published:
			goto published
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("publish was not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
published:
	body, err := proto.Marshal(&pb.Shadow{Device: "dev", EventId: 3})
	if err != nil {
		t.Fatal(err)
	}
	msgs <- amqp.Delivery{Body: []byte("nope")}
	msgs <- amqp.Delivery{Body: body}
	select {
	case <-heard:
	case <-time.After(2 * time.Second):
		t.Fatal("delivery was not published")
	}
	close(msgs)
	bus.Shutdown()
	select {
	case <-pubDone:
	case <-time.After(2 * time.Second):
		t.Fatal("publish loop")
	}
	select {
	case <-subDone:
	case <-time.After(2 * time.Second):
		t.Fatal("subscribe loop")
	}
}

func TestDeliver(t *testing.T) {
	t.Cleanup(func() {
		shadow.Dispatch = nil
		shadow.Inbound = nil
	})
	bus := pubsub.New(2)
	t.Cleanup(bus.Shutdown)
	ch := bus.Sub("t", "t/dev")

	shadow.Dispatch = nil
	shadow.Inbound = nil
	Deliver(bus, "t", &pb.Shadow{Device: "dev", EventId: 4})
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("direct publish")
	}

	called := false
	shadow.Inbound = func(*pb.Shadow) { called = true }
	Deliver(bus, "t", &pb.Shadow{Device: "dev"})
	if !called {
		t.Fatal("inbound")
	}
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("inbound publish")
	}

	shadow.Inbound = func(*pb.Shadow) { t.Fatal("inbound with event") }
	Deliver(bus, "t", &pb.Shadow{Device: "dev", EventId: 1})
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("event publish")
	}

	var ran bool
	shadow.Dispatch = func(sh *pb.Shadow, pub func()) {
		ran = sh.GetDevice() == "dev"
		pub()
	}
	Deliver(bus, "t", &pb.Shadow{Device: "dev"})
	if !ran {
		t.Fatal("dispatch")
	}
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("dispatch publish")
	}
}
