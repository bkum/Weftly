package engine

import (
	"strings"
	"testing"

	"github.com/bkum/weftly/internal/schema"
)

func mustParse(t *testing.T, body string) *schema.Workflow {
	t.Helper()
	wf, err := schema.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return wf
}

// §15.5 — three simultaneously-invalid inputs produce three errors in
// ONE report. A form with three bad fields should not need three round
// trips.
func TestAllInputErrorsReportedAtOnce(t *testing.T) {
	wf := mustParse(t, `
name: t
inputs:
  cases:  { type: int, min: 100 }
  domain: { type: enum, values: [retail, healthcare] }
  when:   { type: duration }
steps: [{id: s, run: echo}]`)
	_, _, err := resolveInputs(wf, ResolveOptions{Supplied: map[string]any{
		"cases": "5", "domain": "retial", "when": "soon",
	}})
	if err == nil {
		t.Fatal("expected input errors")
	}
	ierrs, ok := err.(schema.InputErrors)
	if !ok {
		t.Fatalf("expected schema.InputErrors, got %T: %v", err, err)
	}
	if len(ierrs) != 3 {
		t.Fatalf("expected 3 errors in one report, got %d: %v", len(ierrs), ierrs)
	}
}

// §15.7 — a typed int enters the expression context as a number, so
// `inputs.cases > 1000` compares numerically rather than lexically.
func TestTypedValueEntersExpressionContext(t *testing.T) {
	wf := mustParse(t, `
name: t
inputs:
  cases: { type: int, default: 2500 }
steps: [{id: s, run: echo}]`)
	out, _, err := resolveInputs(wf, ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["cases"].(int); !ok {
		t.Fatalf("cases should be an int in the expression context, got %T", out["cases"])
	}
}

// §15.13 — --preset plus --input: the explicit flag wins, the rest of
// the preset still applies.
func TestPresetPrecedence(t *testing.T) {
	wf := mustParse(t, `
name: t
inputs:
  domain: { type: enum, values: [retail, healthcare] }
  cases:  { type: int }
  profile: { type: string }
presets:
  qa_exceptions:
    values:
      domain: retail
      cases: 500
      profile: exception_heavy
steps: [{id: s, run: echo}]`)
	out, _, err := resolveInputs(wf, ResolveOptions{
		Preset:   "qa_exceptions",
		Supplied: map[string]any{"cases": "800"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["cases"] != 800 {
		t.Errorf("--input must beat the preset: cases=%v", out["cases"])
	}
	if out["domain"] != "retail" || out["profile"] != "exception_heavy" {
		t.Errorf("rest of the preset should still apply: %+v", out)
	}
}

func TestUnknownPresetIsFatal(t *testing.T) {
	wf := mustParse(t, `
name: t
inputs:
  a: { default: x }
presets:
  good: { values: { a: y } }
steps: [{id: s, run: echo}]`)
	_, _, err := resolveInputs(wf, ResolveOptions{Preset: "typo"})
	if err == nil {
		t.Fatal("an unknown preset must fail — running with silent defaults is worse than stopping")
	}
	if !strings.Contains(err.Error(), "good") {
		t.Errorf("error should list the available presets: %v", err)
	}
}

// §3 — a default referencing another input resolves in dependency
// order regardless of map iteration order.
func TestDefaultExpressionReferencesAnotherInput(t *testing.T) {
	wf := mustParse(t, `
name: t
inputs:
  transaction: { default: "850" }
  output_file: { default: "out/${{ inputs.transaction }}.edi" }
steps: [{id: s, run: echo}]`)
	for i := 0; i < 20; i++ { // map order varies per run; repeat
		out, _, err := resolveInputs(wf, ResolveOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if out["output_file"] != "out/850.edi" {
			t.Fatalf("dependency order not honoured: %v", out["output_file"])
		}
	}
}

// A supplied value must NOT trigger its own default's evaluation.
func TestSuppliedValueSkipsDefaultExpression(t *testing.T) {
	wf := mustParse(t, `
name: t
inputs:
  a: { default: "${{ inputs.missing_thing }}" }
steps: [{id: s, run: echo}]`)
	out, _, err := resolveInputs(wf, ResolveOptions{Supplied: map[string]any{"a": "explicit"}})
	if err != nil {
		t.Fatalf("supplying a value should bypass its default entirely: %v", err)
	}
	if out["a"] != "explicit" {
		t.Errorf("got %v", out["a"])
	}
}

// A required input that is never supplied names itself clearly.
func TestRequiredInputError(t *testing.T) {
	wf := mustParse(t, `
name: t
inputs:
  env_url: { required: true }
steps: [{id: s, run: echo}]`)
	_, _, err := resolveInputs(wf, ResolveOptions{})
	if err == nil || !strings.Contains(err.Error(), "env_url") {
		t.Fatalf("want a required-input error naming env_url, got %v", err)
	}
}
