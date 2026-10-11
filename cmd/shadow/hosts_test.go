package main

import "testing"

func TestDefaultDevicesHost(t *testing.T) {
	if devicesHost != "api:8000" {
		t.Fatalf("DEVICES_HOST default %q", devicesHost)
	}
}
