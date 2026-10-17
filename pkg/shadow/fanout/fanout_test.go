package fanout

import (
	"context"
	"errors"
	"testing"

	pb "github.com/infinimesh/proto/shadow"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

type fakeBroker struct {
	exchangeErr error
	declareErr  error
	bindErr     error
	consumeErr  error
	pubErr      error
	msgs        chan amqp.Delivery
	lastKey     string
}

func (f *fakeBroker) ExchangeDeclare(name, kind string, durable, autoDelete, internal, noWait bool, args amqp.Table) error {
	return f.exchangeErr
}

func (f *fakeBroker) QueueDeclare(name string, durable, autoDelete, exclusive, noWait bool, args amqp.Table) (amqp.Queue, error) {
	if f.declareErr != nil {
		return amqp.Queue{}, f.declareErr
	}
	return amqp.Queue{Name: name}, nil
}

func (f *fakeBroker) QueueBind(name, key, exchange string, noWait bool, args amqp.Table) error {
	f.lastKey = key
	return f.bindErr
}

func (f *fakeBroker) Consume(queue, consumer string, autoAck, exclusive, noLocal, noWait bool, args amqp.Table) (<-chan amqp.Delivery, error) {
	if f.consumeErr != nil {
		return nil, f.consumeErr
	}
	return f.msgs, nil
}

func (f *fakeBroker) PublishWithContext(ctx context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error {
	f.lastKey = key
	return f.pubErr
}

func TestSetupPublishSubscribe(t *testing.T) {
	orig := openChannel
	t.Cleanup(func() {
		if _fanout != nil && _fanout.ps != nil {
			_fanout.ps.Shutdown()
		}
		_fanout = nil
		openChannel = orig
	})
	_ = Get()
	if _fanout != nil {
		t.Fatal("singleton")
	}
	openChannel = func(*amqp.Connection) (broker, error) { return nil, errors.New("channel") }
	if _, err := Setup(zap.NewNop(), nil, "ex", 1); err == nil || _fanout != nil {
		t.Fatal("channel error")
	}

	b := &fakeBroker{exchangeErr: errors.New("exchange"), msgs: make(chan amqp.Delivery)}
	openChannel = func(*amqp.Connection) (broker, error) { return b, nil }
	if _, err := Setup(zap.NewNop(), nil, "ex", 1); err == nil {
		t.Fatal("exchange error")
	}

	b.exchangeErr = nil
	got, err := Setup(zap.NewNop(), nil, "shadow.fanout", 2)
	if err != nil {
		t.Fatal(err)
	}
	if Get() != got {
		t.Fatal("get")
	}
	again, err := Setup(zap.NewNop(), nil, "other", 1)
	if err != nil || again != got {
		t.Fatal("cached setup")
	}

	ctx := context.Background()
	f := got.(*fanout)
	if _, err := proto.Marshal(&pb.Shadow{Device: "\xff"}); err != nil {
		if err := f.Publish(ctx, &pb.Shadow{Device: "\xff"}, "rk"); err == nil {
			t.Fatal("marshal")
		}
	}
	if err := f.Publish(ctx, &pb.Shadow{Device: "d"}); err != nil {
		t.Fatal(err)
	}
	if b.lastKey != "" {
		t.Fatal(b.lastKey)
	}
	if err := f.Publish(ctx, &pb.Shadow{Device: "d"}, "rk"); err != nil || b.lastKey != "rk" {
		t.Fatal(err, b.lastKey)
	}
	b.pubErr = errors.New("publish")
	if err := f.Publish(ctx, &pb.Shadow{Device: "d"}); err == nil {
		t.Fatal("publish error")
	}
	b.pubErr = nil

	b.declareErr = errors.New("queue")
	if _, err := f.Subscribe(ctx, "client"); err == nil {
		t.Fatal("declare")
	}
	b.declareErr = nil
	b.bindErr = errors.New("bind")
	if _, err := f.Subscribe(ctx, "client", "rk"); err == nil || b.lastKey != "rk" {
		t.Fatal("bind")
	}
	b.bindErr = nil
	b.consumeErr = errors.New("consume")
	if _, err := f.Subscribe(ctx, "client"); err == nil {
		t.Fatal("consume")
	}
	b.consumeErr = nil
	ch, err := f.Subscribe(ctx, "client", "live")
	if err != nil || ch != b.msgs || b.lastKey != "live" {
		t.Fatal(err)
	}
}
