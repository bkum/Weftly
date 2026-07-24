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

// writeSchedulesFile creates a schedules.yaml pointing at the given
// workflow id. Cron @yearly ensures the scheduler goroutine won't
// accidentally fire during the test — we drive dispatch via the
// explicit trigger endpoint.
func writeSchedulesFile(t *testing.T, wfID string) string {
	t.Helper()
	body := "schedules:\n" +
		"  - id: nightly\n" +
		"    workflow: " + wfID + "\n" +
		"    cron: \"@yearly\"\n"
	path := filepath.Join(t.TempDir(), "schedules.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSchedulesEndpointsRequireAuth(t *testing.T) {
	dir, wfID := writeCatalogue(t)
	sf := writeSchedulesFile(t, wfID)
	ts := startServer(t, server.Config{
		CatalogueDir:  dir,
		RunsDir:       t.TempDir(),
		Token:         "tk",
		SchedulesFile: sf,
	})

	// No token → 401 on both list and trigger.
	for _, url := range []string{"/schedules", "/schedules/nightly", "/schedules/nightly/trigger"} {
		method := "GET"
		if strings.HasSuffix(url, "/trigger") {
			method = "POST"
		}
		req, _ := http.NewRequest(method, ts.URL+url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("%s %s: want 401, got %d", method, url, resp.StatusCode)
		}
	}
}

func TestSchedulesListAndGet(t *testing.T) {
	dir, wfID := writeCatalogue(t)
	sf := writeSchedulesFile(t, wfID)
	ts := startServer(t, server.Config{
		CatalogueDir:  dir,
		RunsDir:       t.TempDir(),
		Token:         "tk",
		SchedulesFile: sf,
	})

	req, _ := http.NewRequest("GET", ts.URL+"/schedules", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("list: %d", resp.StatusCode)
	}
	var listing struct {
		Schedules []struct {
			ID       string    `json:"id"`
			Workflow string    `json:"workflow"`
			Cron     string    `json:"cron"`
			NextFire time.Time `json:"next_fire"`
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Schedules) != 1 || listing.Schedules[0].ID != "nightly" {
		t.Fatalf("unexpected listing: %+v", listing)
	}
	if listing.Schedules[0].NextFire.IsZero() {
		t.Errorf("next_fire should be populated for @yearly")
	}

	// GET /schedules/:id
	req, _ = http.NewRequest("GET", ts.URL+"/schedules/nightly", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("get: %d", resp.StatusCode)
	}

	// Unknown id → 404
	req, _ = http.NewRequest("GET", ts.URL+"/schedules/none", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("unknown id: want 404, got %d", resp.StatusCode)
	}
}

// TestSchedulesTriggerLaunchesRun exercises the manual trigger — it
// should dispatch the underlying workflow via the same runManager path
// that POST /runs uses, so we verify a run id comes back and the run
// eventually appears in GET /runs.
func TestSchedulesTriggerLaunchesRun(t *testing.T) {
	dir, wfID := writeCatalogue(t)
	sf := writeSchedulesFile(t, wfID)
	ts := startServer(t, server.Config{
		CatalogueDir:  dir,
		RunsDir:       t.TempDir(),
		Token:         "tk",
		SchedulesFile: sf,
	})

	req, _ := http.NewRequest("POST", ts.URL+"/schedules/nightly/trigger", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("trigger: %d", resp.StatusCode)
	}
	var body struct {
		RunID string `json:"run_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.RunID == "" {
		t.Fatalf("empty run_id in response")
	}

	// The run is dispatched asynchronously (engine goroutine). Wait for
	// it to reach a terminal state so t.TempDir() cleanup doesn't race
	// state.json / report.html writes that keep landing after 202.
	waitForRunFinish(t, ts.URL, "tk", body.RunID)
}

func TestSchedulesDisabledServerRespondsEmpty(t *testing.T) {
	dir, _ := writeCatalogue(t)
	ts := startServer(t, server.Config{
		CatalogueDir: dir,
		RunsDir:      t.TempDir(),
		Token:        "tk",
	})
	req, _ := http.NewRequest("GET", ts.URL+"/schedules", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("list without scheduler: %d", resp.StatusCode)
	}
	// Trigger against a nonexistent schedule on a disabled scheduler → 404.
	req, _ = http.NewRequest("POST", ts.URL+"/schedules/none/trigger", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("trigger without scheduler: want 404, got %d", resp.StatusCode)
	}
}

func TestSchedulesReloadReplacesEntries(t *testing.T) {
	dir, wfID := writeCatalogue(t)
	sf := writeSchedulesFile(t, wfID)
	ts := startServer(t, server.Config{
		CatalogueDir:  dir,
		RunsDir:       t.TempDir(),
		Token:         "tk",
		SchedulesFile: sf,
	})
	// Rewrite the schedules file with a NEW id, then POST /reload.
	newBody := "schedules:\n" +
		"  - id: renamed\n" +
		"    workflow: " + wfID + "\n" +
		"    cron: \"@yearly\"\n"
	if err := os.WriteFile(sf, []byte(newBody), 0o600); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", ts.URL+"/reload", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("reload: %d", resp.StatusCode)
	}
	// Old id must be gone, new id must exist.
	req, _ = http.NewRequest("GET", ts.URL+"/schedules/nightly", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("old id after reload: want 404, got %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("GET", ts.URL+"/schedules/renamed", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("new id after reload: want 200, got %d", resp.StatusCode)
	}
}
