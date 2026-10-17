package apierr

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
)

func TestForCode(t *testing.T) {
	cases := []struct {
		code   connect.Code
		reason string
	}{
		{connect.CodeInvalidArgument, ReasonInvalidArgument},
		{connect.CodePermissionDenied, ReasonPermissionDenied},
		{connect.CodeUnauthenticated, ReasonUnauthenticated},
		{connect.CodeNotFound, ReasonNotFound},
		{connect.CodeInternal, ReasonInternal},
		{connect.CodeAlreadyExists, ReasonAlreadyExists},
		{connect.CodeUnavailable, ReasonUnavailable},
		{connect.CodeFailedPrecondition, ReasonFailedPrecondition},
		{connect.CodeAborted, ReasonAborted},
		{connect.CodeUnknown, ReasonUnknown},
		{connect.CodeCanceled, ReasonUnknown},
		{connect.CodeDeadlineExceeded, ReasonUnknown},
		{connect.CodeResourceExhausted, ReasonUnknown},
		{connect.CodeDataLoss, ReasonUnknown},
		{connect.CodeOutOfRange, ReasonUnknown},
		{connect.CodeUnimplemented, ReasonUnknown},
	}
	for _, tc := range cases {
		if got := ForCode(tc.code); got != tc.reason {
			t.Fatalf("ForCode(%v) = %s, want %s", tc.code, got, tc.reason)
		}
	}
}

func TestNewAndCode(t *testing.T) {
	err := New(connect.CodeNotFound, "", "missing")
	assertAPI(t, err, connect.CodeNotFound, ReasonNotFound, "missing")

	err = New(connect.CodePermissionDenied, ReasonDeviceOutOfScope, "scope")
	assertAPI(t, err, connect.CodePermissionDenied, ReasonDeviceOutOfScope, "scope")

	err = Newf(connect.CodeInvalidArgument, ReasonIngestDeviceCount, "need %d", 2)
	assertAPI(t, err, connect.CodeInvalidArgument, ReasonIngestDeviceCount, "need 2")

	err = Code(connect.CodeAlreadyExists, "dup")
	assertAPI(t, err, connect.CodeAlreadyExists, ReasonAlreadyExists, "dup")

	err = Codef(connect.CodeAborted, "try %s", "again")
	assertAPI(t, err, connect.CodeAborted, ReasonAborted, "try again")

	err = New(connect.CodeUnavailable, ReasonRateLimited, "slow")
	assertAPI(t, err, connect.CodeUnavailable, ReasonRateLimited, "slow")

	err = New(connect.CodeFailedPrecondition, ReasonReportedWrite, "reported")
	assertAPI(t, err, connect.CodeFailedPrecondition, ReasonReportedWrite, "reported")

	err = New(connect.CodePermissionDenied, ReasonDesiredManage, "desired")
	assertAPI(t, err, connect.CodePermissionDenied, ReasonDesiredManage, "desired")

	err = New(connect.CodeUnauthenticated, ReasonTokenRevoked, "revoked")
	assertAPI(t, err, connect.CodeUnauthenticated, ReasonTokenRevoked, "revoked")

	err = New(connect.CodeResourceExhausted, ReasonBodyTooLarge, "big")
	assertAPI(t, err, connect.CodeResourceExhausted, ReasonBodyTooLarge, "big")

	if Domain == "" {
		t.Fatal("empty domain")
	}
}

func assertAPI(t *testing.T, err error, code connect.Code, reason, msg string) {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("got %T", err)
	}
	if ce.Code() != code {
		t.Fatalf("code %v", ce.Code())
	}
	if ce.Message() != msg {
		t.Fatalf("message %q", ce.Message())
	}
	details := ce.Details()
	if len(details) != 1 {
		t.Fatalf("details %d", len(details))
	}
	got, derr := details[0].Value()
	if derr != nil {
		t.Fatal(derr)
	}
	info, ok := got.(*errdetails.ErrorInfo)
	if !ok {
		t.Fatalf("detail %T", got)
	}
	if info.GetReason() != reason || info.GetDomain() != Domain {
		t.Fatalf("info %+v", info)
	}
}
