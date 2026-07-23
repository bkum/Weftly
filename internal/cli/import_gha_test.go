package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestImportGHACmdOutputsToStdout drives the actual cobra command so a
// regression in the wiring (missing AddCommand, arg parsing) also
// trips the test — the internal/gha package tests only cover the
// translator.
func TestImportGHACmdOutputsToStdout(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "gha.yml")
	if err := os.WriteFile(src, []byte(`
name: X
jobs:
  build:
    steps:
      - id: hello
        run: echo hi
`), 0o600); err != nil {
		t.Fatal(err)
	}
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"import-gha", src})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), "converted from GitHub Actions (job=build)") {
		t.Errorf("stdout missing banner:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "id: hello") || !strings.Contains(out.String(), "run: echo hi") {
		t.Errorf("stdout missing step body:\n%s", out.String())
	}
}

func TestImportGHACmdWritesFileWhenOutFlagSet(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "gha.yml")
	dst := filepath.Join(dir, "weftly.yml")
	if err := os.WriteFile(src, []byte(`
name: X
jobs:
  build:
    steps:
      - id: hello
        run: echo hi
`), 0o600); err != nil {
		t.Fatal(err)
	}
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"import-gha", "--out", dst, src})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "id: hello") {
		t.Errorf("dst missing step body:\n%s", body)
	}
	if out.Len() != 0 {
		t.Errorf("stdout should be empty when --out set, got:\n%s", out.String())
	}
}
