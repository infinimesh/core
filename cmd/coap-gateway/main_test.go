package main

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/infinimesh/core/v3/pkg/pubsub"
	pb "github.com/infinimesh/proto/shadow"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
)

type capturePS struct {
	msg    any
	topics []string
}

func (c *capturePS) Sub(topics ...string) chan interface{}         { return nil }
func (c *capturePS) SubOnce(topics ...string) chan interface{}     { return nil }
func (c *capturePS) SubOnceEach(topics ...string) chan interface{} { return nil }
func (c *capturePS) AddSub(ch chan interface{}, topics ...string)  {}
func (c *capturePS) AddSubOnceEach(ch chan interface{}, topics ...string) {
}
func (c *capturePS) Pub(msg interface{}, topics ...string)    {}
func (c *capturePS) TryPub(msg interface{}, topics ...string) { c.msg = msg; c.topics = topics }
func (c *capturePS) Unsub(ch chan interface{}, topics ...string) {
}
func (c *capturePS) Close(topics ...string) {}
func (c *capturePS) Shutdown()              {}

func TestPublishReported(t *testing.T) {
	ps := &capturePS{}
	if err := publishReported(ps, "dev", []byte("[")); err == nil {
		t.Fatal("bad json")
	}
	if err := publishReported(ps, "dev", []byte(`{"t":1,"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	got := ps.msg.(*pb.Shadow)
	if got.GetDevice() != "dev" || got.GetReported().GetData().GetFields()["t"].GetNumberValue() != 1 {
		t.Fatalf("%+v", got)
	}
	if len(ps.topics) != 1 || ps.topics[0] != "mqtt.incoming" {
		t.Fatal(ps.topics)
	}
}

func TestServe(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	published := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		serve(pc, func(device string, body []byte) error {
			if string(body) == "no" {
				return errors.New("publish")
			}
			published <- device
			return nil
		})
		close(done)
	}()

	client, err := net.DialUDP("udp", nil, pc.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	raw := []byte{
		0x42, codePost, 0x00, 0x07, 'a', 'b',
		0xb7, 'd', 'e', 'v', 'i', 'c', 'e', 's',
		0x03, 'd', 'e', 'v',
		0x05, 's', 't', 'a', 't', 'e',
		0x08, 'r', 'e', 'p', 'o', 'r', 't', 'e', 'd',
		0xff, '{', '}',
	}
	if _, err := client.Write(append([]byte(nil), raw...)); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-published:
		if id != "dev" {
			t.Fatal(id)
		}
	case <-time.After(time.Second):
		t.Fatal("not published")
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 64)
	n, err := client.Read(buf)
	if err != nil || buf[1] != 0x44 {
		t.Fatalf("%x %v", buf[:n], err)
	}

	bad := append([]byte(nil), raw...)
	bad[1] = 0x01
	other := append([]byte(nil), raw...)
	other[7] = 'x'
	failed := append([]byte(nil), raw...)
	failed[len(failed)-2] = 'n'
	failed[len(failed)-1] = 'o'
	for _, pkt := range [][]byte{bad, {0x01}, other, failed} {
		if _, err := client.Write(pkt); err != nil {
			t.Fatal(err)
		}
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	n, err = client.Read(buf)
	if err != nil || buf[1] != 0x80 {
		t.Fatalf("%x %v", buf[:n], err)
	}

	if err := pc.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("serve did not return")
	}
}

func TestMainExitsWithoutBroker(t *testing.T) {
	origDial, origSetup, origListen, origDie := dialAMQP, setupBus, listenUDP, die
	t.Cleanup(func() {
		dialAMQP, setupBus, listenUDP, die = origDial, origSetup, origListen, origDie
	})
	t.Setenv("METRICS_ADDR", "127.0.0.1:0")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	die = func(log *zap.Logger, msg string, fields ...zap.Field) { panic(msg) }

	dialAMQP = func(string) (*amqp.Connection, error) { return nil, errors.New("dial") }
	func() {
		defer func() {
			if recover() != "rabbitmq" {
				t.Fatal("dial")
			}
		}()
		main()
	}()

	dialAMQP = func(string) (*amqp.Connection, error) { return nil, nil }
	setupBus = func(*zap.Logger, *amqp.Connection, string, string, int) (pubsub.PubSub, error) {
		return nil, errors.New("setup")
	}
	func() {
		defer func() {
			if recover() != "pubsub" {
				t.Fatal("pubsub")
			}
		}()
		main()
	}()

	ready := make(chan pubsub.PubSub, 1)
	setupBus = func(_ *zap.Logger, _ *amqp.Connection, _, _ string, n int) (pubsub.PubSub, error) {
		ps := pubsub.New(n)
		ready <- ps
		return ps, nil
	}
	listenUDP = func(string, string) (net.PacketConn, error) { return nil, errors.New("udp") }
	func() {
		defer func() {
			if recover() != "udp" {
				t.Fatal("udp")
			}
		}()
		main()
	}()
	(<-ready).Shutdown()

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listenUDP = func(string, string) (net.PacketConn, error) { return pc, nil }
	done := make(chan struct{})
	go func() {
		defer close(done)
		main()
	}()
	client, err := net.DialUDP("udp", nil, pc.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	raw := []byte{
		0x42, codePost, 0x00, 0x07, 'a', 'b',
		0xb7, 'd', 'e', 'v', 'i', 'c', 'e', 's',
		0x03, 'd', 'e', 'v',
		0x05, 's', 't', 'a', 't', 'e',
		0x08, 'r', 'e', 'p', 'o', 'r', 't', 'e', 'd',
		0xff, '{', '}',
	}
	var bus pubsub.PubSub
	select {
	case bus = <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("bus")
	}
	heard := bus.Sub("mqtt.incoming")
	if _, err := client.Write(raw); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-heard:
		if msg.(*pb.Shadow).GetDevice() != "dev" {
			t.Fatal(msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("main did not publish")
	}
	if err := pc.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("main did not return")
	}
	bus.Shutdown()
}
