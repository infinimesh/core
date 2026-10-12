package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGatewayRateLimit(t *testing.T) {
	var hits int
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ })
	h := wrapGateway(next, "api:8000", false)
	for i := 0; i < gatewayRatePerIP; i++ {
		req := httptest.NewRequest(http.MethodGet, "/accounts", nil)
		req.RemoteAddr = "10.0.0.8:1234"
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/accounts", nil)
	req.RemoteAddr = "10.0.0.8:9999"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusTooManyRequests, rr.Code)
	assert.Equal(t, gatewayRatePerIP, hits)
}

func TestClientIPUsesForwardedFor(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:5"
	req.Header.Set("X-Forwarded-For", "9.9.9.9, 8.8.8.8")
	assert.Equal(t, "9.9.9.9", clientIP(req))
}
