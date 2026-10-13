package main

import "testing"

func TestGatewayHeaderForwardsOTAProvider(t *testing.T) {
	got, ok := gatewayHeader("X-Infinimesh-Ota-Provider")
	if !ok || got != "x-infinimesh-ota-provider" {
		t.Fatalf("header = %q %v", got, ok)
	}
	if _, ok := gatewayHeader("X-Not-A-Gateway-Header"); ok {
		t.Fatal("unknown header was forwarded")
	}
}
