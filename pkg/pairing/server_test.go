package pairing

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/infinimesh/proto/pairing"
	cc "github.com/infinimesh/proto/pairing/pairingconnect"
	"go.uber.org/zap"
)

func Test_JustTest(t *testing.T) {
	srv := NewPairingServer(zap.NewExample())
	code := GenerateCode(srv.db)
	if len(code) != 6 {
		t.Fatalf("Code format is wrong: %s", code)
	}
	t.Logf("Just random Code: %s", code)
}

func TestSend(t *testing.T) {
	srv := NewPairingServer(zap.NewNop())
	ctx := context.Background()
	if _, err := srv.Send(ctx, connect.NewRequest(&pb.ControlPacket{})); err == nil {
		t.Fatal("empty payload")
	}
	if _, err := srv.Send(ctx, connect.NewRequest(&pb.ControlPacket{Payload: []string{"only"}})); err == nil {
		t.Fatal("short payload")
	}
	if _, err := srv.Send(ctx, connect.NewRequest(&pb.ControlPacket{Payload: []string{"missing", "x"}})); err == nil {
		t.Fatal("unknown code")
	}
	ch := make(chan []string, 1)
	srv.db["ab"] = &Connection{Channel: ch, Payload: []string{"meta"}, App: "app"}
	res, err := srv.Send(ctx, connect.NewRequest(&pb.ControlPacket{Payload: []string{"AB", "x", "y"}}))
	if err != nil {
		t.Fatal(err)
	}
	got := <-ch
	if len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Fatal(got)
	}
	if res.Msg.GetCode() != pb.Code_SUCCESS || res.Msg.GetAppId() != "app" || res.Msg.GetPayload()[0] != "meta" {
		t.Fatalf("%+v", res.Msg)
	}
}

func TestConnectExchange(t *testing.T) {
	orig := codeTTL
	codeTTL = time.Hour
	t.Cleanup(func() { codeTTL = orig })

	srv := NewPairingServer(zap.NewNop())
	_, handler := cc.NewPairingServiceHandler(srv)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	client := cc.NewPairingServiceClient(ts.Client(), ts.URL)
	ctx := context.Background()

	denied, err := client.Connect(ctx, connect.NewRequest(&pb.ConnectionRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if denied.Receive() || denied.Err() == nil {
		t.Fatal("missing app id")
	}

	stream, err := client.Connect(ctx, connect.NewRequest(&pb.ConnectionRequest{AppId: "app", Payload: []string{"meta"}}))
	if err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() {
		t.Fatal(stream.Err())
	}
	code := stream.Msg().GetPayload()[0]
	res, err := client.Send(ctx, connect.NewRequest(&pb.ControlPacket{Payload: []string{code, "hi"}}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.GetCode() != pb.Code_SUCCESS || res.Msg.GetPayload()[0] != "meta" {
		t.Fatalf("%+v", res.Msg)
	}
	if !stream.Receive() {
		t.Fatal(stream.Err())
	}
	if stream.Msg().GetCode() != pb.Code_DATA || stream.Msg().GetPayload()[0] != "hi" {
		t.Fatalf("%+v", stream.Msg())
	}
}

func TestConnectRefreshesCode(t *testing.T) {
	orig := codeTTL
	codeTTL = 20 * time.Millisecond
	t.Cleanup(func() { codeTTL = orig })

	srv := NewPairingServer(zap.NewNop())
	_, handler := cc.NewPairingServiceHandler(srv)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	client := cc.NewPairingServiceClient(ts.Client(), ts.URL)
	stream, err := client.Connect(context.Background(), connect.NewRequest(&pb.ConnectionRequest{AppId: "app", Payload: []string{"meta"}}))
	if err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() {
		t.Fatal(stream.Err())
	}
	first := stream.Msg().GetPayload()[0]
	if !stream.Receive() {
		t.Fatal(stream.Err())
	}
	if stream.Msg().GetCode() != pb.Code_AUTH || stream.Msg().GetPayload()[0] == first {
		t.Fatalf("%+v", stream.Msg())
	}
	_ = stream.Close()
}
