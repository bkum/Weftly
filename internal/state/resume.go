package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
