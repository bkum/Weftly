package actions

import (
	"context"
	"testing"

	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/expr"
)

func mustParseYAMLNode(t *testing.T, src string) *(interface{ Decode(any) error }) {
	// placeholder — the real helper `mustParseYAML` already exists in this
	// package's test files; declared here only to keep imports honest for
	// this file if used standalone.
	t.Helper()
	_ = src
	return nil
}

func TestWaitSucceedsOnFirstProbe(t *testing.T) {
	// Use shell built-in `true` (rather than /bin/true) so this test
	// doesn't hinge on where the binary sits on a given host — some
	// CI runners have it under /usr/bin, and `sh -c /path` failed on
	// macOS runners even though the file existed.
	cfg := mustParseYAML(t, `
command: "true"
interval: 10ms
timeout: 1s
`)
	sc := &StepContext{
		Config:  cfg,
		Workdir: t.TempDir(),
		Expr:    expr.New(),
		ExprEnv: expr.Env{},
		Emit:    func(events.Event) {},
	}
	outs, err := waitAction{}.Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if outs["attempts"] != 1 {
		t.Errorf("want 1 attempt, got %v", outs["attempts"])
	}
}

func TestWaitTimesOut(t *testing.T) {
	cfg := mustParseYAML(t, `
command: "false"
interval: 20ms
timeout: 80ms
`)
	sc := &StepContext{
		Config:  cfg,
		Workdir: t.TempDir(),
		Expr:    expr.New(),
		ExprEnv: expr.Env{},
		Emit:    func(events.Event) {},
	}
	_, err := waitAction{}.Run(context.Background(), sc)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestParseJSONFlattensTopLevel(t *testing.T) {
	cfg := mustParseYAML(t, `
source: '{"partner_id":42,"name":"acme"}'
format: json
`)
	sc := &StepContext{
		Config:  cfg,
		Expr:    expr.New(),
		ExprEnv: expr.Env{},
		Emit:    func(events.Event) {},
	}
	outs, err := parseAction{}.Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if outs["partner_id"] != float64(42) {
		t.Errorf("partner_id: got %v (%T)", outs["partner_id"], outs["partner_id"])
	}
	if outs["name"] != "acme" {
		t.Errorf("name: got %v", outs["name"])
	}
}

func TestParseRegexNamedGroups(t *testing.T) {
	cfg := mustParseYAML(t, `
source: "version=1.7 build=abc123"
format: regex
pattern: 'version=(?P<ver>[\d.]+) build=(?P<build>\w+)'
`)
	sc := &StepContext{
		Config:  cfg,
		Expr:    expr.New(),
		ExprEnv: expr.Env{},
		Emit:    func(events.Event) {},
	}
	outs, err := parseAction{}.Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if outs["ver"] != "1.7" {
		t.Errorf("ver: got %v", outs["ver"])
	}
	if outs["build"] != "abc123" {
		t.Errorf("build: got %v", outs["build"])
	}
}

func TestParseRejectsUnknownFormat(t *testing.T) {
	cfg := mustParseYAML(t, `
source: hi
format: bogus
`)
	if err := (parseAction{}).Validate(cfg); err == nil {
		t.Fatal("expected validation error for bogus format")
	}
}

// mustParseYAMLNode is unused but the pattern above uses mustParseYAML,
// defined elsewhere in this package's test files.
var _ = mustParseYAMLNode
