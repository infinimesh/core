package main

import (
	"net"
	"testing"

	"github.com/infinimesh/core/pkg/mqtt/packet"
)

func TestMQTTPublish(t *testing.T) {
	if err := (&mqttPub{addr: "%"}).Publish("t", 0, false, nil); err == nil {
		t.Fatal("bad url")
	}
	if err := (&mqttPub{addr: "ssl://127.0.0.1:1"}).Publish("t", 0, false, nil); err == nil {
		t.Fatal("scheme")
	}
	if err := (&mqttPub{addr: "tcp://127.0.0.1:1"}).Publish("t", 0, false, nil); err == nil {
		t.Fatal("dial")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan *packet.Publish, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		if _, err := packet.ReadPacket(c, 0, 0); err != nil {
			return
		}
		if _, err := (&packet.ConnAck{ProtocolLevel: packet.ProtocolMQTT311}).WriteTo(c); err != nil {
			return
		}
		pkt, err := packet.ReadPacket(c, packet.ProtocolMQTT311, 0)
		if err != nil {
			return
		}
		got <- pkt.(*packet.Publish)
	}()
	err = (&mqttPub{addr: "tcp://" + ln.Addr().String(), user: "u", pass: "p"}).Publish("dev/x", 1, true, []byte("hi"))
	if err != nil {
		t.Fatal(err)
	}
	pub := <-got
	if pub.Topic != "dev/x" || string(pub.Payload) != "hi" || pub.QoS != 1 || !pub.Retain {
		t.Fatalf("%+v", pub)
	}

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer closed.Close()
	go func() {
		c, err := closed.Accept()
		if err != nil {
			return
		}
		c.Close()
	}()
	if err := (&mqttPub{addr: "tcp://" + closed.Addr().String()}).Publish("t", 0, false, []byte("x")); err == nil {
		t.Fatal("closed")
	}
}
