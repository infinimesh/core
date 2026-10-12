package main

import (
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/infinimesh/core/pkg/mqtt/packet"
)

type mqttPub struct {
	addr string
	user string
	pass string
}

func (p *mqttPub) Publish(topic string, qos byte, retain bool, body []byte) error {
	u, err := url.Parse(p.addr)
	if err != nil {
		return err
	}
	host := u.Host
	if u.Scheme != "tcp" && u.Scheme != "" {
		return fmt.Errorf("rules mqtt broker must be tcp://host:port")
	}
	conn, err := net.DialTimeout("tcp", host, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	cp := &packet.Connect{
		ProtocolName: "MQTT", ProtocolLevel: packet.ProtocolMQTT311, CleanStart: true,
		KeepAlive: 30, ClientID: "infinimesh-rules",
	}
	if p.user != "" {
		cp.UsernameFlag = true
		cp.Username = p.user
		cp.PasswordFlag = true
		cp.Password = p.pass
	}
	if _, err := cp.WriteTo(conn); err != nil {
		return err
	}
	if _, err := packet.ReadPacket(conn, packet.ProtocolMQTT311, 0); err != nil {
		return err
	}
	pub := &packet.Publish{ProtocolLevel: packet.ProtocolMQTT311, QoS: qos, Retain: retain, Topic: topic, Payload: body}
	if qos > 0 {
		pub.PacketID = 1
	}
	_, err = pub.WriteTo(conn)
	return err
}
