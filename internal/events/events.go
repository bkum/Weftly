// Package events defines the typed event stream produced during a workflow
// run and a synchronous in-process fan-out bus. Every user-facing renderer
// (TTY today, SSE in Phase 2) subscribes to this bus — nothing else has a
// legitimate way to produce user-visible output.
package events

import (
	"encoding/json"
	"time"
)

// Event is the closed set of things that can happen during a run.
// Callers switch on the concrete type.
type Event interface {
	isEvent()
}

type RunStarted struct {
	Workflow  string
	RunID     string
	Workspace string
}

type StepStarted struct {
	StepID string
	Name   string
	Action string
}

// Stream discriminates the origin of a log line.
type Stream string

const (
	Stdout Stream = "stdout"
	Stderr Stream = "stderr"
	Info   Stream = "info" // engine/action-emitted informational lines
)

type StepLog struct {
	StepID string
	Stream Stream
	Line   string
}

type StepOutput struct {
	StepID string
	Key    string
	Value  any
}

// Status is the terminal state of a step. Matches spec §9.
type Status string

const (
	Pending         Status = "pending"
	Running         Status = "running"
	Success         Status = "success"
	Failed          Status = "failed"
	FailedContinued Status = "failed-continued"
	Skipped         Status = "skipped"
	TimedOut        Status = "timed-out"
)

type StepFinished struct {
	StepID   string
	Status   Status
	Duration time.Duration
	Err      error
	// Resumed is true when this step is being re-emitted from a prior run's
	// state.json (via `weftly run --resume <run-id>`) rather than freshly
	// executed. Renderers/reports use it to badge the step as "resumed".
	Resumed bool
}

// StepRetry announces that a step failed but will be retried under
// the step's `retry:` policy. Renderers surface it as an inline
// "retrying (attempt N/M in Δ)" line so operators see the loop
// happening rather than a mysteriously long-running step. It does NOT
// mark a step as finished — a StepFinished still lands after the last
// attempt (successful or fatal).
type StepRetry struct {
	StepID  string
	Attempt int // 1-indexed count of the attempt that just failed
	Of      int // total attempts allowed
	Delay   time.Duration
	Cause   Status // what failed the attempt (Failed or TimedOut)
	Err     error
}

type SummaryEmitted struct {
	StepID   string
	Markdown string
}

type ArtifactUploaded struct {
	Name string
	Path string
	Size int64
}

type RunFinished struct {
	Status   Status
	Duration time.Duration
}

func (RunStarted) isEvent()       {}
func (StepStarted) isEvent()      {}
func (StepLog) isEvent()          {}
func (StepOutput) isEvent()       {}
func (StepFinished) isEvent()     {}
func (StepRetry) isEvent()        {}
func (SummaryEmitted) isEvent()   {}
func (ArtifactUploaded) isEvent() {}
func (RunFinished) isEvent()      {}

// MarshalJSON on the two events with an `Err error` field renders it
// as the underlying error's Error() string instead of Go's default
// (which would marshal the interface as `{}`, hiding the real cause
// from the SSE client and the SPA — a common trap for "why can't I
// see why my step failed").
func (e StepFinished) MarshalJSON() ([]byte, error) {
	type alias StepFinished
	return json.Marshal(struct {
		alias
		Err string `json:"Err,omitempty"`
	}{alias(e), errString(e.Err)})
}

func (e StepRetry) MarshalJSON() ([]byte, error) {
	type alias StepRetry
	return json.Marshal(struct {
		alias
		Err string `json:"Err,omitempty"`
	}{alias(e), errString(e.Err)})
}

func errString(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}
