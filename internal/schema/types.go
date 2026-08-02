// Package schema defines the workflow YAML data model, the loader that
// turns YAML into that model, and the static validation rules of spec §5.
//
// A Workflow is intentionally close to the on-disk YAML: it holds the raw
// action configuration nodes (see Step.actionKey / Step.rawAction) so the
// compiler in internal/compile can build an IR without re-parsing.
package schema

import (
	"time"

	"gopkg.in/yaml.v3"
)

// InputType is the declared type of a workflow input.
type InputType string

const (
	InputString InputType = "string"
	InputNumber InputType = "number"
	InputBool   InputType = "bool"
)

// Input is a declared parameter to a workflow. JSON tags mirror the
// YAML tags in lowercase so GET /workflows/{id} exposes the same
// field names the SPA form-renderer reads (description, required,
// default, secret, type, enum) — without them Go's default JSON
// marshaller would emit Go-cased field names ("Description",
// "Required", ...) and every form-field extra would silently vanish
// from the payload.
type Input struct {
	Description string    `yaml:"description" json:"description,omitempty"`
	Required    bool      `yaml:"required"    json:"required,omitempty"`
	Default     any       `yaml:"default"     json:"default,omitempty"`
	Secret      bool      `yaml:"secret"      json:"secret,omitempty"`
	Type        InputType `yaml:"type"        json:"type,omitempty"`
	// Enum, when non-empty, restricts the input to one of the listed
	// values. Renders as a picklist in the SPA and is validated at
	// input-resolution time.
	Enum []any `yaml:"enum" json:"enum,omitempty"`
}

// HTTPDefaults holds workflow-level defaults merged into every http step.
type HTTPDefaults struct {
	Timeout time.Duration     `yaml:"timeout"`
	Headers map[string]string `yaml:"headers"`
}

// Defaults holds workflow-level defaults.
type Defaults struct {
	Shell string       `yaml:"shell"`
	HTTP  HTTPDefaults `yaml:"http"`
}

// Workflow is the top-level document.
type Workflow struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	Requires    []string          `yaml:"requires"`
	Inputs      map[string]Input  `yaml:"inputs"`
	Env         map[string]string `yaml:"env"`
	Defaults    Defaults          `yaml:"defaults"`
	Steps       []Step            `yaml:"steps"`
	// Include lists other workflow YAML files (paths relative to the
	// including file) whose steps + env + defaults.shell get merged
	// into this workflow at Load time. Cycles are detected. Included
	// name / inputs / description / requires are ignored — includes are
	// step libraries, not full workflows.
	//
	// This is the top-level "prelude" include (Phase 4). The step-level
	// `include:` (with `with:`) is a different feature — see Step.Include.
	Include []string `yaml:"include"`
	// Library marks this file as a fragment meant only to be included by
	// another workflow, never run on its own. A library is excluded from
	// the served catalogue, rejected as a direct `POST /runs` target, and
	// rejected at schedule-load time.
	//
	// This is an authorisation control, not presentation. Without it every
	// fragment in a toolkit is an ordinary catalogue entry: independently
	// triggerable by any principal holding `workflows: "*"`, and
	// schedulable — even though it was written to run only as part of a
	// caller that supplies its inputs.
	Library bool `yaml:"library"`
	// Outputs is the top-level output contract of the workflow, evaluated
	// in the workflow's own scope after its steps have run. When this
	// workflow is used as a step-level include, the parent references
	// `steps.<include-id>.outputs.<name>` — undeclared names are a
	// compile-time error.
	Outputs map[string]string `yaml:"outputs"`
	// Cleanup runs sequentially after the main graph completes,
	// regardless of the run's outcome. Cleanup steps get success() /
	// failure() / cancelled() populated from the run's aggregate
	// status so `if:` gates work.
	Cleanup []Step `yaml:"cleanup"`
	// Finally is scope teardown: steps that run after THIS workflow's
	// own steps complete, whatever their outcome. Where `cleanup:` is
	// run-level and fires once at the very end, `finally:` belongs to
	// the workflow that declares it — so an included fragment can tear
	// down just the resources it created without knowing anything
	// about its caller.
	//
	// Inside a `finally:` block, success() / failure() report the
	// ENCLOSING SCOPE's status, not the run's. That is the distinction
	// that makes teardown decidable: a fragment wants to know whether
	// it left a half-built tenant behind, not whether some unrelated
	// sibling failed.
	Finally []Step `yaml:"finally"`

	// Source retains the parsed YAML root node for line-number-aware error
	// reporting. Nil after a bare struct construction (e.g. tests).
	Source *yaml.Node `yaml:"-"`
	// Path is the absolute filesystem path this workflow was Load()ed
	// from, or empty for bare struct construction / streaming Parse().
	// Used by the compiler to resolve relative step-level `include:`
	// paths against the including file's directory (not cwd), and to
	// power the `workflow.dir` expression namespace.
	Path string `yaml:"-"`
}

