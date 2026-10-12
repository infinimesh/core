package main

import "testing"

func TestParseReportedPost(t *testing.T) {
	raw := []byte{
		0x42, codePost, 0x00, 0x07, 'a', 'b',
		0xb7, 'd', 'e', 'v', 'i', 'c', 'e', 's',
		0x03, 'd', 'e', 'v',
		0x05, 's', 't', 'a', 't', 'e',
		0x08, 'r', 'e', 'p', 'o', 'r', 't', 'e', 'd',
		0xff, '{', '}',
	}
	msg, err := parseCoAP(raw)
	if err != nil {
		t.Fatal(err)
	}
	id, ok := reportedDevice(msg.Path)
	if !ok || id != "dev" || string(msg.Payload) != "{}" || string(msg.Token) != "ab" {
		t.Fatalf("%+v", msg)
	}
	got := ack(msg, 0x44, nil)
	if got[1] != 0x44 || got[2] != 0 || got[3] != 7 {
		t.Fatalf("%x", got)
	}
}
