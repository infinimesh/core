package httpfs

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
)

const (
	DefaultMaxBytes int64 = 10 << 20
	DefaultTypes          = "image/jpeg,image/png,image/gif,image/webp,application/pdf,text/plain,text/csv,application/json,application/zip,application/octet-stream"
)

type Config struct {
	Root     string
	MaxBytes int64
	AllowAll bool
	Types    map[string]struct{}
	S3       *S3Config
}

type S3Config struct {
	Endpoint  string
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
	PathStyle bool
	Transport http.RoundTripper
}

func ConfigFromEnv() (Config, error) {
	root := os.Getenv("HTTP_FS_ROOT")
	if root == "" {
		root = "/static"
	}
	rawMax := os.Getenv("HTTP_FS_MAX_BYTES")
	if rawMax == "" {
		rawMax = strconv.FormatInt(DefaultMaxBytes, 10)
	}
	max, err := parseMax(rawMax)
	if err != nil {
		return Config{}, err
	}
	rawTypes := os.Getenv("HTTP_FS_TYPES")
	if rawTypes == "" {
		rawTypes = DefaultTypes
	}
	allowAll, types := parseTypes(rawTypes)
	cfg := Config{Root: root, MaxBytes: max, AllowAll: allowAll, Types: types}
	endpoint := strings.TrimSpace(os.Getenv("HTTP_FS_S3_ENDPOINT"))
	if endpoint == "" {
		return cfg, nil
	}
	bucket := strings.TrimSpace(os.Getenv("HTTP_FS_S3_BUCKET"))
	if bucket == "" {
		return Config{}, fmt.Errorf("HTTP_FS_S3_BUCKET is required when HTTP_FS_S3_ENDPOINT is set")
	}
	region := os.Getenv("HTTP_FS_S3_REGION")
	if region == "" {
		region = "us-east-1"
	}
	cfg.S3 = &S3Config{
		Endpoint:  endpoint,
		Bucket:    bucket,
		Region:    region,
		AccessKey: os.Getenv("HTTP_FS_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("HTTP_FS_S3_SECRET_KEY"),
		PathStyle: pathStyle(os.Getenv("HTTP_FS_S3_PATH_STYLE")),
	}
	return cfg, nil
}

func parseMax(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("HTTP_FS_MAX_BYTES is empty")
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("HTTP_FS_MAX_BYTES: %w", err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("HTTP_FS_MAX_BYTES must be positive")
	}
	return n, nil
}

func parseTypes(raw string) (bool, map[string]struct{}) {
	out := map[string]struct{}{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "" {
			continue
		}
		if part == "*" {
			return true, nil
		}
		out[part] = struct{}{}
	}
	return false, out
}

func pathStyle(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "false", "f", "no", "n", "off":
		return false
	default:
		return true
	}
}

func (c Config) allows(contentType string) bool {
	if c.AllowAll {
		return true
	}
	media := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	_, ok := c.Types[media]
	return ok
}
