// Package gha ingests a GitHub Actions workflow YAML and translates the
// supported subset into an equivalent weftly workflow.
//
// This is a compile-time seam, not a runtime one: `weftly import-gha`
// converts the file to a reviewable weftly YAML, then the operator runs
// it through the normal engine. We deliberately don't try to execute
// arbitrary `uses:` marketplace actions — the trust model is "curated
// catalogue", and pulling & running third-party JS/Docker actions on
// demand blows that up. Unsupported constructs are skipped with a note
// so the caller can see what didn't translate.
package gha

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Workflow is the shape of a GHA workflow file — just enough of it to do
// the translation. Extraneous fields (`on:`, `permissions:`, `defaults:`,
// job-level `strategy:` / `matrix:`) are parsed and then dropped with a
// note so a workflow that leans on them doesn't silently lose fidelity.
type Workflow struct {
	Name string            `yaml:"name"`
	Env  map[string]string `yaml:"env"`
	Jobs map[string]Job    `yaml:"jobs"`
	On   any               `yaml:"on"`
}

type Job struct {
	Name       string            `yaml:"name"`
	RunsOn     any               `yaml:"runs-on"`
	Env        map[string]string `yaml:"env"`
	Needs      any               `yaml:"needs"`
	Steps      []Step            `yaml:"steps"`
	Strategy   any               `yaml:"strategy"`
	Defaults   any               `yaml:"defaults"`
	Container  any               `yaml:"container"`
	Timeout    int               `yaml:"timeout-minutes"`
	Outputs    map[string]string `yaml:"outputs"`
	Concurrent any               `yaml:"concurrency"`
}

type Step struct {
	Name             string            `yaml:"name"`
	ID               string            `yaml:"id"`
	If               string            `yaml:"if"`
	Run              string            `yaml:"run"`
	Uses             string            `yaml:"uses"`
	With             map[string]any    `yaml:"with"`
	Env              map[string]string `yaml:"env"`
	Shell            string            `yaml:"shell"`
	WorkingDirectory string            `yaml:"working-directory"`
	ContinueOnError  bool              `yaml:"continue-on-error"`
	TimeoutMinutes   int               `yaml:"timeout-minutes"`
}

// ImportResult carries the converted YAML plus every translation note
// (skipped uses:, dropped GHA-only fields, id sanitisation). Callers
// print the notes so the operator can spot fidelity gaps.
type ImportResult struct {
	YAML  []byte
	Notes []string
	Job   string // which job id was picked
}

// Options tunes the importer.
type Options struct {
	// JobID picks a specific job when the workflow declares multiple.
	// Empty = pick the first alphabetically (deterministic for tests).
	JobID string
}

// Import reads a GHA workflow YAML and returns the converted weftly YAML.
// The result is always a self-contained document; a caller can pipe it
// straight into `weftly validate` or drop it into a catalogue directory.
func Import(r io.Reader, opts Options) (*ImportResult, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var wf Workflow
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("gha: parse: %w", err)
	}
	if len(wf.Jobs) == 0 {
		return nil, errors.New("gha: workflow declares no jobs")
	}
	jobID := opts.JobID
	if jobID == "" {
		jobID = firstJobID(wf.Jobs)
	}
	job, ok := wf.Jobs[jobID]
	if !ok {
		return nil, fmt.Errorf("gha: job %q not found (available: %s)", jobID, strings.Join(sortedKeys(wf.Jobs), ", "))
	}

	notes := []string{}
	if len(wf.Jobs) > 1 {
		notes = append(notes, fmt.Sprintf("workflow has %d jobs; imported %q. Weftly has no matrix/multi-job runner — re-run with --job to pick another.", len(wf.Jobs), jobID))
	}
	if wf.On != nil {
		notes = append(notes, "dropped `on:` — weftly runs from CLI/server, not on push/PR triggers.")
	}
	if job.Strategy != nil {
		notes = append(notes, "dropped job `strategy:` (matrix/parallel is not part of weftly's model).")
	}
	if job.Container != nil {
		notes = append(notes, "dropped job-level `container:` — set `container:` per step instead (P3-M5).")
	}
	if job.Concurrent != nil {
		notes = append(notes, "dropped job `concurrency:` — weftly's server serialises by workflow id.")
	}
	if job.Defaults != nil {
		notes = append(notes, "dropped job `defaults:` — weftly has workflow-level `defaults.shell` only.")
	}

	// Assemble a schema.Workflow-shaped tree by hand rather than
	// depending on schema (avoids an import cycle and lets us emit
	// clean YAML without struct-tag rearrangement).
	out := map[string]any{
		"name": nonEmpty(wf.Name, jobID),
	}
	if len(wf.Env) > 0 {
		out["env"] = wf.Env
	}
	if len(job.Env) > 0 {
		if existing, ok := out["env"].(map[string]string); ok {
			for k, v := range job.Env {
				existing[k] = v
			}
		} else {
			out["env"] = job.Env
		}
	}

	steps := make([]map[string]any, 0, len(job.Steps))
	usedIDs := map[string]int{}
	for i, s := range job.Steps {
		mapped, stepNotes := convertStep(s, i, usedIDs)
		if mapped == nil {
			// Skipped step (e.g. uses: with no run:) — record and move on.
			notes = append(notes, stepNotes...)
			continue
		}
		notes = append(notes, stepNotes...)
		steps = append(steps, mapped)
	}
	if len(steps) == 0 {
		return nil, errors.New("gha: no importable steps (all were `uses:` or otherwise unsupported)")
	}
	out["steps"] = steps

	yout, err := yaml.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("gha: marshal: %w", err)
	}
	return &ImportResult{YAML: yout, Notes: notes, Job: jobID}, nil
}

