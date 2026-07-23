package server_test

import (
	"bufio"
	"bytes"
	"context"
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

// TestListRunsEmptyAndPopulated hits GET /runs on a fresh runsDir (empty
// list) then again after a single run has completed (one entry). Also
// exercises the ?workflow=<id> filter.
func TestListRunsEmptyAndPopulated(t *testing.T) {
	dir, wfID := writeCatalogue(t)
	runsDir := t.TempDir()
	ts := startServer(t, server.Config{
		CatalogueDir: dir,
		RunsDir:      runsDir,
		Token:        "tk",
	})

	// empty
	req, _ := http.NewRequest("GET", ts.URL+"/runs", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Runs []map[string]any
	}
	_ = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if len(got.Runs) != 0 {
		t.Fatalf("empty runsDir should yield 0 runs, got %d", len(got.Runs))
	}

	// start one and wait for RunFinished
	body, _ := json.Marshal(map[string]any{"workflow": wfID, "inputs": map[string]any{"who": "friend"}})
	req, _ = http.NewRequest("POST", ts.URL+"/runs", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tk")
	req.Header.Set("Content-Type", "application/json")
	resp, _ = http.DefaultClient.Do(req)
	var rr struct {
		RunID string `json:"run_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&rr)
	resp.Body.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sseReq, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/runs/"+rr.RunID+"/events", nil)
	sseReq.Header.Set("Authorization", "Bearer tk")
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(sseResp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if strings.Contains(sc.Text(), `"RunFinished"`) {
			break
		}
	}
	sseResp.Body.Close()

	// populated
	req, _ = http.NewRequest("GET", ts.URL+"/runs", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err := json.Unmarshal(body2, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Runs) != 1 {
		t.Fatalf("expected 1 run, got %d, body=%s", len(got.Runs), string(body2))
	}
	if got.Runs[0]["run_id"] != rr.RunID {
		t.Errorf("run_id mismatch: %v", got.Runs[0])
	}
	if got.Runs[0]["status"] != "success" {
		t.Errorf("status: %v", got.Runs[0]["status"])
	}
	if got.Runs[0]["workflow"] != wfID {
		t.Errorf("workflow: %v", got.Runs[0]["workflow"])
	}

	// filter by another workflow → empty
	req, _ = http.NewRequest("GET", ts.URL+"/runs?workflow=other", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, _ = http.DefaultClient.Do(req)
	_ = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if len(got.Runs) != 0 {
		t.Fatalf("filtered by unknown workflow: expected 0, got %d", len(got.Runs))
	}
	// filter by our workflow → 1
	req, _ = http.NewRequest("GET", ts.URL+"/runs?workflow="+wfID, nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, _ = http.DefaultClient.Do(req)
	_ = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if len(got.Runs) != 1 {
		t.Fatalf("filtered by workflow: expected 1, got %d", len(got.Runs))
	}
	// stray files in runs dir don't break the listing
	if err := os.WriteFile(filepath.Join(runsDir, "runs", "stray.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequest("GET", ts.URL+"/runs", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, _ = http.DefaultClient.Do(req)
	_ = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if len(got.Runs) != 1 {
		t.Errorf("stray file should not affect listing: got %d", len(got.Runs))
	}
}
