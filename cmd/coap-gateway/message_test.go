package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestReadExtAndOptionErrors(t *testing.T) {
	if readExt([]byte{5}, 1) != 18 {
		t.Fatal(readExt([]byte{5}, 1))
	}
	if readExt([]byte{0x00, 0x01}, 2) != 270 {
		t.Fatal(readExt([]byte{0x00, 0x01}, 2))
	}

	long := []byte{0x40, codePost, 0x00, 0x01, 0xBD, 0x00}
	long = append(long, bytes.Repeat([]byte{'a'}, 13)...)
	long = append(long, 0xff, 'x')
	msg, err := parseCoAP(long)
	if err != nil || len(msg.Path) != 1 || msg.Path[0] != "aaaaaaaaaaaaa" || string(msg.Payload) != "x" {
		t.Fatalf("%+v %v", msg, err)
	}
	wide := []byte{0x40, codePost, 0x00, 0x01, 0xE1, 0x00, 0x00, 'z'}
	msg, err = parseCoAP(wide)
	if err != nil || len(msg.Path) != 0 {
		t.Fatalf("%+v %v", msg, err)
	}
	withPayload := ack(msg, 0x44, []byte("z"))
	if withPayload[len(withPayload)-1] != 'z' || withPayload[len(withPayload)-2] != 0xff {
		t.Fatalf("%x", withPayload)
	}

	for _, raw := range [][]byte{
		{0x01, 0x02, 0x00},
		{0x00, codePost, 0x00, 0x01},
		{0x49, codePost, 0x00, 0x01},
		{0x40, codePost, 0x00, 0x01, 0xBD},
		{0x40, codePost, 0x00, 0x01, 0xE1, 0x00},
		{0x40, codePost, 0x00, 0x01, 0xB1},
		{0x40, codePost, 0x00, 0x01, 0xF0},
		{0x40, codePost, 0x00, 0x01, 0xD1, 0x00},
	} {
		if _, err := parseCoAP(raw); err == nil {
			t.Fatalf("accepted %x", raw)
		}
	}
	if _, ok := reportedDevice(nil); ok {
		t.Fatal("nil path")
	}
	if _, ok := reportedDevice([]string{"devices", "", "state", "reported"}); ok {
		t.Fatal("empty id")
	}
	if _, ok := reportedDevice([]string{"other"}); ok {
		t.Fatal("other")
	}
}

func TestCoAPPlaintextNotice(t *testing.T) {
	got := coapPlaintextNotice(":5683")
	if !strings.Contains(got, "no DTLS") || !strings.Contains(got, "private network") {
		t.Fatal(got)
	}
}

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