// convertStep maps one GHA step to a weftly step (as a plain map, ready
// for YAML marshal). Returns nil if the step is unsupported (uses:
// without an accompanying run:).
func convertStep(s Step, index int, usedIDs map[string]int) (map[string]any, []string) {
	var notes []string
	if s.Uses != "" && s.Run == "" {
		notes = append(notes, fmt.Sprintf("skipped step[%d] `uses: %s` — weftly does not run marketplace actions; re-implement inline as `run:`.", index, s.Uses))
		return nil, notes
	}
	if s.Run == "" {
		notes = append(notes, fmt.Sprintf("skipped step[%d] with no `run:` body.", index))
		return nil, notes
	}
	step := map[string]any{
		"run": s.Run,
	}
	// id sanitisation: weftly ids are [a-z0-9_]+; GHA allows dashes and
	// mixed case. Convert & de-dup with a numeric suffix so the caller
	// can still reference outputs.
	id := sanitiseID(s.ID)
	if id == "" {
		id = fmt.Sprintf("step_%d", index+1)
	}
	if usedIDs[id] > 0 {
		id = fmt.Sprintf("%s_%d", id, usedIDs[id]+1)
	}
	usedIDs[id]++
	step["id"] = id
	if s.ID != "" && sanitiseID(s.ID) != s.ID {
		notes = append(notes, fmt.Sprintf("step[%d] id %q → %q (weftly ids match [a-z0-9_]+).", index, s.ID, id))
	}
	if s.Name != "" {
		step["name"] = s.Name
	}
	if s.If != "" {
		step["if"] = s.If
		if !looksLikeWeftlyExpr(s.If) {
			notes = append(notes, fmt.Sprintf("step %q: `if:` copied verbatim; GHA-specific functions (success(), failure(), etc.) won't evaluate under weftly.", id))
		}
	}
	if len(s.Env) > 0 {
		step["env"] = s.Env
	}
	if s.Shell != "" {
		step["shell"] = s.Shell
	}
	if s.WorkingDirectory != "" {
		notes = append(notes, fmt.Sprintf("step %q: `working-directory: %s` dropped — cd inside the script instead.", id, s.WorkingDirectory))
	}
	if s.ContinueOnError {
		step["continue-on-error"] = true
	}
	if s.TimeoutMinutes > 0 {
		step["timeout"] = (time.Duration(s.TimeoutMinutes) * time.Minute).String()
	}
	return step, notes
}

// looksLikeWeftlyExpr is a shallow heuristic — true when the expression
// doesn't contain any GHA-only helper by name. Kept intentionally
// conservative: false triggers a note, it never rewrites the expression.
var ghaOnlyHelpers = regexp.MustCompile(`\b(success|failure|always|cancelled|hashFiles|fromJSON|toJSON)\s*\(`)

func looksLikeWeftlyExpr(s string) bool { return !ghaOnlyHelpers.MatchString(s) }

var idClean = regexp.MustCompile(`[^a-z0-9_]+`)

func sanitiseID(s string) string {
	s = strings.ToLower(s)
	s = idClean.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	return s
}

func firstJobID(jobs map[string]Job) string {
	keys := sortedKeys(jobs)
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func nonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
