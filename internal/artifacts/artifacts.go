// Package artifacts abstracts artifact storage so the same upload +
// download paths work against the local filesystem (default) or a
// remote S3-compatible bucket (MinIO, AWS, DO Spaces, Backblaze,
// Cloudflare R2, GCS with S3 interop). Actions and the server always
// call the interface — never touch a store directly.
package artifacts

import (
	"context"
	"errors"
	"io"
	"time"
)

// ObjectInfo describes a stored artifact for listing/HEAD purposes.
type ObjectInfo struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// ErrNotFound is returned by Get when the key is absent. Callers should
// treat this the same as an on-disk "no such file" and 404 in HTTP land.
var ErrNotFound = errors.New("artifact not found")

// Store is the abstract artifact backend. All keys are "run-relative"
// (e.g. "20260723T063210Z-xxxx/artifacts/report.html") so implementations
// can prefix or namespace as they like.
type Store interface {
	// Put writes r to the given key, replacing any existing content.
	// size may be -1 if unknown; implementations that need a known size
	// (S3 for large uploads) will buffer or stream.
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Get returns a reader for the key plus the object's size, or
	// ErrNotFound if absent. The caller closes the reader.
	Get(ctx context.Context, key string) (io.ReadCloser, int64, error)
	// Stat returns metadata without opening the object body.
	Stat(ctx context.Context, key string) (ObjectInfo, error)
	// List returns every object whose key begins with prefix.
	List(ctx context.Context, prefix string) ([]ObjectInfo, error)
}
