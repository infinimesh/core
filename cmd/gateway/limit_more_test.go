package main

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zaptest"
)

type errBody struct{}

func (errBody) Read([]byte) (int, error) { return 0, errors.New("read") }
func (errBody) Close() error             { return nil }

func TestLimitEdges(t *testing.T) {
	lim := &ipLimiter{}
	if !lim.allow("10.9.9.9") {
		t.Fatal("bare host")
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()
	host := strings.TrimPrefix(backend.URL, "http://")
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusCreated) })
	h := wrapGateway(next, host, true)
	req := httptest.NewRequest(http.MethodGet, "/v1/ingest/desired", nil)
	req.RemoteAddr = "10.9.9.10:1"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code == 0 {
		t.Fatal("proxy")
	}
	h = wrapGateway(next, host, false)
	req = httptest.NewRequest(http.MethodGet, "/v1/ingest", nil)
	req.RemoteAddr = "10.9.9.11:1"
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNoContent, rr.Code)

	req = httptest.NewRequest(http.MethodPost, "/devices/token", errBody{})
	req.RemoteAddr = "10.9.9.12:1"
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)

	for _, body := range []string{"", "{", `{"stream":true}`, `{"devices":{"dev":"NOPE"}}`, `{"devices":{"dev":9}}`, `{"devices":{"dev":1.5}}`, `{"devices":{"dev":-1}}`, `{"devices":{"dev":"READ"}}`} {
		req = httptest.NewRequest(http.MethodPost, "/devices/token", strings.NewReader(body))
		req.RemoteAddr = "10.9.9.13:1"
		rr = httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if body == `{"devices":{"dev":"NOPE"}}` || body == `{"devices":{"dev":9}}` || body == `{"devices":{"dev":1.5}}` || body == `{"devices":{"dev":-1}}` {
			assert.Equal(t, http.StatusBadRequest, rr.Code, body)
		} else {
			assert.Equal(t, http.StatusCreated, rr.Code, body)
		}
	}
}

func TestBuildTLSConfigEdges(t *testing.T) {
	cfg, err := buildTLSConfig(true, "", zaptest.NewLogger(t))
	if err != nil || cfg == nil || cfg.RootCAs != nil || cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("%+v %v", cfg, err)
	}
	path := t.TempDir() + "/ca.pem"
	if err := os.WriteFile(path, []byte("not a cert"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := buildTLSConfig(true, path, zaptest.NewLogger(t)); err == nil {
		t.Fatal("pem")
	}
}
