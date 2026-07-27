package compile

import (
	"testing"

	"github.com/bkum/weftly/internal/schema"
)

func TestCompileImplicitNeedsChainsNamedSteps(t *testing.T) {
	wf := &schema.Workflow{Steps: []schema.Step{
		{ID: "a", ActionType: "run"},
		{ID: "b", ActionType: "run"},
		{ID: "c", ActionType: "run"},
	}}
	g := Compile(wf)
	if len(g.Order) != 3 {
		t.Fatalf("want 3 nodes, got %d", len(g.Order))
	}
	if len(g.Order[0].Needs) != 0 {
		t.Errorf("first node should have no needs, got %v", g.Order[0].Needs)
	}
	if got := g.Order[1].Needs; len(got) != 1 || got[0] != "a" {
		t.Errorf("b.needs = %v, want [a]", got)
	}
	if got := g.Order[2].Needs; len(got) != 1 || got[0] != "b" {
		t.Errorf("c.needs = %v, want [b]", got)
	}
}

func TestCompileExplicitNeedsWins(t *testing.T) {
	wf := &schema.Workflow{Steps: []schema.Step{
		{ID: "a", ActionType: "run"},
		{ID: "b", ActionType: "run"},
		{ID: "c", ActionType: "run", Needs: []string{"a"}},
	}}
	g := Compile(wf)
	if got := g.Order[2].Needs; len(got) != 1 || got[0] != "a" {
		t.Errorf("c.needs override lost: %v", got)
	}
}

func TestCompileUnnamedStepsInheritPrevNamedID(t *testing.T) {
	wf := &schema.Workflow{Steps: []schema.Step{
		{ID: "a", ActionType: "run"},
		{ActionType: "summary"},
		{ActionType: "upload"},
		{ID: "b", ActionType: "run"},
	}}
	g := Compile(wf)
	// summary+upload have no id, so both inherit implicit needs of "a"
	// and don't advance prevID; b then also implicitly needs "a".
	if got := g.Order[1].Needs; len(got) != 1 || got[0] != "a" {
		t.Errorf("summary.needs = %v, want [a]", got)
	}
	if got := g.Order[2].Needs; len(got) != 1 || got[0] != "a" {
		t.Errorf("upload.needs = %v, want [a]", got)
	}
	if got := g.Order[3].Needs; len(got) != 1 || got[0] != "a" {
		t.Errorf("b.needs = %v, want [a]", got)
	}
}

func TestCompileCopiesStepFieldsIntoNode(t *testing.T) {
	env := map[string]string{"K": "V"}
	outs := map[string]string{"x": ".body.x"}
	wf := &schema.Workflow{Steps: []schema.Step{{
		ID:              "a",
		Name:            "the a step",
		ActionType:      "http",
		If:              "success()",
		Env:             env,
		ContinueOnError: true,
		Shell:           "bash",
		Container:       "alpine",
		Outputs:         outs,
		ForEach:         "steps.list.outputs.items",
	}}}
	n := Compile(wf).Order[0]
	if n.Name != "the a step" || n.Action != "http" || n.If != "success()" {
		t.Errorf("meta not copied: %+v", n)
	}
	if !n.ContinueOnError || n.Shell != "bash" || n.Container != "alpine" {
		t.Errorf("flags not copied: %+v", n)
	}
	if n.Env["K"] != "V" || n.OutputsMap["x"] != ".body.x" {
		t.Errorf("maps not copied: env=%v outs=%v", n.Env, n.OutputsMap)
	}
	if n.ForEach != "steps.list.outputs.items" {
		t.Errorf("foreach not copied: %q", n.ForEach)
	}
}
