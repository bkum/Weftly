package actions

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/expr"
)

// mockStore records every Put so tests can assert what was mirrored.
type mockStore struct {
	mu   sync.Mutex
	puts map[string][]byte
}

func (m *mockStore) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	buf, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.puts == nil {
		m.puts = map[string][]byte{}
	}
	m.puts[key] = buf
	return nil
}

func TestUploadActionMirrorsToRemoteStore(t *testing.T) {
	ws := t.TempDir()
	artifacts := t.TempDir()
	src := filepath.Join(ws, "out", "report.html")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("<html>mirror me</html>")
	if err := os.WriteFile(src, body, 0o644); err != nil {
		t.Fatal(err)
	}

	store := &mockStore{}
	cfg := mustParseYAML(t, `
path: ./out/report.html
name: report
`)
	sc := &StepContext{
		Config:        cfg,
		Workdir:       ws,
		ArtifactsDir:  artifacts,
		ArtifactStore: store,
		RunID:         "run-42",
		Expr:          expr.New(),
		ExprEnv:       expr.Env{},
		Emit:          func(events.Event) {},
	}
	if _, err := (uploadAction{}).Run(context.Background(), sc); err != nil {
		t.Fatal(err)
	}
	got, ok := store.puts["run-42/report.html"]
	if !ok {
		t.Fatalf("expected mirror under run-42/report.html, got %+v", store.puts)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("mirror body mismatch: got %q", got)
	}
	// Local copy still authoritative.
	if _, err := os.Stat(filepath.Join(artifacts, "report.html")); err != nil {
		t.Errorf("local copy missing: %v", err)
	}
}

// A remote-store Put failure must not fail the run — the local copy is
// authoritative and the failure is logged.
type failStore struct{}

func (failStore) Put(context.Context, string, io.Reader, int64, string) error {
	return io.ErrUnexpectedEOF
}

func TestUploadActionRemoteFailureIsTolerated(t *testing.T) {
	ws := t.TempDir()
	src := filepath.Join(ws, "out", "note.txt")
	_ = os.MkdirAll(filepath.Dir(src), 0o755)
	_ = os.WriteFile(src, []byte("hi"), 0o644)
	sc := &StepContext{
		Config:        mustParseYAML(t, `path: ./out/note.txt`),
		Workdir:       ws,
		ArtifactsDir:  t.TempDir(),
		ArtifactStore: failStore{},
		RunID:         "run-x",
		Expr:          expr.New(),
		ExprEnv:       expr.Env{},
		Emit:          func(events.Event) {},
	}
	if _, err := (uploadAction{}).Run(context.Background(), sc); err != nil {
		t.Fatalf("run should tolerate remote failure, got err=%v", err)
	}
}
