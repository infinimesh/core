package main

import (
	"encoding/json"
	"net"

	mqttps "github.com/infinimesh/core/pkg/mqtt/pubsub"
	"github.com/infinimesh/core/pkg/observe"
	"github.com/infinimesh/core/pkg/pubsub"
	pb "github.com/infinimesh/proto/shadow"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func coapPlaintextNotice(addr string) string {
	return "CoAP " + addr + " is plaintext UDP with no DTLS. Keep it on a private network."
}

var (
	dialAMQP  = amqp.Dial
	setupBus  = mqttps.Setup
	listenUDP = net.ListenPacket
	die       = func(log *zap.Logger, msg string, fields ...zap.Field) { log.Fatal(msg, fields...) }
)

func main() {
	viper.AutomaticEnv()
	viper.SetDefault("COAP_ADDR", ":5683")
	viper.SetDefault("RABBITMQ_CONN", "amqp://infinimesh:infinimesh@rabbitmq:5672/")
	log, _ := zap.NewProduction()
	stop := observe.Start("coap-gateway")
	defer stop()
	conn, err := dialAMQP(viper.GetString("RABBITMQ_CONN"))
	if err != nil {
		die(log, "rabbitmq", zap.Error(err))
	}
	ps, err := setupBus(log, conn, "mqtt.incoming", "coap.unused", 10)
	if err != nil {
		die(log, "pubsub", zap.Error(err))
	}
	addr := viper.GetString("COAP_ADDR")
	pc, err := listenUDP("udp", addr)
	if err != nil {
		die(log, "udp", zap.Error(err))
	}
	log.Warn(coapPlaintextNotice(addr))
	serve(pc, func(device string, body []byte) error {
		return publishReported(ps, device, body)
	})
}

func serve(pc net.PacketConn, publish func(device string, body []byte) error) {
	buf := make([]byte, 64*1024)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		msg, err := parseCoAP(buf[:n])
		if err != nil || msg.Code != codePost {
			continue
		}
		id, ok := reportedDevice(msg.Path)
		code := byte(0x80)
		if ok {
			if err := publish(id, msg.Payload); err == nil {
				code = 0x44
			}
		}
		_, _ = pc.WriteTo(ack(msg, code, nil), addr)
	}
}

func publishReported(ps pubsub.PubSub, device string, body []byte) error {
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		return err
	}
	data, err := structpb.NewStruct(fields)
	if err != nil {
		return err
	}
	ps.TryPub(&pb.Shadow{
		Device:   device,
		Reported: &pb.State{Timestamp: timestamppb.Now(), Data: data},
	}, "mqtt.incoming")
	return nil
}
