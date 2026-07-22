// Package events defines the typed event stream produced during a workflow
// run and a synchronous in-process fan-out bus. Every user-facing renderer
// (TTY today, SSE in Phase 2) subscribes to this bus — nothing else has a
// legitimate way to produce user-visible output.
package events

import "time"

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
func (SummaryEmitted) isEvent()   {}
func (ArtifactUploaded) isEvent() {}
func (RunFinished) isEvent()      {}
