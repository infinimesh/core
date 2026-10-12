package quota

import (
	"testing"
	"time"
)

func TestAllowZeroIsUnlimited(t *testing.T) {
	l := New()
	for i := 0; i < 5; i++ {
		if !l.Allow("dev", 0) {
			t.Fatal("zero rate limited a message")
		}
	}
}

func TestAllowBlocksTheSecondImmediateMessage(t *testing.T) {
	l := New()
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	l.SetNow(func() time.Time { return now })
	if !l.Allow("dev", 1) {
		t.Fatal("first message")
	}
	if l.Allow("dev", 1) {
		t.Fatal("second message in the same instant was allowed")
	}
	now = now.Add(time.Second)
	if !l.Allow("dev", 1) {
		t.Fatal("message after one second")
	}
}
