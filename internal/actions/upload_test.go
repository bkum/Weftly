package actions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/expr"
)

func TestUploadActionCopiesToArtifacts(t *testing.T) {
	ws := t.TempDir()
	artifacts := t.TempDir()
	// create a file inside the workspace
	src := filepath.Join(ws, "out", "report.html")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("<html>ok</html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := mustParseYAML(t, `
path: ./out/report.html
name: report
`)
	var evs []events.Event
	sc := &StepContext{
		Config:       cfg,
		Workdir:      ws,
		ArtifactsDir: artifacts,
		Expr:         expr.New(),
		ExprEnv:      expr.Env{},
		Emit:         func(e events.Event) { evs = append(evs, e) },
	}
	if _, err := (uploadAction{}).Run(context.Background(), sc); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(artifacts, "report.html")
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("artifact not copied: %v", err)
	}
	var found bool
	for _, e := range evs {
		if u, ok := e.(events.ArtifactUploaded); ok && u.Name == "report" {
			found = true
		}
	}
	if !found {
		t.Fatal("no ArtifactUploaded event")
	}
}

func TestUploadActionRejectsTraversal(t *testing.T) {
	ws := t.TempDir()
	cfg := mustParseYAML(t, `path: ../../etc/passwd`)
	sc := &StepContext{Config: cfg, Workdir: ws, Expr: expr.New(), ExprEnv: expr.Env{}}
	_, err := (uploadAction{}).Run(context.Background(), sc)
	if err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("expected traversal rejection, got %v", err)
	}
}

func TestUploadActionNoMatches(t *testing.T) {
	ws := t.TempDir()
	cfg := mustParseYAML(t, `path: ./out/nope.txt`)
	sc := &StepContext{Config: cfg, Workdir: ws, ArtifactsDir: t.TempDir(), Expr: expr.New(), ExprEnv: expr.Env{}}
	_, err := (uploadAction{}).Run(context.Background(), sc)
	if err == nil || !strings.Contains(err.Error(), "no files matched") {
		t.Fatalf("expected no-match error, got %v", err)
	}
}
