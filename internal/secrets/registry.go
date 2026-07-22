// Package secrets holds the values that must be redacted from any log line
// emitted by an action or written to disk. The registry is populated when
// secret inputs are resolved (spec §7.6 / §16). Masking runs *before* any
// renderer or state writer sees the line — actions must never bypass it.
package secrets

import (
	"sort"
	"strings"
	"sync"
)

const mask = "***"

// Registry is safe for concurrent use.
type Registry struct {
	mu     sync.RWMutex
	values []string
}

func NewRegistry() *Registry { return &Registry{} }

// Register adds a value to be masked. Empty and very short values are
// ignored to avoid corrupting output; short secrets are a policy problem.
func (r *Registry) Register(v string) {
	if len(strings.TrimSpace(v)) < 4 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.values {
		if existing == v {
			return
		}
	}
	r.values = append(r.values, v)
	// Sort longest-first so overlapping registrations don't leave
	// partially-masked substrings behind.
	sort.Slice(r.values, func(i, j int) bool { return len(r.values[i]) > len(r.values[j]) })
}

// Mask returns s with every registered value replaced by "***".
func (r *Registry) Mask(s string) string {
	if s == "" {
		return s
	}
	r.mu.RLock()
	values := r.values
	r.mu.RUnlock()
	for _, v := range values {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, mask)
		}
	}
	return s
}
