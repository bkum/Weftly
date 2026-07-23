package artifacts_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bkum/weftly/internal/artifacts"
)

func TestLocalPutGetStat(t *testing.T) {
	root := t.TempDir()
	store := artifacts.NewLocal(root)
	ctx := context.Background()

	// Missing key → ErrNotFound
	if _, _, err := store.Get(ctx, "nope"); !errors.Is(err, artifacts.ErrNotFound) {
		t.Fatalf("Get missing: want ErrNotFound, got %v", err)
	}

	// Put + Get roundtrip
	body := []byte("hello weftly")
	if err := store.Put(ctx, "run-1/report.html", bytes.NewReader(body), int64(len(body)), "text/html"); err != nil {
		t.Fatal(err)
	}
	rc, size, err := store.Get(ctx, "run-1/report.html")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if size != int64(len(body)) {
		t.Errorf("size: want %d, got %d", len(body), size)
	}
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, body) {
		t.Errorf("body: got %q", got)
	}
	// Stat matches
	info, err := store.Stat(ctx, "run-1/report.html")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != int64(len(body)) {
		t.Errorf("stat size: %d", info.Size)
	}
	// The file actually lives under root
	if _, err := os.Stat(filepath.Join(root, "run-1", "report.html")); err != nil {
		t.Errorf("expected file on disk: %v", err)
	}
}

func TestLocalRejectsTraversal(t *testing.T) {
	store := artifacts.NewLocal(t.TempDir())
	err := store.Put(context.Background(), "../escape.txt", bytes.NewReader([]byte("x")), 1, "")
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected traversal error, got %v", err)
	}
}

func TestLocalList(t *testing.T) {
	root := t.TempDir()
	store := artifacts.NewLocal(root)
	ctx := context.Background()
	for _, key := range []string{"run-a/one.txt", "run-a/two.txt", "run-b/three.txt"} {
		if err := store.Put(ctx, key, bytes.NewReader([]byte("x")), 1, ""); err != nil {
			t.Fatal(err)
		}
	}
	all, err := store.List(ctx, "run-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("list run-a: want 2, got %d — %+v", len(all), all)
	}
}
