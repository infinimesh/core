package handlers

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/gorilla/mux"
	"github.com/infinimesh/core/pkg/oauth/config"
	"github.com/infinimesh/proto/node"
	"github.com/infinimesh/proto/node/access"
	"github.com/infinimesh/proto/node/accounts"
	"github.com/infinimesh/proto/node/namespaces"
	"github.com/infinimesh/proto/node/nodeconnect"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
)

func TestGithubAPI(t *testing.T) {
	t.Cleanup(resetGitHub)

	var user *GithubUser
	var err error
	githubHTTP = &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("down")
	})}
	if _, err = getGithubUser("tok"); err == nil {
		t.Fatal("expected do error")
	}
	if _, err = getGithubUserTeams("tok"); err == nil {
		t.Fatal("expected teams do error")
	}

	githubHTTP = &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: errBody{}, Header: http.Header{}}, nil
	})}
	if _, err = getGithubUser("tok"); err == nil {
		t.Fatal("expected read error")
	}
	if _, err = getGithubUserTeams("tok"); err == nil {
		t.Fatal("expected teams read error")
	}

	githubHTTP = &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("not-json")), Header: http.Header{}}, nil
	})}
	if _, err = getGithubUser("tok"); err == nil {
		t.Fatal("expected json error")
	}
	if _, err = getGithubUserTeams("tok"); err == nil {
		t.Fatal("expected teams json error")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			_, _ = w.Write([]byte(`{"login":"octo","name":"Octo"}`))
		case "/user/teams":
			_, _ = w.Write([]byte(`[{"name":"dev","organization":{"login":"acme","name":"acme"}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	githubHTTP = srv.Client()
	githubAPIBase = srv.URL
	user, err = getGithubUser("tok")
	if err != nil || user.Login != "octo" || user.Name != "Octo" {
		t.Fatalf("%+v %v", user, err)
	}
	teams, err := getGithubUserTeams("tok")
	if err != nil || len(teams) != 1 || teams[0].Org.Name != "acme" {
		t.Fatalf("%v %v", teams, err)
	}
}

func TestRegisterAndLogin(t *testing.T) {
	g := &GithubRegistrar{}
	router := mux.NewRouter()
	g.Register(zap.NewNop(), router, &config.Config{
		ClientId:     "cid",
		ClientSecret: "sec",
		RedirectUrl:  "http://localhost/cb",
		Scopes:       []string{"read:user"},
	}, nil, nil, "root")

	cases := []struct {
		target string
		auth   string
		state  string
		token  string
	}{
		{"/oauth/github/login", "", "", ""},
		{"/oauth/github/login?state=s", "", "s", ""},
		{"/oauth/github/login?state=s2&method=sign_in", "Bearer", "s2", ""},
		{"/oauth/github/login?state=s3&method=link", "Bearer tok", "s3", "tok"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.target, nil)
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusTemporaryRedirect {
			t.Fatalf("%s status %d", tc.target, rr.Code)
		}
		if tc.state == "" {
			continue
		}
		got := g.states[tc.state]
		if got == nil || got.Token != tc.token {
			t.Fatalf("%s state %+v", tc.target, got)
		}
	}
}

func TestCheckout(t *testing.T) {
	log := zap.NewNop()
	org := &config.Config{OrganizationMapping: map[string]config.OrgAccess{
		"acme": {Namespace: "ns", Level: 2},
	}}
	g := newRegistrar()

	rr := serveCheckout(g, log, tokenConfig(t), org, &accFake{}, &nsFake{}, "root", "state=missing")
	if rr.Code != http.StatusTemporaryRedirect {
		t.Fatalf("missing state %d", rr.Code)
	}

	g.states["badtok"] = &StateInfo{Method: "sign_in"}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	dead := &oauth2.Config{Endpoint: oauth2.Endpoint{TokenURL: "http://" + addr, AuthStyle: oauth2.AuthStyleInHeader}}
	rr = serveCheckout(g, log, dead, org, &accFake{}, &nsFake{}, "root", "state=badtok&code=c")
	if rr.Code != http.StatusTemporaryRedirect {
		t.Fatalf("exchange %d", rr.Code)
	}

	api := githubUserServer(t, `{"login":"octo","name":"Octo"}`, `[{"name":"dev","organization":{"name":"acme"}}]`)
	useAPI(t, api)

	g.states["userfail"] = &StateInfo{Method: "sign_in"}
	githubHTTP = &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("down")
	})}
	rr = serveCheckout(g, log, tokenConfig(t), org, &accFake{}, &nsFake{}, "root", "state=userfail&code=c")
	if rr.Code != http.StatusTemporaryRedirect {
		t.Fatalf("user %d", rr.Code)
	}

	useAPI(t, githubUserServer(t, `{"login":"octo","name":"Octo"}`, `not-json`))
	g.states["teamsfail"] = &StateInfo{Method: "sign_in"}
	rr = serveCheckout(g, log, tokenConfig(t), org, &accFake{}, &nsFake{}, "root", "state=teamsfail&code=c")
	if rr.Code != http.StatusTemporaryRedirect {
		t.Fatalf("teams %d", rr.Code)
	}

	useAPI(t, githubUserServer(t, `{"login":"octo","name":"Octo"}`, `[{"name":"dev","organization":{"name":"other"}}]`))
	g.states["noorg"] = &StateInfo{Method: "sign_in"}
	rr = serveCheckout(g, log, tokenConfig(t), org, &accFake{}, &nsFake{}, "root", "state=noorg&code=c")
	if rr.Code != http.StatusTemporaryRedirect {
		t.Fatalf("org %d", rr.Code)
	}

	useAPI(t, api)
	g.states["other"] = &StateInfo{Method: "other"}
	rr = serveCheckout(g, log, tokenConfig(t), org, &accFake{}, &nsFake{}, "root", "state=other&code=c")
	if rr.Code != http.StatusTemporaryRedirect {
		t.Fatalf("method %d", rr.Code)
	}

	acc := &accFake{token: func(context.Context, *connect.Request[node.TokenRequest]) (*connect.Response[node.TokenResponse], error) {
		return connect.NewResponse(&node.TokenResponse{Token: "session"}), nil
	}}
	g.states["in"] = &StateInfo{Method: "sign_in"}
	rr = serveCheckout(g, log, tokenConfig(t), org, acc, &nsFake{}, "root", "state=in&code=c")
	if rr.Code != http.StatusOK || rr.Body.String() != "session" {
		t.Fatalf("sign in %d %s", rr.Code, rr.Body.String())
	}

	acc = &accFake{token: func(context.Context, *connect.Request[node.TokenRequest]) (*connect.Response[node.TokenResponse], error) {
		return nil, errors.New("no token")
	}, create: func(context.Context, *connect.Request[accounts.CreateRequest]) (*connect.Response[accounts.CreateResponse], error) {
		return nil, errors.New("create")
	}}
	g.states["cfail"] = &StateInfo{Method: "sign_in"}
	rr = serveCheckout(g, log, tokenConfig(t), org, acc, &nsFake{}, "root", "state=cfail&code=c")
	if rr.Code != http.StatusTemporaryRedirect {
		t.Fatalf("create %d", rr.Code)
	}

	acc = &accFake{token: func(context.Context, *connect.Request[node.TokenRequest]) (*connect.Response[node.TokenResponse], error) {
		return nil, errors.New("no token")
	}, create: func(context.Context, *connect.Request[accounts.CreateRequest]) (*connect.Response[accounts.CreateResponse], error) {
		return connect.NewResponse(&accounts.CreateResponse{Account: &accounts.Account{Uuid: "new"}}), nil
	}}
	ns := &nsFake{join: func(context.Context, *connect.Request[node.JoinRequest]) (*connect.Response[accounts.Accounts], error) {
		return nil, errors.New("join")
	}}
	g.states["jfail"] = &StateInfo{Method: "sign_in"}
	rr = serveCheckout(g, log, tokenConfig(t), org, acc, ns, "root", "state=jfail&code=c")
	if rr.Code != http.StatusTemporaryRedirect {
		t.Fatalf("join %d", rr.Code)
	}

	var tokens int
	acc = &accFake{token: func(context.Context, *connect.Request[node.TokenRequest]) (*connect.Response[node.TokenResponse], error) {
		tokens++
		if tokens == 1 {
			return nil, errors.New("first")
		}
		return nil, errors.New("second")
	}, create: func(context.Context, *connect.Request[accounts.CreateRequest]) (*connect.Response[accounts.CreateResponse], error) {
		return connect.NewResponse(&accounts.CreateResponse{Account: &accounts.Account{Uuid: "new"}}), nil
	}}
	ns = &nsFake{join: func(context.Context, *connect.Request[node.JoinRequest]) (*connect.Response[accounts.Accounts], error) {
		return connect.NewResponse(&accounts.Accounts{}), nil
	}}
	g.states["t2"] = &StateInfo{Method: "sign_in"}
	rr = serveCheckout(g, log, tokenConfig(t), org, acc, ns, "root", "state=t2&code=c")
	if rr.Code != http.StatusTemporaryRedirect {
		t.Fatalf("second token %d", rr.Code)
	}

	tokens = 0
	acc.token = func(context.Context, *connect.Request[node.TokenRequest]) (*connect.Response[node.TokenResponse], error) {
		tokens++
		if tokens == 1 {
			return nil, errors.New("first")
		}
		return connect.NewResponse(&node.TokenResponse{Token: "created"}), nil
	}
	g.states["made"] = &StateInfo{Method: "sign_in"}
	rr = serveCheckout(g, log, tokenConfig(t), org, acc, ns, "root", "state=made&code=c")
	if rr.Code != http.StatusOK || rr.Body.String() != "created" {
		t.Fatalf("created %d %s", rr.Code, rr.Body.String())
	}

	g.states["linkget"] = &StateInfo{Method: "link", Token: "acc"}
	rr = serveCheckout(g, log, tokenConfig(t), org, &accFake{}, &nsFake{}, "root", "state=linkget&code=c")
	if rr.Code != http.StatusTemporaryRedirect {
		t.Fatalf("link get %d", rr.Code)
	}

	acc = &accFake{get: func(context.Context, *connect.Request[accounts.Account]) (*connect.Response[accounts.Account], error) {
		return connect.NewResponse(&accounts.Account{Uuid: "me-id"}), nil
	}, set: func(context.Context, *connect.Request[node.SetCredentialsRequest]) (*connect.Response[node.SetCredentialsResponse], error) {
		return nil, errors.New("set")
	}}
	g.states["linkset"] = &StateInfo{Method: "link", Token: "acc"}
	rr = serveCheckout(g, log, tokenConfig(t), org, acc, &nsFake{}, "root", "state=linkset&code=c")
	if rr.Code != http.StatusTemporaryRedirect {
		t.Fatalf("link set %d", rr.Code)
	}

	acc.set = func(context.Context, *connect.Request[node.SetCredentialsRequest]) (*connect.Response[node.SetCredentialsResponse], error) {
		return connect.NewResponse(&node.SetCredentialsResponse{}), nil
	}
	g.states["linkok"] = &StateInfo{Method: "link", Token: "acc"}
	rr = serveCheckout(g, log, tokenConfig(t), org, acc, &nsFake{}, "root", "state=linkok&code=c")
	if rr.Code != http.StatusOK {
		t.Fatalf("link ok %d", rr.Code)
	}
}

func newRegistrar() *GithubRegistrar {
	return &GithubRegistrar{states: map[string]*StateInfo{}, mu: &sync.Mutex{}}
}

func serveCheckout(g *GithubRegistrar, log *zap.Logger, oc *oauth2.Config, cfg *config.Config, acc nodeconnect.AccountsServiceClient, ns nodeconnect.NamespacesServiceClient, token, rawQuery string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/oauth/github/checkout?"+rawQuery, nil)
	rr := httptest.NewRecorder()
	g.GithubCheckoutHandler(log, oc, cfg, acc, ns, token)(rr, req)
	return rr
}

func tokenConfig(t *testing.T) *oauth2.Config {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"ghtok","token_type":"bearer"}`)
	}))
	t.Cleanup(srv.Close)
	return &oauth2.Config{
		ClientID:     "id",
		ClientSecret: "sec",
		RedirectURL:  "http://localhost/cb",
		Endpoint:     oauth2.Endpoint{AuthURL: srv.URL, TokenURL: srv.URL, AuthStyle: oauth2.AuthStyleInHeader},
	}
}

