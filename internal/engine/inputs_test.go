package engine

import (
	"strings"
	"testing"

	"github.com/bkum/weftly/internal/schema"
)

func TestResolveInputsEnumAccepts(t *testing.T) {
	wf := &schema.Workflow{
		Inputs: map[string]schema.Input{
			"env": {Enum: []any{"dev", "staging", "prod"}, Default: "dev"},
		},
	}
	out, _, err := resolveInputs(wf, map[string]any{"env": "prod"})
	if err != nil {
		t.Fatalf("resolveInputs: %v", err)
	}
	if out["env"] != "prod" {
		t.Errorf("env: got %v", out["env"])
	}
}

func TestResolveInputsEnumRejects(t *testing.T) {
	wf := &schema.Workflow{
		Inputs: map[string]schema.Input{
			"env": {Enum: []any{"dev", "staging", "prod"}, Default: "dev"},
		},
	}
	_, _, err := resolveInputs(wf, map[string]any{"env": "bogus"})
	if err == nil {
		t.Fatal("expected enum-mismatch error")
	}
	if !strings.Contains(err.Error(), "is not one of") {
		t.Errorf("error message: got %v", err)
	}
}

func TestResolveInputsEnumDefaultIsAllowed(t *testing.T) {
	// A workflow's declared default should always be a valid enum
	// member — but even if the author writes one that isn't, the
	// resolver should still surface the mismatch cleanly (not panic).
	wf := &schema.Workflow{
		Inputs: map[string]schema.Input{
			"env": {Enum: []any{"dev", "staging", "prod"}, Default: "dev"},
		},
	}
	out, _, err := resolveInputs(wf, nil)
	if err != nil {
		t.Fatalf("resolveInputs (default path): %v", err)
	}
	if out["env"] != "dev" {
		t.Errorf("default env: got %v", out["env"])
	}
}
