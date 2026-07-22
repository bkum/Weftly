// Package engine drives one run of a workflow through its lifecycle
// (spec §9). It compiles a validated schema.Workflow into an ir.Graph and
// then dispatches each step to the registered action, mediating everything
// through the event bus. The engine holds no user-visible output logic —
// that's the renderers' job.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bkum/weftly/internal/actions"
	"github.com/bkum/weftly/internal/compile"
	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/expr"
	"github.com/bkum/weftly/internal/ir"
	"github.com/bkum/weftly/internal/report"
	"github.com/bkum/weftly/internal/schema"
	"github.com/bkum/weftly/internal/secrets"
	"github.com/bkum/weftly/internal/state"
	"github.com/bkum/weftly/internal/workspace"
)

// Options bundles run-time knobs.
type Options struct {
	BaseDir string // parent of runs/ (default ./.weftly)
	Strict  bool   // pass through to actions
	Inputs  map[string]any
	Vars    map[string]string // --var overrides of workflow env
	Bus     *events.Bus
}

// Result summarises a completed run.
type Result struct {
	RunID      string
	Status     events.Status
	Duration   time.Duration
	StateFile  string
	ReportFile string
}

// ExitCode maps a Result to the CLI's exit code convention.
func (r Result) ExitCode() int {
	switch r.Status {
	case events.Success:
		return 0
	default:
		return 1
	}
}

// Run executes wf end-to-end. Callers wire renderers by subscribing to
// opts.Bus before calling Run.
func Run(ctx context.Context, wf *schema.Workflow, opts Options) (Result, error) {
	if opts.Bus == nil {
		opts.Bus = events.NewBus()
	}
	bus := opts.Bus

	runID := newRunID()
	ws, err := workspace.New(opts.BaseDir, runID)
	if err != nil {
		return Result{}, fmt.Errorf("workspace: %w", err)
	}

	// Merge inputs (flag values) with declared defaults; coerce/validate.
	inputs, secretVals, err := resolveInputs(wf, opts.Inputs)
	if err != nil {
		return Result{}, err
	}
	sec := secrets.NewRegistry()
	for _, v := range secretVals {
		if s, ok := v.(string); ok {
			sec.Register(s)
		}
	}

	// State + report writers subscribe to the same bus every renderer sees.
	sw := state.New(ws.Root, sec)
	rep := report.New(sec)
	bus.Subscribe(sw.Handle)
	bus.Subscribe(rep.Handle)

	// Preflight: check `requires:` tools are on PATH.
	if err := checkRequires(wf.Requires); err != nil {
		return Result{}, err
	}

	// Merge workflow env with --var overrides. Values may contain ${{ }}
	// which the engine resolves per-step (since expressions can reference
	// prior step outputs).
	baseEnv := map[string]string{}
	for k, v := range wf.Env {
		baseEnv[k] = v
	}
	for k, v := range opts.Vars {
		baseEnv[k] = v
	}

	graph := compile.Compile(wf)
	ev := expr.New()
	stepViews := map[string]expr.StepView{}

	bus.Publish(events.RunStarted{Workflow: wf.Name, RunID: runID, Workspace: ws.StepsDir})
	runStart := time.Now()
	overallStatus := events.Success

	defaultShell := wf.Defaults.Shell

	for _, node := range graph.Order {
		status := runStep(ctx, node, runCtx{
			Workflow:     wf,
			Inputs:       inputs,
			Secrets:      sec,
			Env:          baseEnv,
			Steps:        stepViews,
			Workspace:    ws,
			Bus:          bus,
			Expr:         ev,
			DefaultShell: defaultShell,
			Strict:       opts.Strict,
			RunID:        runID,
		})
		if status == events.Failed || status == events.TimedOut {
			overallStatus = events.Failed
			break
		}
	}

	dur := time.Since(runStart)
	bus.Publish(events.RunFinished{Status: overallStatus, Duration: dur})

	// state.json is flushed on every event by sw.Handle; report.html is a
	// terminal render.
	reportPath := ws.Root + "/report.html"
	if err := rep.Write(reportPath); err != nil {
		bus.Publish(events.StepLog{Stream: events.Info, Line: "report: " + err.Error()})
	}
	return Result{
		RunID:      runID,
		Status:     overallStatus,
		Duration:   dur,
		StateFile:  sw.Path,
		ReportFile: reportPath,
	}, nil
}

