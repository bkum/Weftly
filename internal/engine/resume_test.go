package engine_test

import (
	"context"
	"testing"

	_ "github.com/bkum/weftly/internal/actions"
	"github.com/bkum/weftly/internal/engine"
	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/schema"
)

// TestResumeSkipsSuccessfulSteps runs a two-step workflow twice against
// the same base dir. The second run passes --resume; the first step is
// expected to be replayed from state.json (not re-executed), while the
// second step runs freshly (it was NOT successful the first time because
// we make it fail).
func TestResumeSkipsSuccessfulSteps(t *testing.T) {
	src := `
name: t
steps:
  - id: first
    run: |
      echo "answer=42" >> "$WEFTLY_OUTPUT"
  - id: second
    run: |
      if [ -z "$SUCCEED" ]; then
        echo "no SUCCEED set, failing on purpose" >&2
        exit 1
      fi
      echo "used=$ANSWER" >> "$WEFTLY_OUTPUT"
    env:
      SUCCEED: "${{ env.SUCCEED }}"
      ANSWER:  "${{ steps.first.outputs.answer }}"
`
	wf, err := schema.Parse(bytesReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(wf); err != nil {
		t.Fatal(err)
	}
	baseDir := t.TempDir()

	// First run: expect failure at second step.
	res1, err := engine.Run(context.Background(), wf, engine.Options{
		BaseDir: baseDir,
		Inputs:  nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res1.Status != events.Failed {
		t.Fatalf("first run: want Failed, got %s", res1.Status)
	}

	// Second run with --resume and SUCCEED set: first should be replayed
	// (Resumed=true), second executed fresh and succeed.
	bus := events.NewBus()
	var resumedFirst, executedSecond bool
	var secondErr error
	bus.Subscribe(func(e events.Event) {
		if f, ok := e.(events.StepFinished); ok {
			switch f.StepID {
			case "first":
				if f.Status == events.Success && f.Resumed {
					resumedFirst = true
				}
			case "second":
				if f.Status == events.Success && !f.Resumed {
					executedSecond = true
				}
				if f.Err != nil {
					secondErr = f.Err
				}
			}
		}
	})
	res2, err := engine.Run(context.Background(), wf, engine.Options{
		BaseDir: baseDir,
		Resume:  res1.RunID,
		Vars:    map[string]string{"SUCCEED": "1"},
		Bus:     bus,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res2.RunID != res1.RunID {
		t.Errorf("resume must reuse run-id, got %s vs %s", res2.RunID, res1.RunID)
	}
	if res2.Status != events.Success {
		t.Fatalf("resumed run: want Success, got %s (second err: %v)", res2.Status, secondErr)
	}
	if !resumedFirst {
		t.Error("first step should have been replayed with Resumed=true")
	}
	if !executedSecond {
		t.Error("second step should have executed freshly")
	}
}
