package shadow

import (
	"context"
	"errors"
	"math"
	"time"

	"connectrpc.com/connect"
	pb "github.com/infinimesh/proto/shadow"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

var otaStatus = map[string]struct{}{
	"idle": {}, "downloading": {}, "installing": {}, "success": {}, "failed": {},
}

func ValidateState(st *pb.State) error {
	if st == nil || st.GetData() == nil {
		return nil
	}
	v, ok := st.GetData().GetFields()["ota"]
	if !ok || v == nil || isNull(v) {
		return nil
	}
	obj := v.GetStructValue()
	if obj == nil {
		return rpcErr(connect.CodeInvalidArgument, "ota must be an object")
	}
	for key, field := range obj.GetFields() {
		if err := validateOTAField(key, field); err != nil {
			return err
		}
	}
	return nil
}

func isNull(v *structpb.Value) bool {
	if v == nil {
		return true
	}
	_, ok := v.Kind.(*structpb.Value_NullValue)
	return ok
}

func validateOTAField(key string, field *structpb.Value) error {
	switch key {
	case "version", "requested_version", "url", "error":
		if _, ok := field.Kind.(*structpb.Value_StringValue); !ok {
			return rpcErr(connect.CodeInvalidArgument, "ota "+key+" must be a string")
		}
	case "sha256":
		s, ok := field.Kind.(*structpb.Value_StringValue)
		if !ok {
			return rpcErr(connect.CodeInvalidArgument, "ota sha256 must be a string")
		}
		if s.StringValue != "" && !sha256Hex(s.StringValue) {
			return rpcErr(connect.CodeInvalidArgument, "ota sha256 must be 64 lowercase hex characters")
		}
	case "size":
		n, ok := field.Kind.(*structpb.Value_NumberValue)
		if !ok || n.NumberValue < 0 || n.NumberValue != math.Trunc(n.NumberValue) {
			return rpcErr(connect.CodeInvalidArgument, "ota size must be a whole number of zero or greater")
		}
	case "status":
		s, ok := field.Kind.(*structpb.Value_StringValue)
		if !ok {
			return rpcErr(connect.CodeInvalidArgument, "ota status must be a string")
		}
		if _, known := otaStatus[s.StringValue]; !known {
			return rpcErr(connect.CodeInvalidArgument, "ota status must be idle, downloading, installing, success, or failed")
		}
	case "updated_at":
		s, ok := field.Kind.(*structpb.Value_StringValue)
		if !ok {
			return rpcErr(connect.CodeInvalidArgument, "ota updated_at must be a string")
		}
		if _, err := time.Parse(time.RFC3339, s.StringValue); err != nil {
			return rpcErr(connect.CodeInvalidArgument, "ota updated_at must be RFC3339")
		}
	default:
		return rpcErr(connect.CodeInvalidArgument, "ota field "+key+" is not supported")
	}
	return nil
}

func sha256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

func otaEvents(prevReported, prevDesired *pb.State, patch *pb.Shadow) []pb.ShadowEvent {
	if patch == nil {
		return nil
	}
	var ev []pb.ShadowEvent
	if otaChanged(prevDesired, patch.GetDesired(), "version") {
		ev = append(ev, pb.ShadowEvent_OTA_TARGET_SET)
	}
	if otaChanged(prevDesired, patch.GetDesired(), "requested_version") {
		ev = append(ev, pb.ShadowEvent_OTA_REQUESTED)
	}
	if otaChanged(prevReported, patch.GetReported(), "status") {
		ev = append(ev, pb.ShadowEvent_OTA_STATUS_CHANGED)
	}
	return ev
}

func otaChanged(prev, patch *pb.State, key string) bool {
	if patch == nil || patch.GetData() == nil {
		return false
	}
	v, ok := patch.GetData().GetFields()["ota"]
	if !ok || v == nil {
		return false
	}
	if isNull(v) {
		old, _ := otaString(prev, key)
		return old != ""
	}
	val, present := otaString(patch, key)
	if !present {
		return false
	}
	old, _ := otaString(prev, key)
	return val != old
}

func otaString(st *pb.State, key string) (string, bool) {
	if st == nil || st.GetData() == nil {
		return "", false
	}
	v, ok := st.GetData().GetFields()["ota"]
	if !ok || v == nil || isNull(v) {
		return "", false
	}
	obj := v.GetStructValue()
	if obj == nil {
		return "", false
	}
	f, ok := obj.GetFields()[key]
	if !ok || f == nil {
		return "", false
	}
	s, ok := f.Kind.(*structpb.Value_StringValue)
	if !ok {
		return "", false
	}
	return s.StringValue, true
}

func (s *ShadowServiceServer) publishOTA(ctx context.Context, req *pb.Shadow, topics []string, events []pb.ShadowEvent) {
	if len(topics) == 0 {
		return
	}
	for _, ev := range events {
		clone := proto.Clone(req).(*pb.Shadow)
		clone.Event = ev
		clone.EventId = 0
		clone.Sequence = 0
		s.publishStamped(ctx, clone, topics...)
	}
}

func patchErrText(err error) string {
	if err == nil {
		return ""
	}
	if st, ok := status.FromError(err); ok && st.Message() != "" {
		return st.Message()
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Message()
	}
	return err.Error()
}
