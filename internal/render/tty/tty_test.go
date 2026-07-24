package tty

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/secrets"
)

func renderAll(r *Renderer, evs ...events.Event) string {
	for _, e := range evs {
		r.Handle(e)
	}
	return r.Out.(*bytes.Buffer).String()
}

func TestRendererEmitsLifecycle(t *testing.T) {
	buf := &bytes.Buffer{}
	r := New(buf, false, nil)
	out := renderAll(r,
		events.RunStarted{Workflow: "wf", RunID: "r1"},
		events.StepStarted{StepID: "s", Name: "Step", Action: "run"},
		events.StepLog{StepID: "s", Stream: events.Stdout, Line: "hello"},
		events.StepLog{StepID: "s", Stream: events.Stderr, Line: "warn"},
		events.StepFinished{StepID: "s", Status: events.Success, Duration: 5 * time.Millisecond},
		events.RunFinished{Status: events.Success, Duration: 6 * time.Millisecond},
	)
	for _, want := range []string{"workflow wf", "run r1", "Step", "[run]", "hello", "warn", "✓", "s success"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRendererCIWrapsGroups(t *testing.T) {
	buf := &bytes.Buffer{}
	r := NewCI(buf, nil)
	out := renderAll(r,
		events.RunStarted{Workflow: "wf", RunID: "r"},
		events.StepStarted{StepID: "s", Name: "Step", Action: "run"},
		events.StepFinished{StepID: "s", Status: events.Success},
	)
	if !strings.Contains(out, "::group::Step (run)") {
		t.Errorf("missing ::group:: marker: %q", out)
	}
	if !strings.Contains(out, "::endgroup::") {
		t.Errorf("missing ::endgroup:: marker: %q", out)
	}
}

func TestRendererMasksSecretsInLogs(t *testing.T) {
	sec := secrets.NewRegistry()
	sec.Register("api-key-abcdef")
	buf := &bytes.Buffer{}
	r := New(buf, false, sec)
	renderAll(r,
		events.StepStarted{StepID: "s", Action: "run"},
		events.StepLog{StepID: "s", Stream: events.Stdout, Line: "using api-key-abcdef here"},
	)
	if strings.Contains(buf.String(), "api-key-abcdef") {
		t.Errorf("secret leaked into tty output: %q", buf.String())
	}
}

func TestRendererStepFinishedShowsErr(t *testing.T) {
	buf := &bytes.Buffer{}
	r := New(buf, false, nil)
	renderAll(r,
		events.StepStarted{StepID: "s", Action: "run"},
		events.StepFinished{StepID: "s", Status: events.Failed, Err: errors.New("kaboom")},
	)
	if !strings.Contains(buf.String(), "kaboom") {
		t.Errorf("stderr not surfaced: %q", buf.String())
	}
}

func TestRendererParallelActiveTagsLogs(t *testing.T) {
	// With two active steps, log lines should carry the [id] prefix.
	buf := &bytes.Buffer{}
	r := New(buf, false, nil)
	renderAll(r,
		events.StepStarted{StepID: "a", Action: "run"},
		events.StepStarted{StepID: "b", Action: "run"},
		events.StepLog{StepID: "a", Stream: events.Stdout, Line: "from-a"},
	)
	if !strings.Contains(buf.String(), "[a]") {
		t.Errorf("parallel prefix missing: %q", buf.String())
	}
}

func TestJSONRendererEmitsTypedEnvelope(t *testing.T) {
	buf := &bytes.Buffer{}
	r := NewJSON(buf, nil)
	r.Handle(events.StepLog{StepID: "s", Stream: events.Stdout, Line: "hi"})
	// The output should be one JSON object per line with a "type" field.
	line := strings.TrimSpace(buf.String())
	var env struct {
		Type  string          `json:"type"`
		Event json.RawMessage `json:"event"`
	}
	if err := json.Unmarshal([]byte(line), &env); err != nil {
		t.Fatalf("bad JSON: %v (%q)", err, line)
	}
	if env.Type != "StepLog" {
		t.Errorf("want type=StepLog, got %q", env.Type)
	}
}

func TestJSONRendererMasksStepLog(t *testing.T) {
	sec := secrets.NewRegistry()
	sec.Register("hidden-secret-abcdef")
	buf := &bytes.Buffer{}
	r := NewJSON(buf, sec)
	r.Handle(events.StepLog{StepID: "s", Line: "raw hidden-secret-abcdef end"})
	if strings.Contains(buf.String(), "hidden-secret-abcdef") {
		t.Errorf("JSON renderer leaked secret: %q", buf.String())
	}
}

func TestStatusGlyphKnownStatuses(t *testing.T) {
	cases := map[events.Status]string{
		events.Success:         "✓",
		events.Failed:          "✗",
		events.TimedOut:        "✗",
		events.FailedContinued: "⚠",
		events.Skipped:         "⊘",
		events.Pending:         "•",
	}
	for s, want := range cases {
		if got, _ := statusGlyph(s); got != want {
			t.Errorf("statusGlyph(%s)=%q, want %q", s, got, want)
		}
	}
}

func TestRendererColorWraps(t *testing.T) {
	buf := &bytes.Buffer{}
	r := New(buf, true, nil)
	r.Handle(events.RunStarted{Workflow: "wf", RunID: "r"})
	if !strings.Contains(buf.String(), "\x1b[") {
		t.Error("expected ANSI escape when Color=true")
	}
}
