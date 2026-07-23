// Package expr wraps github.com/expr-lang/expr with the namespaces defined
// in spec §10, plus a small set of built-in functions and a "${{ ... }}"
// interpolator used by the loader for header/body strings.
//
// Two evaluation paths are exposed:
//
//	Evaluate       — compiles and evaluates a bare expression string.
//	Interpolate    — replaces every "${{ ... }}" span inside a larger
//	                 string. When the entire input is a single "${{ ... }}"
//	                 wrapper, the raw typed value is returned so downstream
//	                 consumers (e.g. an http outputs map) can preserve bool
//	                 and number types across the boundary.
package expr

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// RunMeta carries per-run metadata exposed as `run.*` plus the two
// pieces of state consumed by status functions (success(), failure(),
// cancelled()).
//
// Status is the run's aggregate outcome so far. During main-graph
// execution it stays "success" — cascade-skip already prevents downstream
// steps from running past a fatal upstream, so a status-function check
// mid-run adds nothing. The engine sets Status to the true final value
// before running the cleanup: block, which is the whole point of the
// helpers.
//
// Cancelled reports whether the run's context was cancelled (SIGINT,
// DELETE /runs/{id}, or an outer deadline). Cleanup steps typically use
// `if: ${{ failure() || cancelled() }}` to run whenever the primary
// path didn't reach a clean success.
type RunMeta struct {
	ID        string
	Workspace string
	Status    string // "" | "success" | "failed" | "timed-out"
	Cancelled bool
}

// StepView is the read-only view of a completed step exposed as `steps.<id>`.
type StepView struct {
	Outputs map[string]any
	Status  string
}

// EachContext is populated inside a for-each iteration and exposed as
// `each.value` + `each.index` in expressions. Nil outside a for-each.
type EachContext struct {
	Value any
	Index int
}

// Env is the resolvable namespaces at the point of evaluation.
type Env struct {
	Inputs   map[string]any
	Steps    map[string]StepView
	Env      map[string]string
	Secrets  map[string]string
	Run      RunMeta
	Response any
	Each     *EachContext
}

// Evaluator is safe for concurrent use once constructed.
type Evaluator struct {
	cache map[string]*vm.Program
}

func New() *Evaluator {
	return &Evaluator{cache: map[string]*vm.Program{}}
}

// Evaluate compiles (with caching) and evaluates a bare expression string.
func (e *Evaluator) Evaluate(expression string, env Env) (any, error) {
	prog, err := e.compile(expression, env)
	if err != nil {
		return nil, err
	}
	return expr.Run(prog, e.envMap(env))
}

// EvaluateBool coerces the result of Evaluate to a bool with the usual
// truthiness rules (nil / false / 0 / "" / empty collection = false).
func (e *Evaluator) EvaluateBool(expression string, env Env) (bool, error) {
	v, err := e.Evaluate(expression, env)
	if err != nil {
		return false, err
	}
	return truthy(v), nil
}

// Interpolate replaces every "${{ ... }}" span in s. If the entire string is
// exactly one wrapped expression, the raw typed value is returned. Otherwise
// the returned any is a plain string.
func (e *Evaluator) Interpolate(s string, env Env) (any, error) {
	if !strings.Contains(s, "${{") {
		return s, nil
	}
	// Whole-string expression → preserve type.
	if trimmed := strings.TrimSpace(s); strings.HasPrefix(trimmed, "${{") && strings.HasSuffix(trimmed, "}}") {
		// verify there is exactly one span
		body, rest, ok := extractSpan(trimmed)
		if ok && rest == "" {
			return e.Evaluate(body, env)
		}
	}
	// Mixed literal + expressions → concatenate as string.
	str, err := e.InterpolateString(s, env)
	if err != nil {
		return nil, err
	}
	return str, nil
}

// InterpolateString is Interpolate that always returns a string.
func (e *Evaluator) InterpolateString(s string, env Env) (string, error) {
	if !strings.Contains(s, "${{") {
		return s, nil
	}
	var b strings.Builder
	for {
		i := strings.Index(s, "${{")
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		s = s[i:]
		body, rest, ok := extractSpan(s)
		if !ok {
			return "", fmt.Errorf("expr: unterminated ${{ ... }} at %q", firstLine(s))
		}
		v, err := e.Evaluate(body, env)
		if err != nil {
			return "", err
		}
		b.WriteString(stringify(v))
		s = rest
	}
	return b.String(), nil
}

