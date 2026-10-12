package httpfs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var errNotFound = errors.New("not found")

type Object struct {
	Name        string
	Size        int64
	ModTime     time.Time
	ContentType string
	Body        io.ReadCloser
}

type Store interface {
	Put(ctx context.Context, namespace, name string, r io.Reader, size int64, contentType string) error
	Get(ctx context.Context, namespace, name string) (Object, error)
	Delete(ctx context.Context, namespace, name string) error
	List(ctx context.Context, namespace string) ([]Object, error)
}

func Open(cfg Config) (Store, error) {
	if cfg.S3 != nil {
		return openS3(cfg.S3)
	}
	if cfg.Root == "" {
		return nil, errors.New("HTTP_FS_ROOT is empty")
	}
	if err := os.MkdirAll(cfg.Root, 0o755); err != nil {
		return nil, err
	}
	return &Local{root: cfg.Root}, nil
}

type Local struct {
	root string
}

func (s *Local) Put(_ context.Context, namespace, name string, r io.Reader, _ int64, contentType string) error {
	path, err := s.path(namespace, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".upload-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmpName)
		}
	}()
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true
	if contentType != "" {
		_ = os.WriteFile(path+".type", []byte(contentType), 0o644)
	}
	return nil
}

func (s *Local) Get(_ context.Context, namespace, name string) (Object, error) {
	path, err := s.path(namespace, name)
	if err != nil {
		return Object{}, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Object{}, errNotFound
	}
	if err != nil {
		return Object{}, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return Object{}, err
	}
	ctype, _ := os.ReadFile(path + ".type")
	return Object{
		Name:        name,
		Size:        st.Size(),
		ModTime:     st.ModTime().UTC(),
		ContentType: strings.TrimSpace(string(ctype)),
		Body:        f,
	}, nil
}

func (s *Local) Delete(_ context.Context, namespace, name string) error {
	path, err := s.path(namespace, name)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	_ = os.Remove(path + ".type")
	return nil
}

func (s *Local) List(_ context.Context, namespace string) ([]Object, error) {
	if err := safeSegment(namespace); err != nil {
		return nil, err
	}
	dir := filepath.Join(s.root, namespace)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Object{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Object{}
	for _, ent := range entries {
		if ent.IsDir() || strings.HasPrefix(ent.Name(), ".") || strings.HasSuffix(ent.Name(), ".type") {
			continue
		}
		info, err := ent.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, Object{Name: ent.Name(), Size: info.Size(), ModTime: info.ModTime().UTC()})
	}
	return out, nil
}

func (s *Local) path(namespace, name string) (string, error) {
	if err := safeSegment(namespace); err != nil {
		return "", err
	}
	if err := safeSegment(name); err != nil {
		return "", err
	}
	dir := filepath.Join(s.root, namespace)
	full := filepath.Join(dir, name)
	rel, err := filepath.Rel(dir, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid name")
	}
	return full, nil
}

func safeSegment(v string) error {
	if v == "" || v == "." || v == ".." || len(v) > 200 {
		return errors.New("invalid name")
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return errors.New("invalid name")
		}
	}
	return nil
}
