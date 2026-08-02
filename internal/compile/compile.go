// Package compile turns a validated schema.Workflow into an executable
// ir.Graph. Two composition mechanisms live here:
//
//   - Top-level `include:` (list of files) is handled by schema.Load at
//     parse time — by the time Compile sees the workflow, its Steps
//     slice already contains the merged prelude. This package doesn't
//     re-do that work.
//
//   - Step-level `include:` (single file with `with:` inputs) is
//     expanded here recursively. Each expansion produces child StepNodes
//     tagged with a Scope that the engine's expression evaluator uses to
//     resolve `inputs.*` and `steps.*` through the caller/callee
//     boundary — expression strings are never rewritten, so error
//     messages and --dry-run plans always match the source file.
//
// Everything downstream (scheduler, engine, actions) sees an ordinary
// []StepNode. Composition is invisible past this file.
package compile

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bkum/weftly/internal/ir"
	"github.com/bkum/weftly/internal/schema"
)

// Options tunes compilation. Zero value is fine — the compiler falls
// back to the workflow's own directory as the catalogue root.
type Options struct {
	// CatalogueRoot, when set, is the absolute directory step-level
	// includes must not escape. Server mode passes its --dir here so a
	// workflow can't include a file outside the operator's catalogue.
	CatalogueRoot string
	// MaxIncludeDepth caps the include chain length. Zero means default.
	MaxIncludeDepth int
}

// Compile is the zero-options convenience path preserved for callers
// that don't need catalogue confinement (CLI runs against a local file,
// where the trust boundary is already the filesystem).
func Compile(wf *schema.Workflow) *ir.Graph {
	g, err := CompileWithOptions(wf, Options{})
	if err != nil {
		// Preserve the legacy signature: legacy callers relied on Compile
		// never failing for non-include workflows, and step-level includes
		// are opt-in. Encode any error as a synthetic node the engine
		// surfaces via its usual failure path.
		g = &ir.Graph{Workflow: wf, Order: []*ir.StepNode{{
			ID:         "__compile_error__",
			Action:     "run",
			SkipReason: err.Error(),
		}}}
	}
	return g
}

// CompileWithOptions is the full compilation entry point. Errors are
// terminal — the caller should surface them and not try to run the
// (partial) graph.
func CompileWithOptions(wf *schema.Workflow, opts Options) (*ir.Graph, error) {
	if opts.MaxIncludeDepth == 0 {
		opts.MaxIncludeDepth = 5
	}
	root := opts.CatalogueRoot
	if root != "" {
		abs, err := filepath.Abs(root)
		if err == nil {
			root = abs
		}
	}
	dir := filepath.Dir(wf.Path) // "" if Path is empty — legacy tests
	g := &ir.Graph{Workflow: wf, Order: make([]*ir.StepNode, 0, len(wf.Steps))}
	nodes, err := compileSteps(wf.Steps, nil, dir, wf.Path, []string{wf.Path}, opts, root)
	if err != nil {
		return nil, err
	}
	g.Order = nodes
	// Preserve the pre-include contract: unless a step declares
	// `needs:`, the immediately-preceding named node is its implicit
	// dependency. Applied AFTER include expansion so a chain like
	//     [greet, include(edi.[a,b,c]), preview]
	// links preview -> edi.c (the last expanded child) rather than the
	// literal previous entry in the source YAML.
	applyImplicitNeeds(g.Order)
	return g, nil
}

