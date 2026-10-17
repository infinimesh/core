package pubsub

import "testing"

func TestNew(t *testing.T) {
	ps := New(1)
	ch := ps.Sub("t")
	ps.Pub("m", "t")
	got := <-ch
	if got != "m" {
		t.Fatalf("%v", got)
	}
	ps.Unsub(ch, "t")
	ps.Close("t")
	ps.Shutdown()
}
