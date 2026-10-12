package observe

import (
	"context"
	"testing"

	"google.golang.org/grpc"
)

func TestUnaryRecordsHandled(t *testing.T) {
	resp, err := Unary()(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/infinimesh.test/Ping"}, func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	})
	if err != nil || resp != "ok" {
		t.Fatalf("resp %v err %v", resp, err)
	}
}
