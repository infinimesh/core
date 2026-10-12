/*
Copyright © 2021-2023 Infinite Devices GmbH

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package plugins

import (
	"context"
	"strings"

	"github.com/infinimesh/core/pkg/pubsub"
	devpb "github.com/infinimesh/proto/node/devices"
	pb "github.com/infinimesh/proto/shadow"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

var (
	logger  *zap.Logger
	channel *amqp.Channel
	qs      map[string]bool
)

type FetchDeviceFunc func(string) *devpb.Device

func Setup(Log *zap.Logger, conn *amqp.Connection, ps pubsub.PubSub, fetcher FetchDeviceFunc) error {
	logger = Log.Named("PluginsExchange")

	ch, err := conn.Channel()
	if err != nil {
		return err
	}

	err = ch.ExchangeDeclare("plugins", "topic", true, false, false, false, nil)
	if err != nil {
		return err
	}

	channel = ch
	qs = make(map[string]bool)

	go func(messages chan interface{}) {
		for msg := range messages {
			logger.Debug("Received message to Broadcast", zap.Any("msg", msg))
			handlePluginMessage(msg, fetcher)
		}
	}(ps.Sub("mqtt.outgoing", "mqtt.incoming"))

	return nil
}

var publishPlugin = Publish

func handlePluginMessage(msg any, fetcher FetchDeviceFunc) {
	shadow, ok := msg.(*pb.Shadow)
	if !ok || shadow == nil {
		logger.Warn("Message corrupted, couldn't convert to Shadow")
		return
	}
	dev := fetcher(shadow.GetDevice())
	if dev == nil {
		logger.Warn("Couldn't get Device from Shadow")
		return
	}
	out := proto.Clone(shadow).(*pb.Shadow)
	if title := dev.GetTitle(); title != "" {
		out.Title = title
	}
	if ns := dev.GetAccess().GetNamespace(); ns != "" {
		out.Namespace = ns
	}
	if tags := dev.GetTags(); len(tags) > 0 {
		out.Tags = append([]string(nil), tags...)
	}
	publishPlugin(dev, out)
}

func Publish(dev *devpb.Device, state *pb.Shadow) {
	log := logger.Named(dev.GetUuid())
	log.Debug("Handling publish to Plugin Queue")
	for _, tag := range dev.Tags {
		plugin := strings.TrimPrefix(tag, "plugin:")
		log.Debug("Device data", zap.String("tag", tag), zap.String("plugin", plugin))
		if plugin != tag {
			PublishSingle(plugin, state)
		}
	}
}

func SetupQueue(log *zap.Logger, channel *amqp.Channel, plugin string) (amqp.Queue, error) {
	queue, err := channel.QueueDeclare(
		"plugin."+plugin, true, false, false, false, nil,
	)
	if err != nil {
		log.Warn("Error declaring queue", zap.Error(err))
		return queue, err
	}

	return queue, channel.QueueBind(queue.Name, plugin, "plugins", false, nil)
}

func PublishSingle(plugin string, state *pb.Shadow) {
	log := logger.Named("Publisher")
	log.Debug("Publish request received", zap.String("plugin", plugin))

	_, ok := qs[plugin]
	if !ok {
		queue, err := SetupQueue(log, channel, plugin)
		if err != nil {
			log.Warn("Error declaring queue", zap.Error(err))
			return
		}

		qs[plugin] = true
		log.Info("Queue declared", zap.String("name", queue.Name))
	}

	body, err := proto.Marshal(state)
	if err != nil {
		log.Warn("Error Marshaling state", zap.Error(err))
		return
	}

	err = channel.PublishWithContext(context.Background(), "plugins", plugin, false, false, amqp.Publishing{
		ContentType: "text/plain", Body: body,
	})
	if err != nil {
		log.Warn("Error Publishing message", zap.Error(err))
		delete(qs, plugin)
		return
	}
	log.Debug("State published")
}
