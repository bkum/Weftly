package actions

import (
	"context"
	"strings"
	"testing"

	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/expr"
	"github.com/bkum/weftly/internal/secrets"
)

func promptContext(t *testing.T, cfgSrc string, autoYes bool) *StepContext {
	t.Helper()
	return &StepContext{
		Config:  mustParseYAML(t, cfgSrc),
		Expr:    expr.New(),
		ExprEnv: expr.Env{},
		Secrets: secrets.NewRegistry(),
		Emit:    func(events.Event) {},
		AutoYes: autoYes,
	}
}

func TestPromptValidate(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		wantErr string
	}{
		{"missing message", `type: text`, "message"},
		{"unknown type", `message: hi
type: weird`, "unknown type"},
		{"select without options", `message: pick
type: select`, "options"},
		{"good text", `message: hi`, ""},
		{"good select", `message: pick
type: select
options: [a, b]`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := (promptAction{}).Validate(mustParseYAML(t, tc.src))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestPromptNonTTYUsesDefault(t *testing.T) {
	// stdin is a pipe during `go test`, so isInteractive() returns false —
	// the action must fall back to `default:`.
	sc := promptContext(t, `
message: pick
type: select
options: [a, b, c]
default: b
`, false)
	outs, err := (promptAction{}).Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if outs["value"] != "b" {
		t.Fatalf("got %v", outs["value"])
	}
}

func TestPromptNonTTYNoDefaultFails(t *testing.T) {
	sc := promptContext(t, `
message: pick
type: text
`, false)
	_, err := (promptAction{}).Run(context.Background(), sc)
	if err == nil || !strings.Contains(err.Error(), "non-interactive") {
		t.Fatalf("want non-interactive error, got %v", err)
	}
}

func TestPromptConfirmAutoYes(t *testing.T) {
	sc := promptContext(t, `
message: ok?
type: confirm
`, true)
	outs, err := (promptAction{}).Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if outs["value"] != true {
		t.Fatalf("want true, got %v (%T)", outs["value"], outs["value"])
	}
}

func TestPromptConfirmDefaultCoerced(t *testing.T) {
	sc := promptContext(t, `
message: proceed?
type: confirm
default: "y"
`, false)
	outs, err := (promptAction{}).Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if outs["value"] != true {
		t.Fatalf("want true, got %v", outs["value"])
	}
}

func TestPromptSecretRegistered(t *testing.T) {
	sc := promptContext(t, `
message: token?
type: text
default: "s3cret-value-abcdef"
secret: true
`, false)
	_, err := (promptAction{}).Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if got := sc.Secrets.Mask("leak: s3cret-value-abcdef here"); got != "leak: *** here" {
		t.Fatalf("secret not registered: %q", got)
	}
}