type runCtx struct {
	Workflow     *schema.Workflow
	Inputs       map[string]any
	Secrets      *secrets.Registry
	Env          map[string]string
	Steps        map[string]expr.StepView
	Workspace    *workspace.Workspace
	Bus          *events.Bus
	Expr         *expr.Evaluator
	DefaultShell string
	Strict       bool
	RunID        string
}

// runStep resolves per-step context, dispatches to the action, and updates
// the shared step view. Returns the terminal status.
func runStep(ctx context.Context, node *ir.StepNode, rc runCtx) events.Status {
	envForExpr := expr.Env{
		Inputs:  rc.Inputs,
		Steps:   rc.Steps,
		Env:     rc.Env,
		Secrets: map[string]string{}, // secrets exposed as-is to expressions; renderer masks output
		Run:     expr.RunMeta{ID: rc.RunID, Workspace: rc.Workspace.StepsDir},
	}
	// Give expressions access to secrets by name too.
	for _, name := range secretNames(rc.Workflow) {
		if v, ok := rc.Inputs[name]; ok {
			if s, ok := v.(string); ok {
				envForExpr.Secrets[name] = s
			}
		}
	}

	// if:
	if strings.TrimSpace(node.If) != "" {
		body := stripWrap(node.If)
		ok, err := rc.Expr.EvaluateBool(body, envForExpr)
		if err != nil {
			rc.Bus.Publish(events.StepStarted{StepID: node.ID, Name: node.Name, Action: node.Action})
			rc.Bus.Publish(events.StepFinished{StepID: node.ID, Status: events.Failed, Err: fmt.Errorf("if: %w", err)})
			rc.Steps[node.ID] = expr.StepView{Status: string(events.Failed), Outputs: map[string]any{}}
			return events.Failed
		}
		if !ok {
			rc.Bus.Publish(events.StepStarted{StepID: node.ID, Name: node.Name, Action: node.Action})
			rc.Bus.Publish(events.StepFinished{StepID: node.ID, Status: events.Skipped})
			rc.Steps[node.ID] = expr.StepView{Status: string(events.Skipped), Outputs: map[string]any{}}
			return events.Skipped
		}
	}

	// Resolve step env values against expr.
	resolvedEnv := map[string]string{}
	for k, v := range rc.Env {
		s, err := rc.Expr.InterpolateString(v, envForExpr)
		if err != nil {
			return abortStep(rc, node, fmt.Errorf("env %s: %w", k, err))
		}
		resolvedEnv[k] = s
	}
	for k, v := range node.Env {
		s, err := rc.Expr.InterpolateString(v, envForExpr)
		if err != nil {
			return abortStep(rc, node, fmt.Errorf("env %s: %w", k, err))
		}
		resolvedEnv[k] = s
	}

	act := actions.Get(node.Action)
	if act == nil {
		return abortStep(rc, node, fmt.Errorf("unknown action %q", node.Action))
	}

	shell := rc.DefaultShell
	if node.Shell != "" {
		shell = node.Shell
	}

	sc := &actions.StepContext{
		StepID:       node.ID,
		StepName:     node.Name,
		Action:       node.Action,
		Config:       node.Config.ActionNode,
		Inputs:       rc.Inputs,
		Steps:        rc.Steps,
		Env:          resolvedEnv,
		Secrets:      rc.Secrets,
		Workdir:      rc.Workspace.StepsDir,
		ArtifactsDir: rc.Workspace.ArtifactsDir,
		ExprEnv:      envForExpr,
		Emit:         rc.Bus.Publish,
		Expr:         rc.Expr,
		Shell:        shell,
		Timeout:      node.Timeout,
		Strict:       rc.Strict,
		HTTPTimeout:  rc.Workflow.Defaults.HTTP.Timeout,
		HTTPHeaders:  rc.Workflow.Defaults.HTTP.Headers,
	}

	rc.Bus.Publish(events.StepStarted{StepID: node.ID, Name: node.Name, Action: node.Action})
	start := time.Now()

	stepCtx := ctx
	var cancel context.CancelFunc
	if node.Timeout > 0 {
		stepCtx, cancel = context.WithTimeout(ctx, node.Timeout)
		defer cancel()
	}

	outs, err := act.Run(stepCtx, sc)
	dur := time.Since(start)

	if err != nil {
		if stepCtx.Err() == context.DeadlineExceeded {
			rc.Bus.Publish(events.StepFinished{StepID: node.ID, Status: events.TimedOut, Duration: dur, Err: err})
			rc.Steps[node.ID] = expr.StepView{Status: string(events.TimedOut), Outputs: map[string]any{}}
			return events.TimedOut
		}
		if node.ContinueOnError {
			rc.Bus.Publish(events.StepFinished{StepID: node.ID, Status: events.FailedContinued, Duration: dur, Err: err})
			rc.Steps[node.ID] = expr.StepView{Status: string(events.FailedContinued), Outputs: map[string]any{}}
			return events.FailedContinued
		}
		rc.Bus.Publish(events.StepFinished{StepID: node.ID, Status: events.Failed, Duration: dur, Err: err})
		rc.Steps[node.ID] = expr.StepView{Status: string(events.Failed), Outputs: map[string]any{}}
		return events.Failed
	}

	// Merge in step-declared outputs (http/template outputs mapping). If the
	// action populated sc.Response (http does), include it in the eval env.
	if len(node.OutputsMap) > 0 {
		if outs == nil {
			outs = actions.Outputs{}
		}
		outEnv := envForExpr
		if sc.Response != nil {
			outEnv.Response = sc.Response
		}
		for k, expression := range node.OutputsMap {
			v, err := rc.Expr.Interpolate(expression, outEnv)
			if err != nil {
				rc.Bus.Publish(events.StepFinished{StepID: node.ID, Status: events.Failed, Duration: dur, Err: fmt.Errorf("outputs.%s: %w", k, err)})
				rc.Steps[node.ID] = expr.StepView{Status: string(events.Failed), Outputs: map[string]any{}}
				return events.Failed
			}
			outs[k] = v
			rc.Bus.Publish(events.StepOutput{StepID: node.ID, Key: k, Value: v})
		}
	}
	rc.Bus.Publish(events.StepFinished{StepID: node.ID, Status: events.Success, Duration: dur})
	rc.Steps[node.ID] = expr.StepView{Status: string(events.Success), Outputs: outs}
	return events.Success
}

