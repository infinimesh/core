package shadow

import (
	"testing"

	pb "github.com/infinimesh/proto/shadow"
)

func TestShadowQueueStopsAtCap(t *testing.T) {
	q := newShadowQueue()
	for i := 0; i < shadowQueueCap+10; i++ {
		q.push(&pb.Shadow{Sequence: uint64(i + 1)})
	}
	if len(q.buf) != shadowQueueCap {
		t.Fatalf("queue length %d, cap %d", len(q.buf), shadowQueueCap)
	}
	if q.overflowErr() == nil {
		t.Fatal("a full queue did not fail the stream")
	}
	first, ok := q.pop()
	if !ok || first.GetSequence() != 1 {
		t.Fatalf("oldest update %v ok=%v", first.GetSequence(), ok)
	}
}
