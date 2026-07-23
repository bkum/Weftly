package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/bkum/weftly/internal/actions"
	"github.com/bkum/weftly/internal/engine"
	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/schema"
)

// TestCleanupRunsOnSuccess verifies cleanup steps fire even when the
// main graph succeeds, and that success() gates them correctly.
func TestCleanupRunsOnSuccess(t *testing.T) {
	baseDir := t.TempDir()
	marker := filepath.Join(baseDir, "cleanup.marker")
	src := `
name: t
steps:
  - id: main
    run: echo hi
cleanup:
  - id: teardown
    if: ${{ success() }}
    env:
      M: ` + marker + `
    run: touch "$M"
  - id: onfail
    if: ${{ failure() }}
    env:
      M: ` + marker + `.fail
    run: touch "$M"
`
	wf, err := schema.Parse(bytesReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(wf); err != nil {
		t.Fatal(err)
	}
	res, err := engine.Run(context.Background(), wf, engine.Options{BaseDir: baseDir})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != events.Success {
		t.Fatalf("run status: got %s", res.Status)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("teardown marker missing: %v", err)
	}
	if _, err := os.Stat(marker + ".fail"); err == nil {
		t.Fatal("onfail marker should NOT exist on a successful run")
	}
}

// TestCleanupRunsOnFailure verifies the reverse: failure() gates fire,
// success() gates skip.
func TestCleanupRunsOnFailure(t *testing.T) {
	baseDir := t.TempDir()
	marker := filepath.Join(baseDir, "cleanup.marker")
	src := `
name: t
steps:
  - id: main
    run: exit 1
cleanup:
  - id: teardown
    if: ${{ success() }}
    env:
      M: ` + marker + `
    run: touch "$M"
  - id: onfail
    if: ${{ failure() }}
    env:
      M: ` + marker + `.fail
    run: touch "$M"
`
	wf, err := schema.Parse(bytesReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(wf); err != nil {
		t.Fatal(err)
	}
	_, err = engine.Run(context.Background(), wf, engine.Options{BaseDir: baseDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("teardown marker should NOT exist on a failed run")
	}
	if _, err := os.Stat(marker + ".fail"); err != nil {
		t.Fatalf("onfail marker missing: %v", err)
	}
}

// TestAlwaysGateFires — the always() helper should let a step run
// under both success and failure.
func TestAlwaysGateFires(t *testing.T) {
	for name, body := range map[string]string{
		"success": "echo hi",
		"failure": "exit 1",
	} {
		t.Run(name, func(t *testing.T) {
			baseDir := t.TempDir()
			marker := filepath.Join(baseDir, "always.marker")
			src := `
name: t
steps:
  - id: main
    run: ` + body + `
cleanup:
  - id: always
    if: ${{ always() }}
    env:
      M: ` + marker + `
    run: touch "$M"
`
			wf, err := schema.Parse(bytesReader(src))
			if err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(wf); err != nil {
				t.Fatal(err)
			}
			_, _ = engine.Run(context.Background(), wf, engine.Options{BaseDir: baseDir})
			if _, err := os.Stat(marker); err != nil {
				t.Errorf("always cleanup missing in %s branch: %v", name, err)
			}
		})
	}
}
