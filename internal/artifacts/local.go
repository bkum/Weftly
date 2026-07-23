package artifacts

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/bkum/weftly/internal/workspace"
)

// Local stores artifacts under a rooted directory on disk. Every key is
// resolved through workspace.SafeJoin so a hostile key can't escape the
// root — matches the guards the upload action already uses.
type Local struct {
	Root string
}

// NewLocal returns a Store backed by the given directory. The dir is
// created on first Put.
func NewLocal(root string) *Local { return &Local{Root: root} }

func (l *Local) resolve(key string) (string, error) {
	return workspace.SafeJoin(l.Root, key)
}

func (l *Local) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	path, err := l.resolve(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return err
	}
	return f.Sync()
}

func (l *Local) Get(_ context.Context, key string) (io.ReadCloser, int64, error) {
	path, err := l.resolve(key)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, fi.Size(), nil
}

func (l *Local) Stat(_ context.Context, key string) (ObjectInfo, error) {
	path, err := l.resolve(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ObjectInfo{}, ErrNotFound
		}
		return ObjectInfo{}, err
	}
	return ObjectInfo{Key: key, Size: fi.Size(), LastModified: fi.ModTime()}, nil
}

func (l *Local) List(_ context.Context, prefix string) ([]ObjectInfo, error) {
	base, err := l.resolve(prefix)
	if err != nil {
		// Empty prefix should list from Root.
		if prefix == "" {
			base = l.Root
		} else {
			return nil, err
		}
	}
	var out []ObjectInfo
	err = filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(l.Root, path)
		if rerr != nil {
			return rerr
		}
		out = append(out, ObjectInfo{Key: filepath.ToSlash(rel), Size: info.Size(), LastModified: info.ModTime()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
