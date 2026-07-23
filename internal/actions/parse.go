package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

func init() { Register(parseAction{}) }

// parseAction extracts structured outputs from an input string.
// Config shape:
//
//	parse:
//	  source: ${{ steps.dump.outputs.body }}   # what to parse
//	  format: json                             # "json" | "regex"
//	  # for format: regex
//	  pattern: 'version=(?P<version>\d+)'
//	  # for format: json — no extra fields; every top-level JSON key
//	  # becomes an output value.
//
// Regex mode exposes every named capture group as an output. JSON mode
// flattens the top-level object into outputs; nested values are
// preserved verbatim (typed).
type parseAction struct{}

func (parseAction) Type() string { return "parse" }

type parseConfig struct {
	Source  string `yaml:"source"`
	Format  string `yaml:"format"`
	Pattern string `yaml:"pattern"`
}

func (parseAction) Validate(cfg StepConfig) error {
	if cfg == nil {
		return errors.New("parse: config is required")
	}
	var pc parseConfig
	if err := cfg.Decode(&pc); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if pc.Source == "" {
		return errors.New("parse: source is required")
	}
	switch pc.Format {
	case "regex":
		if pc.Pattern == "" {
			return errors.New("parse: regex mode requires pattern")
		}
		if _, err := regexp.Compile(pc.Pattern); err != nil {
			return fmt.Errorf("parse: pattern: %w", err)
		}
	case "json":
	default:
		return fmt.Errorf("parse: format must be \"json\" or \"regex\", got %q", pc.Format)
	}
	return nil
}

func (parseAction) Run(_ context.Context, sc *StepContext) (Outputs, error) {
	var pc parseConfig
	if err := sc.Config.Decode(&pc); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	// Source is expression-resolved by the engine's env-value path when
	// this action is dispatched; but sc.Config carries the raw yaml,
	// so we interpolate ourselves here to honour ${{ ... }}.
	src, err := sc.Expr.InterpolateString(pc.Source, sc.ExprEnv)
	if err != nil {
		return nil, fmt.Errorf("parse: source expression: %w", err)
	}
	switch pc.Format {
	case "regex":
		re := regexp.MustCompile(pc.Pattern) // validated above
		m := re.FindStringSubmatch(src)
		if m == nil {
			return nil, fmt.Errorf("parse: pattern did not match source")
		}
		outs := Outputs{}
		for i, name := range re.SubexpNames() {
			if name == "" || i == 0 {
				continue
			}
			outs[name] = m[i]
		}
		return outs, nil
	case "json":
		var top map[string]any
		if err := json.Unmarshal([]byte(src), &top); err != nil {
			return nil, fmt.Errorf("parse: json: %w", err)
		}
		outs := Outputs{}
		for k, v := range top {
			outs[k] = v
		}
		return outs, nil
	}
	return nil, fmt.Errorf("parse: unreachable format %q", pc.Format)
}
