package schema

import (
	"fmt"
	"regexp"
	"strings"
)

// Error is a single validation problem. Line is 0 when unknown.
type Error struct {
	Line    int
	Path    string
	Message string
}

func (e Error) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("line %d: %s: %s", e.Line, e.Path, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Path, e.Message)
}

// Errors is a list of validation errors.
type Errors []Error

func (es Errors) Error() string {
	var b strings.Builder
	for i, e := range es {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(e.Error())
	}
	return b.String()
}

// ExitCode reports how the CLI should exit when this error escapes.
func (es Errors) ExitCode() int { return 2 }

// idPattern accepts letters, digits, and underscore. The spec §5 also lists
// hyphens, but a hyphenated id (e.g. `resolve-id`) is parsed as subtraction
// by the expression engine (`steps.resolve - id.outputs.x`) and cannot be
// referenced. We rejected it here rather than shipping a footgun.
var idPattern = regexp.MustCompile(`^[a-z0-9_]+$`)

// Validate runs the spec §5 validation rules over a parsed workflow.
// It returns nil if the workflow is valid, or a non-nil error (typically
// schema.Errors) enumerating every problem it found.
func Validate(wf *Workflow) error {
	var errs Errors
	if wf == nil {
		return Errors{{Message: "nil workflow"}}
	}
	if strings.TrimSpace(wf.Name) == "" {
		errs = append(errs, Error{Path: "name", Message: "required"})
	}
	errs = append(errs, validateInputs(wf)...)
	errs = append(errs, validateSteps(wf)...)
	if len(errs) == 0 {
		return nil
	}
	return errs
}

func validateInputs(wf *Workflow) Errors {
	var errs Errors
	for name, in := range wf.Inputs {
		if !idPattern.MatchString(name) {
			errs = append(errs, Error{Path: "inputs." + name, Message: "name must match [a-z0-9_-]+"})
		}
		switch in.Type {
		case "", InputString, InputNumber, InputBool:
		default:
			errs = append(errs, Error{Path: "inputs." + name + ".type", Message: fmt.Sprintf("unknown type %q", in.Type)})
		}
	}
	return errs
}

func validateSteps(wf *Workflow) Errors {
	var errs Errors
	if len(wf.Steps) == 0 {
		errs = append(errs, Error{Path: "steps", Message: "at least one step required"})
		return errs
	}
	seen := make(map[string]int, len(wf.Steps))
	for i, s := range wf.Steps {
		path := fmt.Sprintf("steps[%d]", i)
		line := 0
		if s.Source != nil {
			line = s.Source.Line
		}
		// Certain actions (summary, upload) don't require an id.
		if s.ID == "" && requiresID(s.ActionType) {
			errs = append(errs, Error{Line: line, Path: path, Message: "id is required"})
		}
		if s.ID != "" && !idPattern.MatchString(s.ID) {
			errs = append(errs, Error{Line: line, Path: path + ".id", Message: "must match [a-z0-9_-]+"})
		}
		if s.ID != "" {
			if _, dup := seen[s.ID]; dup {
				errs = append(errs, Error{Line: line, Path: path + ".id", Message: fmt.Sprintf("duplicate id %q", s.ID)})
			}
			seen[s.ID] = i
		}
		if s.ActionType == "" {
			errs = append(errs, Error{Line: line, Path: path, Message: "must declare exactly one action key (" + strings.Join(actionKeys, ", ") + ")"})
		}
		// container: is only meaningful when the step body is a shell
		// script — no other action would know what to do with an image.
		if s.Container != "" && s.ActionType != "run" {
			errs = append(errs, Error{Line: line, Path: path + ".container", Message: "container: is only valid on a run step"})
		}
		if s.Retry != nil {
			if s.Retry.Attempts < 2 {
				errs = append(errs, Error{Line: line, Path: path + ".retry.attempts", Message: "attempts must be >= 2 (attempts=1 means no retry — omit the block instead)"})
			}
			if s.Retry.Attempts > 20 {
				errs = append(errs, Error{Line: line, Path: path + ".retry.attempts", Message: "attempts must be <= 20 (guard against runaway loops)"})
			}
			switch s.Retry.Backoff {
			case "", "linear", "exponential":
			default:
				errs = append(errs, Error{Line: line, Path: path + ".retry.backoff", Message: "backoff must be one of \"\" (constant), \"linear\", or \"exponential\""})
			}
			for _, on := range s.Retry.On {
				if on != "failed" && on != "timed-out" {
					errs = append(errs, Error{Line: line, Path: path + ".retry.on", Message: fmt.Sprintf("unknown status %q (allowed: failed, timed-out)", on)})
				}
			}
		}
	}

	// needs references must exist and must not form a cycle.
	for i, s := range wf.Steps {
		path := fmt.Sprintf("steps[%d]", i)
		line := 0
		if s.Source != nil {
			line = s.Source.Line
		}
		for _, dep := range s.Needs {
			if _, ok := seen[dep]; !ok {
				errs = append(errs, Error{Line: line, Path: path + ".needs", Message: fmt.Sprintf("unknown step %q", dep)})
			}
		}
	}
	if cycle := detectCycle(wf.Steps, seen); cycle != nil {
		errs = append(errs, Error{Path: "steps.needs", Message: "cycle in `needs`: " + strings.Join(cycle, " -> ")})
	}
	return errs
}

// requiresID reports whether an action must have a step id. Terminal actions
// like summary and upload are commonly used without one; they cannot be
// referenced from `steps.<id>.outputs` anyway.
func requiresID(action string) bool {
	switch action {
	case "summary", "upload", "assert":
		return false
	default:
		return true
	}
}

// detectCycle returns a cycle path if one exists in the needs graph. Steps
// without `needs` are ignored; DFS with white/grey/black colouring.
func detectCycle(steps []Step, index map[string]int) []string {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := make(map[string]int, len(steps))
	var stack []string
	var visit func(id string) []string
	visit = func(id string) []string {
		if color[id] == grey {
			// found a cycle back to id
			for i, s := range stack {
				if s == id {
					return append(append([]string{}, stack[i:]...), id)
				}
			}
			return []string{id, id}
		}
		if color[id] == black {
			return nil
		}
		color[id] = grey
		stack = append(stack, id)
		i, ok := index[id]
		if ok {
			for _, dep := range steps[i].Needs {
				if _, exists := index[dep]; !exists {
					continue
				}
				if c := visit(dep); c != nil {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return nil
	}
	for id := range index {
		if color[id] == white {
			if c := visit(id); c != nil {
				return c
			}
		}
	}
	return nil
}
