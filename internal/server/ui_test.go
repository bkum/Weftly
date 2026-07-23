package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bkum/weftly/internal/server"
)

func TestUIShellServedWithoutAuth(t *testing.T) {
	dir, _ := writeCatalogue(t)
	ts := startServer(t, server.Config{
		CatalogueDir: dir,
		RunsDir:      t.TempDir(),
		Token:        "tk",
	})

	// GET / returns the index shell — no Authorization header needed.
	for _, path := range []string{"/", "/app.js", "/styles.css"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("%s: want 200, got %d", path, resp.StatusCode)
		}
		if len(body) < 100 {
			t.Errorf("%s: suspiciously small body (%d bytes)", path, len(body))
		}
	}
}

func TestSSETokenViaQueryString(t *testing.T) {
	dir, wfID := writeCatalogue(t)
	ts := startServer(t, server.Config{
		CatalogueDir: dir,
		RunsDir:      t.TempDir(),
		Token:        "tk",
	})
	// Start a run first (needs Authorization).
	req, _ := http.NewRequest("POST", ts.URL+"/runs", strings.NewReader(`{"workflow":"`+wfID+`"}`))
	req.Header.Set("Authorization", "Bearer tk")
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatalf("start run: %d %s", resp.StatusCode, string(body))
	}
	var rr struct {
		RunID string `json:"run_id"`
	}
	_ = json.Unmarshal(body, &rr)

	// SSE via ?token= (no Authorization header).
	sseResp, err := http.Get(ts.URL + "/runs/" + rr.RunID + "/events?token=tk")
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()
	if sseResp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", sseResp.StatusCode)
	}
}
