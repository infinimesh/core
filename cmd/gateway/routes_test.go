package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/grpc-ecosystem/grpc-gateway/v2/utilities"
	rules "github.com/infinimesh/proto/node/rules"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestRulesAuditGrantsRoutesAreRegistered(t *testing.T) {
	src, err := os.ReadFile("main.go")
	require.NoError(t, err)
	require.Contains(t, string(src), "registerRulesAuditGrants(")

	mux := runtime.NewServeMux()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = registerRulesAuditGrants(ctx, mux, "127.0.0.1:1", []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	})
	require.NoError(t, err)

	for _, target := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/rules"},
		{http.MethodPost, "/v1/rules"},
		{http.MethodDelete, "/v1/rules/rule-1"},
		{http.MethodGet, "/v1/alarms?namespace=ns&any_state=true"},
		{http.MethodGet, "/v1/audit"},
		{http.MethodGet, "/v1/grants"},
		{http.MethodPost, "/v1/grants"},
	} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(target.method, target.path, nil)
		mux.ServeHTTP(rr, req)
		assert.NotEqualf(t, http.StatusNotFound, rr.Code, "%s %s", target.method, target.path)
	}
}

func TestAlarmListQueryUsesAnyState(t *testing.T) {
	values, err := url.ParseQuery("namespace=ns&any_state=true")
	require.NoError(t, err)
	var req rules.ListAlarmsRequest
	err = runtime.PopulateQueryParameters(&req, values, &utilities.DoubleArray{
		Encoding: map[string]int{},
		Base:     []int(nil),
		Check:    []int(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, "ns", req.GetNamespace())
	assert.True(t, req.GetAnyState())
}
