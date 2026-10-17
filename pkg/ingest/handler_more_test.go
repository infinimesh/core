package ingest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt/v4"
	"github.com/infinimesh/core/v3/pkg/apierr"
	inf "github.com/infinimesh/core/v3/pkg/shared"
	"github.com/infinimesh/proto/node/access"
	"github.com/infinimesh/proto/shadow"
	"github.com/stretchr/testify/assert"
)

type errBody struct{}

func (errBody) Read([]byte) (int, error) { return 0, errors.New("read") }
func (errBody) Close() error             { return nil }

func TestHandlerEdges(t *testing.T) {
	h := &Handler{Key: []byte("secret")}
	if h.now().IsZero() {
		t.Fatal("now")
	}
	mux := http.NewServeMux()
	h.Register(func(path string, fn http.HandlerFunc) {
		mux.HandleFunc(path, fn)
	})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/ingest", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/ingest/desired", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)

	post := func(body string, claims jwt.MapClaims) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/ingest", strings.NewReader(body))
		if claims != nil {
			req.Header.Set("Authorization", "Bearer "+token(t, claims))
		}
		rr := httptest.NewRecorder()
		h.Post(rr, req)
		return rr
	}
	assert.Equal(t, http.StatusUnauthorized, post(`{}`, nil).Code)
	short := httptest.NewRequest(http.MethodPost, "/v1/ingest", strings.NewReader(`{}`))
	short.Header.Set("Authorization", "Bearer")
	rr = httptest.NewRecorder()
	h.Post(rr, short)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)

	none, err := jwt.NewWithClaims(jwt.SigningMethodNone, oneDevice(true, access.Level_READ)).SignedString(jwt.UnsafeAllowNoneSignatureType)
	assert.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+none)
	rr = httptest.NewRecorder()
	h.Post(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)

	badLevel := jwt.MapClaims{inf.INFINIMESH_DEVICES_CLAIM: map[string]interface{}{"dev": "read"}}
	assert.Equal(t, http.StatusUnauthorized, post(`{}`, badLevel).Code)
	other := jwt.NewWithClaims(jwt.SigningMethodHS256, oneDevice(true, access.Level_MGMT))
	signed, err := other.SignedString([]byte("other"))
	assert.NoError(t, err)
	req = httptest.NewRequest(http.MethodPost, "/v1/ingest", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+signed)
	rr = httptest.NewRecorder()
	h.Post(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)

	req = httptest.NewRequest(http.MethodPost, "/v1/ingest", errBody{})
	req.Header.Set("Authorization", "Bearer "+token(t, oneDevice(false, access.Level_MGMT)))
	rr = httptest.NewRecorder()
	h.Post(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Equal(t, http.StatusBadRequest, post(`not-json`, oneDevice(true, access.Level_READ)).Code)
	assert.Equal(t, http.StatusBadRequest, post(`[1]`, oneDevice(true, access.Level_READ)).Code)
	assert.Equal(t, http.StatusServiceUnavailable, post(`{}`, oneDevice(true, access.Level_READ)).Code)

	h.Patch = func(context.Context, string, *shadow.State) error {
		return apierr.Code(connect.CodeUnavailable, "down")
	}
	assert.Equal(t, http.StatusServiceUnavailable, post(`{"t":1}`, oneDevice(true, access.Level_READ)).Code)

	h.Patch = func(context.Context, string, *shadow.State) error { return nil }
	rr = post(`{"t":1}`, oneDevice(true, access.Level_READ))
	assert.Equal(t, http.StatusNoContent, rr.Code)

	for _, err := range []error{
		apierr.Code(connect.CodeUnauthenticated, "no"),
		apierr.Code(connect.CodePermissionDenied, "no"),
		apierr.New(connect.CodeResourceExhausted, apierr.ReasonRateLimited, "no"),
		errors.New("plain"),
	} {
		rr = httptest.NewRecorder()
		writeErr(rr, err)
		if rr.Code == 0 {
			t.Fatal(err)
		}
	}
}

