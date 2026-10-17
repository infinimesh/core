package main

import "testing"

func TestInitConfig(t *testing.T) {
	if log == nil || port == "" || redisHost == "" || len(SIGNING_KEY) == 0 {
		t.Fatal("init did not set config")
	}
}
