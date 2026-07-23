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
//
// Dependency model (spec §5, "needs optional; else implicit order"):
//   - When a step declares `needs:`, those become its dependencies verbatim.
//   - When a step declares no `needs:`, the immediately-preceding named step
//     becomes an implicit dependency. This preserves reproducibility for
//     GHA-style sequential workflows while letting authors opt into
//     parallelism by declaring `needs:` on independent branches.
//
// Steps without an id (summary / upload / assert) do NOT participate in the
// implicit chain because they can't be referenced anyway; they are simply
// appended after all preceding named steps (implicit needs = previous named
// step's id).
func Compile(wf *schema.Workflow) *ir.Graph {
	g := &ir.Graph{Workflow: wf, Order: make([]*ir.StepNode, 0, len(wf.Steps))}
	var prevID string
	for i := range wf.Steps {
		s := &wf.Steps[i]
		needs := s.Needs
		if len(needs) == 0 && prevID != "" {
			needs = []string{prevID}
		}
		g.Order = append(g.Order, &ir.StepNode{
			ID:              s.ID,
			Name:            s.Name,
			Action:          s.ActionType,
			Config:          s,
			If:              s.If,
			Needs:           needs,
			Env:             s.Env,
			ContinueOnError: s.ContinueOnError,
			Timeout:         s.Timeout,
			Shell:           s.Shell,
			Container:       s.Container,
			Retry:           s.Retry,
			OutputsMap:      s.Outputs,
		})
		if s.ID != "" {
			prevID = s.ID
		}
	}
	return g
}
