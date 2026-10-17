package httpfs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestLocalStoreEdges(t *testing.T) {
	ctx := context.Background()
	if _, err := Open(Config{}); err == nil {
		t.Fatal("empty root")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Config{Root: file}); err == nil {
		t.Fatal("root is a file")
	}

	root := t.TempDir()
	st, err := Open(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	local := st.(*Local)
	if err := local.Put(ctx, "..", "a", strings.NewReader("x"), 1, "text/plain"); err == nil {
		t.Fatal("bad ns")
	}
	if err := local.Put(ctx, "ns", "a b", strings.NewReader("x"), 1, ""); err == nil {
		t.Fatal("bad name")
	}
	if err := local.Put(ctx, "ns", "a", errReader{errors.New("read")}, 1, "text/plain"); err == nil {
		t.Fatal("read")
	}
	if err := os.WriteFile(filepath.Join(root, "blocked"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := local.Put(ctx, "blocked", "a", strings.NewReader("x"), 1, "text/plain"); err == nil {
		t.Fatal("mkdir")
	}
	ns := filepath.Join(root, "ns")
	if err := os.MkdirAll(ns, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(ns, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := local.Put(ctx, "ns", "dir", strings.NewReader("x"), 1, "text/plain"); err == nil {
		t.Fatal("rename onto dir")
	}
	if _, err := local.Get(ctx, "ns", ".."); err == nil {
		t.Fatal("get name")
	}
	if _, err := local.Get(ctx, "ns", "missing"); !errors.Is(err, errNotFound) {
		t.Fatalf("missing %v", err)
	}
	if _, err := local.Get(ctx, "ns", "dir"); err != nil {
		t.Fatal(err)
	}
	if err := local.Delete(ctx, "nope", "a"); !errors.Is(err, errNotFound) {
		t.Fatalf("delete missing %v", err)
	}
	if err := os.WriteFile(filepath.Join(ns, "dir", "keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := local.Delete(ctx, "ns", "dir"); err == nil {
		t.Fatal("delete dir")
	}
	if err := local.Delete(ctx, "", "a"); err == nil {
		t.Fatal("delete name")
	}
	if _, err := local.List(ctx, "."); err == nil {
		t.Fatal("list dot")
	}
	if got, err := local.List(ctx, "absent"); err != nil || len(got) != 0 {
		t.Fatalf("absent %+v %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(root, "notdir"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := local.List(ctx, "notdir"); err == nil {
		t.Fatal("list file")
	}
	if err := os.Mkdir(filepath.Join(ns, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ns, ".hidden"), []byte("h"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ns, "note.type"), []byte("text/plain"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := local.Put(ctx, "ns", "note", strings.NewReader("hello"), 5, "text/plain"); err != nil {
		t.Fatal(err)
	}
	obj, err := local.Get(ctx, "ns", "note")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(obj.Body)
	obj.Body.Close()
	if string(b) != "hello" || obj.ContentType != "text/plain" {
		t.Fatalf("%+v %q", obj, b)
	}
	listed, err := local.List(ctx, "ns")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range listed {
		if item.Name == ".hidden" || strings.HasSuffix(item.Name, ".type") || item.Name == "sub" {
			t.Fatalf("listed %s", item.Name)
		}
	}
	if err := local.Delete(ctx, "ns", "note"); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("a", 201)
	if err := safeSegment(long); err == nil {
		t.Fatal("long")
	}
	if _, err := local.path("ns", "note"); err != nil {
		t.Fatal(err)
	}
}

func TestConfigEdges(t *testing.T) {
	t.Setenv("HTTP_FS_ROOT", "")
	t.Setenv("HTTP_FS_MAX_BYTES", "")
	t.Setenv("HTTP_FS_TYPES", "")
	t.Setenv("HTTP_FS_S3_ENDPOINT", "")
	t.Setenv("HTTP_FS_S3_REGION", "")
	t.Setenv("HTTP_FS_S3_ACCESS_KEY", "")
	t.Setenv("HTTP_FS_S3_SECRET_KEY", "")
	cfg, err := ConfigFromEnv()
	if err != nil || cfg.Root != "/static" || cfg.MaxBytes != DefaultMaxBytes || cfg.allows("text/plain; charset=utf-8") == false {
		t.Fatalf("%+v %v", cfg, err)
	}
	t.Setenv("HTTP_FS_MAX_BYTES", "nope")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("bad max")
	}
	t.Setenv("HTTP_FS_MAX_BYTES", "0")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("zero max")
	}
	t.Setenv("HTTP_FS_MAX_BYTES", "  ")
	if _, err := parseMax("  "); err == nil {
		t.Fatal("empty max")
	}
	t.Setenv("HTTP_FS_TYPES", " image/png, , * ")
	allow, types := parseTypes(" image/png, , * ")
	if !allow || types != nil {
		t.Fatalf("%v %+v", allow, types)
	}
	if !pathStyle("") || !pathStyle("yes") || pathStyle("false") || pathStyle("off") || pathStyle("F") || pathStyle("n") {
		t.Fatal("path style")
	}
	t.Setenv("HTTP_FS_MAX_BYTES", "8")
	t.Setenv("HTTP_FS_TYPES", "*")
	t.Setenv("HTTP_FS_S3_ENDPOINT", " https://s3.example.com ")
	t.Setenv("HTTP_FS_S3_BUCKET", "media")
	t.Setenv("HTTP_FS_S3_REGION", "eu")
	t.Setenv("HTTP_FS_S3_ACCESS_KEY", "ak")
	t.Setenv("HTTP_FS_S3_SECRET_KEY", "sk")
	t.Setenv("HTTP_FS_S3_PATH_STYLE", "no")
	cfg, err = ConfigFromEnv()
	if err != nil || cfg.S3 == nil || cfg.S3.Region != "eu" || cfg.S3.PathStyle || cfg.S3.AccessKey != "ak" || !cfg.AllowAll {
		t.Fatalf("%+v %v", cfg, err)
	}
	if !cfg.allows("anything") {
		t.Fatal("allow all")
	}
}
