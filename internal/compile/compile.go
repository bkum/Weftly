// Package compile turns a validated schema.Workflow into an executable
// ir.Graph. In Phase 1 execution order is the schema order — parallelism
// and explicit `needs`-driven scheduling are Phase 2. The seam is here so
// swapping in a topological scheduler later touches only this package.
package compile

import (
	"github.com/bkum/weftly/internal/ir"
	"github.com/bkum/weftly/internal/schema"
)

// Compile builds an ir.Graph from a workflow. It assumes the workflow has
// already passed schema.Validate; the compiler does not re-check invariants.
func Compile(wf *schema.Workflow) *ir.Graph {
	g := &ir.Graph{Workflow: wf, Order: make([]*ir.StepNode, 0, len(wf.Steps))}
	for i := range wf.Steps {
		s := &wf.Steps[i]
		g.Order = append(g.Order, &ir.StepNode{
			ID:              s.ID,
			Name:            s.Name,
			Action:          s.ActionType,
			Config:          s,
			If:              s.If,
			Needs:           s.Needs,
			Env:             s.Env,
			ContinueOnError: s.ContinueOnError,
			Timeout:         s.Timeout,
			Shell:           s.Shell,
			OutputsMap:      s.Outputs,
		})
	}
	return g
}
