package actions

import (
	"context"

	"github.com/bkum/weftly/internal/events"
)

func init() { Register(&includeOutputsAction{}) }

// includeOutputsAction is the synthesized action attached to the hidden
// bookkeeping node the compiler emits after every step-level include
// (spec §5.2). It evaluates each entry of the child workflow's top-level
// `outputs:` map in the child's scope and returns them as this node's
// own Outputs so the parent's `steps.<include-id>.outputs.X` resolves
// through the ordinary step-view path.
//
// The step's OutputsMap is populated by the compiler from the child's
// `outputs:`; the engine's post-Run outputs-mapping pass evaluates each
// expression through the node's Scope, so this action itself has no
// per-output work to do. It exists so the engine can dispatch a step
// that produces the declared outputs and nothing else.
//
// Hidden=true on the compiler node ensures renderers suppress it under
// non-verbose output — it's plumbing, not user-authored work.
type includeOutputsAction struct{}

func (includeOutputsAction) Type() string                  { return "include_outputs" }
func (includeOutputsAction) Validate(cfg StepConfig) error { return nil }

func (includeOutputsAction) Run(ctx context.Context, sc *StepContext) (Outputs, error) {
	// The engine's post-Run outputs mapping evaluates OutputsMap in the
	// node's Scope, so all we need here is a successful return. Emit a
	// low-noise Info log so a debugging operator running --verbose sees
	// the shim fire, but skip it in the default renderer path.
	if sc.Emit != nil {
		sc.Emit(events.StepLog{StepID: sc.StepID, Stream: events.Info, Line: "include: surfacing declared outputs"})
	}
	return Outputs{}, nil
}
