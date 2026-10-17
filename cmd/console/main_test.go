package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStaticHandler(t *testing.T) {
	if log == nil || ETAG == "" {
		t.Fatal("init did not set config")
	}
	dir := t.TempDir()
	body := []byte("console")
	path := filepath.Join(dir, "app.js")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	h := StaticHandler(dir)
	req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	req.Header.Set("If-Modified-Since", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatal(rr.Code)
	}
	if rr.Body.String() != string(body) {
		t.Fatal(rr.Body.String())
	}
	if rr.Header().Get("Etag") != ETAG {
		t.Fatal(rr.Header().Get("Etag"))
	}

	missing := httptest.NewRequest(http.MethodGet, "/missing.txt", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, missing)
	if rr.Code != http.StatusNotFound {
		t.Fatal(rr.Code)
	}
}
