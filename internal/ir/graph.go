// Package ir is the intermediate representation the engine executes. In
// Phase 1 the "graph" is a topologically ordered slice of nodes; the
// abstraction exists so a future DAG parallel scheduler and multi-format
// front-ends can plug in without changing the executor's contract.
package ir

import (
	"time"

	"github.com/bkum/weftly/internal/schema"
)

// StepNode carries everything the executor needs for one step.
type StepNode struct {
	ID              string
	Name            string
	Action          string
	Config          *schema.Step // holds the raw ActionNode + step meta
	If              string       // optional; empty means "always run"
	Needs           []string
	Env             map[string]string
	ContinueOnError bool
	Timeout         time.Duration
	Shell           string
	Container       string            // opt-in container image for run steps
	Retry           *schema.Retry     // opt-in retry policy
	ForEach         string            // expression: list to fan out over
	OutputsMap      map[string]string // declared outputs for http/template

	// SkipReason is set by the scheduler when this node is being
	// short-circuited because an upstream fatal failed. The executor
	// checks this before dispatching, emits StepStarted + StepFinished
	// with status Skipped, and does not run the action. Empty in the
	// normal path.
	SkipReason string

	// --- workflow-composition (step-level `include:`) fields ---
	//
	// LocalID is the id as authored inside its own file. For top-level
	// steps it equals ID; for children expanded under an include it is
	// the un-qualified name (ID = "<include-id>.<LocalID>").
	LocalID string
	// Scope is nil for top-level steps. When set, the engine resolves
	// `inputs.*` and `steps.*` through the scope chain before evaluating
	// this node's expressions.
	Scope *Scope
	// Hidden marks synthesized bookkeeping nodes (the __include_outputs
	// node emitted for every step-level include). Renderers hide them
	// unless the user asks for verbose output — they're plumbing, not
	// work the operator authored.
	Hidden bool
	// SourceFile is the absolute path of the YAML file that authored
	// this node. Powers `workflow.dir` and improves diagnostic locality
	// (a compile error in an included file names the child file, not
	// the caller).
	SourceFile string
	// RunAlways marks a teardown node from a `finally:` block. The
	// scheduler dispatches these even when an upstream step failed —
	// the cascade-skip that protects ordinary downstream work is
	// precisely wrong for teardown, which exists to run after failure.
	RunAlways bool
}

// Scope is the compile-time surface every included step's expressions
// resolve through. It lets one workflow call another without the
// expression evaluator having to know composition exists.
//
// A depth-2 include chain (root -> A -> B) produces:
//
//	root: nodes with Scope = nil
//	A:    child nodes with Scope = {Prefix:"a", Parent:nil,        Inputs=... , StepIDs=...}
//	B:    grandchild nodes with Scope = {Prefix:"a.b", Parent: A's scope, ...}
//
// `inputs.x` at depth 2 walks Parent up until it hits a binding
// (`Binding.IsExpr` -> evaluate in Parent scope) or the top-level run
// inputs. `steps.child.outputs.y` inside B resolves via B.StepIDs; a
// lookup that misses in a scope falls through to top-level results —
// which is why a child cannot reach parent-scope step results
// (isolation-by-construction, spec §6).
type Scope struct {
	Prefix  string             // qualified prefix, e.g. "edi" or "a.b"
	Dir     string             // directory of the included file → workflow.dir
	Parent  *Scope             // one scope up the chain; nil at depth 1
	Inputs  map[string]Binding // child input name → how to resolve it
	StepIDs map[string]string  // child local id → fully-qualified id
	// Outputs is the child workflow's top-level `outputs:` contract,
	// carried on the scope so the synthesized __include_outputs node can
	// evaluate each entry in the child's own scope at runtime.
	Outputs map[string]string
}

// Binding captures how a single child input is fulfilled. Compile-time
// resolves the shape (expression vs literal); runtime resolves the
// value (parent step outputs aren't known at compile time).
type Binding struct {
	Expr    string // expression to evaluate in Parent scope (from `with:`)
	Literal any    // literal default from the child's `inputs:`
	IsExpr  bool
	Secret  bool // child declared `secret: true`, or expression taints
	// IfSet marks a binding that came from `with_if_set:` — when the
	// expression evaluates empty, Fallback is used instead of the empty
	// value, so the child keeps its own default.
	IfSet    bool
	Fallback any
}

// Graph is the execution plan.
type Graph struct {
	Workflow *schema.Workflow
	Order    []*StepNode
}
