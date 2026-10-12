package main

import (
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
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	return r.RemoteAddr
}
