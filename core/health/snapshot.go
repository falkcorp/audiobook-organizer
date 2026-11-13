// file: core/health/snapshot.go
// version: 1.0.0
// guid: 5b0e1d6c-7a41-4d3b-9a52-6c1f0e8d3b27
// last-edited: 2026-11-13

package health

import (
	"slices"
	"strings"
	"time"
)

// Snapshot is the registry's state at one instant (05-health §5H.3.1). It is
// the body of the authenticated GET /api/core/v1/health after its Detail text
// is redacted; nothing in it is ever served unauthenticated except through
// the token filter of probes.go.
type Snapshot struct {
	Ready      bool
	Live       bool
	Components []ComponentState // sorted by ID
	Startup    []string         // degraded startup tokens
}

// ComponentState is one component in a Snapshot.
type ComponentState struct {
	ID       ComponentID
	Result   Result
	Critical bool
	Duration time.Duration // last check duration
}

// Snapshot is the registry's state: Ready, Live, every component sorted by ID
// and the degraded startup tokens. It is a copy; later changes do not alter it.
func (r *Registry) Snapshot() Snapshot {
	live := r.Live()
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Snapshot{
		Ready:      len(r.notReadyLocked()) == 0,
		Live:       live,
		Components: make([]ComponentState, 0, len(r.comps)),
		Startup:    slices.Clone(r.startupTokens),
	}
	for id, comp := range r.comps {
		s.Components = append(s.Components, ComponentState{ID: id, Result: comp.result, Critical: comp.check.Critical, Duration: comp.duration})
	}
	slices.SortFunc(s.Components, func(a, b ComponentState) int { return strings.Compare(string(a.ID), string(b.ID)) })
	return s
}

// Live reports whether the process is alive, which is what /livez answers. A
// registry whose heartbeat was never started is always live (a build that has
// not wired the heartbeat must not fail its probe); once StartHeartbeat runs,
// it is live while the heartbeat goroutine's last tick is within
// core.health.live_stall. A nil Registry is not live.
func (r *Registry) Live() bool {
	if r == nil {
		return false
	}
	if !r.beating.Load() {
		return true
	}
	last := time.Unix(0, r.lastBeat.Load())
	return r.now().Sub(last) <= r.cfg().LiveStall
}
