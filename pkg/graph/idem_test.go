package graph

import (
	"net/http"
	"testing"

	pb "github.com/infinimesh/proto/node"
	"github.com/stretchr/testify/assert"
)

func TestIdempotencyKeyReplaysCreate(t *testing.T) {
	h := http.Header{}
	h.Set("Idempotency-Key", "same")
	msg := &pb.DeleteResponse{}
	assert.False(t, loadIdem(h, "test.idem", msg))
	saveIdem(h, "test.idem", &pb.DeleteResponse{})
	again := &pb.DeleteResponse{}
	assert.True(t, loadIdem(h, "test.idem", again))

	other := http.Header{}
	other.Set("Idempotency-Key", "other")
	assert.False(t, loadIdem(other, "test.idem", &pb.DeleteResponse{}))
}
