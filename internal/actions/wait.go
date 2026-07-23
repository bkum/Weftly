package actions

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/bkum/weftly/internal/events"
	"gopkg.in/yaml.v3"
)

func init() { Register(waitAction{}) }

// waitAction polls a shell command until it exits 0, or a timeout
// elapses. Common uses: waiting for a service health endpoint to come
// up, or for a queue to drain. Config shape:
//
//	wait:
//	  command: curl -sf http://svc/health
//	  interval: 5s        # between polls (default 5s)
//	  timeout: 2m         # overall wait budget (default 2m)
//
// The command runs in the workspace CWD with the same env as a `run`
// step. Exit codes other than 0 are treated as "not ready yet" and the
// wait continues; a genuine unrecoverable failure needs a proper
// `run` step upstream.
type waitAction struct{}

func (waitAction) Type() string { return "wait" }

type waitConfig struct {
	Command  string        `yaml:"command"`
	Interval time.Duration `yaml:"interval"`
	Timeout  time.Duration `yaml:"timeout"`
}

func (waitAction) Validate(cfg StepConfig) error {
	if cfg == nil {
		return errors.New("wait: config is required")
	}
	var wc waitConfig
	if err := cfg.Decode(&wc); err != nil {
		return fmt.Errorf("wait: %w", err)
	}
	if wc.Command == "" {
		return errors.New("wait: command is required")
	}
	return nil
}

func (waitAction) Run(ctx context.Context, sc *StepContext) (Outputs, error) {
	var wc waitConfig
	if err := sc.Config.Decode(&wc); err != nil {
		return nil, fmt.Errorf("wait: %w", err)
	}
	interval := wc.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	timeout := wc.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	attempts := 0

	for {
		attempts++
		cmd := exec.CommandContext(ctx, "sh", "-c", wc.Command)
		cmd.Dir = sc.Workdir
		out, err := cmd.CombinedOutput()
		if err == nil {
			sc.Log(events.Info, fmt.Sprintf("wait: ready after %d probe(s)", attempts))
			return Outputs{"attempts": attempts}, nil
		}
		if line := firstLine(string(out)); line != "" {
			sc.Log(events.Stderr, "wait: probe "+line)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if time.Now().Add(interval).After(deadline) {
			return nil, fmt.Errorf("wait: condition never met within %s (%d probes)", timeout, attempts)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}

// unused but keeps yaml package tied so linters don't complain if
// waitConfig ever loses its only field.
var _ yaml.Node
