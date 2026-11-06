// file: internal/repairs/registry.go
// version: 1.2.1
// guid: 8d4f2a61-0b7c-4e93-a5d8-1c6e9f3b2a70
// last-edited: 2026-11-05

package repairs

import (
	"fmt"
	"sort"
	"sync"
)

// Registry holds the fixers the Repairs lane offers. Safe for concurrent use.
// NewRegistry doubles as the boot hook that registers the abandoned-calls gauge.
type Registry struct {
	mu     sync.RWMutex
	fixers map[string]Fixer
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	// Built once at server start: the point the abandoned-calls gauge is put on
	// the meter (see registerAbandonedGaugeOnce for why not from init).
	registerAbandonedGaugeOnce()
	return &Registry{fixers: map[string]Fixer{}}
}

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

// List returns every fixer in the lane's display order: by group (Groups), then
// by Meta().Rank inside the group, then by id so two fixers that share a rank
// still list the same way every time. A group id that is not one of Groups
// sorts after every real group, and unknown ids sort among themselves by the
// group string, so each mistyped group stays one contiguous run.
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
	sort.Slice(out, func(i, j int) bool {
		mi, mj := out[i].Meta(), out[j].Meta()
		if gi, gj := groupIndex(mi.Group), groupIndex(mj.Group); gi != gj {
			return gi < gj
		} else if gi == len(groups) && mi.Group != mj.Group {
			// Both groups are unknown: keep each typo's fixers together,
			// ordered by the group string, so two typos never interleave.
			return mi.Group < mj.Group
		}
		if mi.Rank != mj.Rank {
			return mi.Rank < mj.Rank
		}
		return out[i].ID() < out[j].ID()
	})
	return out
}
