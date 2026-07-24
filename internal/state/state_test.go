package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/secrets"
)

// TestMaskValueRecursesIntoMapsAndSlices verifies that a secret hidden
// inside a nested output (e.g. an http action's parsed body) is masked
// before state.json ever hits disk. The old maskValue only handled
// top-level strings, which quietly leaked secrets for structured outputs.
func TestMaskValueRecursesIntoMapsAndSlices(t *testing.T) {
	dir := t.TempDir()
	sec := secrets.NewRegistry()
	sec.Register("hunter2-token")

	w := New(dir, sec)
	w.Handle(events.RunStarted{Workflow: "t", RunID: "r"})
	w.Handle(events.StepStarted{StepID: "s", Name: "s", Action: "http"})
	w.Handle(events.StepOutput{StepID: "s", Key: "body", Value: map[string]any{
		"user":  "alice",
		"token": "hunter2-token",
		"nested": map[string]any{
			"session": "hunter2-token also here",
		},
		"list": []any{"first", "hunter2-token", "third"},
	}})
	w.Handle(events.StepFinished{StepID: "s", Status: events.Success})

	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if strings.Contains(body, "hunter2-token") {
		t.Fatalf("secret leaked into state.json:\n%s", body)
	}
	// Sanity — the mask marker is present in each of the three slots.
	if strings.Count(body, "***") < 3 {
		t.Errorf("expected *** in nested map + inner map + list slot, got:\n%s", body)
	}
}

func TestMaskValueLeavesNonStringTypesAlone(t *testing.T) {
	w := New(t.TempDir(), secrets.NewRegistry())
	got := w.maskValue(map[string]any{
		"count":   42,
		"ratio":   0.5,
		"present": true,
	})
	// Round-trip through JSON so we compare shapes not internal Go types.
	b, _ := json.Marshal(got)
	want := `{"count":42,"present":true,"ratio":0.5}`
	if string(b) != want {
		t.Errorf("got %s, want %s", b, want)
	}
}
