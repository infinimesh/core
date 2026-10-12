package main

import (
	"testing"

	devpb "github.com/infinimesh/proto/node/devices"
	pb "github.com/infinimesh/proto/shadow"
)

func TestCopyDeviceMeta(t *testing.T) {
	device := &devpb.Device{Uuid: "dev", Title: "Pump", Tags: []string{"water", "hot"}}
	sh := &pb.Shadow{Device: device.Uuid}
	copyDeviceMeta(sh, device)
	if sh.Title != "Pump" || len(sh.Tags) != 2 || sh.Tags[0] != "water" {
		t.Fatalf("%+v", sh)
	}
	sh.Tags[0] = "changed"
	if device.Tags[0] != "water" {
		t.Fatal("tags alias the device")
	}
	copyDeviceMeta(nil, device)
	copyDeviceMeta(sh, nil)
}
