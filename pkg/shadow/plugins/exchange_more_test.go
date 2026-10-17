package plugins

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/infinimesh/core/pkg/pubsub"
	devpb "github.com/infinimesh/proto/node/devices"
	pb "github.com/infinimesh/proto/shadow"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

type pbroker struct {
	exErr, qErr, bindErr, pubErr error
	declares                     int
	pubs                         int
}

func (p *pbroker) ExchangeDeclare(name, kind string, durable, autoDelete, internal, noWait bool, args amqp.Table) error {
	return p.exErr
}

func (p *pbroker) QueueDeclare(name string, durable, autoDelete, exclusive, noWait bool, args amqp.Table) (amqp.Queue, error) {
	p.declares++
	if p.qErr != nil {
		return amqp.Queue{}, p.qErr
	}
	return amqp.Queue{Name: name}, nil
}

func (p *pbroker) QueueBind(name, key, exchange string, noWait bool, args amqp.Table) error {
	return p.bindErr
}

func (p *pbroker) PublishWithContext(ctx context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error {
	p.pubs++
	return p.pubErr
}

func TestSetupExchange(t *testing.T) {
	origOpen := openChannel
	origExit := pluginsExit
	t.Cleanup(func() {
		openChannel = origOpen
		pluginsExit = origExit
	})

	ps := pubsub.New(1)
	openChannel = func(*amqp.Connection) (broker, error) { return nil, errors.New("channel") }
	if err := Setup(zap.NewNop(), nil, ps, nil); err == nil {
		t.Fatal("channel")
	}
	ps.Shutdown()

	b := &pbroker{exErr: errors.New("exchange")}
	openChannel = func(*amqp.Connection) (broker, error) { return b, nil }
	ps = pubsub.New(1)
	if err := Setup(zap.NewNop(), nil, ps, nil); err == nil {
		t.Fatal("exchange")
	}
	ps.Shutdown()

	b.exErr = nil
	exited := make(chan struct{})
	pluginsExit = func() { close(exited) }
	fetched := make(chan string, 1)
	ps = pubsub.New(2)
	if err := Setup(zap.NewNop(), nil, ps, func(id string) *devpb.Device {
		fetched <- id
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ps.Pub("nope", "mqtt.incoming")
	ps.Pub(&pb.Shadow{Device: "dev"}, "mqtt.incoming")
	select {
	case id := <-fetched:
		if id != "dev" {
			t.Fatal(id)
		}
	case <-time.After(time.Second):
		t.Fatal("fetch")
	}
	ps.Shutdown()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("exit")
	}
}

func TestPublishSingle(t *testing.T) {
	logger = zap.NewNop()
	qs = map[string]bool{}
	b := &pbroker{}
	channel = b
	dev := &devpb.Device{Uuid: "d", Tags: []string{"plain", "plugin:heat", "plugin:"}}
	Publish(&devpb.Device{Uuid: "d"}, &pb.Shadow{})
	Publish(dev, &pb.Shadow{Device: "d"})
	if b.declares != 2 || b.pubs != 2 {
		t.Fatalf("declares %d pubs %d", b.declares, b.pubs)
	}
	Publish(dev, &pb.Shadow{Device: "d"})
	if b.declares != 2 || b.pubs != 4 {
		t.Fatalf("cached declares %d pubs %d", b.declares, b.pubs)
	}

	b.pubErr = errors.New("publish")
	Publish(dev, &pb.Shadow{Device: "d"})
	if _, ok := qs["heat"]; ok {
		t.Fatal("queue kept after publish error")
	}
	if _, ok := qs[""]; ok {
		t.Fatal("empty plugin kept")
	}

	b.pubErr = nil
	b.qErr = errors.New("queue")
	Publish(dev, &pb.Shadow{Device: "d"})
	b.qErr = nil
	b.bindErr = errors.New("bind")
	Publish(dev, &pb.Shadow{Device: "d"})
	b.bindErr = nil
	if _, err := proto.Marshal(&pb.Shadow{Device: "\xff"}); err != nil {
		before := b.pubs
		PublishSingle("bad", &pb.Shadow{Device: "\xff"})
		if b.pubs != before {
			t.Fatal("invalid state was published")
		}
	}
}
