package plugins

import (
	"testing"

	"github.com/infinimesh/proto/node/access"
	devpb "github.com/infinimesh/proto/node/devices"
	pb "github.com/infinimesh/proto/shadow"
	"go.uber.org/zap"
)

func TestHandlePluginMessage(t *testing.T) {
	logger = zap.NewNop()
	publishPlugin = func(*devpb.Device, *pb.Shadow) {}
	t.Cleanup(func() { publishPlugin = Publish })

	fetched := false
	handlePluginMessage("nope", func(string) *devpb.Device {
		fetched = true
		return &devpb.Device{Uuid: "dev"}
	})
	if fetched {
		t.Fatal("bad message fetched a device")
	}

	published := false
	publishPlugin = func(*devpb.Device, *pb.Shadow) { published = true }
	handlePluginMessage(&pb.Shadow{Device: "dev"}, func(string) *devpb.Device { return nil })
	if published {
		t.Fatal("nil device was published")
	}

	original := &pb.Shadow{Device: "dev"}
	ns := "plant"
	var got *pb.Shadow
	publishPlugin = func(_ *devpb.Device, state *pb.Shadow) { got = state }
	handlePluginMessage(original, func(id string) *devpb.Device {
		if id != "dev" {
			t.Fatalf("device %s", id)
		}
		return &devpb.Device{
			Uuid:  "dev",
			Title: "Pump",
			Tags:  []string{"water"},
			Access: &access.Access{
				Namespace: &ns,
			},
		}
	})
	if got == nil || got == original {
		t.Fatal("published the shared shadow")
	}
	if got.GetTitle() != "Pump" || got.GetNamespace() != "plant" || len(got.GetTags()) != 1 || got.GetTags()[0] != "water" {
		t.Fatalf("metadata %+v", got)
	}
	if original.GetTitle() != "" || original.GetNamespace() != "" || len(original.GetTags()) != 0 {
		t.Fatalf("original mutated %+v", original)
	}
}
