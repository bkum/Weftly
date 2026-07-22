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
	OutputsMap      map[string]string // declared outputs for http/template
}

// Graph is the execution plan.
type Graph struct {
	Workflow *schema.Workflow
	Order    []*StepNode
}
