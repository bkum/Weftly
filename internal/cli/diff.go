package cli

import (
	"fmt"
	"strings"

	"github.com/bkum/weftly/internal/schema"
	"github.com/spf13/cobra"
)

// newDiffCmd — `weftly diff <a.yml> <b.yml>` compares the effective
// (include-expanded) workflows and prints the semantic delta:
// step-set changes, per-step action/env/timeout/container/retry
// changes, workflow-level env delta. Comment/whitespace changes are
// invisible — that's a `weftly fmt --diff` question.
//
// Exit code is 0 when the workflows are semantically equivalent,
// 1 when they differ. Useful in CI as a "did anything material change
// in the workflow?" gate.
func newDiffCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diff <a.yml> <b.yml>",
		Short: "Semantic diff of two weftly workflows (include-expanded)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := schema.Load(args[0])
			if err != nil {
				return fmt.Errorf("load %s: %w", args[0], err)
			}
			b, err := schema.Load(args[1])
			if err != nil {
				return fmt.Errorf("load %s: %w", args[1], err)
			}
			out := diffWorkflows(a, b)
			if len(out) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no differences.")
				return nil
			}
			for _, line := range out {
				fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			// Non-zero exit signals "there is a diff" so CI can gate on it.
			cmd.SilenceUsage = true
			return fmt.Errorf("workflows differ")
		},
	}
	return cmd
}

// diffWorkflows returns a human-readable slice of lines describing the
// semantic difference. Empty slice = equivalent. Format:
//
//	name: "a" → "b"
//	step "id": added / removed
//	step "id".env.X: "old" → "new"
//	step "id".action: run → http
func diffWorkflows(a, b *schema.Workflow) []string {
	var out []string
	if a.Name != b.Name {
		out = append(out, fmt.Sprintf(`name: %q → %q`, a.Name, b.Name))
	}
	if a.Description != b.Description {
		out = append(out, "description: changed")
	}
	out = append(out, diffStringMap("env", a.Env, b.Env)...)
	out = append(out, diffStepList(a.Steps, b.Steps, "steps")...)
	out = append(out, diffStepList(a.Cleanup, b.Cleanup, "cleanup")...)
	return out
}

// diffStringMap emits key-by-key adds / removals / changes with a
// scope prefix so multiple maps can share the diff format.
func diffStringMap(scope string, a, b map[string]string) []string {
	var out []string
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	for k := range seen {
		av, aOK := a[k]
		bv, bOK := b[k]
		switch {
		case !aOK && bOK:
			out = append(out, fmt.Sprintf(`%s.%s: added %q`, scope, k, bv))
		case aOK && !bOK:
			out = append(out, fmt.Sprintf(`%s.%s: removed`, scope, k))
		case av != bv:
			out = append(out, fmt.Sprintf(`%s.%s: %q → %q`, scope, k, av, bv))
		}
	}
	return out
}

// diffStepList indexes by step id where possible. Steps without an id
// (e.g. summary/upload rows) are compared positionally to whatever
// sits in the same slot on the other side.
func diffStepList(a, b []schema.Step, scope string) []string {
	var out []string
	aByID, aNoID := indexSteps(a)
	bByID, bNoID := indexSteps(b)
	seenIDs := map[string]bool{}
	for id := range aByID {
		seenIDs[id] = true
	}
	for id := range bByID {
		seenIDs[id] = true
	}
	for id := range seenIDs {
		as, aOK := aByID[id]
		bs, bOK := bByID[id]
		switch {
		case !aOK && bOK:
			out = append(out, fmt.Sprintf(`%s.%q: added (%s)`, scope, id, bs.ActionType))
		case aOK && !bOK:
			out = append(out, fmt.Sprintf(`%s.%q: removed`, scope, id))
		default:
			out = append(out, diffStep(scope, id, &as, &bs)...)
		}
	}
	// Anonymous steps — compare positionally, best-effort.
	for i := 0; i < len(aNoID) || i < len(bNoID); i++ {
		id := fmt.Sprintf("<%d>", i)
		var as, bs *schema.Step
		if i < len(aNoID) {
			as = &aNoID[i]
		}
		if i < len(bNoID) {
			bs = &bNoID[i]
		}
		switch {
		case as == nil:
			out = append(out, fmt.Sprintf(`%s.%s: added (%s)`, scope, id, bs.ActionType))
		case bs == nil:
			out = append(out, fmt.Sprintf(`%s.%s: removed`, scope, id))
		default:
			out = append(out, diffStep(scope, id, as, bs)...)
		}
	}
	return out
}

func indexSteps(steps []schema.Step) (map[string]schema.Step, []schema.Step) {
	byID := map[string]schema.Step{}
	var noID []schema.Step
	for _, s := range steps {
		if s.ID == "" {
			noID = append(noID, s)
			continue
		}
		byID[s.ID] = s
	}
	return byID, noID
}

func diffStep(scope, id string, a, b *schema.Step) []string {
	var out []string
	prefix := fmt.Sprintf(`%s.%q`, scope, id)
	if a.ActionType != b.ActionType {
		out = append(out, fmt.Sprintf(`%s.action: %s → %s`, prefix, a.ActionType, b.ActionType))
	}
	if a.If != b.If {
		out = append(out, fmt.Sprintf(`%s.if: %q → %q`, prefix, a.If, b.If))
	}
	if a.Timeout != b.Timeout {
		out = append(out, fmt.Sprintf(`%s.timeout: %s → %s`, prefix, a.Timeout, b.Timeout))
	}
	if a.Container != b.Container {
		out = append(out, fmt.Sprintf(`%s.container: %q → %q`, prefix, a.Container, b.Container))
	}
	if a.ForEach != b.ForEach {
		out = append(out, fmt.Sprintf(`%s.for-each: %q → %q`, prefix, a.ForEach, b.ForEach))
	}
	if a.ContinueOnError != b.ContinueOnError {
		out = append(out, fmt.Sprintf(`%s.continue-on-error: %v → %v`, prefix, a.ContinueOnError, b.ContinueOnError))
	}
	if !equalStringSlice(a.Needs, b.Needs) {
		out = append(out, fmt.Sprintf(`%s.needs: %v → %v`, prefix, a.Needs, b.Needs))
	}
	out = append(out, diffStringMap(prefix+".env", a.Env, b.Env)...)
	// Body compare: for run: steps the raw script matters most.
	if a.ActionNode != nil && b.ActionNode != nil {
		aBody := strings.TrimSpace(a.ActionNode.Value)
		bBody := strings.TrimSpace(b.ActionNode.Value)
		if aBody != bBody && a.ActionType == "run" {
			out = append(out, fmt.Sprintf(`%s.run: body changed`, prefix))
		}
	}
	// Retry
	switch {
	case a.Retry == nil && b.Retry != nil:
		out = append(out, fmt.Sprintf(`%s.retry: added (attempts=%d)`, prefix, b.Retry.Attempts))
	case a.Retry != nil && b.Retry == nil:
		out = append(out, fmt.Sprintf(`%s.retry: removed`, prefix))
	case a.Retry != nil && b.Retry != nil:
		if a.Retry.Attempts != b.Retry.Attempts || a.Retry.Delay != b.Retry.Delay || a.Retry.Backoff != b.Retry.Backoff {
			out = append(out, fmt.Sprintf(`%s.retry: %+v → %+v`, prefix, *a.Retry, *b.Retry))
		}
	}
	return out
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
