package actions

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

func init() { Register(&assertAction{}) }

// assertAction evaluates its config as a boolean expression. The value is
// either a scalar expression string (with or without ${{ }}) or a mapping
// with `expr:` and optional `message:`.
type assertAction struct{}

func (assertAction) Type() string { return "assert" }

func (assertAction) Validate(cfg StepConfig) error {
	if cfg == nil {
		return errors.New("assert: expression required")
	}
	if body, _ := assertBody(cfg); body == "" {
		return errors.New("assert: expression required")
	}
	return nil
}

func (assertAction) Run(_ context.Context, sc *StepContext) (Outputs, error) {
	body, message := assertBody(sc.Config)
	if body == "" {
		return nil, errors.New("assert: empty expression")
	}
	body = stripExprWrap(body)
	ok, err := sc.Expr.EvaluateBool(body, sc.ExprEnv)
	if err != nil {
		return nil, err
	}
	if !ok {
		if message != "" {
			return nil, fmt.Errorf("assert failed: %s", message)
		}
		return nil, fmt.Errorf("assert failed: %s", body)
	}
	return Outputs{}, nil
}

func assertBody(cfg *yaml.Node) (body, message string) {
	if cfg == nil {
		return "", ""
	}
	if cfg.Kind == yaml.ScalarNode {
		return cfg.Value, ""
	}
	if cfg.Kind == yaml.MappingNode {
		for i := 0; i < len(cfg.Content); i += 2 {
			k := cfg.Content[i].Value
			v := cfg.Content[i+1]
			switch k {
			case "expr", "expression":
				body = v.Value
			case "message":
				message = v.Value
			}
		}
	}
	return body, message
}

func stripExprWrap(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "${{") && strings.HasSuffix(s, "}}") {
		return strings.TrimSpace(s[3 : len(s)-2])
	}
	return s
}
