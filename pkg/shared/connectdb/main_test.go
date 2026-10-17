package connectdb

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/infinimesh/core/v3/pkg/graph/schema"
	"go.uber.org/zap"
)

func TestMakeDBConnectionRetriesThenSucceeds(t *testing.T) {
	prev := CONNECT_ATTEMPT_DELAY
	CONNECT_ATTEMPT_DELAY = time.Millisecond
	t.Cleanup(func() { CONNECT_ATTEMPT_DELAY = prev })

	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":true,"errorMessage":"down","code":500}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":{}}`))
	}))
	t.Cleanup(srv.Close)

	db := MakeDBConnection(zap.NewNop(), srv.Listener.Addr().String(), "root:secret")
	if db == nil || db.Name() != schema.DB_NAME {
		t.Fatalf("db %#v", db)
	}
	if n.Load() < 2 {
		t.Fatalf("attempts %d", n.Load())
	}
}

func TestMakeDBConnectionBadEndpoint(t *testing.T) {
	prev := logFatal
	logFatal = func(log *zap.Logger, msg string, _ ...zap.Field) {
		panic(msg)
	}
	t.Cleanup(func() { logFatal = prev })
	defer func() {
		if recover() == nil {
			t.Fatal("expected connection failure")
		}
	}()
	MakeDBConnection(zap.NewNop(), "%zz", "root:secret")
}
