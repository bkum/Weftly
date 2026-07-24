package server_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bkum/weftly/internal/server"
)

func TestAuditRecordsPOSTRuns(t *testing.T) {
	dir, wfID := writeCatalogue(t)
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	ts := startServer(t, server.Config{
		CatalogueDir: dir,
		RunsDir:      t.TempDir(),
		Token:        "tk",
		AuditFile:    auditPath,
	})
	body, _ := json.Marshal(map[string]any{"workflow": wfID})
	req, _ := http.NewRequest("POST", ts.URL+"/runs", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer tk")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		RunID string `json:"run_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /runs: %d", resp.StatusCode)
	}
	// Wait for the async engine goroutine to reach RunFinished so it
	// isn't still writing state.json / report.html into the TempDir
	// when the test returns — otherwise t.Cleanup's RemoveAll trips
	// with "directory not empty" (seen intermittently on ubuntu CI).
	waitForRunFinish(t, ts.URL, "tk", created.RunID)
	// GET /audit — bearer-token principal is admin, should be allowed.
	req, _ = http.NewRequest("GET", ts.URL+"/audit", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /audit: %d", resp.StatusCode)
	}
	var body2 struct {
		Entries []struct {
			Method   string `json:"method"`
			Path     string `json:"path"`
			Workflow string `json:"workflow"`
			RunID    string `json:"run_id"`
			Status   int    `json:"status"`
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&body2); err != nil {
		t.Fatal(err)
	}
	if len(body2.Entries) < 1 {
		t.Fatalf("want at least 1 audit entry, got %+v", body2)
	}
	got := body2.Entries[0]
	if got.Method != "POST" || got.Path != "/runs" || got.Workflow != wfID || got.Status != http.StatusAccepted {
		t.Errorf("entry mismatch: %+v", got)
	}
	// POST /runs has no {id} in the URL path — the audit middleware
	// now peeks the response body for run_id so grepping "who created
	// run X" works. Assert that.
	if got.RunID == "" || !strings.HasPrefix(got.RunID, "2026") {
		t.Errorf("audit entry should capture run_id from response body, got %q", got.RunID)
	}
	// The on-disk file should have grown by at least one line.
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"workflow":"`+wfID+`"`) {
		t.Errorf("audit file missing workflow entry:\n%s", data)
	}
}

// waitForRunFinish polls GET /runs/{id} until state.json's status
// leaves "running", or a short deadline elapses. Purely a housekeeping
// helper so a test's t.TempDir() can be cleaned up without racing the
// engine goroutine that keeps writing state.json / report.html after
// POST /runs has already returned 202.
func waitForRunFinish(t *testing.T, base, tok, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest("GET", base+"/runs/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		var body struct {
			Status string `json:"status"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if body.Status != "" && body.Status != "running" && body.Status != "pending" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %s did not finish within deadline", id)
}

func TestAuditSkipsGETRequests(t *testing.T) {
	dir, _ := writeCatalogue(t)
	ts := startServer(t, server.Config{
		CatalogueDir: dir,
		RunsDir:      t.TempDir(),
		Token:        "tk",
	})
	// A read-only GET should not add an audit entry.
	req, _ := http.NewRequest("GET", ts.URL+"/workflows", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	req, _ = http.NewRequest("GET", ts.URL+"/audit", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Entries []any `json:"entries"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Entries) != 0 {
		t.Errorf("GET should not be audited, got %d entries", len(body.Entries))
	}
}
