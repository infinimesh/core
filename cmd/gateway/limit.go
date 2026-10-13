package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	gatewayBodyCap   = 1 << 20
	gatewayRatePerIP = 120
)

type ipLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func (l *ipLimiter) allow(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hits == nil {
		l.hits = map[string][]time.Time{}
	}
	now := time.Now()
	cut := now.Add(-time.Minute)
	kept := l.hits[host][:0]
	for _, ts := range l.hits[host] {
		if ts.After(cut) {
			kept = append(kept, ts)
		}
	}
	if len(kept) >= gatewayRatePerIP {
		l.hits[host] = kept
		return false
	}
	l.hits[host] = append(kept, now)
	return true
}

func wrapGateway(next http.Handler, apiHost string, secure bool) http.Handler {
	scheme := "http"
	if secure {
		scheme = "https"
	}
	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: scheme, Host: apiHost})
	limit := &ipLimiter{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, gatewayBodyCap)
		if !limit.allow(clientIP(r)) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		if r.URL.Path == "/v1/ingest" || r.URL.Path == "/v1/ingest/desired" {
			proxy.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodPost && (r.URL.Path == "/v1/devices/token" || r.URL.Path == "/devices/token") {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			if msg := invalidDeviceToken(body); msg != "" {
				http.Error(w, msg, http.StatusBadRequest)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func invalidDeviceToken(body []byte) string {
	if len(bytes.TrimSpace(body)) == 0 {
		return ""
	}
	var raw struct {
		Stream  bool                       `json:"stream"`
		Devices map[string]json.RawMessage `json:"devices"`
	}
	if json.Unmarshal(body, &raw) != nil {
		return ""
	}
	if raw.Stream {
		return ""
	}
	if len(raw.Devices) == 0 {
		return "a device token needs at least one device with access NONE, READ, MGMT, ADMIN, or ROOT"
	}
	for id, level := range raw.Devices {
		if !validAccessLevel(level) {
			return "access level for " + id + " must be NONE, READ, MGMT, ADMIN, or ROOT"
		}
	}
	return ""
}

func validAccessLevel(raw json.RawMessage) bool {
	var name string
	if json.Unmarshal(raw, &name) == nil {
		switch name {
		case "NONE", "READ", "MGMT", "ADMIN", "ROOT":
			return true
		default:
			return false
		}
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil && n == float64(int(n)) && n >= 0 && n <= 4 {
		return true
	}
	return false
}

func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	return r.RemoteAddr
}
