package httpfs

import (
	"context"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type s3Store struct {
	client *minio.Client
	bucket string
}

func openS3(cfg *S3Config) (Store, error) {
	endpoint, secure := splitEndpoint(cfg.Endpoint)
	lookup := minio.BucketLookupPath
	if !cfg.PathStyle {
		lookup = minio.BucketLookupDNS
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:       secure,
		Region:       cfg.Region,
		BucketLookup: lookup,
	})
	if err != nil {
		return nil, err
	}
	return &s3Store{client: client, bucket: cfg.Bucket}, nil
}

func splitEndpoint(raw string) (string, bool) {
	secure := true
	switch {
	case strings.HasPrefix(raw, "http://"):
		secure = false
		raw = strings.TrimPrefix(raw, "http://")
	case strings.HasPrefix(raw, "https://"):
		raw = strings.TrimPrefix(raw, "https://")
	}
	return strings.TrimRight(raw, "/"), secure
}

func (s *s3Store) key(namespace, name string) (string, error) {
	if err := safeSegment(namespace); err != nil {
		return "", err
	}
	if err := safeSegment(name); err != nil {
		return "", err
	}
	return namespace + "/" + name, nil
}

func (s *s3Store) Put(ctx context.Context, namespace, name string, r io.Reader, size int64, contentType string) error {
	key, err := s.key(namespace, name)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	return err
}

func (s *s3Store) Get(ctx context.Context, namespace, name string) (Object, error) {
	key, err := s.key(namespace, name)
	if err != nil {
		return Object{}, err
	}
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return Object{}, err
	}
	info, err := obj.Stat()
	if err != nil {
		obj.Close()
		if isMissing(err) {
			return Object{}, errNotFound
		}
		return Object{}, err
	}
	return Object{
		Name:        name,
		Size:        info.Size,
		ModTime:     info.LastModified.UTC(),
		ContentType: info.ContentType,
		Body:        obj,
	}, nil
}

func (s *s3Store) Delete(ctx context.Context, namespace, name string) error {
	key, err := s.key(namespace, name)
	if err != nil {
		return err
	}
	_, err = s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if isMissing(err) {
			return errNotFound
		}
		return err
	}
	return s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}

func (s *s3Store) List(ctx context.Context, namespace string) ([]Object, error) {
	if err := safeSegment(namespace); err != nil {
		return nil, err
	}
	prefix := namespace + "/"
	ch := s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true})
	out := []Object{}
	for obj := range ch {
		if obj.Err != nil {
			return nil, obj.Err
		}
		name := strings.TrimPrefix(obj.Key, prefix)
		if name == "" || strings.Contains(name, "/") {
			continue
		}
		out = append(out, Object{Name: name, Size: obj.Size, ModTime: obj.LastModified.UTC()})
	}
	return out, nil
}

func isMissing(err error) bool {
	resp := minio.ToErrorResponse(err)
	return resp.Code == "NoSuchKey" || resp.StatusCode == 404
}