// extractSpan splits "${{ body }}rest" → body, rest, true. It supports
// nested braces inside strings to a shallow degree — enough for
// "fromJSON('{...}')"-style expressions.
func extractSpan(s string) (body, rest string, ok bool) {
	if !strings.HasPrefix(s, "${{") {
		return "", s, false
	}
	// Scan for the matching "}}" with basic quote awareness so that a "}}"
	// inside a string literal is not treated as a terminator.
	i := 3
	inStr := byte(0)
	for i < len(s)-1 {
		c := s[i]
		if inStr != 0 {
			if c == '\\' && i+1 < len(s) {
				i += 2
				continue
			}
			if c == inStr {
				inStr = 0
			}
			i++
			continue
		}
		if c == '"' || c == '\'' {
			inStr = c
			i++
			continue
		}
		if c == '}' && s[i+1] == '}' {
			return strings.TrimSpace(s[3:i]), s[i+2:], true
		}
		i++
	}
	return "", s, false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func (e *Evaluator) compile(source string, env Env) (*vm.Program, error) {
	if p, ok := e.cache[source]; ok {
		return p, nil
	}
	opts := []expr.Option{
		expr.Env(e.envMap(env)),
		expr.AllowUndefinedVariables(),
	}
	for name, fn := range builtinFuncs() {
		opts = append(opts, expr.Function(name, fn))
	}
	prog, err := expr.Compile(source, opts...)
	if err != nil {
		return nil, fmt.Errorf("expr compile %q: %w", source, err)
	}
	e.cache[source] = prog
	return prog, nil
}

// envMap builds the map[string]any that expr sees as its top-level env.
// Note: expr does not care about types being consistent across compiles as
// long as field accesses resolve; AllowUndefinedVariables lets absent step
// ids evaluate to nil rather than blow up compile.
func (e *Evaluator) envMap(env Env) map[string]any {
	stepMap := make(map[string]any, len(env.Steps))
	for id, sv := range env.Steps {
		stepMap[id] = map[string]any{
			"outputs": sv.Outputs,
			"status":  sv.Status,
		}
	}
	envStrings := make(map[string]any, len(env.Env))
	for k, v := range env.Env {
		envStrings[k] = v
	}
	secretStrings := make(map[string]any, len(env.Secrets))
	for k, v := range env.Secrets {
		secretStrings[k] = v
	}
	m := map[string]any{
		"inputs":  env.Inputs,
		"steps":   stepMap,
		"env":     envStrings,
		"secrets": secretStrings,
		"run": map[string]any{
			"id":        env.Run.ID,
			"workspace": env.Run.Workspace,
			"status":    env.Run.Status,
			"cancelled": env.Run.Cancelled,
		},
	}
	if env.Response != nil {
		m["response"] = env.Response
	}
	if env.Each != nil {
		m["each"] = map[string]any{
			"value": env.Each.Value,
			"index": env.Each.Index,
		}
	}
	// Register function names so expr can compile references to them even
	// when the map itself is used as the environment.
	for name, fn := range builtinFuncs() {
		m[name] = fn
	}
	// Status functions close over this call's env snapshot; they're
	// re-registered per envMap() call so the value they see always
	// matches the evaluator's current invocation, never a stale one
	// from the compile-time cache.
	status := env.Run.Status
	cancelled := env.Run.Cancelled
	m["success"] = func(args ...any) (any, error) {
		return status == "" || status == "success", nil
	}
	m["failure"] = func(args ...any) (any, error) {
		return status == "failed" || status == "timed-out", nil
	}
	m["always"] = func(args ...any) (any, error) { return true, nil }
	m["cancelled"] = func(args ...any) (any, error) { return cancelled, nil }
	return m
}

// builtinFuncs are the spec §10 helpers. Note that expr-lang reserves
// `contains`, `startsWith`, and `endsWith` as native operators/methods, so
// workflow expressions use them as: `s contains "x"`, `s startsWith "x"`,
// or the string method form `s.startsWith("x")`. Only `default`, `fromJSON`,
// and `toJSON` need explicit registration here.
func builtinFuncs() map[string]func(args ...any) (any, error) {
	return map[string]func(args ...any) (any, error){
		"default": func(args ...any) (any, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("default: expected (value, fallback)")
			}
			if args[0] == nil {
				return args[1], nil
			}
			if s, ok := args[0].(string); ok && s == "" {
				return args[1], nil
			}
			return args[0], nil
		},
		"fromJSON": func(args ...any) (any, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("fromJSON: expected 1 arg")
			}
			s, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("fromJSON: arg must be string")
			}
			var v any
			if err := json.Unmarshal([]byte(s), &v); err != nil {
				return nil, err
			}
			return v, nil
		},
		"urlquery": func(args ...any) (any, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("urlquery: expected 1 arg")
			}
			return url.QueryEscape(stringify(args[0])), nil
		},
		"toJSON": func(args ...any) (any, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("toJSON: expected 1 arg")
			}
			b, err := json.Marshal(args[0])
			if err != nil {
				return nil, err
			}
			return string(b), nil
		},
	}
}

// truthy applies GHA-style truthiness to an arbitrary evaluated value.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != "" && x != "false" && x != "0"
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// stringify renders an evaluated value for embedding into a string.
func stringify(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case []byte:
		return string(x)
	default:
		// Fall back to JSON for structured values so that a map/slice doesn't
		// render as "map[a:1]" in a URL or a header.
		if b, err := json.Marshal(x); err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", x)
	}
}
