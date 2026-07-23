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

// Input is a declared parameter to a workflow.
type Input struct {
	Description string    `yaml:"description"`
	Required    bool      `yaml:"required"`
	Default     any       `yaml:"default"`
	Secret      bool      `yaml:"secret"`
	Type        InputType `yaml:"type"`
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
	Include []string `yaml:"include"`
	// Cleanup runs sequentially after the main graph completes,
	// regardless of the run's outcome. Cleanup steps get success() /
	// failure() / cancelled() populated from the run's aggregate
	// status so `if:` gates work.
	Cleanup []Step `yaml:"cleanup"`

	// Source retains the parsed YAML root node for line-number-aware error
	// reporting. Nil after a bare struct construction (e.g. tests).
	Source *yaml.Node `yaml:"-"`
}

// The set of action keys recognised on a step. Exactly one must be present.
var actionKeys = []string{"run", "http", "template", "prompt", "assert", "summary", "upload", "wait", "parse", "notify"}

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

	// Populated by custom unmarshal. ActionType is one of actionKeys.
	// ActionNode holds the raw YAML for that action's config so per-action
	// decoders can Decode it.
	ActionType string     `yaml:"-"`
	ActionNode *yaml.Node `yaml:"-"`

	// Source is the mapping node for this step (line-number aware errors).
	Source *yaml.Node `yaml:"-"`
}