func TestDesiredWait(t *testing.T) {
	body := []byte(`{"mode":"on"}`)
	next := body
	var version uint64 = 3
	var wantErr error
	h := &Handler{
		Key: []byte("secret"),
		Desired: func(context.Context, string) ([]byte, uint64, error) {
			if wantErr != nil {
				return nil, 0, wantErr
			}
			return next, version, nil
		},
	}
	auth := "Bearer " + token(t, oneDevice(true, access.Level_READ))
	req := httptest.NewRequest(http.MethodGet, "/v1/ingest/desired?wait=nope", nil)
	req.Header.Set("Authorization", auth)
	rr := httptest.NewRecorder()
	h.DesiredState(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)

	req = httptest.NewRequest(http.MethodGet, "/v1/ingest/desired?wait=61", nil)
	req.Header.Set("Authorization", auth)
	rr = httptest.NewRecorder()
	h.DesiredState(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)

	wantErr = apierr.Code(connect.CodeUnavailable, "down")
	req = httptest.NewRequest(http.MethodGet, "/v1/ingest/desired", nil)
	req.Header.Set("Authorization", auth)
	rr = httptest.NewRecorder()
	h.DesiredState(rr, req)
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
	wantErr = nil

	h.Desired = nil
	req = httptest.NewRequest(http.MethodGet, "/v1/ingest/desired", nil)
	req.Header.Set("Authorization", auth)
	rr = httptest.NewRecorder()
	h.DesiredState(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "{}", strings.TrimSpace(rr.Body.String()))
	var mu sync.Mutex
	h.Desired = func(context.Context, string) ([]byte, uint64, error) {
		mu.Lock()
		defer mu.Unlock()
		if wantErr != nil {
			return nil, 0, wantErr
		}
		return append([]byte(nil), next...), version, nil
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/ingest/desired", nil)
	req.Header.Set("Authorization", auth)
	rr = httptest.NewRecorder()
	h.DesiredState(rr, req)
	tag := rr.Header().Get("ETag")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req = httptest.NewRequest(http.MethodGet, "/v1/ingest/desired?wait=30", nil).WithContext(ctx)
	req.Header.Set("Authorization", auth)
	req.Header.Set("If-None-Match", tag)
	rr = httptest.NewRecorder()
	h.DesiredState(rr, req)

	done := make(chan struct{})
	go func() {
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		next = []byte(`{"mode":"off"}`)
		version = 4
		mu.Unlock()
		h.notify("dev")
		close(done)
	}()
	req = httptest.NewRequest(http.MethodGet, "/v1/ingest/desired?wait=2", nil)
	req.Header.Set("Authorization", auth)
	req.Header.Set("If-None-Match", tag)
	rr = httptest.NewRecorder()
	h.DesiredState(rr, req)
	<-done
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "off")

	wantErr = errors.New("again")
	req = httptest.NewRequest(http.MethodGet, "/v1/ingest/desired?wait=2", nil)
	req.Header.Set("Authorization", auth)
	req.Header.Set("If-None-Match", rr.Header().Get("ETag"))
	rr = httptest.NewRecorder()
	h.DesiredState(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)

	wantErr = nil
	next = []byte(`{"mode":"off"}`)
	version = 4
	req = httptest.NewRequest(http.MethodGet, "/v1/ingest/desired", nil)
	req.Header.Set("Authorization", auth)
	rr = httptest.NewRecorder()
	h.DesiredState(rr, req)
	same := rr.Header().Get("ETag")
	req = httptest.NewRequest(http.MethodGet, "/v1/ingest/desired?wait=1", nil)
	req.Header.Set("Authorization", auth)
	req.Header.Set("If-None-Match", same)
	rr = httptest.NewRecorder()
	started := time.Now()
	h.DesiredState(rr, req)
	if time.Since(started) < 900*time.Millisecond || rr.Code != http.StatusNotModified {
		t.Fatalf("timeout %s %d tag %q body %s", time.Since(started), rr.Code, same, rr.Body.String())
	}
	calls := 0
	h.Desired = func(context.Context, string) ([]byte, uint64, error) {
		calls++
		if calls > 1 {
			return nil, 0, errors.New("again")
		}
		return next, version, nil
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		h.notify("dev")
	}()
	req = httptest.NewRequest(http.MethodGet, "/v1/ingest/desired?wait=2", nil)
	req.Header.Set("Authorization", auth)
	req.Header.Set("If-None-Match", same)
	rr = httptest.NewRecorder()
	h.DesiredState(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("refresh %d %s", rr.Code, rr.Body)
	}

	h.notify("nobody")
	ch := h.wait("dev")
	h.notify("dev")
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("waiter")
	}
}
