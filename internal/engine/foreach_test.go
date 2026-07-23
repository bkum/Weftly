package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	_ "github.com/bkum/weftly/internal/actions"
	"github.com/bkum/weftly/internal/engine"
	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/schema"
)

func TestForEachRunsPerElement(t *testing.T) {
	baseDir := t.TempDir()
	outDir := filepath.Join(baseDir, "marks")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `
name: t
inputs:
  hosts:
    default: [alpha, beta, gamma]
steps:
  - id: fanout
    for-each: ${{ inputs.hosts }}
    env:
      OUT: ` + outDir + `
    run: |
      touch "$OUT/host-$EACH_INDEX-$EACH_VALUE"
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
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, e := range entries {
		got = append(got, e.Name())
	}
	sort.Strings(got)
	want := []string{"host-0-alpha", "host-1-beta", "host-2-gamma"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("markers: got %v, want %v", got, want)
	}
}

func TestForEachEmptyListIsSuccess(t *testing.T) {
	src := `
name: t
inputs:
  hosts:
    default: []
steps:
  - id: fanout
    for-each: ${{ inputs.hosts }}
    run: echo unused
`
	wf, err := schema.Parse(bytesReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(wf); err != nil {
		t.Fatal(err)
	}
	res, err := engine.Run(context.Background(), wf, engine.Options{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != events.Success {
		t.Errorf("empty for-each should succeed, got %s", res.Status)
	}
}

func TestForEachFailureAggregates(t *testing.T) {
	src := `
name: t
inputs:
  hosts:
    default: [ok, bad, ok]
steps:
  - id: fanout
    for-each: ${{ inputs.hosts }}
    run: |
      if [ "$EACH_VALUE" = "bad" ]; then exit 2; fi
`
	wf, err := schema.Parse(bytesReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(wf); err != nil {
		t.Fatal(err)
	}
	res, err := engine.Run(context.Background(), wf, engine.Options{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != events.Failed {
		t.Errorf("one bad iteration should fail the step, got %s", res.Status)
	}
}
