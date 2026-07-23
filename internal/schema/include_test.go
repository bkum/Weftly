package schema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestIncludeMergesSteps(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "prelude.yml", `
name: prelude-only
steps:
  - id: pre1
    run: echo pre1
  - id: pre2
    run: echo pre2
`)
	main := writeFile(t, dir, "main.yml", `
name: main
include: [prelude.yml]
steps:
  - id: body
    run: echo body
`)
	wf, err := Load(main)
	if err != nil {
		t.Fatal(err)
	}
	if len(wf.Steps) != 3 {
		t.Fatalf("want 3 steps, got %d", len(wf.Steps))
	}
	if wf.Steps[0].ID != "pre1" || wf.Steps[1].ID != "pre2" || wf.Steps[2].ID != "body" {
		t.Errorf("unexpected order: %+v", wf.Steps)
	}
}

func TestIncludeCycleRejected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.yml", `
name: a
include: [b.yml]
steps: [{run: echo a}]
`)
	writeFile(t, dir, "b.yml", `
name: b
include: [a.yml]
steps: [{run: echo b}]
`)
	_, err := Load(filepath.Join(dir, "a.yml"))
	if err == nil || !strings.Contains(err.Error(), "cycle detected") {
		t.Fatalf("expected cycle-detected error, got %v", err)
	}
}

func TestIncludeMissingFileRejected(t *testing.T) {
	dir := t.TempDir()
	main := writeFile(t, dir, "main.yml", `
name: main
include: [nope.yml]
steps: [{run: echo hi}]
`)
	_, err := Load(main)
	if err == nil || !strings.Contains(err.Error(), "nope.yml") {
		t.Fatalf("expected missing-file error, got %v", err)
	}
}

func TestIncludeEnvMergedParentWins(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "child.yml", `
name: child
env:
  A: from-child
  B: from-child
steps: [{run: echo x}]
`)
	main := writeFile(t, dir, "main.yml", `
name: main
env:
  A: from-parent
include: [child.yml]
steps: [{run: echo y}]
`)
	wf, err := Load(main)
	if err != nil {
		t.Fatal(err)
	}
	if wf.Env["A"] != "from-parent" {
		t.Errorf("parent should win for A: got %q", wf.Env["A"])
	}
	if wf.Env["B"] != "from-child" {
		t.Errorf("child B should be inherited: got %q", wf.Env["B"])
	}
}
