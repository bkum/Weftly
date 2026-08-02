package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/bkum/weftly/internal/server"
)

func writeTypedCatalogue(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "seed.yml"), []byte(`
name: seed
inputs:
  domain: { type: enum, values: [retail, healthcare] }
  cases:  { type: int, min: 100, max: 50000, default: 2500 }
presets:
  qa:
    description: small corpus
    values: { domain: healthcare, cases: 500 }
steps:
  - id: s
    run: echo "$D $C"
    env:
      D: "${{ inputs.domain }}"
      C: "${{ inputs.cases }}"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// §15.16 — an out-of-range input returns 400 with a structured,
// per-field error list, not a 500. The SPA renders each error against
// its own control, which it can't do with an opaque server error.
func TestCreateRunReturns400WithInputErrorList(t *testing.T) {
	ts := startServer(t, server.Config{
		CatalogueDir: writeTypedCatalogue(t),
		RunsDir:      t.TempDir(),
		Token:        "tk",
	})
	body, _ := json.Marshal(map[string]any{
		"workflow": "seed",
		"inputs":   map[string]any{"cases": 99, "domain": "retial"},
	})
	req, _ := http.NewRequest("POST", ts.URL+"/runs", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tk")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 for a bad input, got %d", resp.StatusCode)
	}
	var out struct {
		Error       string `json:"error"`
		InputErrors []struct {
			Input   string   `json:"input"`
			Message string   `json:"message"`
			Hint    string   `json:"hint"`
			Allowed []string `json:"allowed"`
		} `json:"input_errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.InputErrors) != 2 {
		t.Fatalf("want one error per bad field, got %d: %+v", len(out.InputErrors), out.InputErrors)
	}
	byInput := map[string]string{}
	for _, e := range out.InputErrors {
		byInput[e.Input] = e.Hint
	}
	if _, ok := byInput["cases"]; !ok {
		t.Error("missing an error for cases")
	}
	if byInput["domain"] != "retail" {
		t.Errorf("domain error should carry the did-you-mean, got %q", byInput["domain"])
	}
}

// §15.13 over the API: preset applies, explicit inputs still win.
func TestCreateRunAppliesPreset(t *testing.T) {
	ts := startServer(t, server.Config{
		CatalogueDir: writeTypedCatalogue(t),
		RunsDir:      t.TempDir(),
		Token:        "tk",
	})
	body, _ := json.Marshal(map[string]any{
		"workflow": "seed",
		"preset":   "qa",
		"inputs":   map[string]any{"cases": 800},
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
		t.Fatalf("preset run should be accepted, got %d", resp.StatusCode)
	}
	var rr struct {
		RunID string `json:"run_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&rr)
	waitForRunFinish(t, ts.URL, "tk", rr.RunID)
}

// The SPA needs presets in the detail payload to render its buttons.
func TestWorkflowDetailExposesPresets(t *testing.T) {
	ts := startServer(t, server.Config{
		CatalogueDir: writeTypedCatalogue(t),
		RunsDir:      t.TempDir(),
		Token:        "tk",
	})
	req, _ := http.NewRequest("GET", ts.URL+"/workflows/seed", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Presets map[string]struct {
			Description string         `json:"description"`
			Values      map[string]any `json:"values"`
		} `json:"presets"`
		Inputs map[string]struct {
			Type   string   `json:"type"`
			Values []string `json:"values"`
			Min    *float64 `json:"min"`
			Max    *float64 `json:"max"`
		} `json:"inputs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Presets["qa"]; !ok {
		t.Fatalf("presets missing from detail payload: %+v", out.Presets)
	}
	// Constraints must reach the SPA too, or the typed controls can't
	// render bounds.
	if out.Inputs["cases"].Min == nil || out.Inputs["cases"].Max == nil {
		t.Errorf("int bounds missing from payload: %+v", out.Inputs["cases"])
	}
	if len(out.Inputs["domain"].Values) != 2 {
		t.Errorf("enum values missing from payload: %+v", out.Inputs["domain"])
	}
}