// compileSteps walks a step slice, producing IR nodes. Recurses into
// step-level `include:` steps, threading a chain slice for cycle
// detection and a scope pointer for parent lookups.
func compileSteps(steps []schema.Step, parentScope *ir.Scope, dir, sourceFile string, chain []string, opts Options, root string) ([]*ir.StepNode, error) {
	out := make([]*ir.StepNode, 0, len(steps))
	for i := range steps {
		s := &steps[i]
		if s.ActionType != "include" {
			out = append(out, buildNode(s, parentScope, sourceFile))
			continue
		}
		// --- step-level include ---
		if len(chain) >= opts.MaxIncludeDepth+1 { // +1 because chain[0] is root
			return nil, fmt.Errorf("include %q: depth exceeds %d (chain: %s)", s.Include, opts.MaxIncludeDepth, strings.Join(chain, " -> "))
		}
		incPath, err := resolveIncludePath(dir, s.Include, root)
		if err != nil {
			return nil, fmt.Errorf("step %q include %q: %w", s.ID, s.Include, err)
		}
		for _, prev := range chain {
			if prev == incPath {
				return nil, fmt.Errorf("include cycle: %s -> %s", strings.Join(chain, " -> "), incPath)
			}
		}
		child, err := schema.Load(incPath)
		if err != nil {
			return nil, fmt.Errorf("step %q include %q: load: %w", s.ID, s.Include, err)
		}
		if err := schema.Validate(child); err != nil {
			return nil, fmt.Errorf("step %q include %q: validate: %w", s.ID, s.Include, err)
		}

		// Bind child inputs from the parent's `with:` map, checking each
		// key is declared and every required child input is either bound
		// or defaulted.
		bindings, err := bindInputs(s, child)
		if err != nil {
			return nil, err
		}

		childPrefix := s.ID
		if parentScope != nil {
			childPrefix = parentScope.Prefix + "." + s.ID
		}
		childScope := &ir.Scope{
			Prefix:  childPrefix,
			Dir:     filepath.Dir(incPath),
			Parent:  parentScope,
			Inputs:  bindings,
			StepIDs: map[string]string{},
			Outputs: child.Outputs,
		}
		for _, cs := range child.Steps {
			if cs.ID != "" {
				childScope.StepIDs[cs.ID] = childPrefix + "." + cs.ID
			}
		}

		// Compile the child's steps first, then rewrite each child's
		// ID to be qualified. Nested includes recurse — they get their
		// own scope with childScope as Parent.
		newChain := append([]string{}, chain...)
		newChain = append(newChain, incPath)
		childNodes, err := compileSteps(child.Steps, childScope, filepath.Dir(incPath), incPath, newChain, opts, root)
		if err != nil {
			return nil, err
		}
		for _, n := range childNodes {
			// A nested include has already qualified its ID against its
			// own scope's Prefix — that Prefix already begins with our
			// prefix, so we don't re-prefix.
			if !strings.HasPrefix(n.ID, childPrefix+".") && n.ID != childPrefix {
				n.ID = childPrefix + "." + n.LocalID
			}
			// Rewrite `needs:` entries: local sibling ids get qualified.
			for i, dep := range n.Needs {
				if q, ok := childScope.StepIDs[dep]; ok {
					n.Needs[i] = q
				}
			}
		}

		// Modifier propagation: parent's if:/continue-on-error/needs
		// per spec §3.2.
		applyIncludeModifiers(childNodes, s)

		// Verify the parent's step.outputs.X references only names the
		// child actually declares. We can't see the parent expressions
		// here, but we can at least catch a with: key that references
		// an undeclared child input (done in bindInputs) and ensure
		// child.outputs entries are well-formed strings.
		out = append(out, childNodes...)

		// Scope teardown. The child's `finally:` steps run after its own
		// steps, whatever their outcome — RunAlways exempts them from the
		// scheduler's cascade-skip, which is right for downstream work but
		// exactly wrong for teardown.
		//
		// Ordering is innermost-first by construction: a nested include's
		// finally nodes were already appended by the recursive call above,
		// so they precede this scope's in `out` and therefore in the
		// topological walk.
		if len(child.Finally) > 0 {
			finallyNodes, ferr := compileSteps(child.Finally, childScope, filepath.Dir(incPath), incPath, newChain, opts, root)
			if ferr != nil {
				return nil, ferr
			}
			var prev string
			if len(childNodes) > 0 {
				prev = childNodes[len(childNodes)-1].ID
			}
			for i, fn := range finallyNodes {
				fn.RunAlways = true
				// Namespace teardown ids so they can't collide with the
				// fragment's main steps, and chain them sequentially after
				// the last main step.
				fn.ID = childPrefix + ".finally." + fn.LocalID
				if i == 0 {
					if prev != "" {
						fn.Needs = []string{prev}
					}
				} else {
					fn.Needs = []string{finallyNodes[i-1].ID}
				}
			}
			out = append(out, finallyNodes...)
			// The outputs shim (below) must land after teardown so the
			// include's own completion genuinely means "everything this
			// fragment does is finished".
			childNodes = append(childNodes, finallyNodes...)
		}

		// Synthesized outputs node: an internal `include_outputs`
		// action that evaluates each entry of child.Outputs in the
		// child scope at runtime, producing them as its own Outputs so
		// the parent's `steps.<include-id>.outputs.X` resolves normally.
		if len(child.Outputs) > 0 {
			last := childNodes[len(childNodes)-1]
			out = append(out, &ir.StepNode{
				ID:         childPrefix,
				LocalID:    s.ID,
				Name:       s.Name,
				Action:     "include_outputs",
				Scope:      childScope,
				Hidden:     true,
				SourceFile: incPath,
				OutputsMap: child.Outputs,
				Needs:      []string{last.ID},
			})
		}
	}
	return out, nil
}

