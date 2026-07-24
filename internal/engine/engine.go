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
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	"github.com/bkum/weftly/internal/tracing"
	"github.com/bkum/weftly/internal/workspace"
	"go.opentelemetry.io/otel/attribute"
)

// Options bundles run-time knobs.
type Options struct {
	BaseDir  string // parent of runs/ (default ./.weftly)
	Strict   bool   // pass through to actions
	AutoYes  bool   // --yes: prompt(type:confirm) auto-answers true
	Parallel int    // max concurrent steps; default 4
	Resume   string // resume this run-id (or state.json path); empty starts a new run
	Inputs   map[string]any
	Vars     map[string]string // --var overrides of workflow env
	Bus      *events.Bus

	// ArtifactStore, when non-nil, is passed to actions so upload
	// mirrors each artifact to it in addition to the local artifacts
	// dir. Server mode wires this to an S3 / MinIO store from config.
	ArtifactStore actions.RemoteArtifactStore

	// PostSubscribers are event-bus subscribers registered AFTER
	// engine's own state.Writer + report. They observe events strictly
	// after those two have processed them, so a caller (typically the
	// server's per-run record) can hand off from live-SSE to disk
	// (state.json / report.html / artifacts) without racing the
	// filesystem. Empty in CLI mode.
	PostSubscribers []func(events.Event)

	// Logger, when non-nil, is handed to internal writers (state.json,
	// audit-adjacent code paths) so a persistence failure (disk full,
	// read-only mount) is surfaced instead of silently swallowed.
	// Server mode wires the server's logger here; CLI leaves it nil.
	Logger *slog.Logger
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

	// Resume: reload prior run state, reuse its workspace + run-id.
	var (
		runID       string
		resumeCache map[string]*state.StepState
	)
	baseDir := opts.BaseDir
	if baseDir == "" {
		baseDir = "./.weftly"
	}
	if opts.Resume != "" {
		prior, _, err := state.Load(filepath.Join(baseDir, "runs"), opts.Resume)
		if err != nil {
			return Result{}, err
		}
		runID = prior.RunID
		resumeCache = map[string]*state.StepState{}
		for id, s := range prior.Steps {
			if s.Status == events.Success {
				resumeCache[id] = s
			}
		}
	} else {
		runID = newRunID()
	}
	ws, err := workspace.New(baseDir, runID)
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
	sw.Logger = opts.Logger
	if opts.Resume != "" {
		prior, _, _ := state.Load(filepath.Join(baseDir, "runs"), opts.Resume)
		if prior != nil {
			sw.Adopt(prior)
		}
	}
	rep := report.New(sec)
	bus.Subscribe(sw.Handle)
	bus.Subscribe(rep.Handle)
	// Late subscribers see events strictly after state.Writer + report
	// have processed them — required for the server's runRecord so an
	// SSE client that pivots to GET /runs/:id can't observe stale
	// state.json.
	for _, sub := range opts.PostSubscribers {
		bus.Subscribe(sub)
	}

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
	// stepViews is read by runStep to build the expr env; writes are
	// serialised by stepMu so parallel steps don't race.
	stepViews := map[string]expr.StepView{}
	var stepMu sync.Mutex

	bus.Publish(events.RunStarted{Workflow: wf.Name, RunID: runID, Workspace: ws.StepsDir})
	runStart := time.Now()

	// Wrap the whole run in a tracing span. tracing.Start returns a
	// no-op when no exporter is configured, so this is free when the
	// operator hasn't set --otel-endpoint.
	ctx, runSpan := tracing.Start(ctx, "workflow.run",
		attribute.String("weftly.workflow", wf.Name),
		attribute.String("weftly.run_id", runID),
	)
	defer runSpan.End()

	defaultShell := wf.Defaults.Shell
	parallel := opts.Parallel
	if parallel < 1 {
		parallel = 4
	}

	rc := runCtx{
		Workflow:      wf,
		Inputs:        inputs,
		Secrets:       sec,
		Env:           baseEnv,
		Steps:         stepViews,
		StepMu:        &stepMu,
		ResumeCache:   resumeCache,
		Workspace:     ws,
		ArtifactStore: opts.ArtifactStore,
		Bus:           bus,
		Expr:          ev,
		DefaultShell:  defaultShell,
		Strict:        opts.Strict,
		AutoYes:       opts.AutoYes,
		RunID:         runID,
	}
	overallStatus := schedule(ctx, graph, parallel, func(ctx context.Context, node *ir.StepNode) events.Status {
		return runStep(ctx, node, rc)
	})

	// Cleanup pass. Runs sequentially (no needs: edges among cleanup
	// steps in v0.4), sees the aggregated overallStatus via
	// expr.Env.Run.Status so `if: ${{ failure() }}` gates work, and
	// uses a detached child context so a run cancelled mid-graph still
	// gets its cleanup — the alternative (skipping cleanup on
	// cancellation) is exactly the case where cleanup matters most.
	if len(wf.Cleanup) > 0 {
		runCleanup(ctx, wf.Cleanup, rc, overallStatus)
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
	Workflow      *schema.Workflow
	Inputs        map[string]any
	Secrets       *secrets.Registry
	Env           map[string]string
	Steps         map[string]expr.StepView
	StepMu        *sync.Mutex // serialises reads/writes to Steps under parallel execution
	ResumeCache   map[string]*state.StepState
	Workspace     *workspace.Workspace
	Bus           *events.Bus
	Expr          *expr.Evaluator
	DefaultShell  string
	Strict        bool
	AutoYes       bool
	RunID         string
	ArtifactStore actions.RemoteArtifactStore
	// CleanupStatus is empty during main-graph execution; set to the
	// run's aggregate final status when runCleanup dispatches a
	// cleanup step. Feeds success()/failure() so cleanup gates work.
	CleanupStatus    string
	CleanupCancelled bool
	// ForEachIter, when non-nil, means we're inside one iteration of a
	// for-each expansion. runStep injects it into envForExpr as `each`
	// and skips re-expansion. Nil for regular steps.
	ForEachIter *expr.EachContext
	// SuppressLifecycle causes runStep to skip publishing the outer
	// StepStarted/StepFinished events and skip the setStepView writes.
	// Used when the outer for-each wrapper is already tracking the
	// step's lifecycle and each iteration must not double-emit.
	SuppressLifecycle bool
}

// runStep resolves per-step context, dispatches to the action, and updates
// the shared step view. Returns the terminal status. Safe under parallel
// invocation as long as rc.StepMu guards reads/writes of rc.Steps.
func runStep(ctx context.Context, node *ir.StepNode, rc runCtx) events.Status {
	// Scheduler-injected cascade skip: emit Started + Finished{Skipped}
	// and don't execute the action.
	if node.SkipReason != "" {
		rc.Bus.Publish(events.StepStarted{StepID: node.ID, Name: node.Name, Action: node.Action})
		rc.Bus.Publish(events.StepFinished{StepID: node.ID, Status: events.Skipped, Err: fmt.Errorf("%s", node.SkipReason)})
		setStepView(rc, node.ID, expr.StepView{Status: string(events.Skipped), Outputs: map[string]any{}})
		return events.Skipped
	}

	// Resume replay: if this step's success is on disk from a prior run,
	// re-emit its outputs synthetically and mark the finish as Resumed=true
	// so renderers/reports read the same as a fresh success.
	if rc.ResumeCache != nil {
		if prior, ok := rc.ResumeCache[node.ID]; ok && prior.Status == events.Success {
			rc.Bus.Publish(events.StepStarted{StepID: node.ID, Name: node.Name, Action: node.Action})
			for k, v := range prior.Outputs {
				rc.Bus.Publish(events.StepOutput{StepID: node.ID, Key: k, Value: v})
			}
			rc.Bus.Publish(events.StepFinished{
				StepID: node.ID, Status: events.Success, Duration: prior.Duration, Resumed: true,
			})
			outs := map[string]any{}
			for k, v := range prior.Outputs {
				outs[k] = v
			}
			setStepView(rc, node.ID, expr.StepView{Status: string(events.Success), Outputs: outs})
			return events.Success
		}
	}

	// For-each expansion. When ForEach is set and we're at the outer
	// invocation (rc.ForEachIter == nil), evaluate the expression to a
	// list, emit one wrapping StepStarted, then recurse per element
	// with rc.ForEachIter populated. Aggregate outcome is the worst
	// element status (Failed > TimedOut > Success). Iterations run
	// sequentially; parallelism would need per-iteration StepView keys
	// and is deferred.
	if node.ForEach != "" && rc.ForEachIter == nil {
		rc.Bus.Publish(events.StepStarted{StepID: node.ID, Name: node.Name, Action: node.Action})
		list, ferr := evaluateForEach(rc, node)
		if ferr != nil {
			rc.Bus.Publish(events.StepFinished{StepID: node.ID, Status: events.Failed, Err: ferr})
			setStepView(rc, node.ID, expr.StepView{Status: string(events.Failed), Outputs: map[string]any{}})
			return events.Failed
		}
		if len(list) == 0 {
			// Empty list = nothing to do; treat as success so downstream
			// steps aren't blocked. Emit an informational log line so
			// operators can see the loop ran zero times deliberately.
			rc.Bus.Publish(events.StepLog{StepID: node.ID, Stream: events.Info, Line: "for-each: empty list, skipping body"})
			rc.Bus.Publish(events.StepFinished{StepID: node.ID, Status: events.Success})
			setStepView(rc, node.ID, expr.StepView{Status: string(events.Success), Outputs: map[string]any{}})
			return events.Success
		}
		worst := events.Success
		childNode := *node
		childNode.ForEach = "" // prevent infinite recursion
		for i, v := range list {
			rc.Bus.Publish(events.StepLog{StepID: node.ID, Stream: events.Info, Line: fmt.Sprintf("for-each[%d/%d] value=%v", i+1, len(list), v)})
			rc2 := rc
			rc2.ForEachIter = &expr.EachContext{Value: v, Index: i}
			rc2.SuppressLifecycle = true
			status := runStep(ctx, &childNode, rc2)
			if isFatal(status) && !isFatal(worst) {
				worst = status
			}
		}
		rc.Bus.Publish(events.StepFinished{StepID: node.ID, Status: worst})
		setStepView(rc, node.ID, expr.StepView{Status: string(worst), Outputs: map[string]any{}})
		return worst
	}

	// Snapshot the step map under lock so parallel reads see a consistent
	// view even while other steps are writing.
	rc.StepMu.Lock()
	stepsSnap := make(map[string]expr.StepView, len(rc.Steps))
	for k, v := range rc.Steps {
		stepsSnap[k] = v
	}
	rc.StepMu.Unlock()

	envForExpr := expr.Env{
		Inputs:  rc.Inputs,
		Steps:   stepsSnap,
		Env:     rc.Env,
		Secrets: map[string]string{}, // secrets exposed as-is to expressions; renderer masks output
		Run: expr.RunMeta{
			ID:        rc.RunID,
			Workspace: rc.Workspace.StepsDir,
			Status:    rc.CleanupStatus,    // empty during main graph, set during cleanup
			Cancelled: rc.CleanupCancelled, // ditto
		},
		Each: rc.ForEachIter, // nil outside a for-each iteration
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
			setStepView(rc, node.ID, expr.StepView{Status: string(events.Failed), Outputs: map[string]any{}})
			return events.Failed
		}
		if !ok {
			rc.Bus.Publish(events.StepStarted{StepID: node.ID, Name: node.Name, Action: node.Action})
			rc.Bus.Publish(events.StepFinished{StepID: node.ID, Status: events.Skipped})
			setStepView(rc, node.ID, expr.StepView{Status: string(events.Skipped), Outputs: map[string]any{}})
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
	if rc.ForEachIter != nil {
		resolvedEnv["EACH_INDEX"] = fmt.Sprintf("%d", rc.ForEachIter.Index)
		resolvedEnv["EACH_VALUE"] = fmt.Sprintf("%v", rc.ForEachIter.Value)
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
		StepID:        node.ID,
		StepName:      node.Name,
		Action:        node.Action,
		Config:        node.Config.ActionNode,
		Inputs:        rc.Inputs,
		Steps:         rc.Steps,
		Env:           resolvedEnv,
		Secrets:       rc.Secrets,
		Workdir:       rc.Workspace.StepsDir,
		ArtifactsDir:  rc.Workspace.ArtifactsDir,
		ExprEnv:       envForExpr,
		Emit:          rc.Bus.Publish,
		Expr:          rc.Expr,
		Shell:         shell,
		Container:     node.Container,
		Timeout:       node.Timeout,
		Strict:        rc.Strict,
		AutoYes:       rc.AutoYes,
		HTTPTimeout:   rc.Workflow.Defaults.HTTP.Timeout,
		HTTPHeaders:   rc.Workflow.Defaults.HTTP.Headers,
		ArtifactStore: rc.ArtifactStore,
		RunID:         rc.RunID,
	}

	if !rc.SuppressLifecycle {
		rc.Bus.Publish(events.StepStarted{StepID: node.ID, Name: node.Name, Action: node.Action})
	}
	stepCtx, stepSpan := tracing.Start(ctx, "workflow.step",
		attribute.String("weftly.step_id", node.ID),
		attribute.String("weftly.action", node.Action),
	)
	ctx = stepCtx
	defer stepSpan.End()

	// runOnce wraps a single attempt so the retry loop can call it
	// without duplicating the timeout / stepCtx plumbing. Returns the
	// action outputs on success, or (nil, cause, err) on failure — the
	// caller decides whether to loop or surface a StepFinished. Per-
	// attempt duration isn't returned because StepFinished carries the
	// wall-clock total (see `time.Since(start)` below).
	runOnce := func() (actions.Outputs, events.Status, error) {
		stepCtx := ctx
		var cancel context.CancelFunc
		if node.Timeout > 0 {
			stepCtx, cancel = context.WithTimeout(ctx, node.Timeout)
			defer cancel()
		}
		outs, err := act.Run(stepCtx, sc)
		if err != nil {
			if stepCtx.Err() == context.DeadlineExceeded {
				return nil, events.TimedOut, err
			}
			return nil, events.Failed, err
		}
		return outs, events.Success, nil
	}

	start := time.Now()
	outs, cause, err := runOnce()

	// Retry loop. Attempts is 1-indexed against total; attempt==1 was
	// the initial call above. Only loop while the observed cause is in
	// the retryable set.
	if err != nil && node.Retry != nil && retryHandles(node.Retry, cause) {
		total := node.Retry.Attempts
		for attempt := 1; attempt < total; attempt++ {
			delay := retryDelay(node.Retry, attempt)
			rc.Bus.Publish(events.StepRetry{
				StepID:  node.ID,
				Attempt: attempt,
				Of:      total,
				Delay:   delay,
				Cause:   cause,
				Err:     err,
			})
			select {
			case <-ctx.Done():
				// Run was cancelled while we were sleeping between
				// attempts. Fall through to the normal failure path.
				err = ctx.Err()
				goto finishAttempts
			case <-time.After(delay):
			}
			outs, cause, err = runOnce()
			if err == nil {
				break
			}
			if !retryHandles(node.Retry, cause) {
				break
			}
		}
	}
finishAttempts:
	dur := time.Since(start)

	if err != nil {
		if cause == events.TimedOut {
			emitFinished(rc, node.ID,
				events.StepFinished{StepID: node.ID, Status: events.TimedOut, Duration: dur, Err: err},
				expr.StepView{Status: string(events.TimedOut), Outputs: map[string]any{}})
			return events.TimedOut
		}
		if node.ContinueOnError {
			emitFinished(rc, node.ID,
				events.StepFinished{StepID: node.ID, Status: events.FailedContinued, Duration: dur, Err: err},
				expr.StepView{Status: string(events.FailedContinued), Outputs: map[string]any{}})
			return events.FailedContinued
		}
		emitFinished(rc, node.ID,
			events.StepFinished{StepID: node.ID, Status: events.Failed, Duration: dur, Err: err},
			expr.StepView{Status: string(events.Failed), Outputs: map[string]any{}})
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
				setStepView(rc, node.ID, expr.StepView{Status: string(events.Failed), Outputs: map[string]any{}})
				return events.Failed
			}
			outs[k] = v
			rc.Bus.Publish(events.StepOutput{StepID: node.ID, Key: k, Value: v})
		}
	}
	emitFinished(rc, node.ID,
		events.StepFinished{StepID: node.ID, Status: events.Success, Duration: dur},
		expr.StepView{Status: string(events.Success), Outputs: outs})
	return events.Success
}

