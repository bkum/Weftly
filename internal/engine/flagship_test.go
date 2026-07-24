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
	"github.com/bkum/weftly/internal/mockpetclinic"
	"github.com/bkum/weftly/internal/schema"
)

// TestFlagshipWorkflowE2E runs workflows/petclinic-onboarding.yml against
// an in-process mock server. It exercises every core mechanism named in
// the spec's DoD: declarative http, idempotent `if:`, http→run/jq
// handoff, env-var-safe secret passing, template rendering, summary, and
// upload — against a generic REST shape rather than a proprietary one.
func TestFlagshipWorkflowE2E(t *testing.T) {
	for _, tool := range []string{"curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("host tool %s not available on PATH", tool)
		}
	}

	apiKey := "test-api-key-value-abcdef"
	srv := mockpetclinic.New(apiKey)
	defer srv.Close()

	wfPath := filepath.Join("..", "..", "workflows", "petclinic-onboarding.yml")
	wf, err := schema.Load(wfPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := schema.Validate(wf); err != nil {
		t.Fatalf("validate: %v", err)
	}

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
			"env_url":    srv.URL,
			"api_key":    apiKey,
			"owner_last": "Doe",
		},
		Bus: bus,
	})
	if err != nil {
		t.Fatalf("run: %v\nlogs:\n%s", err, strings.Join(logs, "\n"))
	}
	if res.Status != events.Success {
		t.Fatalf("expected success, got %s\nlogs:\n%s", res.Status, strings.Join(logs, "\n"))
	}

	// The api_key must never appear in any log line.
	for _, l := range logs {
		if strings.Contains(l, apiKey) {
			t.Fatalf("secret api_key leaked into log: %q", l)
		}
	}

	runDir := strings.TrimSuffix(res.StateFile, "state.json")
	report := filepath.Join(runDir, "report.html")
	if _, err := os.Stat(report); err != nil {
		t.Errorf("report not written: %v", err)
	}
	artifact := filepath.Join(runDir, "artifacts", "onboarding-report.html")
	if _, err := os.Stat(artifact); err != nil {
		t.Errorf("artifact not copied: %v", err)
	}
	if body, err := os.ReadFile(artifact); err == nil {
		if !strings.Contains(string(body), "Jane Doe") {
			t.Errorf("artifact body missing owner: %q", string(body[:min(200, len(body))]))
		}
		if !strings.Contains(string(body), "Rex") {
			t.Errorf("artifact body missing pet: %q", string(body[:min(200, len(body))]))
		}
	}

	// Server state: exactly one owner + one pet + one visit produced.
	if got := srv.Owners(); len(got) != 1 || got[0].LastName != "Doe" {
		t.Errorf("expected 1 owner 'Doe', got %+v", got)
	}
	if got := srv.Pets(); len(got) != 1 {
		t.Errorf("expected 1 pet, got %+v", got)
	}
	if got := srv.Visits(); len(got) != 1 {
		t.Errorf("expected 1 visit, got %+v", got)
	}
}
