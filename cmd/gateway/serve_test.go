package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestConsoleServices(t *testing.T) {
	for k := range SERVICES_ENABLED_MAP {
		delete(SERVICES_ENABLED_MAP, k)
	}
	viper.Set("HTTP_FS", "")
	viper.Set("PAIRING", "")
	viper.Set("CHATTING", "")
	init_cs(zap.NewNop())
	rr := httptest.NewRecorder()
	cs_handler()(rr, httptest.NewRequest(http.MethodGet, "/console/services", nil), nil)
	if rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != "{}" {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}

	viper.Set("HTTP_FS", "http://files")
	viper.Set("PAIRING", "pairing:1")
	viper.Set("CHATTING", "http://chat")
	init_cs(zap.NewNop())
	rr = httptest.NewRecorder()
	cs_handler()(rr, httptest.NewRequest(http.MethodGet, "/console/services", nil), nil)
	body := rr.Body.String()
	if !strings.Contains(body, "http://files") || !strings.Contains(body, "chatting") || strings.Contains(body, "pairing:1") {
		t.Fatalf("%s", body)
	}
	if SERVICES_ENABLED_MAP["pairing"] != "pairing:1" {
		t.Fatalf("%v", SERVICES_ENABLED_MAP)
	}
}

func TestServeSuccessAndErrors(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()
	host := strings.TrimPrefix(backend.URL, "http://")

	oldAPI, oldSecure, oldBlock, oldCA := apiserver, secure, with_block, caFile
	oldCORS := append([]string(nil), corsAllowed...)
	oldListen := listenAndServe
	t.Cleanup(func() {
		apiserver, secure, with_block, caFile = oldAPI, oldSecure, oldBlock, oldCA
		corsAllowed = oldCORS
		listenAndServe = oldListen
		for k := range SERVICES_ENABLED_MAP {
			delete(SERVICES_ENABLED_MAP, k)
		}
		viper.Set("HTTP_FS", "")
		viper.Set("PAIRING", "")
		viper.Set("CHATTING", "")
	})

	for k := range SERVICES_ENABLED_MAP {
		delete(SERVICES_ENABLED_MAP, k)
	}
	apiserver = host
	secure = false
	with_block = false
	caFile = ""
	corsAllowed = []string{"https://console.example"}
	viper.Set("HTTP_FS", "http://files")
	viper.Set("PAIRING", host)
	viper.Set("CHATTING", "http://chat")
	var got http.Handler
	listenAndServe = func(addr string, h http.Handler) error {
		if addr != ":8000" {
			t.Errorf("addr %s", addr)
		}
		got = h
		return errors.New("stop")
	}
	if err := serve(); err == nil || err.Error() != "stop" {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/console/services", nil)
	req.RemoteAddr = "10.4.4.4:9"
	got.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "http_fs") {
		t.Fatalf("console %d %s", rr.Code, rr.Body)
	}
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/ingest", strings.NewReader("x"))
	req.RemoteAddr = "10.4.4.5:9"
	got.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("ingest %d %s", rr.Code, rr.Body)
	}

	viper.Set("PAIRING", "%zz")
	if err := serve(); err == nil || err.Error() == "stop" {
		t.Fatalf("pairing %v", err)
	}
	viper.Set("PAIRING", host)

	caFile = "does-not-exist.pem"
	secure = true
	if err := serve(); err == nil {
		t.Fatal("ca")
	}

	for k := range SERVICES_ENABLED_MAP {
		delete(SERVICES_ENABLED_MAP, k)
	}
	viper.Set("PAIRING", "")
	viper.Set("HTTP_FS", "")
	viper.Set("CHATTING", "")
	caFile = ""
	secure = true
	with_block = true
	apiserver = "%zz"
	listenAndServe = func(string, http.Handler) error { return nil }
	if err := serve(); err == nil {
		t.Fatal("dial")
	}
}

func TestRegisterRulesAuditGrantsErrors(t *testing.T) {
	ctx := context.Background()
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	require.Error(t, registerRulesAuditGrants(ctx, runtime.NewServeMux(), "%zz", opts))
}