// buildNode is the common leaf-step constructor for non-include steps.
func buildNode(s *schema.Step, scope *ir.Scope, sourceFile string) *ir.StepNode {
	qualified := s.ID
	if scope != nil && s.ID != "" {
		qualified = scope.Prefix + "." + s.ID
	}
	return &ir.StepNode{
		ID:              qualified,
		LocalID:         s.ID,
		Name:            s.Name,
		Action:          s.ActionType,
		Config:          s,
		If:              s.If,
		Needs:           append([]string(nil), s.Needs...),
		Env:             s.Env,
		ContinueOnError: s.ContinueOnError,
		Timeout:         s.Timeout,
		Shell:           s.Shell,
		Container:       s.Container,
		Retry:           s.Retry,
		ForEach:         s.ForEach,
		OutputsMap:      s.Outputs,
		Scope:           scope,
		SourceFile:      sourceFile,
	}
}

// applyImplicitNeeds re-creates the pre-include "step N implicitly
// depends on step N-1 unless it declared its own needs:" behaviour,
// applied AFTER include expansion. Hidden bookkeeping nodes (the
// outputs shim) already have their own Needs so we don't overwrite
// them — but they still ADVANCE the prev-id cursor because from the
// parent's viewpoint the shim IS the include's identity (its ID equals
// the include step's qualified id, e.g. `sub`). A downstream step
// authored as depending on the include block naturally chains onto the
// shim so `steps.<include-id>.outputs.*` is guaranteed populated by
// the time the downstream step evaluates it.
func applyImplicitNeeds(nodes []*ir.StepNode) {
	var prevID string
	for _, n := range nodes {
		if !n.Hidden && len(n.Needs) == 0 && prevID != "" {
			n.Needs = []string{prevID}
		}
		if n.ID != "" {
			prevID = n.ID
		}
	}
}

// applyIncludeModifiers pushes parent-step modifiers down into the
// expanded child nodes per spec §3.2.
//
//   - if:                conjoined into every child's own if:
//   - continue-on-error: OR'd onto every child
//   - needs:             applied to the FIRST child; later children
//     chain via their normal (rewritten) local needs.
func applyIncludeModifiers(children []*ir.StepNode, parent *schema.Step) {
	if len(children) == 0 {
		return
	}
	if parent.If != "" {
		for _, c := range children {
			if c.If == "" {
				c.If = parent.If
			} else {
				c.If = "(" + stripWrap(parent.If) + ") && (" + stripWrap(c.If) + ")"
			}
		}
	}
	if parent.ContinueOnError {
		for _, c := range children {
			c.ContinueOnError = true
		}
	}
	if len(parent.Needs) > 0 {
		children[0].Needs = append(append([]string(nil), parent.Needs...), children[0].Needs...)
	}
}