func githubUserServer(t *testing.T, user, teams string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			_, _ = io.WriteString(w, user)
		case "/user/teams":
			_, _ = io.WriteString(w, teams)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func useAPI(t *testing.T, srv *httptest.Server) {
	t.Helper()
	githubHTTP = srv.Client()
	githubAPIBase = srv.URL
	t.Cleanup(resetGitHub)
}

func resetGitHub() {
	githubHTTP = http.DefaultClient
	githubAPIBase = "https://api.github.com"
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type errBody struct{}

func (errBody) Read([]byte) (int, error) { return 0, errors.New("read") }
func (errBody) Close() error             { return nil }

type accFake struct {
	token  func(context.Context, *connect.Request[node.TokenRequest]) (*connect.Response[node.TokenResponse], error)
	get    func(context.Context, *connect.Request[accounts.Account]) (*connect.Response[accounts.Account], error)
	create func(context.Context, *connect.Request[accounts.CreateRequest]) (*connect.Response[accounts.CreateResponse], error)
	set    func(context.Context, *connect.Request[node.SetCredentialsRequest]) (*connect.Response[node.SetCredentialsResponse], error)
}

func (a *accFake) Token(ctx context.Context, req *connect.Request[node.TokenRequest]) (*connect.Response[node.TokenResponse], error) {
	if a.token == nil {
		return nil, errors.New("token")
	}
	return a.token(ctx, req)
}
func (a *accFake) Get(ctx context.Context, req *connect.Request[accounts.Account]) (*connect.Response[accounts.Account], error) {
	if a.get == nil {
		return nil, errors.New("get")
	}
	return a.get(ctx, req)
}
func (a *accFake) List(context.Context, *connect.Request[node.ListRequest]) (*connect.Response[accounts.Accounts], error) {
	return nil, errors.New("unused")
}
func (a *accFake) Create(ctx context.Context, req *connect.Request[accounts.CreateRequest]) (*connect.Response[accounts.CreateResponse], error) {
	if a.create == nil {
		return nil, errors.New("create")
	}
	return a.create(ctx, req)
}
func (a *accFake) Update(context.Context, *connect.Request[accounts.Account]) (*connect.Response[accounts.Account], error) {
	return nil, errors.New("unused")
}
func (a *accFake) Toggle(context.Context, *connect.Request[accounts.Account]) (*connect.Response[accounts.Account], error) {
	return nil, errors.New("unused")
}
func (a *accFake) Delete(context.Context, *connect.Request[accounts.Account]) (*connect.Response[node.DeleteResponse], error) {
	return nil, errors.New("unused")
}
func (a *accFake) Deletables(context.Context, *connect.Request[accounts.Account]) (*connect.Response[access.Nodes], error) {
	return nil, errors.New("unused")
}
func (a *accFake) SetCredentials(ctx context.Context, req *connect.Request[node.SetCredentialsRequest]) (*connect.Response[node.SetCredentialsResponse], error) {
	if a.set == nil {
		return nil, errors.New("set")
	}
	return a.set(ctx, req)
}
func (a *accFake) GetCredentials(context.Context, *connect.Request[node.GetCredentialsRequest]) (*connect.Response[node.GetCredentialsResponse], error) {
	return nil, errors.New("unused")
}
func (a *accFake) DeleteCredentials(context.Context, *connect.Request[node.DeleteCredentialsRequest]) (*connect.Response[node.DeleteResponse], error) {
	return nil, errors.New("unused")
}

type nsFake struct {
	join func(context.Context, *connect.Request[node.JoinRequest]) (*connect.Response[accounts.Accounts], error)
}

func (n *nsFake) Get(context.Context, *connect.Request[namespaces.Namespace]) (*connect.Response[namespaces.Namespace], error) {
	return nil, errors.New("unused")
}
func (n *nsFake) List(context.Context, *connect.Request[node.ListRequest]) (*connect.Response[namespaces.Namespaces], error) {
	return nil, errors.New("unused")
}
func (n *nsFake) Create(context.Context, *connect.Request[namespaces.Namespace]) (*connect.Response[namespaces.Namespace], error) {
	return nil, errors.New("unused")
}
func (n *nsFake) Update(context.Context, *connect.Request[namespaces.Namespace]) (*connect.Response[namespaces.Namespace], error) {
	return nil, errors.New("unused")
}
func (n *nsFake) Delete(context.Context, *connect.Request[namespaces.Namespace]) (*connect.Response[node.DeleteResponse], error) {
	return nil, errors.New("unused")
}
func (n *nsFake) Deletables(context.Context, *connect.Request[namespaces.Namespace]) (*connect.Response[access.Nodes], error) {
	return nil, errors.New("unused")
}
func (n *nsFake) Joins(context.Context, *connect.Request[namespaces.Namespace]) (*connect.Response[accounts.Accounts], error) {
	return nil, errors.New("unused")
}
func (n *nsFake) Join(ctx context.Context, req *connect.Request[node.JoinRequest]) (*connect.Response[accounts.Accounts], error) {
	if n.join == nil {
		return nil, errors.New("join")
	}
	return n.join(ctx, req)
}