// The set of action keys recognised on a step. Exactly one must be present.
// `include` is a special key handled by the compiler (not the action
// registry): steps with `include:` are expanded into the child workflow's
// steps at compile time, so by the time the scheduler sees the IR, no
// StepNode carries Action="include".
var actionKeys = []string{"run", "http", "template", "prompt", "assert", "summary", "upload", "wait", "parse", "notify", "include"}

// Retry declares an automatic-retry policy for a step. Attempts is the
// total number of tries (including the first), so `attempts: 3` means
// "try up to 3 times". Delay is the base wait between attempts;
// Backoff=exponential doubles it each round. On restricts which
// terminal statuses trigger a retry — omitted defaults to failed only,
// which matches the intuition that a timeout budget was already the
// operator's choice for "this step is too slow".
type Retry struct {
	Attempts int           `yaml:"attempts"`
	Delay    time.Duration `yaml:"delay"`
	Backoff  string        `yaml:"backoff"` // "" (constant) | "linear" | "exponential"
	On       []string      `yaml:"on"`      // subset of {"failed", "timed-out"}
}

// Step is one node in a workflow. Exactly one of the action-shaped fields is
// populated after unmarshal; the raw yaml.Node for that action is exposed as
// Action so downstream consumers can decode it into an action-specific type.
type Step struct {
	ID              string            `yaml:"id"`
	Name            string            `yaml:"name"`
	If              string            `yaml:"if"`
	Needs           []string          `yaml:"needs"`
	Env             map[string]string `yaml:"env"`
	ContinueOnError bool              `yaml:"continue-on-error"`
	Timeout         time.Duration     `yaml:"timeout"`
	Shell           string            `yaml:"shell"`     // per-step override for run action
	Container       string            `yaml:"container"` // image ref; only valid with run action
	Retry           *Retry            `yaml:"retry"`     // opt-in retry policy on failure/timeout
	ForEach         string            `yaml:"for-each"`  // expression → list; runs step N times
	Outputs         map[string]string `yaml:"outputs"`

	// Include, when non-empty, marks this step as a step-level workflow
	// include. The compiler expands the referenced file's steps in place
	// under this step's id, wiring the parent-supplied `With:` map into
	// the child's inputs and exposing the child's top-level `outputs:`
	// contract at `steps.<this-id>.outputs.*`.
	Include string            `yaml:"include"`
	With    map[string]string `yaml:"with"`
	// WithIfSet binds a child input ONLY when the expression evaluates
	// to a non-empty value; otherwise the child's own `default:` stays
	// in force. This is the conditional-passthrough case that plain
	// `with:` gets wrong: a caller forwarding its own optional input
	// (`parties_json: "${{ inputs.parties_json }}"`) clobbers the
	// child's carefully-chosen default with "" whenever the caller's
	// input wasn't supplied.
	//
	// Deciding at runtime rather than compile time is required — the
	// bound expression can reference a prior step's output, whose
	// emptiness isn't knowable until that step runs.
	WithIfSet map[string]string `yaml:"with_if_set"`

	// Populated by custom unmarshal. ActionType is one of actionKeys.
	// ActionNode holds the raw YAML for that action's config so per-action
	// decoders can Decode it.
	ActionType string     `yaml:"-"`
	ActionNode *yaml.Node `yaml:"-"`

	// Source is the mapping node for this step (line-number aware errors).
	Source *yaml.Node `yaml:"-"`
}
