package engine_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	// register built-in actions
	_ "github.com/bkum/weftly/internal/actions"

	"github.com/bkum/weftly/internal/engine"
	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/mocktn"
	"github.com/bkum/weftly/internal/schema"
)

// TestFlagshipWorkflowE2E runs examples/b2b-getting-started.yml against an
// in-process mock TN server, exercising every core mechanism named in the
// spec's DoD: declarative http, idempotent `if:`, http→run/jq handoff,
// env-var-safe secret passing, template rendering, summary, and upload.
func TestFlagshipWorkflowE2E(t *testing.T) {
	for _, tool := range []string{"curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("host tool %s not available on PATH", tool)
		}
	}

	token := "test-token-value-abcdef"
	srv := mocktn.New(token)
	defer srv.Close()

	// resolve examples/b2b-getting-started.yml relative to this file
	wfPath := filepath.Join("..", "..", "examples", "b2b-getting-started.yml")
	wf, err := schema.Load(wfPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := schema.Validate(wf); err != nil {
		t.Fatalf("validate: %v", err)
	}

	// Isolate run state to a per-test dir; keep it around on failure for
	// inspection.
	baseDir := t.TempDir()

	bus := events.NewBus()
	var logs []string
	bus.Subscribe(func(e events.Event) {
		switch v := e.(type) {
		case events.StepLog:
			logs = append(logs, string(v.Stream)+": "+v.Line)
		case events.StepFinished:
			msg := "step " + v.StepID + " " + string(v.Status)
			if v.Err != nil {
				msg += " err=" + v.Err.Error()
			}
			logs = append(logs, msg)
		}
	})

	res, err := engine.Run(context.Background(), wf, engine.Options{
		BaseDir: baseDir,
		Inputs: map[string]any{
			"env_url":      srv.URL,
			"api_token":    token,
			"partner_name": "Acme Corp",
		},
		Bus: bus,
	})
	if err != nil {
		t.Fatalf("run: %v\nlogs:\n%s", err, strings.Join(logs, "\n"))
	}
	if res.Status != events.Success {
		t.Fatalf("expected success, got %s\nlogs:\n%s", res.Status, strings.Join(logs, "\n"))
	}

	// Token must never appear in any log line.
	for _, l := range logs {
		if strings.Contains(l, token) {
			t.Fatalf("secret token leaked into log: %q", l)
		}
	}

	// The report and artifact are present.
	report := filepath.Join(res.StateFile[:len(res.StateFile)-len("state.json")], "report.html")
	if _, err := os.Stat(report); err != nil {
		t.Errorf("report not written: %v", err)
	}
	artifact := filepath.Join(res.StateFile[:len(res.StateFile)-len("state.json")], "artifacts", "onboarding-report.html")
	if _, err := os.Stat(artifact); err != nil {
		t.Errorf("artifact not copied: %v", err)
	}
	body, err := os.ReadFile(artifact)
	if err == nil && !strings.Contains(string(body), "Acme Corp") {
		t.Errorf("artifact body missing partner name: %q", string(body[:min(200, len(body))]))
	}

	// Server state: exactly one partner + one document produced.
	if got := srv.Partners(); len(got) != 1 || got[0].Name != "Acme Corp" {
		t.Errorf("expected 1 partner 'Acme Corp', got %+v", got)
	}
	if got := srv.Documents(); len(got) != 1 {
		t.Errorf("expected 1 document, got %+v", got)
	}
}
