package artifacts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Config bundles the connection details for an S3-compatible endpoint.
// Endpoint is the hostname without scheme (`s3.amazonaws.com`,
// `minio.internal:9000`, `nyc3.digitaloceanspaces.com`). UseSSL toggles
// https vs http on that endpoint; use false only when talking to a
// local MinIO in development.
type S3Config struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	UseSSL    bool
	// KeyPrefix optionally namespaces every stored key. Useful when the
	// bucket is shared across environments.
	KeyPrefix string
}

// NewS3 constructs an S3 store. It does not perform network I/O — a
// bucket existence check happens on first Put/List to keep New cheap.
func NewS3(cfg S3Config) (*S3, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, errors.New("s3: endpoint and bucket are required")
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("s3: access key and secret key are required")
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3: %w", err)
	}
	return &S3{client: client, cfg: cfg}, nil
}

// S3 stores artifacts in an S3-compatible bucket.
type S3 struct {
	client *minio.Client
	cfg    S3Config
}

func (s *S3) key(k string) string {
	if s.cfg.KeyPrefix == "" {
		return k
	}
	return strings.TrimSuffix(s.cfg.KeyPrefix, "/") + "/" + strings.TrimPrefix(k, "/")
}

func (s *S3) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	if size < 0 {
		// minio-go needs a size for streaming multipart; buffer to memory
		// as a fallback. Artifacts are typically kilobytes, not gigs.
		buf, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		size = int64(len(buf))
		r = bytes.NewReader(buf)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	_, err := s.client.PutObject(ctx, s.cfg.Bucket, s.key(key), r, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	return nil
}

func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	obj, err := s.client.GetObject(ctx, s.cfg.Bucket, s.key(key), minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, err
	}
	// GetObject is lazy — verify existence via Stat and surface a clean
	// ErrNotFound so the server can 404 rather than 500.
	stat, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		if isNotFound(err) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, err
	}
	return obj, stat.Size, nil
}

func (s *S3) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	info, err := s.client.StatObject(ctx, s.cfg.Bucket, s.key(key), minio.StatObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return ObjectInfo{}, ErrNotFound
		}
		return ObjectInfo{}, err
	}
	return ObjectInfo{Key: key, Size: info.Size, LastModified: info.LastModified}, nil
}

func (s *S3) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	var out []ObjectInfo
	ch := s.client.ListObjects(ctx, s.cfg.Bucket, minio.ListObjectsOptions{
		Prefix:    s.key(prefix),
		Recursive: true,
	})
	for obj := range ch {
		if obj.Err != nil {
			return nil, obj.Err
		}
		key := obj.Key
		if s.cfg.KeyPrefix != "" {
			key = strings.TrimPrefix(key, strings.TrimSuffix(s.cfg.KeyPrefix, "/")+"/")
		}
		out = append(out, ObjectInfo{Key: key, Size: obj.Size, LastModified: obj.LastModified})
	}
	return out, nil
}

// isNotFound recognises the "NoSuchKey" error class minio-go returns.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var mer minio.ErrorResponse
	if errors.As(err, &mer) {
		return mer.Code == "NoSuchKey" || mer.StatusCode == 404
	}
	// minio-go sometimes returns unwrapped structs
	s := err.Error()
	return strings.Contains(s, "NoSuchKey") || strings.Contains(s, "does not exist")
}
