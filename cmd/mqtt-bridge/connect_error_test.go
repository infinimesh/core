package main

import (
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/infinimesh/core/v3/pkg/mqtt/packet"
	devpb "github.com/infinimesh/proto/node/devices"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConnectionRejectionWire(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		v3, v5 byte
	}{
		{"credentials", status.Error(codes.Unauthenticated, "bad password"), packet.CodeBadCredentials, packet.ReasonBadCredentials},
		{"disabled", status.Error(codes.PermissionDenied, "disabled"), packet.CodeNotAuthorized, packet.ReasonNotAuthorized},
		{"unknown device", status.Error(codes.NotFound, "missing"), packet.CodeNotAuthorized, packet.ReasonNotAuthorized},
		{"registry unavailable", status.Error(codes.Unavailable, "offline"), packet.CodeServerUnavailable, packet.ReasonServerUnavailable},
		{"registry internal", status.Error(codes.Internal, "500"), packet.CodeServerUnavailable, packet.ReasonServerUnavailable},
		{"unknown backend error", fmt.Errorf("backend failed"), packet.CodeServerUnavailable, packet.ReasonServerUnavailable},
	}
	for _, level := range []byte{packet.ProtocolMQTT311, packet.ProtocolMQTT5} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%d/%s", level, tc.name), func(t *testing.T) {
				server, client := net.Pipe()
				defer client.Close()
				require.NoError(t, client.SetDeadline(time.Now().Add(2*time.Second)))
				go LogErrorAndClose(server, &packet.Connect{ProtocolLevel: level}, fmt.Errorf("lookup: %w", tc.err))
				pkt, err := packet.ReadPacket(client, level, 0)
				require.NoError(t, err)
				ack, ok := pkt.(*packet.ConnAck)
				require.True(t, ok)
				want := tc.v3
				if level == packet.ProtocolMQTT5 {
					want = tc.v5
				}
				require.Equal(t, want, ack.Reason)
				require.False(t, ack.SessionPresent)
				var b [1]byte
				_, err = client.Read(b[:])
				require.ErrorIs(t, err, io.EOF)
			})
		}
	}
}

func TestConnectionFailureBeforeConnectClosesWithoutAck(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	require.NoError(t, client.SetDeadline(time.Now().Add(time.Second)))
	go LogErrorAndClose(server, nil, io.ErrUnexpectedEOF)
	var b [1]byte
	n, err := client.Read(b[:])
	require.Zero(t, n)
	require.ErrorIs(t, err, io.EOF)
}

func TestTCPRejectsUsingClientProtocol(t *testing.T) {
	previous := client
	t.Cleanup(func() { client = previous })
	for _, level := range []byte{packet.ProtocolMQTT311, packet.ProtocolMQTT5} {
		for _, tc := range []struct {
			name     string
			password string
			lookup   fpCall
			v3, v5   byte
		}{
			{name: "missing password", v3: packet.CodeBadCredentials, v5: packet.ReasonBadCredentials},
			{name: "registry offline", password: "AQ==", lookup: fpCall{err: status.Error(codes.Unavailable, "offline")}, v3: packet.CodeServerUnavailable, v5: packet.ReasonServerUnavailable},
			{name: "disabled device", password: "AQ==", lookup: fpCall{dev: &devpb.Device{Uuid: "dev", BasicEnabled: true}}, v3: packet.CodeNotAuthorized, v5: packet.ReasonNotAuthorized},
		} {
			t.Run(fmt.Sprintf("%d/%s", level, tc.name), func(t *testing.T) {
				client = &scriptDevices{fp: []fpCall{tc.lookup}}
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				require.NoError(t, err)
				done := make(chan struct{})
				go func() { HandleTCPConnections(ln); close(done) }()
				defer func() { ln.Close(); <-done }()
				c, err := net.Dial("tcp", ln.Addr().String())
				require.NoError(t, err)
				defer c.Close()
				require.NoError(t, c.SetDeadline(time.Now().Add(2*time.Second)))
				_, err = c.Write(connectBytes(t, flagsConnect(level, 0xC2, 0, "cid", "dev", tc.password, packet.ConnectProperties{}, "", nil)))
				require.NoError(t, err)
				pkt, err := packet.ReadPacket(c, level, 0)
				require.NoError(t, err)
				want := tc.v3
				if level == packet.ProtocolMQTT5 {
					want = tc.v5
				}
				require.Equal(t, want, pkt.(*packet.ConnAck).Reason)
				var b [1]byte
				_, err = c.Read(b[:])
				require.ErrorIs(t, err, io.EOF)
			})
		}
	}
}
