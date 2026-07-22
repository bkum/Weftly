package actions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bkum/weftly/internal/expr"
)

func TestTemplateActionInline(t *testing.T) {
	ws := t.TempDir()
	cfg := mustParseYAML(t, `
inline: |
  hello {{ .who }} count={{ .n }}
dest: ./out/greet.txt
vars:
  who: "${{ inputs.who }}"
  n: "${{ inputs.n }}"
`)
	sc := &StepContext{
		Config:  cfg,
		Workdir: ws,
		Expr:    expr.New(),
		ExprEnv: expr.Env{Inputs: map[string]any{"who": "world", "n": 3}},
	}
	if _, err := (templateAction{}).Run(context.Background(), sc); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(ws, "out", "greet.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hello world count=3") {
		t.Fatalf("template output: %q", data)
	}
}

func TestTemplateActionRejectsTraversal(t *testing.T) {
	ws := t.TempDir()
	cfg := mustParseYAML(t, `
inline: hi
dest: ../escape.txt
`)
	sc := &StepContext{Config: cfg, Workdir: ws, Expr: expr.New(), ExprEnv: expr.Env{}}
	_, err := (templateAction{}).Run(context.Background(), sc)
	if err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("expected traversal rejection, got %v", err)
	}
}
