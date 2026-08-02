package schema

import (
	"strings"
	"testing"
)

func load(t *testing.T, body string) *Workflow {
	t.Helper()
	wf, err := Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return wf
}

// §15.1 — a workflow with no `type:` anywhere behaves as before.
func TestUntypedInputsUnchanged(t *testing.T) {
	wf := load(t, `
name: t
inputs:
  a: { default: "hello" }
  b: { required: true }
steps:
  - id: s
    run: echo hi
`)
	if err := Validate(wf); err != nil {
		t.Fatalf("untyped workflow should validate: %v", err)
	}
	in := wf.Inputs["a"]
	if in.EffectiveType() != InputString {
		t.Errorf("omitted type should mean string, got %q", in.EffectiveType())
	}
	if v, e := in.Coerce("a", "hello"); e != nil || v != "hello" {
		t.Errorf("coerce: %v %v", v, e)
	}
}

// §15.3 — int is strict: "3.0" rejected, "3" accepted.
func TestIntCoercionIsStrict(t *testing.T) {
	in := Input{Type: InputInt}
	if _, e := in.Coerce("n", "3.0"); e == nil {
		t.Error(`"3.0" must not coerce to int — silent truncation is how 2500.7 becomes 2500`)
	}
	if _, e := in.Coerce("n", "3abc"); e == nil {
		t.Error(`"3abc" must not coerce to int`)
	}
	v, e := in.Coerce("n", "3")
	if e != nil || v != 3 {
		t.Errorf(`"3" should coerce to 3, got %v %v`, v, e)
	}
}

// §15.2 — a bound is enforced and names input, value, and constraint.
func TestIntBoundsEnforced(t *testing.T) {
	minV := 100.0
	in := Input{Type: InputInt, Min: &minV}
	_, e := in.Coerce("cases", 99)
	if e == nil {
		t.Fatal("99 should fail min=100")
	}
	msg := e.Error()
	for _, want := range []string{"cases", "99", "100"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should mention %q: %s", want, msg)
		}
	}
}

// §15.4 — enum mismatch produces a did-you-mean plus the full value list.
func TestEnumDidYouMean(t *testing.T) {
	in := Input{Type: InputEnum, Values: []string{"retail", "healthcare", "manufacturing"}}
	_, e := in.Coerce("domain", "retial")
	if e == nil {
		t.Fatal("retial should be rejected")
	}
	if e.Hint != "retail" {
		t.Errorf("expected did-you-mean 'retail', got %q", e.Hint)
	}
	if len(e.Allowed) != 3 {
		t.Errorf("expected the full value list, got %v", e.Allowed)
	}
}

// §15.6 — a secret's constraint violation reports the constraint and
// NEVER the value. This is the highest-consequence behaviour here.
func TestSecretConstraintNeverEchoesValue(t *testing.T) {
	minLen := 20
	in := Input{Type: InputString, Secret: true, MinLen: &minLen}
	_, e := in.Coerce("api_token", "hunter2")
	if e == nil {
		t.Fatal("short token should fail min_length")
	}
	if strings.Contains(e.Error(), "hunter2") {
		t.Fatalf("secret value leaked into the error: %s", e.Error())
	}
	if !strings.Contains(e.Error(), "min_length") {
		t.Errorf("error should name the constraint: %s", e.Error())
	}

	// Same for an enum near-miss: no value, no suggestion.
	esec := Input{Type: InputEnum, Secret: true, Values: []string{"alpha", "beta"}}
	_, e2 := esec.Coerce("token", "alpga")
	if e2 == nil {
		t.Fatal("expected rejection")
	}
	if strings.Contains(e2.Error(), "alpga") || e2.Hint != "" || len(e2.Allowed) > 0 {
		t.Errorf("secret enum error leaked value/suggestion/values: %+v", e2)
	}
}

// §15.8 — a json input serialises to compact JSON for env injection.
func TestJSONInputEnvString(t *testing.T) {
	in := Input{Type: InputJSON}
	v, e := in.Coerce("partners", `{"b":2,"a":1}`)
	if e != nil {
		t.Fatal(e)
	}
	s := EnvString(v)
	if !strings.HasPrefix(s, "{") || strings.Contains(s, "\n") || strings.Contains(s, "map[") {
		t.Errorf("expected compact JSON, got %q", s)
	}
}

func TestDurationCoercionAndBounds(t *testing.T) {
	maxV := float64(10 * 60 * 1e9) // 10m in ns
	in := Input{Type: InputDuration, Max: &maxV}
	if _, e := in.Coerce("t", "30s"); e != nil {
		t.Errorf("30s should parse: %v", e)
	}
	if _, e := in.Coerce("t", "30"); e == nil {
		t.Error("bare 30 is not a duration")
	}
	if _, e := in.Coerce("t", "20m"); e == nil {
		t.Error("20m should exceed max=10m")
	}
}

