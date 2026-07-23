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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bkum/weftly/internal/mockpetclinic"
	"github.com/bkum/weftly/internal/server"
)

// TestServerFullFlagshipE2E starts the server pointing at a catalogue
// containing the flagship petclinic workflow, POSTs a run, tails the SSE
// stream to completion, and verifies the artifact downloads. This is the
// server-mode counterpart to internal/engine's flagship test.
func TestServerFullFlagshipE2E(t *testing.T) {
	for _, tool := range []string{"curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("host tool %s not on PATH", tool)
		}
	}
	// Stand up the mock PetClinic backend.
	mock := mockpetclinic.New("mock-key")
	defer mock.Close()

	// Build a catalogue dir that contains just the flagship workflow.
	cat := t.TempDir()
	src := filepath.Join("..", "..", "examples", "petclinic-onboarding.yml")
	b, err := os.ReadFile(src)
	if err != nil {
		t.Skipf("example not accessible: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cat, "petclinic-onboarding.yml"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	// Start the server.
	srv, err := server.New(server.Config{
		CatalogueDir: cat,
		RunsDir:      t.TempDir(),
		Token:        "tk",
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Discover the workflow via /workflows.
	req, _ := http.NewRequest("GET", ts.URL+"/workflows", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var listing struct {
		Workflows []struct {
			ID string `json:"id"`
		}
	}
	_ = json.NewDecoder(resp.Body).Decode(&listing)
	resp.Body.Close()
	if len(listing.Workflows) != 1 {
		t.Fatalf("expected exactly one workflow, got %d", len(listing.Workflows))
	}
	wfID := listing.Workflows[0].ID

	// Kick off the run.
	body, _ := json.Marshal(map[string]any{
		"workflow": wfID,
		"inputs": map[string]any{
			"env_url":    mock.URL,
			"api_key":    "mock-key",
			"owner_last": "Doe",
		},
	})
	req, _ = http.NewRequest("POST", ts.URL+"/runs", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tk")
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /runs: %d %s", resp.StatusCode, string(b))
	}
	var rr struct {
		RunID string `json:"run_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&rr)
	resp.Body.Close()

	// Tail SSE until we see RunFinished; assert the run succeeded and
	// the secret api_key never appears in a log line.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sseReq, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/runs/"+rr.RunID+"/events", nil)
	sseReq.Header.Set("Authorization", "Bearer tk")
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()
	sc := bufio.NewScanner(sseResp.Body)
	sc.Buffer(make([]byte, 128*1024), 4*1024*1024)
	sawFinished := false
	finishedOK := false
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "mock-key") {
			t.Errorf("secret api_key leaked into SSE line: %q", line)
		}
		if strings.Contains(line, `"RunFinished"`) {
			sawFinished = true
			finishedOK = strings.Contains(line, `"Status":"success"`)
			break
		}
	}
	if !sawFinished {
		t.Fatal("SSE closed without RunFinished")
	}
	if !finishedOK {
		t.Fatal("run did not finish with Status=success")
	}

	// Artifact must be downloadable and contain the owner name.
	aReq, _ := http.NewRequest("GET", ts.URL+"/runs/"+rr.RunID+"/artifacts/onboarding-report.html", nil)
	aReq.Header.Set("Authorization", "Bearer tk")
	aResp, err := http.DefaultClient.Do(aReq)
	if err != nil {
		t.Fatal(err)
	}
	defer aResp.Body.Close()
	if aResp.StatusCode != 200 {
		t.Fatalf("artifact download: got %d", aResp.StatusCode)
	}
	buf, _ := io.ReadAll(aResp.Body)
	if !bytes.Contains(buf, []byte("Doe")) {
		t.Errorf("artifact body missing owner: %.200s", string(buf))
	}

	// Server-side state on the mock: one owner, one pet, one visit.
	if got := mock.Owners(); len(got) != 1 {
		t.Errorf("expected 1 owner, got %+v", got)
	}
	if got := mock.Pets(); len(got) != 1 {
		t.Errorf("expected 1 pet, got %+v", got)
	}
	if got := mock.Visits(); len(got) != 1 {
		t.Errorf("expected 1 visit, got %+v", got)
	}
}

// TestServerCatalogueReload verifies POST /reload picks up a newly-added
// workflow file without restarting the server.
func TestServerCatalogueReload(t *testing.T) {
	dir := t.TempDir()
	// Start with an empty catalogue.
	srv, err := server.New(server.Config{
		CatalogueDir: dir,
		RunsDir:      t.TempDir(),
		Token:        "tk",
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// /workflows returns empty initially.
	req, _ := http.NewRequest("GET", ts.URL+"/workflows", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, _ := http.DefaultClient.Do(req)
	var l struct {
		Workflows []any `json:"workflows"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&l)
	resp.Body.Close()
	if len(l.Workflows) != 0 {
		t.Fatalf("expected empty catalogue, got %d", len(l.Workflows))
	}

	// Drop a workflow file and reload.
	if err := os.WriteFile(filepath.Join(dir, "hi.yml"), []byte("name: t\nsteps:\n  - id: a\n    run: echo hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequest("POST", ts.URL+"/reload", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("reload: got %d", resp.StatusCode)
	}

	// /workflows now returns one.
	req, _ = http.NewRequest("GET", ts.URL+"/workflows", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, _ = http.DefaultClient.Do(req)
	_ = json.NewDecoder(resp.Body).Decode(&l)
	resp.Body.Close()
	if len(l.Workflows) != 1 {
		t.Fatalf("expected 1 workflow after reload, got %d", len(l.Workflows))
	}
}
