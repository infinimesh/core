package shadow

import (
	"connectrpc.com/connect"
	"github.com/infinimesh/core/v3/pkg/apierr"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// wireError is a Connect error for in-process callers and a gRPC status with
// ErrorInfo for clients of the shadow process.
type wireError struct {
	connectErr error
	st         *status.Status
}

func (e *wireError) Error() string { return e.connectErr.Error() }

func (e *wireError) Unwrap() error { return e.connectErr }

func (e *wireError) GRPCStatus() *status.Status { return e.st }

func rpcErr(code connect.Code, msg string) error {
	st, err := status.New(codes.Code(code), msg).WithDetails(&errdetails.ErrorInfo{
		Reason: apierr.ForCode(code),
		Domain: apierr.Domain,
	})
	if err != nil {
		st = status.New(codes.Code(code), msg)
	}
	return &wireError{connectErr: apierr.Code(code, msg), st: st}
}