// setStepView updates rc.Steps under the shared mutex so parallel step
// execution doesn't race on the map. Anonymous steps (empty id) are
// tracked in the map under a synthetic key so the scheduler's completion
// tally is correct, but nobody references them via expressions.
func setStepView(rc runCtx, id string, sv expr.StepView) {
	if id == "" {
		return
	}
	rc.StepMu.Lock()
	rc.Steps[id] = sv
	rc.StepMu.Unlock()
}

func abortStep(rc runCtx, node *ir.StepNode, err error) events.Status {
	rc.Bus.Publish(events.StepStarted{StepID: node.ID, Name: node.Name, Action: node.Action})
	rc.Bus.Publish(events.StepFinished{StepID: node.ID, Status: events.Failed, Err: err})
	setStepView(rc, node.ID, expr.StepView{Status: string(events.Failed), Outputs: map[string]any{}})
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

// runCleanup executes each cleanup step in schema order. The step's
// `if:` expression is evaluated against the final run status via the
// updated expr.Env.Run.Status, so `if: ${{ failure() || cancelled() }}`
// behaves as documented. Cleanup steps ignore the retry: block (loops
// on top of a "we're already tearing down" pass are more surprising
// than useful) but honour timeout: and continue-on-error.
//
// A cleanup step's failure never changes the run's aggregate status —
// the main graph already decided the run's outcome. Failures still
// emit StepFinished{Failed} so state.json + report.html show them.
func runCleanup(parentCtx context.Context, steps []schema.Step, rc runCtx, finalStatus events.Status) {
	// Detached context so a run that was cancelled mid-graph still runs
	// its cleanup — that's precisely the "please tear this down" case.
	// A separate 60 s cap ensures a wedged cleanup can't hang the process.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	statusStr := string(finalStatus)
	cancelled := parentCtx.Err() != nil
	for i := range steps {
		s := &steps[i]
		// Cleanup steps are compiled ad-hoc so the loop can propagate
		// the run's aggregate status into every expression evaluation.
		node := &ir.StepNode{
			ID:              nonEmpty(s.ID, fmt.Sprintf("cleanup_%d", i+1)),
			Name:            s.Name,
			Action:          s.ActionType,
			Config:          s,
			If:              s.If,
			Env:             s.Env,
			ContinueOnError: s.ContinueOnError,
			Timeout:         s.Timeout,
			Shell:           s.Shell,
			Container:       s.Container,
			OutputsMap:      s.Outputs,
		}
		// Snapshot the shared env into a per-call view with the run's
		// aggregate status set. runStep re-derives envForExpr from
		// rc, so we mutate rc's fields transiently via a wrapper.
		rc2 := rc
		rc2.CleanupStatus = statusStr
		rc2.CleanupCancelled = cancelled
		_ = runStep(ctx, node, rc2)
	}
}

// evaluateForEach compiles + evaluates the step's for-each expression
// and coerces the result to a []any. Accepts a real slice, a JSON
// array, or a comma-separated string (for cheap host input passing).
func evaluateForEach(rc runCtx, node *ir.StepNode) ([]any, error) {
	rc.StepMu.Lock()
	stepsSnap := make(map[string]expr.StepView, len(rc.Steps))
	for k, v := range rc.Steps {
		stepsSnap[k] = v
	}
	rc.StepMu.Unlock()
	env := expr.Env{
		Inputs:  rc.Inputs,
		Steps:   stepsSnap,
		Env:     rc.Env,
		Secrets: map[string]string{},
		Run:     expr.RunMeta{ID: rc.RunID, Workspace: rc.Workspace.StepsDir},
	}
	body := stripWrap(node.ForEach)
	v, err := rc.Expr.Evaluate(body, env)
	if err != nil {
		return nil, fmt.Errorf("for-each: %w", err)
	}
	switch xs := v.(type) {
	case []any:
		return xs, nil
	case []string:
		out := make([]any, len(xs))
		for i, s := range xs {
			out[i] = s
		}
		return out, nil
	case string:
		if xs == "" {
			return nil, nil
		}
		// comma-separated fallback so `for-each: ${{ inputs.hosts }}`
		// works with a plain string input.
		parts := strings.Split(xs, ",")
		out := make([]any, len(parts))
		for i, p := range parts {
			out[i] = strings.TrimSpace(p)
		}
		return out, nil
	case nil:
		return nil, nil
	}
	return nil, fmt.Errorf("for-each: expression must evaluate to a list, got %T", v)
}

// emitFinished publishes StepFinished and writes the step view unless
// the runCtx is marked SuppressLifecycle (i.e. we're inside a for-each
// iteration whose outer wrapper is doing the bookkeeping).
func emitFinished(rc runCtx, id string, fin events.StepFinished, view expr.StepView) {
	if rc.SuppressLifecycle {
		return
	}
	rc.Bus.Publish(fin)
	setStepView(rc, id, view)
}

func nonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// retryHandles reports whether a Retry policy covers the given
// terminal cause. Empty `on:` defaults to {failed} — a step that hit
// its explicit timeout is usually not what a retry loop is meant to
// paper over.
func retryHandles(r *schema.Retry, cause events.Status) bool {
	if r == nil {
		return false
	}
	set := r.On
	if len(set) == 0 {
		set = []string{"failed"}
	}
	for _, s := range set {
		if s == string(cause) {
			return true
		}
	}
	return false
}

// retryDelay computes the wait before attempt N+1 (attempt is 1-indexed
// against the loop counter used in executeStep). Backoff "" is
// constant, "linear" scales by attempt count, "exponential" doubles.
// A zero Delay stays zero — no accidental sleep from "linear * 0".
func retryDelay(r *schema.Retry, attempt int) time.Duration {
	if r == nil || r.Delay <= 0 {
		return 0
	}
	switch r.Backoff {
	case "linear":
		return r.Delay * time.Duration(attempt)
	case "exponential":
		return r.Delay << (attempt - 1)
	default:
		return r.Delay
	}
}
