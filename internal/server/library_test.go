package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bkum/weftly/internal/server"
)

// writeLibraryCatalogue stages one runnable workflow and one library
// fragment in a catalogue directory.
func writeLibraryCatalogue(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runnable.yml"), []byte(`
name: runnable
steps:
  - id: a
    run: echo hi
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "frag.yml"), []byte(`
name: frag
library: true
inputs:
  who: { required: true }
steps:
  - id: b
    run: echo "$W"
    env: { W: "${{ inputs.who }}" }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestLibraryExcludedFromCatalogueListing — a fragment must not appear
// in GET /workflows. A client should not be able to enumerate what it
// cannot run.
func TestLibraryExcludedFromCatalogueListing(t *testing.T) {
	ts := startServer(t, server.Config{
		CatalogueDir: writeLibraryCatalogue(t),
		RunsDir:      t.TempDir(),
		Token:        "tk",
	})
	req, _ := http.NewRequest("GET", ts.URL+"/workflows", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var listing struct {
		Workflows []struct {
			ID string `json:"id"`
		}
	}
	_ = json.NewDecoder(resp.Body).Decode(&listing)
	for _, w := range listing.Workflows {
		if w.ID == "frag" {
			t.Fatalf("library fragment appeared in catalogue listing: %+v", listing)
		}
	}
	if len(listing.Workflows) != 1 || listing.Workflows[0].ID != "runnable" {
		t.Fatalf("expected only the runnable workflow, got %+v", listing)
	}
}

// TestLibraryRejectedAsRunTarget — the authorisation control itself.
// Without it, any principal with `workflows: "*"` can trigger a
// fragment directly.
func TestLibraryRejectedAsRunTarget(t *testing.T) {
	ts := startServer(t, server.Config{
		CatalogueDir: writeLibraryCatalogue(t),
		RunsDir:      t.TempDir(),
		Token:        "tk",
	})
	body, _ := json.Marshal(map[string]any{
		"workflow": "frag",
		"inputs":   map[string]any{"who": "someone"},
	})
	req, _ := http.NewRequest("POST", ts.URL+"/runs", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tk")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted {
		t.Fatal("library fragment was accepted as a run target")
	}
	var e struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&e)
	if !strings.Contains(e.Error, "library fragment") {
		t.Errorf("error should name the reason, got %q", e.Error)
	}
}

// TestLibraryHiddenFromWorkflowDetail — GET /workflows/{id} must not
// serve a fragment's input schema either.
func TestLibraryHiddenFromWorkflowDetail(t *testing.T) {
	ts := startServer(t, server.Config{
		CatalogueDir: writeLibraryCatalogue(t),
		RunsDir:      t.TempDir(),
		Token:        "tk",
	})
	req, _ := http.NewRequest("GET", ts.URL+"/workflows/frag", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("library fragment detail was served")
	}
}

// TestLibraryRejectedAtScheduleLoad — a schedule naming a fragment must
// fail at startup, not silently wait to fail at fire time.
func TestLibraryRejectedAtScheduleLoad(t *testing.T) {
	dir := writeLibraryCatalogue(t)
	sf := filepath.Join(t.TempDir(), "schedules.yaml")
	if err := os.WriteFile(sf, []byte("schedules:\n  - id: nightly\n    workflow: frag\n    cron: \"@yearly\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := server.New(server.Config{
		CatalogueDir:  dir,
		RunsDir:       t.TempDir(),
		Token:         "tk",
		SchedulesFile: sf,
	})
	if err == nil {
		t.Fatal("expected server.New to reject a schedule targeting a library fragment")
	}
	if !strings.Contains(err.Error(), "library fragment") {
		t.Errorf("error should explain why, got %v", err)
	}
}

// TestLibraryStillIncludable — the whole point: excluded from the
// catalogue, but a catalogued workflow can still include it.
func TestLibraryStillIncludable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "frag.yml"), []byte(`
name: frag
library: true
inputs:
  who: { default: "world" }
steps:
  - id: greet
    run: echo "hello $W"
    env: { W: "${{ inputs.who }}" }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "caller.yml"), []byte(`
name: caller
steps:
  - id: sub
    include: frag.yml
    with: { who: "included" }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	ts := startServer(t, server.Config{
		CatalogueDir: dir,
		RunsDir:      t.TempDir(),
		Token:        "tk",
	})
	body, _ := json.Marshal(map[string]any{"workflow": "caller"})
	req, _ := http.NewRequest("POST", ts.URL+"/runs", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tk")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("caller including a library should run, got %d", resp.StatusCode)
	}
	var rr struct {
		RunID string `json:"run_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&rr)
	waitForRunFinish(t, ts.URL, "tk", rr.RunID)
}
