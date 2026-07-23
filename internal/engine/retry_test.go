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

// TestRetrySucceedsAfterN — a step that reads a counter file and fails
// until the third attempt, with retry:{attempts:3}. Assert the step ends
// as Success and that two StepRetry events fired.
func TestRetrySucceedsAfterN(t *testing.T) {
	baseDir := t.TempDir()
	counter := filepath.Join(baseDir, "counter")
	if err := os.WriteFile(counter, []byte("0"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := `
name: t
steps:
  - id: flaky
    retry: { attempts: 3, delay: 10ms }
    env:
      COUNTER: ` + counter + `
    run: |
      n=$(cat "$COUNTER")
      n=$((n+1))
      printf "%s" "$n" > "$COUNTER"
      if [ "$n" -lt 3 ]; then
        echo "not yet" >&2
        exit 1
      fi
      echo "attempts=$n" >> "$WEFTLY_OUTPUT"
`
	wf, err := schema.Parse(bytesReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(wf); err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	var retries int
	var finalStatus events.Status
	bus.Subscribe(func(e events.Event) {
		switch ev := e.(type) {
		case events.StepRetry:
			retries++
		case events.StepFinished:
			finalStatus = ev.Status
		}
	})
	res, err := engine.Run(context.Background(), wf, engine.Options{
		BaseDir: baseDir,
		Bus:     bus,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != events.Success {
		t.Fatalf("run status: want Success, got %s", res.Status)
	}
	if finalStatus != events.Success {
		t.Fatalf("step final status: want Success, got %s", finalStatus)
	}
	if retries != 2 {
		t.Errorf("StepRetry count: want 2 (attempts 3 → two failures then success), got %d", retries)
	}
}

// TestRetryExhaustedFails — a step that always fails should end Failed
// after using all attempts and fire (attempts-1) StepRetry events.
func TestRetryExhaustedFails(t *testing.T) {
	src := `
name: t
steps:
  - id: always_fails
    retry: { attempts: 3, delay: 5ms }
    run: exit 7
`
	wf, err := schema.Parse(bytesReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(wf); err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	var retries int
	bus.Subscribe(func(e events.Event) {
		if _, ok := e.(events.StepRetry); ok {
			retries++
		}
	})
	res, err := engine.Run(context.Background(), wf, engine.Options{
		BaseDir: t.TempDir(),
		Bus:     bus,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != events.Failed {
		t.Fatalf("want Failed, got %s", res.Status)
	}
	if retries != 2 {
		t.Errorf("StepRetry count: want 2, got %d", retries)
	}
}

// TestRetryDoesNotHandleTimeoutByDefault — the default `on:` is
// {failed}, so a step whose ctx deadline expires should NOT retry.
func TestRetryDoesNotHandleTimeoutByDefault(t *testing.T) {
	src := `
name: t
steps:
  - id: slow
    timeout: 100ms
    retry: { attempts: 5, delay: 10ms }
    run: sleep 5
`
	wf, err := schema.Parse(bytesReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(wf); err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	var retries int
	var stepStatus events.Status
	bus.Subscribe(func(e events.Event) {
		switch ev := e.(type) {
		case events.StepRetry:
			retries++
		case events.StepFinished:
			stepStatus = ev.Status
		}
	})
	_, err = engine.Run(context.Background(), wf, engine.Options{
		BaseDir: t.TempDir(),
		Bus:     bus,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Run-level status collapses TimedOut → Failed; the per-step
	// StepFinished is where we look for the true cause.
	if stepStatus != events.TimedOut {
		t.Fatalf("step status: want TimedOut, got %s", stepStatus)
	}
	if retries != 0 {
		t.Errorf("StepRetry count: want 0 (default on: [failed] does not cover timed-out), got %d", retries)
	}
}

// TestRetryHandlesTimeoutWhenRequested — with on:[timed-out] set, a
// timed-out step should trigger retries.
func TestRetryHandlesTimeoutWhenRequested(t *testing.T) {
	src := `
name: t
steps:
  - id: slow
    timeout: 100ms
    retry: { attempts: 3, delay: 5ms, on: [timed-out] }
    run: sleep 5
`
	wf, err := schema.Parse(bytesReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(wf); err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	var retries int
	bus.Subscribe(func(e events.Event) {
		if _, ok := e.(events.StepRetry); ok {
			retries++
		}
	})
	_, err = engine.Run(context.Background(), wf, engine.Options{
		BaseDir: t.TempDir(),
		Bus:     bus,
	})
	if err != nil {
		t.Fatal(err)
	}
	if retries != 2 {
		t.Errorf("StepRetry count with on:[timed-out]: want 2, got %d", retries)
	}
}
