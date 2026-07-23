package server_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bkum/weftly/internal/server"
)

// writeLongRunningCatalogue provides a workflow whose first step sleeps
// long enough for a DELETE to land before natural completion.
func writeLongRunningCatalogue(t *testing.T) (dir, id string) {
	t.Helper()
	dir = t.TempDir()
	body := `
name: slowpoke
steps:
  - id: sleep
    run: |
      sleep 30
      echo "done=1" >> "$WEFTLY_OUTPUT"
`
	if err := os.WriteFile(filepath.Join(dir, "slowpoke.yml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, "slowpoke"
}

func TestCancelRunTerminatesInFlightRun(t *testing.T) {
	dir, wfID := writeLongRunningCatalogue(t)
	ts := startServer(t, server.Config{
		CatalogueDir: dir,
		RunsDir:      t.TempDir(),
		Token:        "tk",
	})

	// Kick the run.
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
	if created.RunID == "" {
		t.Fatal("empty run_id")
	}

	// Open the SSE stream so we can see when RunFinished lands after
	// the cancel.
	sseReq, _ := http.NewRequest("GET", ts.URL+"/runs/"+created.RunID+"/events?token=tk", nil)
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()

	// Give the engine a beat to actually start the step and register
	// its cmd.Cancel, otherwise DELETE races the goroutine.
	time.Sleep(150 * time.Millisecond)

	delReq, _ := http.NewRequest("DELETE", ts.URL+"/runs/"+created.RunID, nil)
	delReq.Header.Set("Authorization", "Bearer tk")
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusAccepted && delResp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE: %d", delResp.StatusCode)
	}

	// Read the SSE until we see RunFinished — with a deadline much
	// shorter than the 30 s sleep so a broken cancel path visibly fails.
	deadline := time.Now().Add(8 * time.Second)
	scan := bufio.NewScanner(sseResp.Body)
	scan.Buffer(make([]byte, 64*1024), 1024*1024)
	saw := false
	for time.Now().Before(deadline) && scan.Scan() {
		line := scan.Text()
		if strings.Contains(line, `"RunFinished"`) {
			saw = true
			break
		}
	}
	if !saw {
		t.Fatalf("no RunFinished within deadline (cancel didn't propagate)")
	}
}

func TestCancelUnknownRun(t *testing.T) {
	dir, _ := writeCatalogue(t)
	ts := startServer(t, server.Config{
		CatalogueDir: dir,
		RunsDir:      t.TempDir(),
		Token:        "tk",
	})
	req, _ := http.NewRequest("DELETE", ts.URL+"/runs/nope", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("want 404 for unknown run, got %d: %s", resp.StatusCode, body)
	}
}

func TestCancelAfterCompletionIsIdempotent(t *testing.T) {
	dir, wfID := writeCatalogue(t) // the smoke wf finishes instantly
	ts := startServer(t, server.Config{
		CatalogueDir: dir,
		RunsDir:      t.TempDir(),
		Token:        "tk",
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
	if created.RunID == "" {
		t.Fatal("empty run_id")
	}
	// Give the run a moment to naturally finish.
	time.Sleep(400 * time.Millisecond)
	delReq, _ := http.NewRequest("DELETE", ts.URL+"/runs/"+created.RunID, nil)
	delReq.Header.Set("Authorization", "Bearer tk")
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	delResp.Body.Close()
	// Either 202 (raced, cancelled harmlessly) or 200 (already finished).
	if delResp.StatusCode != http.StatusOK && delResp.StatusCode != http.StatusAccepted {
		t.Fatalf("DELETE after completion: got %d", delResp.StatusCode)
	}
}