func TestBoolAcceptsOperatorSpellings(t *testing.T) {
	in := Input{Type: InputBool}
	for _, s := range []string{"true", "TRUE", "1", "yes", "on", "false", "0", "no", "off"} {
		if _, e := in.Coerce("b", s); e != nil {
			t.Errorf("%q should coerce to bool: %v", s, e)
		}
	}
	if _, e := in.Coerce("b", "maybe"); e == nil {
		t.Error("maybe is not a bool")
	}
}

// §15.9/15.10 — default-expression cycles and steps.* references are
// compile-time errors.
func TestDefaultExpressionValidation(t *testing.T) {
	cyc := load(t, `
name: t
inputs:
  a: { default: "${{ inputs.b }}" }
  b: { default: "${{ inputs.a }}" }
steps:
  - id: s
    run: echo hi
`)
	err := Validate(cyc)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected a default-expression cycle error, got %v", err)
	}

	steps := load(t, `
name: t
inputs:
  a: { default: "${{ steps.foo.outputs.x }}" }
steps:
  - id: s
    run: echo hi
`)
	err = Validate(steps)
	if err == nil || !strings.Contains(err.Error(), "steps.") {
		t.Errorf("expected a steps.* rejection, got %v", err)
	}
}

// §7.1 rules 1–4: malformed declarations.
func TestInputSchemaValidation(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"enum without values", `
name: t
inputs:
  a: { type: enum }
steps: [{id: s, run: echo}]`, "requires a non-empty `values:`"},
		{"list without items", `
name: t
inputs:
  a: { type: list }
steps: [{id: s, run: echo}]`, "requires `items:`"},
		{"pattern on int", `
name: t
inputs:
  a: { type: int, pattern: "^x" }
steps: [{id: s, run: echo}]`, "pattern does not apply"},
		{"min on bool", `
name: t
inputs:
  a: { type: bool, min: 1 }
steps: [{id: s, run: echo}]`, "min does not apply"},
		{"min greater than max", `
name: t
inputs:
  a: { type: int, min: 10, max: 5 }
steps: [{id: s, run: echo}]`, "min is greater than max"},
		{"default violates own declaration", `
name: t
inputs:
  a: { type: int, min: 100, default: 5 }
steps: [{id: s, run: echo}]`, "below the minimum"},
		{"unknown type", `
name: t
inputs:
  a: { type: wibble }
steps: [{id: s, run: echo}]`, "unknown type"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Validate(load(t, c.body))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

// §15.11/15.12 — preset validation, including the secret prohibition.
func TestPresetValidation(t *testing.T) {
	undeclared := load(t, `
name: t
inputs:
  domain: { type: enum, values: [retail] }
presets:
  p:
    values:
      dommain: retail
steps: [{id: s, run: echo}]`)
	err := Validate(undeclared)
	if err == nil || !strings.Contains(err.Error(), "not a declared input") {
		t.Errorf("preset naming an undeclared input should fail: %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "did you mean") {
		t.Errorf("expected a did-you-mean for a near-miss key: %v", err)
	}

	badval := load(t, `
name: t
inputs:
  domain: { type: enum, values: [retail, healthcare] }
presets:
  p:
    values:
      domain: retial
steps: [{id: s, run: echo}]`)
	err = Validate(badval)
	if err == nil || !strings.Contains(err.Error(), "not a valid choice") {
		t.Errorf("preset value failing its input's constraints should fail: %v", err)
	}

	// The security rule: a preset may never carry a secret, because
	// presets live in committed YAML that GET /workflows/{id} exposes.
	secret := load(t, `
name: t
inputs:
  api_token: { secret: true }
presets:
  p:
    values:
      api_token: "hunter2"
steps: [{id: s, run: echo}]`)
	err = Validate(secret)
	if err == nil || !strings.Contains(err.Error(), "secret") {
		t.Fatalf("a preset supplying a secret must be rejected: %v", err)
	}
}

// HasDefault must survive `default: false` and `default: 0`, which a
// zero-value check would discard.
func TestHasDefaultDistinguishesFalsyDefaults(t *testing.T) {
	wf := load(t, `
name: t
inputs:
  flag:  { type: bool, default: false }
  count: { type: int,  default: 0 }
  none:  { type: int }
steps: [{id: s, run: echo}]`)
	if !wf.Inputs["flag"].HasDefault {
		t.Error("default: false must set HasDefault")
	}
	if !wf.Inputs["count"].HasDefault {
		t.Error("default: 0 must set HasDefault")
	}
	if wf.Inputs["none"].HasDefault {
		t.Error("absent default must not set HasDefault")
	}
}
