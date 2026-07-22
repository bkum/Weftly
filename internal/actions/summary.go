package actions

import (
	"context"
	"errors"

	"github.com/bkum/weftly/internal/events"
	"gopkg.in/yaml.v3"
)

func init() { Register(&summaryAction{}) }

// summaryAction emits a markdown block into the report. The engine's
// report package accumulates these; the TTY renderer prints them at end of
// run.
type summaryAction struct{}

func (summaryAction) Type() string { return "summary" }

func (summaryAction) Validate(cfg StepConfig) error {
	if cfg == nil || cfg.Kind != yaml.ScalarNode || cfg.Value == "" {
		return errors.New("summary: markdown body required")
	}
	return nil
}

func (summaryAction) Run(_ context.Context, sc *StepContext) (Outputs, error) {
	md, err := sc.Expr.InterpolateString(sc.Config.Value, sc.ExprEnv)
	if err != nil {
		return nil, err
	}
	if sc.Secrets != nil {
		md = sc.Secrets.Mask(md)
	}
	sc.Emit(events.SummaryEmitted{StepID: sc.StepID, Markdown: md})
	return Outputs{}, nil
}
