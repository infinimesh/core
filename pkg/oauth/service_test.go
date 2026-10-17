package oauth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/infinimesh/core/pkg/oauth/config"
	"go.uber.org/zap"
)

func TestRegisterProviders(t *testing.T) {
	svc := NewOauthService(zap.NewNop())
	router := mux.NewRouter()
	svc.Register(router, map[string]config.Config{
		"missing": {},
		"github":  {ClientId: "id", ClientSecret: "sec", RedirectUrl: "http://localhost/cb"},
	}, nil, nil, "root-token")

	req := httptest.NewRequest(http.MethodGet, "/oauth/providers", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	if rr.Body.String() != `["github"]` && rr.Body.String() != `["github"]`+"\n" {
		if len(svc.registeredProviders) != 1 || svc.registeredProviders[0] != "github" {
			t.Fatalf("providers %v body %s", svc.registeredProviders, rr.Body.String())
		}
	}

	prev := encodeProviders
	encodeProviders = func(any) ([]byte, error) { return nil, errors.New("boom") }
	t.Cleanup(func() { encodeProviders = prev })
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
}
