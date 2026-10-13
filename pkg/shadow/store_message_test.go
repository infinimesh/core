package shadow

import (
	"testing"

	pb "github.com/infinimesh/proto/shadow"
	"go.uber.org/zap"
)

func TestStoreMessageSkipsOTAEvent(t *testing.T) {
	s := NewShadowServiceServer(zap.NewNop(), nil, nil)
	s.storeMessage(zap.NewNop(), &pb.Shadow{
		Device:  "dev-a",
		Event:   pb.ShadowEvent_OTA_TARGET_SET,
		Desired: &pb.State{},
	})
}
