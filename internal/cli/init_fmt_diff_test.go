package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The template writes a self-contained example that must parse + validate.
func TestInitProducesValidWorkflow(t *testing.T) {
	dir := t.TempDir()
	wfPath := filepath.Join(dir, "sample.yml")
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"init", "--out", wfPath, "sample"})
	if err := root.Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}
	// Run validate against it to confirm the generated file parses.
	root = NewRootCmd()
	out.Reset()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"validate", wfPath})
	if err := root.Execute(); err != nil {
		t.Fatalf("validate scaffolded file: %v\n%s", err, out.String())
	}
}

func TestFmtIdempotent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "wf.yml")
	body := `name: t
steps:
  - id: hi
    run: echo hi
`
	if err := os.WriteFile(src, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"fmt", src})
	if err := root.Execute(); err != nil {
		t.Fatalf("fmt: %v", err)
	}
	first := out.String()
	// Second run should be a no-op (byte-identical output).
	root = NewRootCmd()
	out.Reset()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"fmt", src})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if first != out.String() {
		t.Errorf("fmt is not idempotent:\nfirst:\n%s\nsecond:\n%s", first, out.String())
	}
}

func TestDiffReportsChanges(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.yml")
	b := filepath.Join(dir, "b.yml")
	if err := os.WriteFile(a, []byte(`name: t
steps:
  - id: hi
    run: echo old
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte(`name: t
steps:
  - id: hi
    run: echo new
  - id: extra
    run: echo added
`), 0o600); err != nil {
		t.Fatal(err)
	}
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"diff", a, b})
	err := root.Execute()
	if err == nil {
		t.Fatalf("expected non-zero exit when diff found; got success:\n%s", out.String())
	}
	joined := out.String()
	if !strings.Contains(joined, `steps."extra": added`) {
		t.Errorf("expected added-step line, got:\n%s", joined)
	}
	if !strings.Contains(joined, "body changed") {
		t.Errorf("expected body-changed line, got:\n%s", joined)
	}
}
