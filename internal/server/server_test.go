package server_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bkum/weftly/internal/server"
)

// writeCatalogue drops a tiny valid workflow into a temp dir and returns
// the dir path plus the workflow id (its filename stem).
func writeCatalogue(t *testing.T) (dir, id string) {
	t.Helper()
	dir = t.TempDir()
	body := `
name: smoke
description: minimal workflow for server tests
inputs:
  who:
    default: world
steps:
  - id: hello
    run: |
      echo "greeting=hi-$WHO" >> "$WEFTLY_OUTPUT"
    env:
      WHO: "${{ inputs.who }}"
`
	path := filepath.Join(dir, "smoke.yml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, "smoke"
}

func startServer(t *testing.T, cfg server.Config) *httptest.Server {
	t.Helper()
	srv, err := server.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestServerHealthAndCatalogue(t *testing.T) {
	dir, wfID := writeCatalogue(t)
	ts := startServer(t, server.Config{
		CatalogueDir: dir,
		RunsDir:      t.TempDir(),
		Token:        "test-token",
	})

	// /healthz stays open (no auth).
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("/healthz: got %d", resp.StatusCode)
	}

	// /workflows without a token → 401.
	resp, err = http.Get(ts.URL + "/workflows")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("/workflows unauth: want 401, got %d", resp.StatusCode)
	}

	// /workflows with token → 200 and lists smoke.
	req, _ := http.NewRequest("GET", ts.URL+"/workflows", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("/workflows auth: want 200, got %d", resp.StatusCode)
	}
	var body struct {
		Workflows []struct{ ID, Name string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Workflows) != 1 || body.Workflows[0].ID != wfID {
		t.Fatalf("unexpected workflows: %+v", body)
	}
}

func TestServerRunAndSSE(t *testing.T) {
	dir, wfID := writeCatalogue(t)
	runsDir := t.TempDir()
	ts := startServer(t, server.Config{
		CatalogueDir: dir,
		RunsDir:      runsDir,
		Token:        "tk",
	})

	// POST /runs
	body, _ := json.Marshal(map[string]any{
		"workflow": wfID,
		"inputs":   map[string]any{"who": "friend"},
	})
	req, _ := http.NewRequest("POST", ts.URL+"/runs", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tk")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /runs: want 202, got %d, body=%s", resp.StatusCode, string(b))
	}
	var runResp struct {
		RunID string `json:"run_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&runResp); err != nil {
		t.Fatal(err)
	}
	if runResp.RunID == "" {
		t.Fatal("empty run_id")
	}

	// GET /runs/:id/events (SSE) — read until RunFinished
	sseReq, _ := http.NewRequest("GET", ts.URL+"/runs/"+runResp.RunID+"/events", nil)
	sseReq.Header.Set("Authorization", "Bearer tk")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sseReq = sseReq.WithContext(ctx)
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()
	if sseResp.StatusCode != 200 {
		t.Fatalf("SSE: got %d", sseResp.StatusCode)
	}
	sc := bufio.NewScanner(sseResp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	sawFinished := false
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, `"RunFinished"`) {
			sawFinished = true
			break
		}
	}
	if !sawFinished {
		t.Fatalf("did not see RunFinished in SSE stream")
	}

	// GET /runs/:id returns state.json
	sReq, _ := http.NewRequest("GET", ts.URL+"/runs/"+runResp.RunID, nil)
	sReq.Header.Set("Authorization", "Bearer tk")
	sResp, err := http.DefaultClient.Do(sReq)
	if err != nil {
		t.Fatal(err)
	}
	defer sResp.Body.Close()
	if sResp.StatusCode != 200 {
		t.Fatalf("GET /runs/:id: got %d", sResp.StatusCode)
	}
	var st map[string]any
	if err := json.NewDecoder(sResp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st["run_id"] != runResp.RunID {
		t.Errorf("state run_id mismatch: %v", st["run_id"])
	}
}

func TestServerArtifactDownloadPathSafe(t *testing.T) {
	// A tiny workflow that writes a file into the workspace and uploads it.
	dir := t.TempDir()
	body := `
name: with-artifact
steps:
  - id: write
    run: |
      mkdir -p out
      echo "hello from weftly server test" > out/note.txt
  - upload:
      path: ./out/note.txt
      name: note
`
	if err := os.WriteFile(filepath.Join(dir, "art.yml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	runsDir := t.TempDir()
	ts := startServer(t, server.Config{
		CatalogueDir: dir,
		RunsDir:      runsDir,
		Token:        "tk",
	})
	// Start run.
	post, _ := json.Marshal(map[string]any{"workflow": "art"})
	req, _ := http.NewRequest("POST", ts.URL+"/runs", bytes.NewReader(post))
	req.Header.Set("Authorization", "Bearer tk")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /runs: %d %s", resp.StatusCode, string(b))
	}
	var rr struct {
		RunID string `json:"run_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&rr)

	// Drain SSE until RunFinished so the upload has definitely landed.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sseReq, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/runs/"+rr.RunID+"/events", nil)
	sseReq.Header.Set("Authorization", "Bearer tk")
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()
	sc := bufio.NewScanner(sseResp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if strings.Contains(sc.Text(), `"RunFinished"`) {
			break
		}
	}

	_ = runsDir
	// Artifact GET works.
	aReq, _ := http.NewRequest("GET", ts.URL+"/runs/"+rr.RunID+"/artifacts/note.txt", nil)
	aReq.Header.Set("Authorization", "Bearer tk")
	aResp, err := http.DefaultClient.Do(aReq)
	if err != nil {
		t.Fatal(err)
	}
	defer aResp.Body.Close()
	if aResp.StatusCode != 200 {
		t.Fatalf("artifact GET: want 200, got %d", aResp.StatusCode)
	}
	data, _ := io.ReadAll(aResp.Body)
	if !bytes.Contains(data, []byte("hello from weftly server test")) {
		t.Errorf("artifact body wrong: %q", string(data))
	}

	// Traversal rejected.
	tReq, _ := http.NewRequest("GET", ts.URL+"/runs/"+rr.RunID+"/artifacts/..%2F..%2Fetc%2Fpasswd", nil)
	tReq.Header.Set("Authorization", "Bearer tk")
	tResp, err := http.DefaultClient.Do(tReq)
	if err != nil {
		t.Fatal(err)
	}
	tResp.Body.Close()
	if tResp.StatusCode == 200 {
		t.Errorf("traversal: expected non-200, got 200")
	}
}
