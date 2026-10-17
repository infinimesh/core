package observe

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

type serverStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s serverStream) Context() context.Context { return s.ctx }

func TestUnaryRecordsHandled(t *testing.T) {
	resp, err := Unary()(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/infinimesh.test/Ping"}, func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	})
	if err != nil || resp != "ok" {
		t.Fatalf("resp %v err %v", resp, err)
	}
	_, err = Unary()(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/infinimesh.test/Missing"}, func(ctx context.Context, req any) (any, error) {
		return nil, status.Error(codes.NotFound, "missing")
	})
	if status.Code(err) != codes.NotFound {
		t.Fatal(err)
	}
}

func TestStreamRecordsHandled(t *testing.T) {
	err := Stream()(nil, serverStream{ctx: context.Background()}, &grpc.StreamServerInfo{FullMethod: "/infinimesh.test/Watch"}, func(srv any, ss grpc.ServerStream) error {
		if ss.Context() == nil {
			t.Fatal("context")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = Stream()(nil, serverStream{ctx: context.Background()}, &grpc.StreamServerInfo{FullMethod: "/infinimesh.test/Watch"}, func(srv any, ss grpc.ServerStream) error {
		return status.Error(codes.Unavailable, "down")
	})
	if status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
}

func TestInterceptorRecordsCode(t *testing.T) {
	next := Interceptor()(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&structpb.Struct{}), nil
	})
	if _, err := next(context.Background(), connect.NewRequest(&structpb.Struct{})); err != nil {
		t.Fatal(err)
	}
	fail := Interceptor()(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("bad"))
	})
	_, err := fail(context.Background(), connect.NewRequest(&structpb.Struct{}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatal(err)
	}
}

func TestListenAndStart(t *testing.T) {
	srv := Listen("127.0.0.1:0")
	if err := srv.Shutdown(context.Background()); err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
	bound := Listen("")
	if err := bound.Shutdown(context.Background()); err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}

	t.Setenv("METRICS_ADDR", "127.0.0.1:0")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	stop := Start("observe-test")
	stop()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	stop = Tracing(ctx, "127.0.0.1:1", "observe-test")
	stop()
	if Tracing(context.Background(), "", "observe-test") == nil {
		t.Fatal("empty endpoint")
	}
}
