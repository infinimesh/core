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
	// client, shadow, and ps are set once at startup. A connection keeps the
	// values it saw so a later assignment cannot race the accept goroutines.
	registry := client
	bus := ps
	shadows := shadow

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
	var devMu sync.Mutex
	devOf := func() *devpb.Device {
		devMu.Lock()
		defer devMu.Unlock()
		return device
	}
	setDev := func(next *devpb.Device) {
		devMu.Lock()
		device = next
		devMu.Unlock()
	}
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
		current := devOf()
		reported := &pb.Shadow{
			Device: current.Uuid,
			Reported: &pb.State{
				Timestamp: timestamppb.Now(),
				Data:      &data,
			},
			Connection: &pb.ConnectionState{
				Connected: true,
				Timestamp: timestamppb.Now(),
			},
		}
		copyDeviceMeta(reported, current)
		bus.TryPub(reported, "mqtt.incoming")
		return true
	}

	serveDevice(c, device.Uuid, connectPacket, meta, bridgeSessions, deviceHooks{
		enabled: func() bool {
			current := devOf()
			next, err := registry.GetByToken(ctx, current)
			if err != nil {
				log.Warn("Can't retrieve device status from registry", zap.Error(err))
				return current.Enabled
			}
			setDev(next)
			if !next.Enabled {
				log.Debug("Device is disabled, disconnecting", zap.String("device", next.Uuid), zap.Bool("enabled", next.Enabled))
			}
			return next.Enabled
		},
		reported: func(payload []byte, _ packet.Properties, _ bool) bool {
			return storeReported(payload)
		},
		connected: func(up bool) {
			current := devOf()
			sh := &pb.Shadow{
				Device: current.Uuid,
				Connection: &pb.ConnectionState{
					Connected: up,
					Timestamp: timestamppb.Now(),
					ClientId:  connectPacket.ClientID,
				},
			}
			copyDeviceMeta(sh, current)
			bus.TryPub(sh, "mqtt.incoming")
		},
		subscribed: func(send func([]byte, packet.Properties)) {
			current := devOf()
			if back == nil {
				back = bus.Sub()
			}
			bus.AddSub(back, "mqtt.outgoing/"+current.Uuid)
			forward.Do(func() {
				go handleBackChannel(log, back, send, func() {
					live := devOf()
					sh := &pb.Shadow{
						Device: live.Uuid,
						Connection: &pb.ConnectionState{
							Connected: true,
							Timestamp: timestamppb.Now(),
						},
					}
					copyDeviceMeta(sh, live)
					bus.TryPub(sh, "mqtt.incoming")
				})
			})
			go func() {
				if shadows == nil {
					return
				}
				r, err := shadows.Get(ctx, &pb.GetRequest{Pool: []string{current.Uuid}})
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
			log.Debug("Added Subscription", zap.String("topic", desiredTopic(current.Uuid)), zap.String("device", current.Uuid))
		},
		allow: func() bool {
			current := devOf()
			q := current.GetQuota()
			if q == nil {
				return true
			}
			ns := ""
			if current.GetAccess() != nil {
				ns = current.GetAccess().GetNamespace()
			}
			if !publishLimits.Allow(current.GetUuid(), q.GetDeviceMessagesPerSecond()) {
				return false
			}
			return publishLimits.Allow("ns:"+ns, q.GetNamespaceMessagesPerSecond())
		},
		unsub: func() {
			current := devOf()
			if back != nil {
				bus.Unsub(back, "mqtt.outgoing/"+current.Uuid)
			}
			log.Debug("Removed Subscription", zap.String("topic", desiredTopic(current.Uuid)), zap.String("device", current.Uuid))
		},
		will: func(topic string, payload []byte, _ bool) {
			current := devOf()
			if classifyTopic(topic, current.Uuid) != topicReported {
				log.Debug("Ignoring will for a topic the device cannot report", zap.String("topic", topic))
				return
			}
			storeReported(payload)
		},
	})
	if back != nil {
		unsub(bus, back)
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
