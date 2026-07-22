package actions

import (
	"context"
	"errors"
)

func init() { Register(&promptStub{}) }

// promptStub reserves the `prompt` action name and returns a clean error if
// executed. The real interactive implementation is a Phase 2 target
// (spec §19).
type promptStub struct{}

func (promptStub) Type() string                  { return "prompt" }
func (promptStub) Validate(cfg StepConfig) error { return nil }
func (promptStub) Run(context.Context, *StepContext) (Outputs, error) {
	return nil, errors.New("prompt: interactive prompts are a Phase 2 feature; supply the input via --input for now")
}
