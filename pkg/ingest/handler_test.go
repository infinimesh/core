package ingest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	inf "github.com/infinimesh/core/v3/pkg/shared"
	"github.com/infinimesh/proto/node/access"
	"github.com/infinimesh/proto/shadow"
	"github.com/stretchr/testify/assert"
)

func token(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("secret"))
	assert.NoError(t, err)
	return signed
}

func oneDevice(post bool, level access.Level) jwt.MapClaims {
	claims := jwt.MapClaims{
		inf.INFINIMESH_DEVICES_CLAIM: map[string]interface{}{"dev": float64(level)},
	}
	if post {
		claims[inf.INFINIMESH_POST_CLAIM] = true
	}
	return claims
}

func TestPostIngest(t *testing.T) {
	var got string
	h := &Handler{
		Key: []byte("secret"),
		Now: func() time.Time { return time.Unix(10, 0) },
		Patch: func(_ context.Context, device string, reported *shadow.State) error {
			got = device
			assert.Equal(t, "hot", reported.GetData().GetFields()["t"].GetStringValue())
			return nil
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest", strings.NewReader(`{"t":"hot"}`))
	req.Header.Set("Authorization", "Bearer "+token(t, oneDevice(true, access.Level_READ)))
	rr := httptest.NewRecorder()
	h.Post(rr, req)
	assert.Equal(t, http.StatusNoContent, rr.Code)
	assert.Equal(t, "dev", got)

	req = httptest.NewRequest(http.MethodPost, "/v1/ingest", strings.NewReader(`{"t":"hot"}`))
	req.Header.Set("Authorization", "Bearer "+token(t, oneDevice(false, access.Level_READ)))
	rr = httptest.NewRecorder()
	h.Post(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)

	two := jwt.MapClaims{inf.INFINIMESH_DEVICES_CLAIM: map[string]interface{}{
		"a": float64(access.Level_MGMT),
		"b": float64(access.Level_MGMT),
	}}
	req = httptest.NewRequest(http.MethodPost, "/v1/ingest", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+token(t, two))
	rr = httptest.NewRecorder()
	h.Post(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)

	req = httptest.NewRequest(http.MethodPost, "/v1/ingest", strings.NewReader(strings.Repeat("a", MaxBody+1)))
	req.Header.Set("Authorization", "Bearer "+token(t, oneDevice(true, access.Level_READ)))
	rr = httptest.NewRecorder()
	h.Post(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)

	h.Limit = 1
	req = httptest.NewRequest(http.MethodPost, "/v1/ingest", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+token(t, oneDevice(true, access.Level_READ)))
	rr = httptest.NewRecorder()
	h.Post(rr, req)
	assert.Equal(t, http.StatusTooManyRequests, rr.Code)
}

func TestPostNamespaceQuota(t *testing.T) {
	h := &Handler{
		Key: []byte("secret"),
		Now: func() time.Time { return time.Unix(10, 0) },
		Patch: func(context.Context, string, *shadow.State) error {
			return nil
		},
		Quota: func(context.Context, string) (float64, float64, string) {
			return 1, 0, "ns"
		},
	}
	post := func() int {
		req := httptest.NewRequest(http.MethodPost, "/v1/ingest", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+token(t, oneDevice(true, access.Level_READ)))
		rr := httptest.NewRecorder()
		h.Post(rr, req)
		return rr.Code
	}
	if post() != http.StatusNoContent {
		t.Fatal("first post")
	}
	if post() != http.StatusTooManyRequests {
		t.Fatal("second post")
	}
}

func TestDesiredNotModified(t *testing.T) {
	body := []byte(`{"mode":"on"}`)
	h := &Handler{
		Key: []byte("secret"),
		Desired: func(context.Context, string) ([]byte, uint64, error) {
			return body, 3, nil
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/ingest/desired", nil)
	req.Header.Set("Authorization", "Bearer "+token(t, oneDevice(true, access.Level_READ)))
	rr := httptest.NewRecorder()
	h.DesiredState(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	tag := rr.Header().Get("ETag")
	assert.NotEmpty(t, tag)
	assert.Equal(t, "3", rr.Header().Get("X-Shadow-Version"))

	req = httptest.NewRequest(http.MethodGet, "/v1/ingest/desired", nil)
	req.Header.Set("Authorization", "Bearer "+token(t, oneDevice(true, access.Level_READ)))
	req.Header.Set("If-None-Match", tag)
	rr = httptest.NewRecorder()
	h.DesiredState(rr, req)
	assert.Equal(t, http.StatusNotModified, rr.Code)
}
