/*
Copyright © 2018-2023 Infinite Devices GmbH

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
package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"

	"github.com/go-redis/redis/v8"
	"github.com/infinimesh/core/pkg/graph/schema"
	inflog "github.com/infinimesh/core/pkg/log"
	"github.com/infinimesh/core/pkg/mqtt/metrics"
	"github.com/infinimesh/core/pkg/mqtt/packet"
	mqttps "github.com/infinimesh/core/pkg/mqtt/pubsub"
	"github.com/infinimesh/core/pkg/observe"
	"github.com/infinimesh/core/pkg/pubsub"
	"github.com/infinimesh/core/pkg/shared/auth"
	pb "github.com/infinimesh/proto/node"
	devpb "github.com/infinimesh/proto/node/devices"
	stpb "github.com/infinimesh/proto/shadow"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

func verifyBasicAuth(p *packet.Connect) (fingerprint []byte, err error) {
	if p.Username == "" {
		return nil, errors.New("payload Username is Empty")
	}
	if p.Password == "" {
		return nil, errors.New("payload Password is Empty")
	}
	return base64.StdEncoding.DecodeString(p.Password)
}

func getFingerprint(c []byte) []byte {
	s := sha256.New()
	_, _ = s.Write(c) // nolint: gosec
	return s.Sum(nil)
}

var (
	conn   *grpc.ClientConn
	client pb.DevicesServiceClient
	shadow stpb.ShadowServiceClient
	debug  bool

	devicesHost string
	shadowHost  string

	RabbitMQConn  string
	acme_path     string
	tlsCertFile   string
	tlsKeyFile    string
	tlsECCertFile string
	tlsECKeyFile  string

	ps pubsub.PubSub

	log             *zap.Logger
	internal_ctx    context.Context
	buffer_capacity int
)

func init() {
	viper.AutomaticEnv()

	log = inflog.NewLogger()

	viper.SetDefault("DEVICES_HOST", "api.127.0.0.1.nip.io:8000")
	viper.SetDefault("SHADOW_HOST", "shadow:8080")
	viper.SetDefault("RABBITMQ_CONN", "amqp://infinimesh:infinimesh@localhost:5672/")
	viper.SetDefault("ACME", "")
	viper.SetDefault("TLS_CERT_FILE", "/cert/tls.crt")
	viper.SetDefault("TLS_KEY_FILE", "/cert/tls.key")
	viper.SetDefault("TLS_EC_CERT_FILE", "")
	viper.SetDefault("TLS_EC_KEY_FILE", "")
	viper.SetDefault("TLS_HANDSHAKE_TIMEOUT", "30s")
	viper.SetDefault("DEBUG", false)
	viper.SetDefault("SIGNING_KEY", "seeeecreet")
	viper.SetDefault("BUFFER_CAPACITY", 10)

	devicesHost = viper.GetString("DEVICES_HOST")
	shadowHost = viper.GetString("SHADOW_HOST")
	RabbitMQConn = viper.GetString("RABBITMQ_CONN")
	acme_path = viper.GetString("ACME")
	tlsCertFile = viper.GetString("TLS_CERT_FILE")
	tlsKeyFile = viper.GetString("TLS_KEY_FILE")
	tlsECCertFile = viper.GetString("TLS_EC_CERT_FILE")
	tlsECKeyFile = viper.GetString("TLS_EC_KEY_FILE")
	debug = viper.GetBool("DEBUG")
	buffer_capacity = viper.GetInt("BUFFER_CAPACITY")
}

func main() {
	defer func() {
		_ = log.Sync()
	}()

	stopTrace := observe.Start("mqtt-bridge")
	defer stopTrace()

	log.Info("Starting MQTT Bridge")
	if viper.GetString("MQTT_SESSION_STORE") == "redis" {
		viper.SetDefault("REDIS_HOST", "redis:6379")
		rdb := redis.NewClient(&redis.Options{Addr: viper.GetString("REDIS_HOST")})
		bridgeSessions.persist = newRedisSessions(rdb)
		go bridgeSessions.keep()
	}

	timeout, err := parseHandshakeTimeout(viper.GetString("TLS_HANDSHAKE_TIMEOUT"))
	if err != nil {
		log.Fatal("Invalid TLS handshake timeout", zap.Error(err))
	}
	tlsHandshakeTimeout = timeout

	certs, err := loadServerCerts(acme_path, tlsCertFile, tlsKeyFile, tlsECCertFile, tlsECKeyFile)
	if err != nil {
		log.Fatal("Error loading server certificate", zap.Error(err))
	}

	log.Info("Connecting to registry", zap.String("host", devicesHost))
	conn, err = grpc.Dial(devicesHost, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal("Error dialing device registry", zap.Error(err))
	}
	client = pb.NewDevicesServiceClient(conn)

	conn, err = grpc.Dial(shadowHost, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Warn("Error dialing shadow registry", zap.Error(err))
	} else {
		shadow = stpb.NewShadowServiceClient(conn)
	}

	SIGNING_KEY := []byte(viper.GetString("SIGNING_KEY"))
	auth := auth.NewAuthInterceptor(log, nil, nil, SIGNING_KEY)
	token, err := auth.MakeToken(schema.ROOT_ACCOUNT_KEY)
	if err != nil {
		log.Fatal("Error making token", zap.Error(err))
	}
	internal_ctx = metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)

	log.Info("Connecting to RabbitMQ", zap.String("url", RabbitMQConn))
	rbmq, err := amqp.Dial(RabbitMQConn)
	if err != nil {
		log.Fatal("Error dialing RabbitMQ", zap.Error(err))
	}
	defer rbmq.Close()

	ps, err = mqttps.Setup(log, rbmq, "mqtt.incoming", "mqtt.outgoing", buffer_capacity)
	if err != nil {
		log.Fatal("Error setting up pubsub", zap.Error(err))
	}

	tlsl, err := tls.Listen("tcp", ":8883", serverTLSConfig(certs))
	if err != nil {
		panic(err)
	}
	if plaintextRequested(viper.GetString("MQTT_PLAINTEXT")) {
		log.Warn(plaintextWarning())
		tcp, err := net.Listen("tcp", ":1883")
		if err != nil {
			panic(err)
		}
		go HandleTCPConnections(tcp)
	}

	{
		log := log.Named("TLS")
		for {
			c, err := tlsl.Accept() // nolint: gosec
			metrics.TlsAcceptedTotal.Inc()
			if err != nil {
				log.Warn("Couldn't accept TLS Connection", zap.Error(err))
				metrics.TlsFailedToAcceptTotal.Inc()
				continue
			}
			if debug {
				printConnState(c)
			}
			log.Debug("Connection Accepted", zap.String("remote", c.RemoteAddr().String()))

			go func(c net.Conn) {

				conn, ok := c.(*tls.Conn)
				if !ok {
					log.Warn("Couldn't cast conection to tls.Connection")
					if err := conn.Close(); err != nil {
						log.Warn("Couldn't close connection", zap.Error(err))
					}
					metrics.TlsFailedToAcceptTotal.Inc()
					return
				}

				if err := waitHandshake(conn, tlsHandshakeTimeout); err != nil {
					LogErrorAndClose(c, err)
					metrics.TlsFailedToAcceptTotal.Inc()
					return
				}

				connectPacket, meta, err := readConnect(conn)
				if err != nil {
					LogErrorAndClose(conn, fmt.Errorf("error while reading connect packet: %v", err))
					metrics.ConnNotAnMqttPacketTotal.Inc()
					return
				}
				log.Debug("ConnectPacket", zap.String("client", connectPacket.ClientID))

				fingerprint, err := clientFingerprint(conn)
				if err != nil {
					LogErrorAndClose(conn, err)
					metrics.TlsDeviceAuthFailedTotal.Inc()
					return
				}
				log.Debug("Fingerprint", zap.Binary("fingerprint", fingerprint))

				device, err := GetByFingerprintAndVerify(fingerprint, func(device *devpb.Device) bool {
					if device.Enabled {
						log.Info("Device is enabled", zap.String("device", device.Uuid), zap.Strings("tags", device.Tags))
						return true
					} else {
						log.Warn("Failed to verify client as the device is not enabled", zap.String("device", device.Uuid))
						return false
					}
				})
				if err != nil {
					LogErrorAndClose(conn, err)
					metrics.TlsDeviceAuthFailedTotal.Inc()
					return
				}

				go HandleConn(conn, connectPacket, meta, device)
			}(c)
		}
	}
}

func printConnState(con net.Conn) {
	conn := con.(*tls.Conn)
	state := conn.ConnectionState()

	log.Info("Connection state",
		zap.Uint16("version", state.Version),
		zap.Bool("handshake-complete", state.HandshakeComplete),
		zap.Bool("did-resume", state.DidResume),
		zap.Uint16("cipher-suite", state.CipherSuite),
		zap.String("proto-version", state.NegotiatedProtocol),
		zap.Any("certs", state.PeerCertificates),
	)
}

// MQTT5 schema
// const mqtt5Schema = `{
//     "$schema": "http://json-schema.org/draft-04/schema#",
//     "type": "object",
//     "properties": {
//       "Timestamp": {
//         "type": "string"
//       },
//       "Message": {
//         "type": "array",
//         "items": [
//           {
//             "type": "object",
//             "properties": {
//               "Topic": {
//                 "type": "string"
//               },
//               "Data": {
//                 "type": "object"
//               }
//             },
//             "required": [
//               "Topic",
//               "Data"
//             ]
//           }
//         ]
//       }
//     },
//     "required": [
//       "Timestamp",
//       "Message"
//     ]
//   }`

// func schemaValidation(data []byte, version int) bool {
// 	if version == 4 {
// 		return true
// 	}
// 	var payload mqtt.Payload
// 	err := json.Unmarshal(data, &payload)
// 	if err != nil {
// 		log.Warn("invalid payload format", zap.Error(err))
// 		return false
// 	}
// 	loader := gojsonschema.NewGoLoader(payload)
// 	//filename := "file:///mqtt-bridge/schema-mqtt5.json"
// 	//log.Printf("json file path: %v", filename)
// 	schemaLoader := gojsonschema.NewStringLoader(mqtt5Schema)
// 	schema, err := gojsonschema.NewSchema(schemaLoader)
// 	if err != nil {
// 		log.Warn("Loading new schema failed", zap.Error(err))
// 		return false
// 	}
// 	result, err := schema.Validate(loader)
// 	if err != nil {
// 		log.Warn("Schema validation failed", zap.Error(err))
// 		return false
// 	}
// 	return result.Valid()
// }
