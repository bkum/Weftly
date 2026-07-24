package engine

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/bkum/weftly/internal/schema"
)

// resolveInputs merges declared workflow inputs with values supplied by the
// caller (flags/input-file), applies defaults, coerces types, enforces
// required-ness, and returns the resolved map plus the subset of values
// that should be registered with the secret masker.
//
// Precedence: supplied > env (WEFTLY_INPUT_<UPPER>) > declared default.
func resolveInputs(wf *schema.Workflow, supplied map[string]any) (map[string]any, []any, error) {
	out := map[string]any{}
	var secretVals []any
	for name, in := range wf.Inputs {
		var raw any
		if v, ok := supplied[name]; ok {
			raw = v
		} else if v, ok := os.LookupEnv("WEFTLY_INPUT_" + strings.ToUpper(name)); ok {
			raw = v
		} else if in.Default != nil {
			raw = in.Default
		}
		if raw == nil {
			if in.Required {
				return nil, nil, fmt.Errorf("input %q is required", name)
			}
			continue
		}
		v, err := coerceInput(raw, in.Type)
		if err != nil {
			return nil, nil, fmt.Errorf("input %q: %w", name, err)
		}
		if len(in.Enum) > 0 {
			// Compare via fmt.Sprintf so `type: string` inputs match a
			// numeric enum entry and vice versa — the YAML author's
			// declared allowed-set is authoritative, not Go's type
			// system.
			ok := false
			sv := fmt.Sprintf("%v", v)
			for _, allowed := range in.Enum {
				if fmt.Sprintf("%v", allowed) == sv {
					ok = true
					break
				}
			}
			if !ok {
				return nil, nil, fmt.Errorf("input %q: %v is not one of %v", name, v, in.Enum)
			}
		}
		out[name] = v
		if in.Secret {
			secretVals = append(secretVals, v)
		}
	}
	// Also allow inputs the workflow didn't declare — pass-through, useful
	// for --var-style tests. Declared beats undeclared.
	for k, v := range supplied {
		if _, ok := out[k]; ok {
			continue
		}
		out[k] = v
	}
	return out, secretVals, nil
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

func coerceInput(raw any, t schema.InputType) (any, error) {
	if t == "" {
		// Default to string coercion for scalar-shaped values so
		// existing workflows (which lean on strings) keep working, but
		// pass structured values (list / map) through unchanged so
		// for-each can consume a `default: [a, b, c]` directly.
		switch raw.(type) {
		case []any, []string, map[string]any:
			return raw, nil
		}
		t = schema.InputString
	}
	switch t {
	case schema.InputString:
		return fmt.Sprintf("%v", raw), nil
	case schema.InputNumber:
		switch v := raw.(type) {
		case int:
			return float64(v), nil
		case int64:
			return float64(v), nil
		case float64:
			return v, nil
		case string:
			return strconv.ParseFloat(v, 64)
		}
		return nil, fmt.Errorf("cannot coerce %T to number", raw)
	case schema.InputBool:
		switch v := raw.(type) {
		case bool:
			return v, nil
		case string:
			return strconv.ParseBool(v)
		}
		return nil, fmt.Errorf("cannot coerce %T to bool", raw)
	}
	return raw, nil
}
