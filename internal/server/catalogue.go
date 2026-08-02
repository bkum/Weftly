package server

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/bkum/weftly/internal/schema"
)

// catalogueEntry summarises one workflow for the /workflows list.
type catalogueEntry struct {
	ID          string                  `json:"id"`
	Name        string                  `json:"name"`
	Description string                  `json:"description,omitempty"`
	Path        string                  `json:"-"` // never surfaced to clients
	Inputs      map[string]schema.Input `json:"inputs,omitempty"`
	Workflow    *schema.Workflow        `json:"-"` // for run dispatch
	// Library fragments are loaded (so includes and diagnostics work) but
	// are neither listed nor runnable. Never surfaced to clients — a
	// client should not be able to enumerate what it cannot run.
	Library bool `json:"-"`
}

type catalogue struct {
	mu   sync.RWMutex
	byID map[string]*catalogueEntry
}

// loadCatalogue scans dir non-recursively for *.yml / *.yaml files, parses
// and validates each. The ID is the filename stem (without extension), so
// callers reference workflows by URL-safe short names.
func loadCatalogue(dir string) (*catalogue, error) {
	c := &catalogue{byID: map[string]*catalogueEntry{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("catalogue: read %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		path := filepath.Join(dir, name)
		wf, err := schema.Load(path)
		if err != nil {
			return nil, fmt.Errorf("catalogue: %s: %w", name, err)
		}
		if err := schema.Validate(wf); err != nil {
			return nil, fmt.Errorf("catalogue: %s: %w", name, err)
		}
		id := strings.TrimSuffix(name, filepath.Ext(name))
		c.byID[id] = &catalogueEntry{
			ID:          id,
			Name:        wf.Name,
			Description: wf.Description,
			Path:        path,
			Inputs:      wf.Inputs,
			Workflow:    wf,
			Library:     wf.Library,
		}
	}
	return c, nil
}

// list returns the RUNNABLE catalogue. Library fragments are loaded and
// kept in byID (so a request naming one can be answered with a specific
// error rather than a bare 404) but never advertised.
func (c *catalogue) list() []*catalogueEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*catalogueEntry, 0, len(c.byID))
	for _, e := range c.byID {
		if e.Library {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (c *catalogue) get(id string) *catalogueEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e := c.byID[id]
	// A library is not part of the served catalogue. Returning nil here
	// makes every existing caller — GET /workflows/{id}, POST /runs, the
	// scheduler — reject it without each having to remember the rule.
	if e != nil && e.Library {
		return nil
	}
	return e
}

// getIncludingLibraries returns an entry even when it is a library.
// Used only to distinguish "no such workflow" from "that is a library
// fragment, not a runnable workflow" in error messages.
func (c *catalogue) getIncludingLibraries(id string) *catalogueEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.byID[id]
}

// reload swaps the in-memory catalogue for a freshly-loaded one. A
// parse/validate failure on any workflow leaves the existing catalogue
// untouched — better to serve slightly-stale but valid content than to
// go dark on a bad edit.
func (c *catalogue) reload(dir string) error {
	fresh, err := loadCatalogue(dir)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.byID = fresh.byID
	c.mu.Unlock()
	return nil
}
