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
package main

import (
	"context"
	"errors"
	"net"
	"sync"

	structpb "github.com/golang/protobuf/ptypes/struct"
	"github.com/infinimesh/core/pkg/mqtt/metrics"
	"github.com/infinimesh/core/pkg/mqtt/packet"
	"github.com/infinimesh/core/pkg/pubsub"
	devpb "github.com/infinimesh/proto/node/devices"
	pb "github.com/infinimesh/proto/shadow"
	"go.uber.org/zap"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type VerifyDeviceFunc func(*devpb.Device) bool

func GetByFingerprintAndVerify(fingerprint []byte, cb VerifyDeviceFunc) (device *devpb.Device, err error) {
	resp, err := client.GetByFingerprint(internal_ctx, &devpb.GetByFingerprintRequest{
		Fingerprint: fingerprint,
	})
	if err != nil {
		return nil, err
	}

	if cb(resp) {
		return resp, nil
	}

	return nil, errors.New("not found")
}

// LogErrorAndClose - Logs Error, Sends Acknowlegement(ACK) packet and Closes the connection
// ACK Packet needs to be sent to prevent MQTT Client sending CONN packets further
func LogErrorAndClose(c net.Conn, err error) {
	log.Warn("Closing connection on error", zap.Error(err))
	_ = writeConnAck(c, packet.ProtocolMQTT311, false, packet.CodeUnacceptableProto, "")
	c.Close()
}

// HandleConn - note: Connection is expected to be valid & legitimate at this point
func HandleConn(c net.Conn, connectPacket *packet.Connect, meta connectMeta, device *devpb.Device) {
	log := log.Named(device.GetUuid()).Named(connectPacket.ClientID)
	defer log.Debug("Client disconnected")
	log.Debug(
		"Client connected", zap.String("device", device.Uuid),
		zap.Int("protocol_level", int(connectPacket.ProtocolLevel)),
		zap.String("protocol", connectPacket.ProtocolName),
		zap.Int("QoS", int(connectPacket.WillQoS)),
	)

	var back chan interface{}
	var forward sync.Once
	token := device.GetToken()
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)
	metrics.ActiveConnectionsTotal.Inc()
	defer metrics.ActiveConnectionsTotal.Dec()

	storeReported := func(payload []byte) bool {
		var data structpb.Struct
		if err := data.UnmarshalJSON(payload); err != nil {
			log.Warn("Failed to handle Publish", zap.Error(err))
			return false
		}
		reported := &pb.Shadow{
			Device: device.Uuid,
			Reported: &pb.State{
				Timestamp: timestamppb.Now(),
				Data:      &data,
			},
			Connection: &pb.ConnectionState{
				Connected: true,
				Timestamp: timestamppb.Now(),
			},
		}
		copyDeviceMeta(reported, device)
		ps.TryPub(reported, "mqtt.incoming")
		return true
	}

	serveDevice(c, device.Uuid, connectPacket, meta, bridgeSessions, deviceHooks{
		enabled: func() bool {
			next, err := client.GetByToken(ctx, device)
			if err != nil {
				log.Warn("Can't retrieve device status from registry", zap.Error(err))
				return device.Enabled
			}
			device = next
			if !device.Enabled {
				log.Debug("Device is disabled, disconnecting", zap.String("device", device.Uuid), zap.Bool("enabled", device.Enabled))
			}
			return device.Enabled
		},
		reported: func(payload []byte, _ packet.Properties, _ bool) bool {
			return storeReported(payload)
		},
		connected: func(up bool) {
			sh := &pb.Shadow{
				Device: device.Uuid,
				Connection: &pb.ConnectionState{
					Connected: up,
					Timestamp: timestamppb.Now(),
					ClientId:  connectPacket.ClientID,
				},
			}
			copyDeviceMeta(sh, device)
			ps.TryPub(sh, "mqtt.incoming")
		},
		subscribed: func(send func([]byte, packet.Properties)) {
			if back == nil {
				back = ps.Sub()
			}
			ps.AddSub(back, "mqtt.outgoing/"+device.Uuid)
			forward.Do(func() {
				go handleBackChannel(log, back, send, func() {
					sh := &pb.Shadow{
						Device: device.Uuid,
						Connection: &pb.ConnectionState{
							Connected: true,
							Timestamp: timestamppb.Now(),
						},
					}
					copyDeviceMeta(sh, device)
					ps.TryPub(sh, "mqtt.incoming")
				})
			})
			go func() {
				if shadow == nil {
					return
				}
				r, err := shadow.Get(ctx, &pb.GetRequest{Pool: []string{device.Uuid}})
				if err != nil || len(r.GetShadows()) == 0 {
					return
				}
				state := r.GetShadows()[0]
				if state.Desired == nil || state.Desired.Data == nil {
					return
				}
				payload, err := state.Desired.Data.MarshalJSON()
				if err != nil {
					return
				}
				send(payload, packet.Properties{})
			}()
			log.Debug("Added Subscription", zap.String("topic", desiredTopic(device.Uuid)), zap.String("device", device.Uuid))
		},
		unsub: func() {
			if back != nil {
				ps.Unsub(back, "mqtt.outgoing/"+device.Uuid)
			}
			log.Debug("Removed Subscription", zap.String("topic", desiredTopic(device.Uuid)), zap.String("device", device.Uuid))
		},
		will: func(topic string, payload []byte, _ bool) {
			if classifyTopic(topic, device.Uuid) != topicReported {
				log.Debug("Ignoring will for a topic the device cannot report", zap.String("topic", topic))
				return
			}
			storeReported(payload)
		},
	})
	if back != nil {
		unsub(ps, back)
	}
}

func copyDeviceMeta(sh *pb.Shadow, device *devpb.Device) {
	if sh == nil || device == nil {
		return
	}
	sh.Title = device.GetTitle()
	if tags := device.GetTags(); len(tags) > 0 {
		sh.Tags = append([]string(nil), tags...)
	}
}

func handleBackChannel(log *zap.Logger, ch chan interface{}, send func([]byte, packet.Properties), connected func()) {
	defer log.Debug("BackChannel handler closed")
	var ts int64 = 0
	for msg := range ch {
		shadow := msg.(*pb.Shadow)
		log.Debug("Received message", zap.String("device", shadow.Device))
		if shadow.Desired == nil || shadow.Desired.Timestamp == nil || shadow.Desired.Data == nil {
			log.Debug("Skipping empty Desired state")
			continue
		}
		if shadow.Desired.Timestamp.Seconds < ts {
			log.Debug("Skipping message", zap.String("device", shadow.Device))
			continue
		}
		ts = shadow.Desired.Timestamp.Seconds
		payload, err := shadow.Desired.Data.MarshalJSON()
		if err != nil {
			log.Warn("Failed to marshal shadow", zap.Error(err))
			continue
		}
		send(payload, packet.Properties{})
		if connected != nil {
			connected()
		}
	}
}

func unsub[T chan any](ps pubsub.PubSub, ch chan any) {
	go ps.Unsub(ch)

	for range ch {
	}
}
