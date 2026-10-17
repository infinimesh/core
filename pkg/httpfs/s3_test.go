package httpfs

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func s3Resp(code int, body string, hdr http.Header) *http.Response {
	if hdr == nil {
		hdr = http.Header{}
	}
	return &http.Response{
		StatusCode:    code,
		Status:        http.StatusText(code),
		Header:        hdr,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

func s3Transport(mode string) roundTrip {
	return func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			_, _ = io.Copy(io.Discard, r.Body)
			r.Body.Close()
		}
		if mode == "net" {
			return nil, errors.New("net")
		}
		q := r.URL.Query()
		if q.Has("location") {
			return s3Resp(http.StatusOK, `<LocationConstraint>us-east-1</LocationConstraint>`, nil), nil
		}
		if q.Has("list-type") || q.Get("prefix") != "" {
			if mode == "list-err" {
				return s3Resp(http.StatusInternalServerError, `<Error><Code>InternalError</Code><Message>boom</Message></Error>`, nil), nil
			}
			body := `<ListBucketResult><IsTruncated>false</IsTruncated>` +
				`<Contents><Key>ns/a.txt</Key><Size>2</Size><LastModified>2020-01-02T03:04:05.000Z</LastModified></Contents>` +
				`<Contents><Key>ns/dir/b</Key><Size>1</Size><LastModified>2020-01-02T03:04:05.000Z</LastModified></Contents>` +
				`<Contents><Key>ns/</Key><Size>0</Size><LastModified>2020-01-02T03:04:05.000Z</LastModified></Contents>` +
				`</ListBucketResult>`
			return s3Resp(http.StatusOK, body, nil), nil
		}
		if mode == "missing" {
			return s3Resp(http.StatusNotFound, "", nil), nil
		}
		if mode == "boom" {
			return s3Resp(http.StatusInternalServerError, `<Error><Code>InternalError</Code><Message>boom</Message></Error>`, nil), nil
		}
		h := http.Header{}
		h.Set("Content-Type", "text/plain")
		h.Set("Last-Modified", "Thu, 02 Jan 2020 03:04:05 GMT")
		h.Set("ETag", `"abc"`)
		h.Set("Content-Length", "2")
		if r.Method == http.MethodGet {
			return s3Resp(http.StatusOK, "hi", h), nil
		}
		if r.Method == http.MethodDelete {
			return s3Resp(http.StatusNoContent, "", h), nil
		}
		return s3Resp(http.StatusOK, "", h), nil
	}
}

func TestS3Store(t *testing.T) {
	ctx := context.Background()
	if _, err := openS3(&S3Config{Endpoint: "http://["}); err == nil {
		t.Fatal("bad endpoint")
	}
	dns, err := openS3(&S3Config{Endpoint: "https://s3.example.com", Bucket: "media", Region: "eu-central-1", PathStyle: false})
	if err != nil {
		t.Fatal(err)
	}
	if err := dns.Put(ctx, "..", "a", strings.NewReader("x"), 1, "text/plain"); err == nil {
		t.Fatal("bad namespace")
	}

	st, err := openS3(&S3Config{
		Endpoint: "http://127.0.0.1:9", Bucket: "media", Region: "us-east-1",
		AccessKey: "minio", SecretKey: "minio123", PathStyle: true, Transport: s3Transport(""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Put(ctx, "ns", "a.txt", strings.NewReader("hi"), 2, "text/plain"); err != nil {
		t.Fatal(err)
	}
	if err := st.Put(ctx, "ns", "a/b", strings.NewReader("hi"), 2, "text/plain"); err == nil {
		t.Fatal("slash name")
	}
	obj, err := st.Get(ctx, "ns", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(obj.Body)
	obj.Body.Close()
	if err != nil || string(b) != "hi" || obj.ContentType != "text/plain" || obj.Name != "a.txt" {
		t.Fatalf("get %+v %q %v", obj, b, err)
	}
	if _, err := st.Get(ctx, "", "a"); err == nil {
		t.Fatal("empty ns")
	}
	if err := st.Delete(ctx, "ns", "a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(ctx, "bad name", "a"); err == nil {
		t.Fatal("bad delete name")
	}
	list, err := st.List(ctx, "ns")
	if err != nil || len(list) != 1 || list[0].Name != "a.txt" {
		t.Fatalf("list %+v %v", list, err)
	}
	if _, err := st.List(ctx, ".."); err == nil {
		t.Fatal("bad list")
	}

	missing, err := openS3(&S3Config{
		Endpoint: "http://127.0.0.1:9", Bucket: "media", Region: "us-east-1",
		AccessKey: "minio", SecretKey: "minio123", Transport: s3Transport("missing"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := missing.Get(ctx, "ns", "a.txt"); !errors.Is(err, errNotFound) {
		t.Fatalf("get missing %v", err)
	}
	if err := missing.Delete(ctx, "ns", "a.txt"); !errors.Is(err, errNotFound) {
		t.Fatalf("delete missing %v", err)
	}

	boom, err := openS3(&S3Config{
		Endpoint: "http://127.0.0.1:9", Bucket: "media", Region: "us-east-1",
		AccessKey: "minio", SecretKey: "minio123", Transport: s3Transport("boom"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := boom.Get(ctx, "ns", "a.txt"); err == nil || errors.Is(err, errNotFound) {
		t.Fatalf("get boom %v", err)
	}
	if err := boom.Delete(ctx, "ns", "a.txt"); err == nil || errors.Is(err, errNotFound) {
		t.Fatalf("delete boom %v", err)
	}
	if err := boom.Put(ctx, "ns", "a.txt", strings.NewReader("hi"), 2, "text/plain"); err == nil {
		t.Fatal("put boom")
	}

	listed, err := openS3(&S3Config{
		Endpoint: "http://127.0.0.1:9", Bucket: "media", Region: "us-east-1",
		AccessKey: "minio", SecretKey: "minio123", Transport: s3Transport("list-err"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := listed.List(ctx, "ns"); err == nil {
		t.Fatal("list err")
	}

	down, err := openS3(&S3Config{
		Endpoint: "http://127.0.0.1:9", Bucket: "media", Region: "us-east-1",
		AccessKey: "minio", SecretKey: "minio123", Transport: s3Transport("net"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := down.Put(ctx, "ns", "a.txt", strings.NewReader("hi"), 2, "text/plain"); err == nil {
		t.Fatal("put net")
	}
}

func TestSplitEndpoint(t *testing.T) {
	host, secure := splitEndpoint("https://s3.example.com/")
	if host != "s3.example.com" || !secure {
		t.Fatalf("%s %v", host, secure)
	}
	host, secure = splitEndpoint("minio:9000")
	if host != "minio:9000" || !secure {
		t.Fatalf("%s %v", host, secure)
	}
}
