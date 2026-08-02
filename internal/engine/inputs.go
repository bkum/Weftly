package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
		// type: path — confine, then (optionally) check existence.
		// Confinement first: the value is caller-supplied and flows on
		// into steps that open it, so an unconfined path input is a
		// read/write primitive against the whole host for anyone who can
		// POST /runs. must_exist would additionally make it a file
		// existence oracle.
		if in.EffectiveType() == schema.InputPath {
			p, ok := v.(string)
			if !ok {
				out[name] = v
				continue
			}
			abs, perr := resolvePathInput(p, in.MustExist, opts)
			if perr != "" {
				ierrs = append(ierrs, schema.InputError{Input: name, Line: in.Line, Message: perr})
				continue
			}
			v = abs
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

// resolvePathInput resolves, confines, and (when mustExist) verifies a
// `type: path` value, returning the absolute path or a non-empty error
// message.
//
// The existence check lives HERE rather than at the call site so the
// containment guard and the only filesystem call that consumes the
// path sit in one function body. Splitting them put the guard beyond
// the reach of intraprocedural taint analysis, which then — correctly,
// on the evidence available to it — reported an unconstrained
// caller-supplied path reaching os.Stat.
//
// Two roots are permitted, because the spec asks for both and they serve
// different purposes:
//
//   - the run WORKSPACE, where a path input naming somewhere to write
//     belongs (this is the `upload` / `template dest:` rule);
//   - the WORKFLOW's own directory tree, because a self-contained
//     library legitimately points at its bundled assets with
//     `default: "${{ workflow.dir }}/profiles/x12.json"` — which is in
//     the catalogue, not the workspace.
//
// Anything else is rejected. Relative paths resolve against the
// workspace, matching how every other path in a step is interpreted.
//
// Symlinks are resolved before the containment test where the target
// exists, so a symlink planted inside a root cannot point out of it.
// A path that doesn't exist yet is tested lexically, which is correct
// for an output path the workflow is about to create.
func resolvePathInput(p string, mustExist bool, opts ResolveOptions) (string, string) {
	roots := make([]string, 0, 2)
	for _, r := range []string{opts.WorkspaceDir, opts.WorkflowDir} {
		if r == "" {
			continue
		}
		if abs, err := filepath.Abs(r); err == nil {
			roots = append(roots, canonicalPath(abs))
		}
	}
	// No roots configured — only reachable from bare-struct construction
	// in tests, since engine.Run always supplies a workspace. Pass the
	// value through unresolved and, crucially, do NOT stat it: with no
	// root to confine against there is nothing to make the access safe,
	// and a must_exist probe here would be precisely the unconstrained
	// filesystem read this function exists to prevent.
	if len(roots) == 0 {
		return p, ""
	}

	abs := p
	if !filepath.IsAbs(abs) {
		base := opts.WorkspaceDir
		if base == "" {
			base = opts.WorkflowDir
		}
		abs = filepath.Join(base, abs)
	}
	var err error
	if abs, err = filepath.Abs(abs); err != nil {
		return "", fmt.Sprintf("path %q could not be resolved: %v", p, err)
	}
	abs = canonicalPath(abs)
	for _, root := range roots {
		rel, rerr := filepath.Rel(root, abs)
		if rerr != nil {
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue // escapes this root; try the next
		}
		// Re-anchor onto the root rather than carrying the
		// caller-derived absolute path forward. `rel` has been proven
		// not to climb out, so Join can only produce something inside
		// `root`: the result is constructed from a trusted base plus a
		// verified-relative remainder rather than merely checked.
		safe := filepath.Join(root, rel)
		if mustExist {
			// The existence probe goes through os.Root, which confines
			// every operation to the opened directory in the kernel —
			// a traversal or symlink escape is not merely rejected but
			// unrepresentable. That makes the check independent of the
			// Rel/prefix logic above: even if this function's own
			// containment reasoning were wrong, the probe still cannot
			// read outside `root`.
			//
			// Failing here rather than at the step that opens the file
			// means a missing profile registry names the input instead
			// of surfacing as a shell error four steps later.
			r, oerr := os.OpenRoot(root)
			if oerr != nil {
				return "", fmt.Sprintf("path %q could not be checked: %v", p, oerr)
			}
			_, serr := r.Stat(rel)
			r.Close()
			if serr != nil {
				return "", fmt.Sprintf("path %q does not exist (must_exist: true)", p)
			}
		}
		return safe, ""
	}
	return "", fmt.Sprintf("path %q resolves outside the run workspace and the workflow directory", p)
}

// canonicalPath resolves symlinks as far as the path actually exists,
// then re-appends the remainder.
//
// Plain EvalSymlinks fails outright on a path whose leaf doesn't exist
// yet, which is the common case for an output path a workflow is about
// to create. Leaving such a path un-canonicalised while the roots ARE
// canonicalised makes filepath.Rel see two unrelated trees — on macOS
// every temp dir is /var/... symlinked to /private/var/..., so a
// perfectly legal workspace-relative path gets rejected.
//
// Resolving the longest existing prefix keeps the containment test
// honest (a symlink that exists is followed, so it can't smuggle the
// path out of a root) without requiring the leaf to exist.
func canonicalPath(abs string) string {
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	dir, leaf := filepath.Split(abs)
	dir = filepath.Clean(dir)
	if dir == abs || leaf == "" {
		// Reached the root without finding an existing ancestor.
		return abs
	}
	return filepath.Join(canonicalPath(dir), leaf)
}
