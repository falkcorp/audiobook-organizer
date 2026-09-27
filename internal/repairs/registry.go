// file: internal/repairs/registry.go
// version: 1.0.0
// guid: 8d4f2a61-0b7c-4e93-a5d8-1c6e9f3b2a70
// last-edited: 2026-09-27

package repairs

import (
	"fmt"
	"sort"
	"sync"
)

// Registry holds the fixers the Repairs lane offers. Safe for concurrent use.
type Registry struct {
	mu     sync.RWMutex
	fixers map[string]Fixer
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{fixers: map[string]Fixer{}} }

// Register adds f. A nil fixer, an empty id or a duplicate id is an error.
func (r *Registry) Register(f Fixer) error {
	if f == nil || f.ID() == "" {
		return fmt.Errorf("repairs: fixer with no id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.fixers[f.ID()]; dup {
		return fmt.Errorf("repairs: fixer %q registered twice", f.ID())
	}
	r.fixers[f.ID()] = f
	return nil
}

// Get returns the fixer with that id.
func (r *Registry) Get(id string) (Fixer, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.fixers[id]
	return f, ok
}

// List returns every fixer, sorted by id.
func (r *Registry) List() []Fixer {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Fixer, 0, len(r.fixers))
	for _, f := range r.fixers {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}