// stripWrap trims a surrounding ${{ ... }} wrapper if present. Copied
// (rather than shared) from engine to avoid an import cycle.
func stripWrap(s string) string {
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "${{") && strings.HasSuffix(t, "}}") {
		return strings.TrimSpace(t[3 : len(t)-2])
	}
	return s
}

// bindInputs realises each declared child input as either a `with:`
// expression (from the parent) or a literal default, and rejects both
// missing-required and unknown-input errors.
func bindInputs(step *schema.Step, child *schema.Workflow) (map[string]ir.Binding, error) {
	// with:/with_if_set: keys the child never declared → typo class of errors.
	for _, m := range []map[string]string{step.With, step.WithIfSet} {
		for k := range m {
			if _, ok := child.Inputs[k]; !ok {
				return nil, fmt.Errorf("step %q include: with: key %q is not a declared input of %s", step.ID, k, child.Path)
			}
		}
	}
	// The same key in both maps is ambiguous — one unconditionally
	// overrides, the other conditionally defers. Reject rather than
	// silently pick.
	for k := range step.WithIfSet {
		if _, dup := step.With[k]; dup {
			return nil, fmt.Errorf("step %q include: key %q appears in both with: and with_if_set: — use one", step.ID, k)
		}
	}
	out := make(map[string]ir.Binding, len(child.Inputs))
	for name, in := range child.Inputs {
		if expr, ok := step.With[name]; ok {
			out[name] = ir.Binding{Expr: expr, IsExpr: true, Secret: in.Secret}
			continue
		}
		if expr, ok := step.WithIfSet[name]; ok {
			// Carry the child's own default as the fallback so an
			// empty runtime value lands on it instead of on "".
			out[name] = ir.Binding{
				Expr: expr, IsExpr: true, Secret: in.Secret,
				IfSet: true, Fallback: in.Default,
			}
			continue
		}
		if in.Default != nil {
			out[name] = ir.Binding{Literal: in.Default, Secret: in.Secret}
			continue
		}
		if in.Required {
			return nil, fmt.Errorf("step %q include %s: required input %q has no value (add it to with:)", step.ID, child.Path, name)
		}
		// unspecified optional input: bind to nil literal so lookups don't fault.
		out[name] = ir.Binding{Literal: nil, Secret: in.Secret}
	}
	return out, nil
}

// resolveIncludePath applies path resolution + catalogue confinement
// per spec §7. Symlinks are resolved before the confinement check so a
// symlink cannot escape the root.
func resolveIncludePath(dir, rel, root string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute paths not allowed")
	}
	if dir == "" {
		// The caller's workflow has no on-disk Path — legacy tests only.
		// Fall back to CWD; confinement is a no-op in this mode.
		abs, err := filepath.Abs(rel)
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	joined := filepath.Join(dir, rel)
	abs, err := filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	// EvalSymlinks may fail if the target does not exist; try it but
	// fall back to the raw abs path so the "file not found" error at
	// Load time is what users see instead of an EvalSymlinks quirk.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	if root != "" {
		// Canonicalise both sides before the Rel test. On macOS /tmp
		// (and every test tempdir under /var/folders) is a symlink to
		// /private/var/..., so EvalSymlinks on the include path but
		// not on the root would make `filepath.Rel` see them as
		// unrelated even when they're logically one is a subtree of
		// the other. Fall back to the raw value if EvalSymlinks fails
		// (e.g. the root doesn't exist yet at compile time).
		canonRoot := root
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			canonRoot = resolved
		}
		rrel, err := filepath.Rel(canonRoot, abs)
		if err != nil || strings.HasPrefix(rrel, "..") || rrel == ".." {
			return "", fmt.Errorf("include escapes catalogue root %s (resolved to %s); if this is legitimate project layout, widen the boundary with `weftly server --include-root <project-root>`", root, abs)
		}
	}
	return abs, nil
}
