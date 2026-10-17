package fanout

import (
	"context"
	"sync"

	"github.com/infinimesh/core/pkg/observe"
	"github.com/infinimesh/core/pkg/pubsub"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

type IFanout interface {
	Publish(ctx context.Context, message proto.Message, args ...string) error
	Subscribe(ctx context.Context, client string, args ...string) (<-chan amqp.Delivery, error)
}

type fanout struct {
	log *zap.Logger
	ps  pubsub.PubSub

	exchange string
	channel  broker

	buffer_capacity int
}

type broker interface {
	QueueDeclare(name string, durable, autoDelete, exclusive, noWait bool, args amqp.Table) (amqp.Queue, error)
	QueueBind(name, key, exchange string, noWait bool, args amqp.Table) error
	Consume(queue, consumer string, autoAck, exclusive, noLocal, noWait bool, args amqp.Table) (<-chan amqp.Delivery, error)
	ExchangeDeclare(name, kind string, durable, autoDelete, internal, noWait bool, args amqp.Table) error
	PublishWithContext(ctx context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error
}

var openChannel = func(conn *amqp.Connection) (broker, error) {
	return conn.Channel()
}

var lock = &sync.Mutex{}
var _fanout *fanout

func Get() IFanout {
	return _fanout
}

func Setup(log *zap.Logger, conn *amqp.Connection, name string, buffer_capacity int) (IFanout, error) {
	if _fanout != nil {
		return _fanout, nil
	}
	lock.Lock()
	defer lock.Unlock()

	ch, err := openChannel(conn)
	if err != nil {
		return nil, err
	}

	err = ch.ExchangeDeclare(name, "fanout", true, false, false, false, nil)
	if err != nil {
		return nil, err
	}

	_fanout = &fanout{
		log:             log.Named("fanout"),
		ps:              pubsub.New(buffer_capacity),
		exchange:        name,
		channel:         ch,
		buffer_capacity: buffer_capacity,
	}

	return _fanout, nil
}

// Publish - Publishes message to RabbitMQ Exchange
// Note: This is a blocking call
// Args: Routing Keys
func (f *fanout) Publish(ctx context.Context, message proto.Message, args ...string) error {
	log := f.log.Named("publish")

	routing_key := ""
	if len(args) > 0 {
		routing_key = args[0]
	}

	payload, err := proto.Marshal(message)
	if err != nil {
		log.Warn("Error while publishing message:", zap.Error(err))
		return err
	}

	err = f.channel.PublishWithContext(ctx, f.exchange, routing_key, false, false, amqp.Publishing{
		ContentType: "text/plain", Body: payload,
	})
	if err != nil {
		observe.RabbitPublishErrors.Inc()
	}
	return err
}

// Subscribe - Subscribes to RabbitMQ Exchange

func (f *fanout) Subscribe(ctx context.Context, client string, args ...string) (<-chan amqp.Delivery, error) {
	log := f.log.Named("subscribe")

	q, err := f.channel.QueueDeclare(
		client, false, false, false, false, nil,
	)
	if err != nil {
		log.Warn("Error declaring queue", zap.Error(err))
		return nil, err
	}

	routing_key := ""
	if len(args) > 0 {
		routing_key = args[0]
	}

	err = f.channel.QueueBind(q.Name, routing_key, f.exchange, false, nil)
	if err != nil {
		return nil, err
	}

	return f.channel.Consume(q.Name, client, false, false, false, false, nil)
}
