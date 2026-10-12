package httpfs

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/golang-jwt/jwt/v4"
	"github.com/infinimesh/proto/node/access"
)

const testKey = "seeeecreet"

type grant struct {
	m map[string]access.Level
}

func (g grant) Level(_ context.Context, account, namespace string) (access.Level, error) {
	return g.m[account+"/"+namespace], nil
}

func token(t *testing.T, account string) string {
	t.Helper()
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"account": account}).SignedString([]byte(testKey))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func newServer(t *testing.T, max int64, types string, access Access) *httptest.Server {
	t.Helper()
	allowAll, parsed := parseTypes(types)
	dir := t.TempDir()
	store, err := Open(Config{Root: dir, MaxBytes: max})
	if err != nil {
		t.Fatal(err)
	}
	h := New(store, access, []byte(testKey), Config{Root: dir, MaxBytes: max, AllowAll: allowAll, Types: parsed})
	return httptest.NewServer(h)
}

func upload(t *testing.T, srv *httptest.Server, path, bearer, filename string, body []byte, contentType string) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
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
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestUpload(t *testing.T) {
	srv := newServer(t, 1024, "text/plain,image/png", grant{m: map[string]access.Level{"acc/ns": access.Level_MGMT}})
	defer srv.Close()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00}
	res := upload(t, srv, "/ns/photo.png", token(t, "acc"), "photo.png", png, "image/png")
	if res.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("upload %d %s", res.StatusCode, b)
	}
	res.Body.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/ns", nil)
	req.Header.Set("Authorization", "Bearer "+token(t, "acc"))
	list, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer list.Body.Close()
	var body struct {
		Files []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"files"`
		FileLimit int64 `json:"file_limit"`
	}
	if err := json.NewDecoder(list.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.FileLimit != 1024 || len(body.Files) != 1 || body.Files[0].Name != "photo.png" || body.Files[0].Size != int64(len(png)) {
		t.Fatalf("list %+v", body)
	}

	got, err := srv.Client().Get(srv.URL + "/ns/photo.png")
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	if got.StatusCode != http.StatusOK {
		t.Fatalf("get %d", got.StatusCode)
	}
	raw, _ := io.ReadAll(got.Body)
	if !bytes.Equal(raw, png) {
		t.Fatalf("body %q", raw)
	}

	del, _ := http.NewRequest(http.MethodDelete, srv.URL+"/ns/photo.png", nil)
	del.Header.Set("Authorization", "Bearer "+token(t, "acc"))
	deleted, err := srv.Client().Do(del)
	if err != nil {
		t.Fatal(err)
	}
	deleted.Body.Close()
	if deleted.StatusCode != http.StatusOK {
		t.Fatalf("delete %d", deleted.StatusCode)
	}
	missing, err := srv.Client().Get(srv.URL + "/ns/photo.png")
	if err != nil {
		t.Fatal(err)
	}
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("after delete %d", missing.StatusCode)
	}
}

func TestUploadAuth(t *testing.T) {
	srv := newServer(t, 1024, "text/plain", grant{m: map[string]access.Level{
		"reader/ns": access.Level_READ,
		"none/ns":   access.Level_NONE,
	}})
	defer srv.Close()
	body := []byte("hello")

	noAuth := upload(t, srv, "/ns/a.txt", "", "a.txt", body, "text/plain")
	noAuth.Body.Close()
	if noAuth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token %d", noAuth.StatusCode)
	}

	bad := upload(t, srv, "/ns/a.txt", "not-a-token", "a.txt", body, "text/plain")
	bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token %d", bad.StatusCode)
	}

	read := upload(t, srv, "/ns/a.txt", token(t, "reader"), "a.txt", body, "text/plain")
	read.Body.Close()
	if read.StatusCode != http.StatusForbidden {
		t.Fatalf("read %d", read.StatusCode)
	}

	none := upload(t, srv, "/ns/a.txt", token(t, "none"), "a.txt", body, "text/plain")
	none.Body.Close()
	if none.StatusCode != http.StatusForbidden {
		t.Fatalf("none %d", none.StatusCode)
	}

	if _, err := os.Stat(filepath.Join(srv.Config.Handler.(*Handler).cfg.Root, "ns", "a.txt")); !os.IsNotExist(err) {
		t.Fatal("rejected upload was stored")
	}
}

func TestUploadSizeLimit(t *testing.T) {
	srv := newServer(t, 4, "text/plain", grant{m: map[string]access.Level{"acc/ns": access.Level_ADMIN}})
	defer srv.Close()

	over := upload(t, srv, "/ns/a.txt", token(t, "acc"), "a.txt", []byte("hello"), "text/plain")
	defer over.Body.Close()
	if over.StatusCode != http.StatusRequestEntityTooLarge {
		b, _ := io.ReadAll(over.Body)
		t.Fatalf("over %d %s", over.StatusCode, b)
	}
	var msg struct {
		Message string `json:"message"`
	}
	_ = json.NewDecoder(over.Body).Decode(&msg)
	if msg.Message == "" {
		t.Fatal("size error has no message")
	}

	ok := upload(t, srv, "/ns/a.txt", token(t, "acc"), "a.txt", []byte("hell"), "text/plain")
	ok.Body.Close()
	if ok.StatusCode != http.StatusCreated {
		t.Fatalf("exact %d", ok.StatusCode)
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("HTTP_FS_ROOT", "/tmp/http-fs-test")
	t.Setenv("HTTP_FS_MAX_BYTES", "32")
	t.Setenv("HTTP_FS_TYPES", "image/png")
	t.Setenv("HTTP_FS_S3_ENDPOINT", "")
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxBytes != 32 || cfg.S3 != nil || !cfg.allows("image/png") || cfg.allows("text/plain") {
		t.Fatalf("%+v", cfg)
	}

	t.Setenv("HTTP_FS_S3_ENDPOINT", "http://minio:9000")
	t.Setenv("HTTP_FS_S3_BUCKET", "")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("missing bucket")
	}
	t.Setenv("HTTP_FS_S3_BUCKET", "media")
	t.Setenv("HTTP_FS_S3_PATH_STYLE", "0")
	cfg, err = ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.S3 == nil || cfg.S3.Bucket != "media" || cfg.S3.PathStyle {
		t.Fatalf("%+v", cfg.S3)
	}
	host, secure := splitEndpoint(cfg.S3.Endpoint)
	if host != "minio:9000" || secure {
		t.Fatalf("endpoint %s %v", host, secure)
	}
}
