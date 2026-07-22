// Package actions defines the Action interface (spec §8.3) and the built-in
// registry. Every workflow verb (`run`, `http`, `template`, ...) implements
// this interface and self-registers via init().
//
// The engine dispatches to actions through StepContext. StepContext.Emit is
// the only channel by which an action produces user-visible output — no
// action should ever write to os.Stdout/os.Stderr directly.
package actions

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/expr"
	"github.com/bkum/weftly/internal/secrets"
	"gopkg.in/yaml.v3"
)

// StepConfig is the raw yaml.Node of a step's action value (e.g. the string
// for `run:` or the mapping for `http:`). Each action decodes it into its
// own typed config in Validate/Run.
type StepConfig = *yaml.Node

// Outputs is the map of named values a step produces. The spec §8.3 shows
// map[string]string; we widen to map[string]any so that http and template
// actions can preserve bool/number types across the ${{ }} boundary
// (Appendix A's `if: ${{ !steps.lookup.outputs.exists }}` requires this).
// The engine converts to string form for state.json persistence.
type Outputs map[string]any

// StepContext is everything an action needs to execute one step.
type StepContext struct {
	StepID   string
	StepName string
	Action   string     // action type key
	Config   StepConfig // raw yaml for this action

	Inputs       map[string]any
	Steps        map[string]expr.StepView
	Env          map[string]string
	Secrets      *secrets.Registry
	Workdir      string   // per-run workspace (step cwd)
	ArtifactsDir string   // per-run artifacts destination
	ExprEnv      expr.Env // pre-built evaluation env for this step

	Emit func(events.Event)
	Expr *expr.Evaluator

	Shell   string        // default shell (overridden per-step if set)
	Timeout time.Duration // per-step timeout; 0 = none
	Strict  bool          // --strict: inline ${{ }} in run: bodies is an error

	// HTTPDefaults are workflow-level http defaults merged into every http
	// action call. Populated by the engine from schema.Defaults.HTTP.
	HTTPTimeout time.Duration
	HTTPHeaders map[string]string

	// Response is a slot the http action fills in before returning so that
	// the engine's post-Run outputs mapping (`outputs: { id: "${{ response.
	// body.partnerId }}" }`) can see it. Other actions leave it nil.
	Response any
}

// Log is a convenience for emitting a StepLog line. A nil Emit (e.g. in a
// test without a bus) is silently dropped.
func (sc *StepContext) Log(stream events.Stream, line string) {
	if sc.Emit == nil {
		return
	}
	sc.Emit(events.StepLog{StepID: sc.StepID, Stream: stream, Line: line})
}

// Action is the seam every built-in implements.
type Action interface {
	Type() string
	Validate(cfg StepConfig) error
	Run(ctx context.Context, sc *StepContext) (Outputs, error)
}

var (
	regMu    sync.RWMutex
	registry = map[string]Action{}
)

// Register makes an action discoverable by its Type().
func Register(a Action) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, exists := registry[a.Type()]; exists {
		panic(fmt.Sprintf("actions: %q already registered", a.Type()))
	}
	registry[a.Type()] = a
}

// Get returns the registered action for name, or nil if unknown.
func Get(name string) Action {
	regMu.RLock()
	defer regMu.RUnlock()
	return registry[name]
}

// Known returns the sorted list of registered action names (for diagnostics).
func Known() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	names := make([]string, 0, len(registry))
	for k := range registry {
		names = append(names, k)
	}
	return names
}
