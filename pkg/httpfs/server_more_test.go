package httpfs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v4"
	"github.com/infinimesh/proto/node/access"
)

type fakeStore struct {
	putErr, getErr, delErr, listErr error
	obj                             Object
	list                            []Object
}

func (f fakeStore) Put(context.Context, string, string, io.Reader, int64, string) error {
	return f.putErr
}
func (f fakeStore) Get(context.Context, string, string) (Object, error) {
	return f.obj, f.getErr
}
func (f fakeStore) Delete(context.Context, string, string) error { return f.delErr }
func (f fakeStore) List(context.Context, string) ([]Object, error) {
	return f.list, f.listErr
}

type errAccess struct{}

func (errAccess) Level(context.Context, string, string) (access.Level, error) {
	return 0, errors.New("down")
}

func TestServerEdges(t *testing.T) {
	accessOK := grant{m: map[string]access.Level{"acc/ns": access.Level_ADMIN}}
	cfg := Config{MaxBytes: 8, Types: map[string]struct{}{"text/plain": {}}}
	h := New(fakeStore{}, accessOK, []byte(testKey), cfg)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodOptions, "/ns/a", nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("options %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("health %d", rr.Code)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/ns", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, listReq)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("list auth %d", rr.Code)
	}
	listReq.Header.Set("Authorization", "Bearer "+token(t, "acc"))
	h = New(fakeStore{listErr: errors.New("invalid name")}, accessOK, []byte(testKey), cfg)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, listReq)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("list name %d %s", rr.Code, rr.Body)
	}
	h = New(fakeStore{listErr: errors.New("disk")}, accessOK, []byte(testKey), cfg)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, listReq)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("list disk %d", rr.Code)
	}
	h = New(fakeStore{listErr: errNotFound}, accessOK, []byte(testKey), cfg)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, listReq)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("list missing %d", rr.Code)
	}

	body := io.NopCloser(strings.NewReader("hello"))
	h = New(fakeStore{obj: Object{Name: "a.txt", Size: 5, ContentType: "text/plain", Body: body}}, accessOK, []byte(testKey), cfg)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/ns/a.txt", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "hello" || rr.Header().Get("Content-Type") != "text/plain" {
		t.Fatalf("get %d %s", rr.Code, rr.Body)
	}
	badBody := io.NopCloser(errReader{errors.New("read")})
	h = New(fakeStore{obj: Object{Name: "a.txt", Size: 5, Body: badBody}}, accessOK, []byte(testKey), cfg)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/ns/a.txt", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("bad body %d", rr.Code)
	}

	h = New(fakeStore{}, errAccess{}, []byte(testKey), cfg)
	del := httptest.NewRequest(http.MethodDelete, "/ns/a.txt", nil)
	del.Header.Set("Authorization", "Bearer "+token(t, "acc"))
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, del)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("access %d", rr.Code)
	}
	h = New(fakeStore{delErr: errNotFound}, accessOK, []byte(testKey), cfg)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, del)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("del %d", rr.Code)
	}

	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{}).SignedString([]byte(testKey))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accountFromBearer([]byte(testKey), "Bearer "+raw); err == nil {
		t.Fatal("no account")
	}
	if _, err := accountFromBearer([]byte(testKey), "Token x"); err == nil {
		t.Fatal("scheme")
	}
	none, err := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"account": "acc"}).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accountFromBearer([]byte(testKey), "Bearer "+none); err == nil {
		t.Fatal("alg")
	}

	up := New(fakeStore{putErr: errors.New("disk")}, accessOK, []byte(testKey), cfg)
	res := uploadHandler(t, up, "/ns/a.txt", token(t, "acc"), "a.txt", []byte("hi"), "text/plain")
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("put %d %s", res.Code, res.Body)
	}
	badName := uploadHandler(t, up, "/ns/a$b", token(t, "acc"), "a", []byte("hi"), "text/plain")
	if badName.Code != http.StatusBadRequest {
		t.Fatalf("name %d %s", badName.Code, badName.Body)
	}
	plain := httptest.NewRequest(http.MethodPost, "/ns/a.txt", strings.NewReader("nope"))
	plain.Header.Set("Authorization", "Bearer "+token(t, "acc"))
	rr = httptest.NewRecorder()
	up.ServeHTTP(rr, plain)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("form %d %s", rr.Code, rr.Body)
	}
	noFile := uploadHandler(t, up, "/ns/a.txt", token(t, "acc"), "", nil, "")
	if noFile.Code != http.StatusBadRequest {
		t.Fatalf("nofile %d", noFile.Code)
	}
	big := uploadHandler(t, up, "/ns/a.txt", token(t, "acc"), "a.txt", bytes.Repeat([]byte("a"), 20), "text/plain")
	if big.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("big %d %s", big.Code, big.Body)
	}
	octet := uploadHandler(t, up, "/ns/a.txt", token(t, "acc"), "a.txt", []byte("hi"), "application/octet-stream")
	if octet.Code != http.StatusUnsupportedMediaType && octet.Code != http.StatusCreated && octet.Code != http.StatusInternalServerError {
		t.Fatalf("octet %d %s", octet.Code, octet.Body)
	}
	denied := uploadHandler(t, New(fakeStore{}, accessOK, []byte(testKey), cfg), "/ns/a.txt", token(t, "acc"), "a.txt", []byte{0xff, 0xfe, 0x00, 0x01}, "application/zip")
	if denied.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("type %d %s", denied.Code, denied.Body)
	}
}

func uploadHandler(t *testing.T, h http.Handler, path, bearer, filename string, body []byte, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if filename != "" {
		part, err := w.CreatePart(map[string][]string{
			"Content-Disposition": {`form-data; name="file"; filename="` + filename + `"`},
			"Content-Type":        {contentType},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}
