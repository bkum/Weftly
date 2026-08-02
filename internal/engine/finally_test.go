package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/bkum/weftly/internal/actions"

	"github.com/bkum/weftly/internal/engine"
	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/schema"
)

// writePair writes a caller + library into one dir and returns the caller path.
func writePair(t *testing.T, caller, lib string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lib.yml"), []byte(lib), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "main.yml")
	if err := os.WriteFile(main, []byte(caller), 0o644); err != nil {
		t.Fatal(err)
	}
	return main
}

func runWF(t *testing.T, path string) (events.Status, []events.Event) {
	t.Helper()
	wf, err := schema.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	mu, evs := collectEvents(bus)
	res, err := engine.Run(context.Background(), wf, engine.Options{
		BaseDir: t.TempDir(), Bus: bus,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	out := make([]events.Event, len(*evs))
	copy(out, *evs)
	return res.Status, out
}

func sawStep(evs []events.Event, id string) bool {
	for _, e := range evs {
		if s, ok := e.(events.StepStarted); ok && s.StepID == id {
			return true
		}
	}
	return false
}

func sawLog(evs []events.Event, substr string) bool {
	for _, e := range evs {
		if l, ok := e.(events.StepLog); ok && strings.Contains(l.Line, substr) {
			return true
		}
	}
	return false
}

// TestFinallyRunsAfterScopeSuccess — teardown fires on the happy path.
func TestFinallyRunsAfterScopeSuccess(t *testing.T) {
	main := writePair(t, `
name: caller
steps:
  - id: sub
    include: lib.yml
`, `
name: lib
steps:
  - id: work
    run: echo working
finally:
  - id: teardown
    run: echo TORE-DOWN
`)
	st, evs := runWF(t, main)
	if st != events.Success {
		t.Fatalf("want success, got %s", st)
	}
	if !sawStep(evs, "sub.finally.teardown") {
		t.Error("finally step did not run on the success path")
	}
	if !sawLog(evs, "TORE-DOWN") {
		t.Error("teardown produced no output")
	}
}

// TestFinallyRunsAfterScopeFailure is the case teardown exists for: the
// fragment's own step failed, and its finally: must still run despite
// the scheduler's cascade-skip.
func TestFinallyRunsAfterScopeFailure(t *testing.T) {
	main := writePair(t, `
name: caller
steps:
  - id: sub
    include: lib.yml
`, `
name: lib
steps:
  - id: work
    run: |
      echo half-built
      exit 1
finally:
  - id: teardown
    run: echo CLEANED-UP
`)
	st, evs := runWF(t, main)
	if st == events.Success {
		t.Fatal("expected the run to fail")
	}
	if !sawStep(evs, "sub.finally.teardown") {
		t.Fatal("finally step was cascade-skipped — teardown must run after failure")
	}
	if !sawLog(evs, "CLEANED-UP") {
		t.Error("teardown produced no output")
	}
}

// TestFinallySeesScopeStatusNotRunStatus is the semantic that makes
// teardown decidable: inside a fragment's finally:, failure() must
// report on THAT fragment, not on an unrelated sibling.
func TestFinallySeesScopeStatusNotRunStatus(t *testing.T) {
	dir := t.TempDir()
	// good.yml succeeds and asserts its own finally sees success().
	if err := os.WriteFile(filepath.Join(dir, "good.yml"), []byte(`
name: good
steps:
  - id: work
    run: echo fine
finally:
  - id: check
    if: ${{ success() }}
    run: echo GOOD-SAW-SUCCESS
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// bad.yml fails and asserts its own finally sees failure().
	if err := os.WriteFile(filepath.Join(dir, "bad.yml"), []byte(`
name: bad
steps:
  - id: work
    run: exit 1
finally:
  - id: check
    if: ${{ failure() }}
    run: echo BAD-SAW-FAILURE
`), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "main.yml")
	if err := os.WriteFile(main, []byte(`
name: caller
steps:
  - id: alpha
    include: good.yml
  - id: beta
    include: bad.yml
    continue-on-error: true
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, evs := runWF(t, main)
	if !sawLog(evs, "GOOD-SAW-SUCCESS") {
		t.Error("succeeding fragment's finally did not observe success() — scope status leaked from the run")
	}
	if !sawLog(evs, "BAD-SAW-FAILURE") {
		t.Error("failing fragment's finally did not observe failure() in its own scope")
	}
}

// TestFinallyInnermostFirst — a nested fragment tears down before its
// parent, so a parent's teardown can rely on children already being done.
func TestFinallyInnermostFirst(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "inner.yml"), []byte(`
name: inner
steps:
  - id: work
    run: echo inner-work
finally:
  - id: down
    run: echo INNER-DOWN
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "outer.yml"), []byte(`
name: outer
steps:
  - id: nested
    include: inner.yml
finally:
  - id: down
    run: echo OUTER-DOWN
`), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "main.yml")
	if err := os.WriteFile(main, []byte(`
name: caller
steps:
  - id: sub
    include: outer.yml
`), 0o644); err != nil {
		t.Fatal(err)
	}
	st, evs := runWF(t, main)
	if st != events.Success {
		t.Fatalf("want success, got %s", st)
	}
	// Both teardowns ran, inner before outer.
	innerAt, outerAt := -1, -1
	for i, e := range evs {
		if l, ok := e.(events.StepLog); ok {
			if strings.Contains(l.Line, "INNER-DOWN") && innerAt < 0 {
				innerAt = i
			}
			if strings.Contains(l.Line, "OUTER-DOWN") && outerAt < 0 {
				outerAt = i
			}
		}
	}
	if innerAt < 0 || outerAt < 0 {
		t.Fatalf("missing teardown output (inner=%d outer=%d)", innerAt, outerAt)
	}
	if innerAt > outerAt {
		t.Errorf("outer teardown ran before inner (inner=%d outer=%d)", innerAt, outerAt)
	}
}
