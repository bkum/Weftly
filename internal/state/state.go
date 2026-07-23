// Package state persists a run's progress to disk as JSON. It subscribes to
// the event bus, mutates an in-memory model, and flushes to
// ./.weftly/runs/<id>/state.json at every step transition. Secrets are
// masked before values ever leave the process (spec §16).
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/secrets"
)

type StepState struct {
	Name     string         `json:"name,omitempty"`
	Action   string         `json:"action,omitempty"`
	Status   events.Status  `json:"status"`
	Duration time.Duration  `json:"duration_ns,omitempty"`
	Outputs  map[string]any `json:"outputs,omitempty"`
	Error    string         `json:"error,omitempty"`
	// Attempts is the number of times executeStep called the action for
	// this step under a `retry:` policy. Absent = single attempt (the
	// common case); 2+ means the retry loop kicked in. Renderers/
	// reports use this to badge a step "succeeded after N attempts".
	Attempts   int        `json:"attempts,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type Run struct {
	RunID      string                `json:"run_id"`
	Workflow   string                `json:"workflow"`
	Workspace  string                `json:"workspace"`
	StartedAt  time.Time             `json:"started_at"`
	FinishedAt *time.Time            `json:"finished_at,omitempty"`
	Status     events.Status         `json:"status"`
	Steps      map[string]*StepState `json:"steps"`
	StepOrder  []string              `json:"step_order"`
}

// Writer collects events into a Run and flushes state.json.
type Writer struct {
	Path    string
	Secrets *secrets.Registry

	mu  sync.Mutex
	run *Run
}

// New returns a Writer that persists state to file. dir is the run root
// (./.weftly/runs/<id>). state.json is created on the first flush.
func New(dir string, sec *secrets.Registry) *Writer {
	return &Writer{
		Path:    filepath.Join(dir, "state.json"),
		Secrets: sec,
		run:     &Run{Steps: map[string]*StepState{}},
	}
}

// Snapshot returns a deep-ish copy of the current run for reporting.
func (w *Writer) Snapshot() *Run {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.run
}

// Handle is the events.Bus subscriber.
func (w *Writer) Handle(e events.Event) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch ev := e.(type) {
	case events.RunStarted:
		w.run.RunID = ev.RunID
		w.run.Workflow = ev.Workflow
		w.run.Workspace = ev.Workspace
		w.run.StartedAt = time.Now()
		w.run.Status = events.Running
	case events.StepStarted:
		start := time.Now()
		s := w.stepLocked(ev.StepID)
		s.Name = ev.Name
		s.Action = ev.Action
		s.Status = events.Running
		s.StartedAt = &start
		w.run.StepOrder = appendUnique(w.run.StepOrder, ev.StepID)
	case events.StepRetry:
		s := w.stepLocked(ev.StepID)
		// Attempts on StepRetry is 1-indexed against the attempt that
		// just failed. The final attempt count lands on StepFinished
		// via ev.Attempt+1 when the retry succeeds, but if the step
		// fails all attempts we still want the full count — set it to
		// ev.Attempt here and Finished bumps only on success.
		if ev.Attempt+1 > s.Attempts {
			s.Attempts = ev.Attempt + 1
		}
	case events.StepOutput:
		s := w.stepLocked(ev.StepID)
		if s.Outputs == nil {
			s.Outputs = map[string]any{}
		}
		s.Outputs[ev.Key] = w.maskValue(ev.Value)
	case events.StepFinished:
		fin := time.Now()
		s := w.stepLocked(ev.StepID)
		s.Status = ev.Status
		s.Duration = ev.Duration
		s.FinishedAt = &fin
		if ev.Err != nil {
			s.Error = w.maskString(ev.Err.Error())
		}
	case events.RunFinished:
		fin := time.Now()
		w.run.FinishedAt = &fin
		w.run.Status = ev.Status
	}
	_ = w.flushLocked()
}

func (w *Writer) stepLocked(id string) *StepState {
	if w.run.Steps == nil {
		w.run.Steps = map[string]*StepState{}
	}
	s, ok := w.run.Steps[id]
	if !ok {
		s = &StepState{}
		w.run.Steps[id] = s
	}
	return s
}

func (w *Writer) flushLocked() error {
	if w.Path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(w.Path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(w.run, "", "  ")
	if err != nil {
		return err
	}
	tmp := w.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, w.Path)
}

func (w *Writer) maskString(s string) string {
	if w.Secrets == nil {
		return s
	}
	return w.Secrets.Mask(s)
}

func (w *Writer) maskValue(v any) any {
	if s, ok := v.(string); ok {
		return w.maskString(s)
	}
	return v
}

func appendUnique(xs []string, x string) []string {
	for _, e := range xs {
		if e == x {
			return xs
		}
	}
	return append(xs, x)
}
