package credentials

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/arangodb/go-driver"
	ldap "github.com/go-ldap/ldap/v3"
	driver_mocks "github.com/infinimesh/core/mocks/github.com/arangodb/go-driver"
	accountspb "github.com/infinimesh/proto/node/accounts"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/mock"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

func TestStandardCredentials(t *testing.T) {
	cred, err := NewStandardCredentials("user", "pw")
	if err != nil {
		t.Fatal(err)
	}
	sc := cred.(*StandardCredentials)
	if sc.Type() != "standard" || len(sc.Listable()) != 1 || sc.Listable()[0] != "user" {
		t.Fatalf("%+v", sc.Listable())
	}
	if !sc.Authorize("user", "pw") || sc.Authorize("user", "no") {
		t.Fatal("authorize")
	}
	sc.ID = driver.NewDocumentID("credentials", "std")
	if sc.Key() != "std" {
		t.Fatal(sc.Key())
	}
	sc.SetLogger(zap.NewNop())

	if _, err = StandardFromMap(map[string]interface{}{}); err == nil {
		t.Fatal("missing username")
	}
	if _, err = StandardFromMap(map[string]interface{}{"username": 1}); err == nil {
		t.Fatal("username type")
	}
	got, err := StandardFromMap(map[string]interface{}{"username": "user"})
	if err != nil || got.Listable()[0] != "user" {
		t.Fatal(err)
	}

	ctx := context.Background()
	db := driver_mocks.NewMockDatabase(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q")).Once()
	if sc.Find(ctx, db) {
		t.Fatal("query error")
	}
	cur := driver_mocks.NewMockCursor(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(cur, nil).Once()
	cur.On("Close").Return(nil).Once()
	cur.On("ReadDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("read")).Once()
	if sc.Find(ctx, db) {
		t.Fatal("read error")
	}
	cur = driver_mocks.NewMockCursor(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(cur, nil).Once()
	cur.On("Close").Return(nil).Once()
	cur.On("ReadDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, nil).Once()
	if !sc.Find(ctx, db) {
		t.Fatal("find")
	}

	col := driver_mocks.NewMockCollection(t)
	col.On("ReadDocument", mock.Anything, "k", mock.Anything).Return(driver.DocumentMeta{}, errors.New("x")).Once()
	if sc.FindByKey(ctx, col, "k") == nil {
		t.Fatal("find by key")
	}
	col.On("ReadDocument", mock.Anything, "k", mock.Anything).Return(driver.DocumentMeta{}, nil).Once()
	if err = sc.FindByKey(ctx, col, "k"); err != nil {
		t.Fatal(err)
	}
}

func TestDetermineAndFind(t *testing.T) {
	if _, ok := Determine("nope"); ok {
		t.Fatal("unknown type")
	}
	cred, ok := Determine("standard")
	if !ok || cred.Type() != "standard" {
		t.Fatal("standard")
	}

	ctx := context.Background()
	log := zap.NewNop()
	db := driver_mocks.NewMockDatabase(t)
	if _, err := Find(ctx, db, log, "nope"); err == nil {
		t.Fatal("unknown")
	}

	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q")).Once()
	if _, err := Find(ctx, db, log, "standard", "user", "pw"); err == nil {
		t.Fatal("missing")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	cur := driver_mocks.NewMockCursor(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(cur, nil).Once()
	cur.On("Close").Return(nil).Once()
	cur.On("ReadDocument", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		dst := args.Get(1).(**StandardCredentials)
		(*dst).PasswordHash = string(hash)
	}).Return(driver.DocumentMeta{}, nil).Once()
	if _, err = Find(ctx, db, log, "standard", "user", "no"); err == nil {
		t.Fatal("unauthorized")
	}

	cur = driver_mocks.NewMockCursor(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(cur, nil).Once()
	cur.On("Close").Return(nil).Once()
	cur.On("ReadDocument", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		dst := args.Get(1).(**StandardCredentials)
		(*dst).PasswordHash = string(hash)
	}).Return(driver.DocumentMeta{}, nil).Once()
	got, err := Find(ctx, db, log, "standard", "user", "pw")
	if err != nil || got.Type() != "standard" {
		t.Fatal(err)
	}

	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q")).Once()
	if _, err = Find(ctx, db, log, "ldap", "user", "pw"); err == nil {
		t.Fatal("ldap missing")
	}
}

func TestMakeAndList(t *testing.T) {
	log := zap.NewNop()
	if _, err := MakeCredentials(nil, log); err == nil {
		t.Fatal("nil")
	}
	if _, err := MakeCredentials(&accountspb.Credentials{Type: "nope"}, log); err == nil {
		t.Fatal("type")
	}
	got, err := MakeCredentials(&accountspb.Credentials{Type: "standard", Data: []string{"user", "pw"}}, log)
	if err != nil || got.Type() != "standard" {
		t.Fatal(err)
	}
	if _, err = MakeCredentials(&accountspb.Credentials{Type: "ldap", Data: []string{"user"}}, log); err == nil {
		t.Fatal("ldap key")
	}
	if _, err = MakeCredentials(&accountspb.Credentials{Type: "ldap", Data: []string{"user", "missing"}}, log); err == nil {
		t.Fatal("ldap provider")
	}
	LDAP.Providers = map[string]LDAPProvider{"local": {}}
	t.Cleanup(func() { LDAP.Providers = nil })
	got, err = MakeCredentials(&accountspb.Credentials{Type: "ldap", Data: []string{"user", "local"}}, log)
	if err != nil || got.Type() != "ldap" {
		t.Fatal(err)
	}
	if _, err = MakeCredentials(&accountspb.Credentials{Type: "oauth", Data: []string{"tok"}}, log); err == nil {
		t.Fatal("oauth shape")
	}
	if _, err = MakeCredentials(&accountspb.Credentials{Type: "oauth2-nope", Data: []string{"tok"}}, log); err == nil {
		t.Fatal("oauth provider")
	}

	if _, err = MakeListable(ListCredentialsResponse{Type: "nope"}); err == nil {
		t.Fatal("listable type")
	}
	if _, err = MakeListable(ListCredentialsResponse{Type: "standard", D: map[string]interface{}{}}); err == nil {
		t.Fatal("listable data")
	}
	listed, err := MakeListable(ListCredentialsResponse{Type: "standard", D: map[string]interface{}{"username": "user"}})
	if err != nil || listed.Listable()[0] != "user" {
		t.Fatal(err)
	}

	ctx := context.Background()
	db := driver_mocks.NewMockDatabase(t)
	acc := driver.NewDocumentID("Accounts", "a")
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q")).Once()
	if _, err = ListCredentialsAndEdges(ctx, log, db, acc); err == nil {
		t.Fatal("edges query")
	}
	cur := driver_mocks.NewMockCursor(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(cur, nil).Once()
	cur.On("Close").Return(nil).Once()
	cur.On("ReadDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("read")).Once()
	if _, err = ListCredentialsAndEdges(ctx, log, db, acc); err == nil {
		t.Fatal("edges read")
	}
	cur = driver_mocks.NewMockCursor(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(cur, nil).Once()
	cur.On("Close").Return(nil).Once()
	cur.On("ReadDocument", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		dst := args.Get(1).(*[]string)
		*dst = []string{"n", "e"}
	}).Return(driver.DocumentMeta{}, nil).Once()
	nodes, err := ListCredentialsAndEdges(ctx, log, db, acc)
	if err != nil || len(nodes) != 2 {
		t.Fatal(err, nodes)
	}

	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q")).Once()
	if _, err = ListCredentials(ctx, log, db, acc); err == nil {
		t.Fatal("list query")
	}
	cur = driver_mocks.NewMockCursor(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(cur, nil).Once()
	cur.On("Close").Return(nil).Once()
	cur.On("ReadDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("read")).Once()
	if _, err = ListCredentials(ctx, log, db, acc); err == nil {
		t.Fatal("list read")
	}
	cur = driver_mocks.NewMockCursor(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(cur, nil).Once()
	cur.On("Close").Return(nil).Once()
	cur.On("ReadDocument", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		dst := args.Get(1).(*ListCredentialsResponse)
		dst.Type = "standard"
	}).Return(driver.DocumentMeta{}, nil).Once()
	cur.On("ReadDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, driver.NoMoreDocumentsError{}).Once()
	rows, err := ListCredentials(ctx, log, db, acc)
	if err != nil || len(rows) != 1 || rows[0].Type != "standard" {
		t.Fatal(err, rows)
	}
}

func TestOauthCredentials(t *testing.T) {
	t.Cleanup(func() {
		githubHTTP = http.DefaultClient
		githubAPIBase = "https://api.github.com"
	})

	if _, err := NewOauthCredentials("github", "tok"); err == nil {
		t.Fatal("shape")
	}
	if _, err := NewOauthCredentials("oauth2-nope", "tok"); err == nil {
		t.Fatal("provider")
	}
	githubHTTP = &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("down")
	})}
	if _, err := NewOauthCredentials("oauth2-github", "tok"); err == nil {
		t.Fatal("verifier")
	}
	if _, err := GithubVerifier("tok"); err == nil {
		t.Fatal("do")
	}
	githubHTTP = &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: errBody{}, Header: http.Header{}}, nil
	})}
	if _, err := GithubVerifier("tok"); err == nil {
		t.Fatal("read")
	}
	githubHTTP = &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("not-json")), Header: http.Header{}}, nil
	})}
	if _, err := GithubVerifier("tok"); err == nil {
		t.Fatal("json")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"login":"octo","name":"Octo"}`)
	}))
	defer srv.Close()
	githubHTTP = srv.Client()
	githubAPIBase = srv.URL
	got, err := NewOauthCredentials("oauth2-github", "tok")
	if err != nil || got.Value != "octo" || got.Type() != "oauth2-github" {
		t.Fatal(err, got)
	}
	if strings.Join(got.Listable(), ",") != "oauth2-github,octo" {
		t.Fatal(got.Listable())
	}
	if !got.Authorize("tok") || got.Authorize("other") {
		t.Fatal("authorize match")
	}
	got.Value = "other"
	if got.Authorize("tok") {
		t.Fatal("mismatch")
	}
	got.OauthType = "github"
	if got.Authorize("tok") {
		t.Fatal("bad type")
	}
	got.OauthType = "oauth2-nope"
	if got.Authorize("tok") {
		t.Fatal("bad provider")
	}
	githubHTTP = &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("down")
	})}
	got.OauthType = "oauth2-github"
	got.Value = "octo"
	if got.Authorize("tok") {
		t.Fatal("verifier error")
	}
	got.ID = driver.NewDocumentID("credentials", "oa")
	if got.Key() != "oa" {
		t.Fatal(got.Key())
	}
	got.SetLogger(zap.NewNop())

	ctx := context.Background()
	db := driver_mocks.NewMockDatabase(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q")).Once()
	if got.Find(ctx, db) {
		t.Fatal("query")
	}
	cur := driver_mocks.NewMockCursor(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(cur, nil).Once()
	cur.On("Close").Return(nil).Once()
	cur.On("ReadDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("read")).Once()
	if got.Find(ctx, db) {
		t.Fatal("read")
	}
	cur = driver_mocks.NewMockCursor(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(cur, nil).Once()
	cur.On("Close").Return(nil).Once()
	cur.On("ReadDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, nil).Once()
	if !got.Find(ctx, db) {
		t.Fatal("found")
	}
	col := driver_mocks.NewMockCollection(t)
	col.On("ReadDocument", mock.Anything, "k", mock.Anything).Return(driver.DocumentMeta{}, errors.New("x")).Once()
	if got.FindByKey(ctx, col, "k") == nil {
		t.Fatal("key")
	}
	col.On("ReadDocument", mock.Anything, "k", mock.Anything).Return(driver.DocumentMeta{}, nil).Once()
	if err = got.FindByKey(ctx, col, "k"); err != nil {
		t.Fatal(err)
	}

	log := zap.NewNop()
	githubHTTP = srv.Client()
	made, err := MakeCredentials(&accountspb.Credentials{Type: "oauth2-github", Data: []string{"tok"}}, log)
	if err != nil || made.Type() != "oauth2-github" {
		t.Fatal(err)
	}
}

func TestLDAPCredentials(t *testing.T) {
	saveLDAP(t)
	c := &LDAPCredentials{Username: "user", ProviderKey: "local"}
	if strings.Join(c.Listable(), ",") != "user,local" || c.Type() != "ldap" {
		t.Fatal("meta")
	}
	c.ID = driver.NewDocumentID("credentials", "ld")
	if c.Key() != "ld" {
		t.Fatal(c.Key())
	}
	c.SetLogger(zap.NewNop())
	if c.Authorize("user", "pw") {
		t.Fatal("missing provider")
	}
	if _, err := NewLDAPCredentials("user", "missing"); err == nil {
		t.Fatal("provider")
	}

	if _, err := LDAPFromMap(map[string]interface{}{}); err == nil {
		t.Fatal("user")
	}
	if _, err := LDAPFromMap(map[string]interface{}{"username": 1}); err == nil {
		t.Fatal("user type")
	}
	if _, err := LDAPFromMap(map[string]interface{}{"username": "user"}); err == nil {
		t.Fatal("key")
	}
	if _, err := LDAPFromMap(map[string]interface{}{"username": "user", "key": 1}); err == nil {
		t.Fatal("key type")
	}
	from, err := LDAPFromMap(map[string]interface{}{"username": "user", "key": "local"})
	if err != nil || from.Listable()[1] != "local" {
		t.Fatal(err)
	}

	srv := newLDAPServer(t, ldapScript{entries: 1, bindPassword: "password"})
	conn, err := ldap.DialURL("ldap://" + srv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	LDAP.Providers = map[string]LDAPProvider{"local": {Conn: conn, BaseDN: "dc=example,dc=com", Scope: 2}}
	made, err := NewLDAPCredentials("user", "local")
	if err != nil {
		t.Fatal(err)
	}
	lc := made.(*LDAPCredentials)
	lc.SetLogger(zap.NewNop())
	if !lc.Authorize("user", "password") {
		t.Fatal("bind")
	}
	if lc.Authorize("user", "nope") {
		t.Fatal("bad bind")
	}

	two := newLDAPServer(t, ldapScript{entries: 2, bindPassword: "password"})
	conn2, err := ldap.DialURL("ldap://" + two)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn2.Close() })
	lc.ProviderKey = "two"
	LDAP.Providers["two"] = LDAPProvider{Conn: conn2, BaseDN: "dc=example,dc=com"}
	if !lc.Authorize("user", "password") {
		t.Fatal("two entries")
	}

	bad := newLDAPServer(t, ldapScript{searchCode: 1})
	conn3, err := ldap.DialURL("ldap://" + bad)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn3.Close() })
	lc.ProviderKey = "bad"
	LDAP.Providers["bad"] = LDAPProvider{Conn: conn3}
	if lc.Authorize("user", "password") {
		t.Fatal("search error")
	}

	ctx := context.Background()
	db := driver_mocks.NewMockDatabase(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("q")).Once()
	if lc.Find(ctx, db) {
		t.Fatal("query")
	}
	cur := driver_mocks.NewMockCursor(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(cur, nil).Once()
	cur.On("Close").Return(nil).Once()
	cur.On("ReadDocument", mock.Anything, mock.Anything).Return(driver.DocumentMeta{}, errors.New("read")).Once()
	if lc.Find(ctx, db) {
		t.Fatal("read")
	}
	cur = driver_mocks.NewMockCursor(t)
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(cur, nil).Once()
	cur.On("Close").Return(nil).Once()
	cur.On("ReadDocument", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		dst := args.Get(1).(**LDAPCredentials)
		(*dst).ProviderKey = "local"
	}).Return(driver.DocumentMeta{}, nil).Once()
	if !lc.Find(ctx, db) {
		t.Fatal("found")
	}
	col := driver_mocks.NewMockCollection(t)
	col.On("ReadDocument", mock.Anything, "k", mock.Anything).Return(driver.DocumentMeta{}, errors.New("x")).Once()
	if lc.FindByKey(ctx, col, "k") == nil {
		t.Fatal("key")
	}
	col.On("ReadDocument", mock.Anything, "k", mock.Anything).Return(driver.DocumentMeta{}, nil).Once()
	if err = lc.FindByKey(ctx, col, "k"); err != nil {
		t.Fatal(err)
	}

	log := zap.NewNop()
	db.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(cur, nil)
	cur.On("Close").Return(nil)
	cur.On("ReadDocument", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		dst := args.Get(1).(**LDAPCredentials)
		(*dst).Username = "user"
		(*dst).ProviderKey = "local"
	}).Return(driver.DocumentMeta{}, nil)
	found, err := Find(ctx, db, log, "ldap", "user", "password")
	if err != nil || found.Type() != "ldap" {
		t.Fatal(err)
	}
}

func TestLoadLDAP(t *testing.T) {
	saveLDAP(t)
	LDAP = LDAPConfFile{}
	LDAP_CONFIGURED = false
	viper.Set("LDAP_CONF", "")
	loadLDAP()
	if LDAP_CONFIGURED {
		t.Fatal("empty")
	}

	viper.Set("LDAP_CONF", filepath.Join(t.TempDir(), "missing.yaml"))
	loadLDAP()
	if LDAP_CONFIGURED {
		t.Fatal("missing")
	}

	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("providers: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	LDAP = LDAPConfFile{}
	viper.Set("LDAP_CONF", bad)
	loadLDAP()
	if LDAP_CONFIGURED {
		t.Fatal("yaml")
	}

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := closed.Addr().String()
	closed.Close()
	refused := filepath.Join(t.TempDir(), "refused.yaml")
	if err = os.WriteFile(refused, []byte("providers:\n  a:\n    url: ldap://"+closedAddr+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	LDAP = LDAPConfFile{}
	viper.Set("LDAP_CONF", refused)
	loadLDAP()
	if LDAP_CONFIGURED {
		t.Fatal("dial")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(io.Discard, c); c.Close() }()
		}
	}()
	okPath := filepath.Join(t.TempDir(), "ok.yaml")
	raw := fmt.Sprintf("providers:\n  good:\n    url: ldap://%s\n    base_dn: dc=example,dc=com\n  bad:\n    url: ldap://%s\n", ln.Addr().String(), closedAddr)
	if err = os.WriteFile(okPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	LDAP = LDAPConfFile{}
	LDAP_CONFIGURED = false
	viper.Set("LDAP_CONF", okPath)
	loadLDAP()
	if !LDAP_CONFIGURED {
		t.Fatal("configured")
	}
	if _, still := LDAP.Providers["bad"]; still {
		t.Fatal("bad provider kept")
	}
	if LDAP.Providers["good"].Conn == nil {
		t.Fatal("missing conn")
	}
	closeLDAP(syscall.SIGTERM)
}

func saveLDAP(t *testing.T) {
	t.Helper()
	prevC := LDAP_CONFIGURED
	prev := LDAP
	prevPath := viper.GetString("LDAP_CONF")
	t.Cleanup(func() {
		LDAP_CONFIGURED = prevC
		LDAP = prev
		viper.Set("LDAP_CONF", prevPath)
	})
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type errBody struct{}

func (errBody) Read([]byte) (int, error) { return 0, errors.New("read") }
func (errBody) Close() error             { return nil }

type ldapScript struct {
	entries      int
	searchCode   int
	bindPassword string
}

func newLDAPServer(t *testing.T, script ldapScript) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveLDAP(c, script)
		}
	}()
	return ln.Addr().String()
}

func serveLDAP(c net.Conn, script ldapScript) {
	defer c.Close()
	for {
		_, content, err := readTLV(c)
		if err != nil {
			return
		}
		id, op, opBody, err := parseLDAP(content)
		if err != nil {
			return
		}
		switch op {
		case 0x63:
			if script.searchCode != 0 {
				_, _ = c.Write(ldapMessage(id, ldapResult(0x65, script.searchCode)))
				continue
			}
			n := script.entries
			if n == 0 {
				n = 1
			}
			for i := 0; i < n; i++ {
				_, _ = c.Write(ldapMessage(id, searchEntry("uid=user,dc=example,dc=com")))
			}
			_, _ = c.Write(ldapMessage(id, ldapResult(0x65, 0)))
		case 0x60:
			code := 49
			if simplePassword(opBody) == script.bindPassword {
				code = 0
			}
			_, _ = c.Write(ldapMessage(id, ldapResult(0x61, code)))
		default:
			return
		}
	}
}

func readTLV(r io.Reader) (tag byte, content []byte, err error) {
	hdr := make([]byte, 2)
	if _, err = io.ReadFull(r, hdr); err != nil {
		return
	}
	tag = hdr[0]
	var n int
	if hdr[1]&0x80 == 0 {
		n = int(hdr[1])
	} else {
		lb := make([]byte, int(hdr[1]&0x7f))
		if _, err = io.ReadFull(r, lb); err != nil {
			return
		}
		for _, b := range lb {
			n = n<<8 | int(b)
		}
	}
	content = make([]byte, n)
	_, err = io.ReadFull(r, content)
	return
}

func parseLDAP(content []byte) (id int, op byte, opBody []byte, err error) {
	if len(content) < 2 || content[0] != 0x02 {
		return 0, 0, nil, errors.New("id")
	}
	idLen := int(content[1])
	if idLen <= 0 || 2+idLen > len(content) {
		return 0, 0, nil, errors.New("idlen")
	}
	for _, b := range content[2 : 2+idLen] {
		id = id<<8 | int(b)
	}
	rest := content[2+idLen:]
	if len(rest) < 2 {
		return 0, 0, nil, errors.New("op")
	}
	op = rest[0]
	body, err := tlvBody(rest)
	return id, op, body, err
}

func tlvBody(b []byte) ([]byte, error) {
	if len(b) < 2 {
		return nil, errors.New("short")
	}
	if b[1]&0x80 == 0 {
		n := int(b[1])
		if 2+n > len(b) {
			return nil, errors.New("len")
		}
		return b[2 : 2+n], nil
	}
	nlen := int(b[1] & 0x7f)
	if 2+nlen > len(b) {
		return nil, errors.New("lhdr")
	}
	n := 0
	for _, x := range b[2 : 2+nlen] {
		n = n<<8 | int(x)
	}
	if 2+nlen+n > len(b) {
		return nil, errors.New("body")
	}
	return b[2+nlen : 2+nlen+n], nil
}

func simplePassword(body []byte) string {
	i := 0
	for i+2 <= len(body) {
		tag := body[i]
		n := int(body[i+1])
		if body[i+1]&0x80 != 0 || i+2+n > len(body) {
			return ""
		}
		val := body[i+2 : i+2+n]
		if tag == 0x80 {
			return string(val)
		}
		i += 2 + n
	}
	return ""
}

func berLen(n int) []byte {
	if n < 128 {
		return []byte{byte(n)}
	}
	return []byte{0x81, byte(n)}
}

func tlv(tag byte, content []byte) []byte {
	out := []byte{tag}
	out = append(out, berLen(len(content))...)
	return append(out, content...)
}

func ldapMessage(id int, op []byte) []byte {
	body := append(tlv(0x02, []byte{byte(id)}), op...)
	return tlv(0x30, body)
}

func ldapResult(tag byte, code int) []byte {
	body := append(tlv(0x0a, []byte{byte(code)}), tlv(0x04, nil)...)
	body = append(body, tlv(0x04, nil)...)
	return tlv(tag, body)
}

func searchEntry(dn string) []byte {
	body := append(tlv(0x04, []byte(dn)), tlv(0x30, nil)...)
	return tlv(0x64, body)
}
