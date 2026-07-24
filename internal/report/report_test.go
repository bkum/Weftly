package report

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/secrets"
)

func TestReportHandleAccumulatesLifecycle(t *testing.T) {
	r := New(nil)
	r.Handle(events.RunStarted{Workflow: "wf", RunID: "r1", Workspace: "/tmp/ws"})
	r.Handle(events.StepStarted{StepID: "s1", Name: "Step One", Action: "run"})
	r.Handle(events.StepFinished{StepID: "s1", Status: events.Success, Duration: 42 * time.Millisecond})
	r.Handle(events.SummaryEmitted{StepID: "s1", Markdown: "# Done"})
	r.Handle(events.ArtifactUploaded{Name: "out.txt", Path: "/tmp/out.txt", Size: 12})
	r.Handle(events.RunFinished{Status: events.Success, Duration: time.Second})

	path := filepath.Join(t.TempDir(), "sub", "report.html")
	if err := r.Write(path); err != nil {
		t.Fatalf("write: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{
		"<h1>wf</h1>",
		"run <code>r1</code>",
		"class=ok", // success statusClass
		"Step One",
		"<h2>Summary</h2>",
		"<h1>Done</h1>", // markdown # heading rendered
		"<h2>Artifacts</h2>",
		"out.txt",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q\n---\n%s", want, got)
		}
	}
}

func TestReportRetryBumpsAttempts(t *testing.T) {
	r := New(nil)
	r.Handle(events.RunStarted{Workflow: "wf", RunID: "r"})
	r.Handle(events.StepStarted{StepID: "s", Name: "S", Action: "run"})
	r.Handle(events.StepRetry{StepID: "s", Attempt: 0}) // -> 1
	r.Handle(events.StepRetry{StepID: "s", Attempt: 1}) // -> 2
	r.Handle(events.StepFinished{StepID: "s", Status: events.Success})
	r.Handle(events.RunFinished{Status: events.Success})

	path := filepath.Join(t.TempDir(), "report.html")
	if err := r.Write(path); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "attempts=2") {
		t.Errorf("expected attempts=2 in report, got:\n%s", body)
	}
}

func TestReportResumedBadgeRendered(t *testing.T) {
	r := New(nil)
	r.Handle(events.RunStarted{Workflow: "wf", RunID: "r"})
	r.Handle(events.StepStarted{StepID: "s", Name: "S", Action: "run"})
	r.Handle(events.StepFinished{StepID: "s", Status: events.Success, Resumed: true})
	r.Handle(events.RunFinished{Status: events.Success})

	path := filepath.Join(t.TempDir(), "report.html")
	_ = r.Write(path)
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "(resumed)") {
		t.Errorf("expected (resumed) badge, got:\n%s", body)
	}
}

func TestReportMasksSecretsInSummaryAndError(t *testing.T) {
	sec := secrets.NewRegistry()
	sec.Register("super-secret-abcdef")
	r := New(sec)
	r.Handle(events.RunStarted{Workflow: "wf", RunID: "r"})
	r.Handle(events.StepStarted{StepID: "s", Name: "S", Action: "run"})
	r.Handle(events.StepFinished{StepID: "s", Status: events.Failed,
		Err: errors.New("failed with token super-secret-abcdef")})
	r.Handle(events.SummaryEmitted{StepID: "s", Markdown: "leak: super-secret-abcdef"})
	r.Handle(events.RunFinished{Status: events.Failed})

	path := filepath.Join(t.TempDir(), "report.html")
	if err := r.Write(path); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), "super-secret-abcdef") {
		t.Errorf("secret leaked into report:\n%s", body)
	}
}

func TestReportStatusClassCovers(t *testing.T) {
	cases := map[events.Status]string{
		events.Success:         "ok",
		events.Failed:          "err",
		events.TimedOut:        "err",
		events.FailedContinued: "warn",
		events.Pending:         "info",
	}
	for in, want := range cases {
		if got := statusClass(in); got != want {
			t.Errorf("statusClass(%s)=%s, want %s", in, got, want)
		}
	}
}

func TestReportRenderMarkdownSubset(t *testing.T) {
	md := "# H\n\n- a\n- b\n\n```\ncode\n```\n\n**bold** and `x`"
	out := renderMarkdown(md)
	for _, w := range []string{"<h1>H</h1>", "<ul>", "<li>a</li>", "<pre>", "code", "<strong>bold</strong>", "<code>x</code>"} {
		if !strings.Contains(out, w) {
			t.Errorf("markdown output missing %q\n---\n%s", w, out)
		}
	}
}
