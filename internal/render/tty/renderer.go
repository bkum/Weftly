// Package tty is the live CLI renderer. It subscribes to the event bus and
// prints per-step grouped output with status glyphs. Secrets are masked at
// the emit boundary (in the run action) so this layer never sees them; the
// double-check here is defence in depth.
package tty

import (
	"fmt"
	"io"
	"time"

	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/secrets"
)

// Renderer writes human-readable output for a run.
type Renderer struct {
	Out     io.Writer
	Color   bool // ANSI escapes enabled
	Secrets *secrets.Registry

	currentStep string
	stepStart   time.Time
	summaries   []string
}

// New returns a Renderer bound to w.
func New(w io.Writer, color bool, sec *secrets.Registry) *Renderer {
	return &Renderer{Out: w, Color: color, Secrets: sec}
}

// Handle is the subscriber function passed to events.Bus.Subscribe.
func (r *Renderer) Handle(e events.Event) {
	switch ev := e.(type) {
	case events.RunStarted:
		r.printf("%s workflow %s  run %s\n", r.color(">", cyan), ev.Workflow, ev.RunID)
	case events.StepStarted:
		if r.currentStep != "" {
			// no-op; grouped step already announced its start
		}
		r.currentStep = ev.StepID
		r.stepStart = time.Now()
		name := ev.Name
		if name == "" {
			name = ev.StepID
		}
		r.printf("\n%s %s  %s\n", r.color("▶", cyan), name, r.color("["+ev.Action+"]", dim))
	case events.StepLog:
		line := ev.Line
		if r.Secrets != nil {
			line = r.Secrets.Mask(line)
		}
		prefix := "  "
		if ev.Stream == events.Stderr {
			prefix = "  " + r.color("!", yellow) + " "
		}
		r.printf("%s%s\n", prefix, line)
	case events.StepOutput:
		r.printf("  %s %s=%v\n", r.color("→", dim), ev.Key, ev.Value)
	case events.StepFinished:
		glyph, col := statusGlyph(ev.Status)
		msg := fmt.Sprintf("  %s %s in %s", r.color(glyph, col), ev.Status, ev.Duration.Round(time.Millisecond))
		if ev.Err != nil {
			msg += "  " + r.color(ev.Err.Error(), red)
		}
		r.printf("%s\n", msg)
	case events.SummaryEmitted:
		r.summaries = append(r.summaries, ev.Markdown)
	case events.ArtifactUploaded:
		r.printf("  %s artifact %s → %s (%d bytes)\n", r.color("📦", cyan), ev.Name, ev.Path, ev.Size)
	case events.RunFinished:
		if len(r.summaries) > 0 {
			r.printf("\n%s Summary\n", r.color("═══", cyan))
			for _, s := range r.summaries {
				r.printf("%s\n", s)
			}
		}
		glyph, col := statusGlyph(ev.Status)
		r.printf("\n%s run %s in %s\n", r.color(glyph, col), ev.Status, ev.Duration.Round(time.Millisecond))
	}
}

func (r *Renderer) printf(format string, args ...any) {
	fmt.Fprintf(r.Out, format, args...)
}

// --- colors ---

const (
	reset  = "\x1b[0m"
	dim    = "\x1b[2m"
	red    = "\x1b[31m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	cyan   = "\x1b[36m"
)

func (r *Renderer) color(s, code string) string {
	if !r.Color {
		return s
	}
	return code + s + reset
}

func statusGlyph(s events.Status) (string, string) {
	switch s {
	case events.Success:
		return "✓", green
	case events.Failed, events.TimedOut:
		return "✗", red
	case events.FailedContinued:
		return "⚠", yellow
	case events.Skipped:
		return "⊘", dim
	default:
		return "•", dim
	}
}
