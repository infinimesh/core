package apierr

import (
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
)

const Domain = "infinimesh.org"

const (
	ReasonInvalidArgument    = "INVALID_ARGUMENT"
	ReasonPermissionDenied   = "PERMISSION_DENIED"
	ReasonUnauthenticated    = "UNAUTHENTICATED"
	ReasonNotFound           = "NOT_FOUND"
	ReasonInternal           = "INTERNAL"
	ReasonAlreadyExists      = "ALREADY_EXISTS"
	ReasonUnavailable        = "UNAVAILABLE"
	ReasonFailedPrecondition = "FAILED_PRECONDITION"
	ReasonAborted            = "ABORTED"
	ReasonUnknown            = "UNKNOWN"
	ReasonDeviceOutOfScope   = "DEVICE_OUT_OF_SCOPE"
	ReasonReportedWrite      = "REPORTED_WRITE_REQUIRED"
	ReasonDesiredManage      = "DESIRED_MANAGE_REQUIRED"
	ReasonTokenRevoked       = "TOKEN_REVOKED"
	ReasonIngestDeviceCount  = "INGEST_DEVICE_COUNT"
	ReasonRateLimited        = "RATE_LIMITED"
	ReasonBodyTooLarge       = "BODY_TOO_LARGE"
)

func ForCode(code connect.Code) string {
	switch code {
	case connect.CodeInvalidArgument:
		return ReasonInvalidArgument
	case connect.CodePermissionDenied:
		return ReasonPermissionDenied
	case connect.CodeUnauthenticated:
		return ReasonUnauthenticated
	case connect.CodeNotFound:
		return ReasonNotFound
	case connect.CodeInternal:
		return ReasonInternal
	case connect.CodeAlreadyExists:
		return ReasonAlreadyExists
	case connect.CodeUnavailable:
		return ReasonUnavailable
	case connect.CodeFailedPrecondition:
		return ReasonFailedPrecondition
	case connect.CodeAborted:
		return ReasonAborted
	default:
		return ReasonUnknown
	}
}

func New(code connect.Code, reason, msg string) error {
	if reason == "" {
		reason = ForCode(code)
	}
	err := connect.NewError(code, errors.New(msg))
	detail, derr := connect.NewErrorDetail(&errdetails.ErrorInfo{
		Reason: reason,
		Domain: Domain,
	})
	if derr == nil {
		err.AddDetail(detail)
	}
	return err
}

func Newf(code connect.Code, reason, format string, args ...any) error {
	return New(code, reason, fmt.Sprintf(format, args...))
}

func Code(code connect.Code, msg string) error {
	return New(code, ForCode(code), msg)
}

func Codef(code connect.Code, format string, args ...any) error {
	return New(code, ForCode(code), fmt.Sprintf(format, args...))
}
