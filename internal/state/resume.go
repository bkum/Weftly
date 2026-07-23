package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Load reads a prior run's state.json. Path may be either the full path
// to state.json OR a run-id (in which case dir is treated as the runs
// parent, typically ./.weftly/runs).
func Load(baseRunsDir, runIDOrPath string) (*Run, string, error) {
	candidate := runIDOrPath
	if _, err := os.Stat(candidate); err != nil {
		// try as run-id under baseRunsDir
		candidate = filepath.Join(baseRunsDir, runIDOrPath, "state.json")
	}
	data, err := os.ReadFile(candidate)
	if err != nil {
		return nil, "", fmt.Errorf("resume: read %s: %w", candidate, err)
	}
	var r Run
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, "", fmt.Errorf("resume: parse %s: %w", candidate, err)
	}
	return &r, filepath.Dir(candidate), nil
}

// Adopt attaches an existing Run to a Writer so subsequent Handle calls
// mutate the loaded state rather than starting fresh.
func (w *Writer) Adopt(run *Run) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.run = run
	if w.run.Steps == nil {
		w.run.Steps = map[string]*StepState{}
	}
}

// RunSummary is a lightweight projection of Run suitable for the history
// list — everything a client needs to render one row without paging in the
// per-step data.
type RunSummary struct {
	RunID      string     `json:"run_id"`
	Workflow   string     `json:"workflow"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Steps      int        `json:"steps"`
}

// LoadRunsDir walks baseRunsDir (typically ./.weftly/runs) and returns a
// summary for every subdirectory that contains a state.json. Sorted by
// StartedAt descending — newest first. A per-run parse error is logged as
// a nil summary and skipped rather than failing the whole listing.
func LoadRunsDir(baseRunsDir string) ([]RunSummary, error) {
	entries, err := os.ReadDir(baseRunsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]RunSummary, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(baseRunsDir, e.Name(), "state.json"))
		if err != nil {
			continue // partial write, unrelated dir, etc.
		}
		var r Run
		if err := json.Unmarshal(data, &r); err != nil {
			continue
		}
		out = append(out, RunSummary{
			RunID:      r.RunID,
			Workflow:   r.Workflow,
			Status:     string(r.Status),
			StartedAt:  r.StartedAt,
			FinishedAt: r.FinishedAt,
			Steps:      len(r.Steps),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}