func abortStep(rc runCtx, node *ir.StepNode, err error) events.Status {
	rc.Bus.Publish(events.StepStarted{StepID: node.ID, Name: node.Name, Action: node.Action})
	rc.Bus.Publish(events.StepFinished{StepID: node.ID, Status: events.Failed, Err: err})
	rc.Steps[node.ID] = expr.StepView{Status: string(events.Failed), Outputs: map[string]any{}}
	return events.Failed
}

// stripWrap tolerates `if:` values written as `${{ ... }}` or as a bare
// expression — the spec's Appendix A uses both forms.
func stripWrap(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "${{") && strings.HasSuffix(s, "}}") {
		return strings.TrimSpace(s[3 : len(s)-2])
	}
	return s
}

func secretNames(wf *schema.Workflow) []string {
	var names []string
	for name, in := range wf.Inputs {
		if in.Secret {
			names = append(names, name)
		}
	}
	return names
}

func newRunID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}

func checkRequires(tools []string) error {
	var missing []string
	for _, t := range tools {
		if t == "" {
			continue
		}
		if _, err := exec.LookPath(t); err != nil {
			missing = append(missing, t)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("required tools missing from PATH: %s", strings.Join(missing, ", "))
}

// ensure imports we might not use in this file don't fail vet
var _ = os.Environ
