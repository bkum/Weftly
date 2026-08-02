package engine

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/bkum/weftly/internal/expr"
	"github.com/bkum/weftly/internal/schema"
)

// ResolveOptions carries everything input resolution needs beyond the
// workflow itself.
type ResolveOptions struct {
	Supplied     map[string]any // --input / --input-file / API inputs
	Preset       string         // --preset / API preset (empty = none)
	WorkflowDir  string         // for ${{ workflow.dir }} in defaults
	WorkspaceDir string         // for ${{ workspace.dir }} in defaults
	RunID        string
}

// resolveInputs merges declared workflow inputs with values supplied by
// the caller, applies a preset and defaults, coerces to the declared
// type, checks constraints, and returns the resolved map plus the subset
// of values that should be registered with the secret masker.
//
// Precedence, lowest to highest (spec §8):
//
//	default:  ->  preset  ->  WEFTLY_INPUT_<NAME> env  ->  supplied
//
// so `--preset qa --input cases=800` yields cases=800 with the rest of
// the preset applied, which is the obvious reading.
//
// Errors are COLLECTED, not returned on the first failure: a form with
// three bad fields should produce three errors in one report, not force
// three round trips.
func resolveInputs(wf *schema.Workflow, opts ResolveOptions) (map[string]any, []any, error) {
	supplied := opts.Supplied
	var ierrs schema.InputErrors

	// Preset layer. An unknown name is fatal on its own — continuing
	// would silently run with defaults the operator didn't ask for.
	presetVals := map[string]any{}
	if opts.Preset != "" {
		p, ok := wf.Presets[opts.Preset]
		if !ok {
			names := make([]string, 0, len(wf.Presets))
			for n := range wf.Presets {
				names = append(names, n)
			}
			sort.Strings(names)
			msg := fmt.Sprintf("unknown preset %q", opts.Preset)
			if len(names) > 0 {
				msg += " (available: " + strings.Join(names, ", ") + ")"
			} else {
				msg += " (this workflow declares no presets)"
			}
			return nil, nil, errors.New(msg)
		}
		for k, v := range p.Values {
			presetVals[k] = v
		}
	}

	out := map[string]any{}
	var secretVals []any
	ev := expr.New()

	// Defaults may reference other inputs, so resolve in dependency
	// order. ValidateInputSchema has already rejected cycles.
	for _, name := range schema.ResolutionOrder(wf.Inputs) {
		in := wf.Inputs[name]
		var raw any
		var have bool
		switch {
		case supplied != nil && hasKey(supplied, name):
			raw, have = supplied[name], true
		case hasKey(presetVals, name):
			raw, have = presetVals[name], true
		default:
			if v, ok := os.LookupEnv("WEFTLY_INPUT_" + strings.ToUpper(name)); ok {
				raw, have = v, true
			} else if in.HasDefault || in.Default != nil {
				// A default expression is evaluated ONLY when the input
				// wasn't supplied — supplying a value must never trigger
				// a default's side-effect-free-but-still-wasteful eval.
				raw, have = in.Default, in.Default != nil || in.HasDefault
				if s, ok := raw.(string); ok && strings.Contains(s, "${{") {
					env := expr.Env{
						Inputs:       out, // only inputs resolved so far
						Steps:        map[string]expr.StepView{},
						Env:          map[string]string{},
						Secrets:      map[string]string{},
						Run:          expr.RunMeta{ID: opts.RunID, Workspace: opts.WorkspaceDir},
						WorkflowDir:  opts.WorkflowDir,
						WorkspaceDir: opts.WorkspaceDir,
					}
					v, err := ev.Interpolate(s, env)
					if err != nil {
						ierrs = append(ierrs, schema.InputError{
							Input: name, Line: in.Line,
							Message: fmt.Sprintf("default expression failed: %v", err),
						})
						continue
					}
					raw = v
				}
			}
		}
		if !have || raw == nil {
			if in.Required {
				ierrs = append(ierrs, schema.InputError{
					Input: name, Line: in.Line, Message: "is required but was not supplied",
				})
			}
			continue
		}
		v, cerr := in.Coerce(name, raw)
		if cerr != nil {
			cerr.Line = in.Line
			ierrs = append(ierrs, *cerr)
			continue
		}
		// type: path with must_exist fails here rather than at the step
		// that opens the file — a missing profile registry should name
		// the input, not surface as a shell error four steps later.
		if in.EffectiveType() == schema.InputPath && in.MustExist {
			if p, ok := v.(string); ok {
				if _, err := os.Stat(p); err != nil {
					ierrs = append(ierrs, schema.InputError{
						Input: name, Line: in.Line,
						Message: fmt.Sprintf("path %q does not exist (must_exist: true)", p),
					})
					continue
				}
			}
		}
		out[name] = v
		if in.Secret {
			secretVals = append(secretVals, v)
		}
	}

	if len(ierrs) > 0 {
		return nil, nil, ierrs
	}

	// Undeclared pass-through, unchanged: useful for --var-style tests
	// and for callers that supply extras. Declared beats undeclared.
	for k, v := range supplied {
		if _, ok := out[k]; ok {
			continue
		}
		out[k] = v
	}
	return out, secretVals, nil
}

func hasKey(m map[string]any, k string) bool {
	if m == nil {
		return false
	}
	_, ok := m[k]
	return ok
}

// ParseKV turns "k=v" strings from --input k=v into a map.
func ParseKV(pairs []string) (map[string]any, error) {
	m := map[string]any{}
	for _, p := range pairs {
		i := strings.IndexByte(p, '=')
		if i <= 0 {
			return nil, fmt.Errorf("expected key=value, got %q", p)
		}
		m[p[:i]] = p[i+1:]
	}
	return m, nil
}

// ParseKVString turns "k=v" strings into a map[string]string for --var.
func ParseKVString(pairs []string) (map[string]string, error) {
	m := map[string]string{}
	for _, p := range pairs {
		i := strings.IndexByte(p, '=')
		if i <= 0 {
			return nil, fmt.Errorf("expected key=value, got %q", p)
		}
		m[p[:i]] = p[i+1:]
	}
	return m, nil
}
