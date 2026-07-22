package expr

import (
	"testing"
)

func env(t *testing.T) Env {
	t.Helper()
	return Env{
		Inputs: map[string]any{
			"partner_name": "Acme",
			"count":        3,
		},
		Steps: map[string]StepView{
			"lookup": {
				Status: "success",
				Outputs: map[string]any{
					"exists": true,
					"total":  0,
					"name":   "Acme",
				},
			},
		},
		Env:     map[string]string{"REGION": "us-east-1"},
		Secrets: map[string]string{"token": "s3cr3t"},
		Run:     RunMeta{ID: "run-1", Workspace: "/tmp/ws"},
	}
}

func TestEvaluate(t *testing.T) {
	e := New()
	cases := []struct {
		expr string
		want any
	}{
		{`inputs.partner_name`, "Acme"},
		{`inputs.count > 2`, true},
		{`steps.lookup.outputs.exists`, true},
		{`!steps.lookup.outputs.exists`, false},
		{`steps.lookup.status == "success"`, true},
		{`env.REGION`, "us-east-1"},
		{`secrets.token`, "s3cr3t"},
		{`run.id`, "run-1"},
		{`"hello world" contains "world"`, true},
		{`"abcdef" startsWith "abc"`, true},
		{`"abcdef" endsWith "def"`, true},
		{`default(nil, "fb")`, "fb"},
		{`fromJSON("{\"a\":1}").a`, float64(1)},
		{`toJSON(inputs)`, ``}, // not checked (order); only ensure it evaluates
	}
	for _, tc := range cases {
		got, err := e.Evaluate(tc.expr, env(t))
		if err != nil {
			t.Errorf("%s: %v", tc.expr, err)
			continue
		}
		if tc.want == "" && tc.expr == `toJSON(inputs)` {
			if _, ok := got.(string); !ok {
				t.Errorf("toJSON: want string result, got %T", got)
			}
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %v (%T), want %v (%T)", tc.expr, got, got, tc.want, tc.want)
		}
	}
}

func TestInterpolate(t *testing.T) {
	e := New()
	cases := []struct {
		name string
		src  string
		want any
	}{
		{"literal", "hello", "hello"},
		{"single wrap preserves bool", "${{ steps.lookup.outputs.exists }}", true},
		{"single wrap preserves int", "${{ inputs.count }}", 3},
		{"mixed becomes string", "hi ${{ inputs.partner_name }}", "hi Acme"},
		{"no interpolation", "plain text", "plain text"},
	}
	for _, tc := range cases {
		got, err := e.Interpolate(tc.src, env(t))
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %v (%T), want %v (%T)", tc.name, got, got, tc.want, tc.want)
		}
	}
}

func TestEvaluateBoolTruthy(t *testing.T) {
	e := New()
	cases := map[string]bool{
		`inputs.count > 0`:                    true,
		`inputs.count == 0`:                   false,
		`steps.lookup.outputs.exists`:         true,
		`!steps.lookup.outputs.exists`:        false,
		`steps.lookup.outputs.name == "Acme"`: true,
	}
	for src, want := range cases {
		got, err := e.EvaluateBool(src, env(t))
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		if got != want {
			t.Errorf("%s: got %v, want %v", src, got, want)
		}
	}
}

func TestUnterminatedSpan(t *testing.T) {
	e := New()
	_, err := e.InterpolateString("hi ${{ inputs.x ", Env{})
	if err == nil {
		t.Fatal("expected error on unterminated span")
	}
}
