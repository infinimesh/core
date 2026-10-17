package fanoutpublisher

import (
	"context"

	"github.com/infinimesh/core/v3/pkg/pubsub"
	"github.com/infinimesh/core/v3/pkg/shadow/fanout"
	pb "github.com/infinimesh/proto/shadow"
	amqp "github.com/rabbitmq/amqp091-go"

	"go.uber.org/zap"
)

var (
	setupFanout     = fanout.Setup
	publishLoopExit = func() {}
)

func Setup(logger *zap.Logger, conn *amqp.Connection, ps pubsub.PubSub) error {
	log := logger.Named("FanoutPublisher")
	f, err := setupFanout(log, conn, "shadow.fanout", 1000)
	if err != nil {
		return err
	}

	go publishLoop(log, f, ps.Sub("mqtt.outgoing", "mqtt.incoming"))

	return nil
}

func publishLoop(log *zap.Logger, f fanout.IFanout, messages <-chan interface{}) {
	defer publishLoopExit()
	for msg := range messages {
		log.Debug("Received message to Broadcast", zap.Any("msg", msg))
		shadow, ok := msg.(*pb.Shadow)
		if !ok {
			log.Warn("Message corrupted, couldn't convert to Shadow")
			continue
		}

		err := f.Publish(context.Background(), shadow)
		if err != nil {
			log.Warn("Error publishing to fanout", zap.Error(err))
		}
	}
}
