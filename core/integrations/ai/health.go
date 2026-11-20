// file: core/integrations/ai/health.go
// version: 1.1.0
// guid: 6b2d9f40-7c15-4e83-a9d6-1f8e3b5a0c72
// last-edited: 2026-11-19

package ai

import (
	"sync"
	"time"

	"github.com/falkcorp/mediacore/integrations"
)

// bench is the router's per-endpoint failure memory (05-ai section 4, step 4):
// a failure benches the endpoint for cooldown_base times its consecutive
// failures, capped at cooldown_max; a quota failure benches it for
// quota_cooldown; a success resets it. It belongs to one Router, reads that
// router's clock, and has no package-level instance.
type bench struct {
	mu  sync.Mutex
	m   map[integrations.Ref]*benchState
	now func() time.Time
}

type benchState struct {
	consecFails int
	until       time.Time
	// quota marks a bench set by markQuota, the class a health probe cannot
	// end: a probe succeeds on a spent balance. markFailure replaces it.
	quota bool
}

func newBench(now func() time.Time) *bench {
	return &bench{m: map[integrations.Ref]*benchState{}, now: now}
}

func (b *bench) state(ref integrations.Ref) *benchState {
	s := b.m[ref]
	if s == nil {
		s = &benchState{}
		b.m[ref] = s
	}
	return s
}

// markFailure counts one more consecutive failure and benches the endpoint for
// min(consecutive * base, max).
func (b *bench) markFailure(ref integrations.Ref, base, limit time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.state(ref)
	s.consecFails++
	s.quota = false
	s.until = b.now().Add(min(time.Duration(s.consecFails)*base, limit))
}

// markQuota benches the endpoint for the quota window; the failure counts
// toward the consecutive run, so a success afterwards starts from zero.
func (b *bench) markQuota(ref integrations.Ref, window time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.state(ref)
	s.consecFails++
	s.quota = true
	s.until = b.now().Add(window)
}

// markSuccess resets the endpoint.
func (b *bench) markSuccess(ref integrations.Ref) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s := b.m[ref]; s != nil {
		s.consecFails = 0
		s.until = time.Time{}
		s.quota = false
	}
}

// clearTransient is markSuccess for a probe: it leaves a quota bench still in
// force alone (the probe proved the endpoint reachable, not the balance
// refilled) and resets everything else, an expired quota bench included.
func (b *bench) clearTransient(ref integrations.Ref) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.m[ref]
	if s == nil {
		return
	}
	if s.quota && b.now().Before(s.until) {
		return
	}
	s.consecFails = 0
	s.until = time.Time{}
	s.quota = false
}

// until is when the endpoint's bench ends; the zero time when it is not benched.
func (b *bench) benchedUntil(ref integrations.Ref) time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s := b.m[ref]; s != nil && b.now().Before(s.until) {
		return s.until
	}
	return time.Time{}
}

// ClearBench ends ref's failure bench now (the health task's probe succeeded,
// or an operator tested the endpoint). A quota bench still in force stays: the
// probe is GET /v1/models, which succeeds on a spent balance, so it is no
// evidence the quota window is over. A real call that succeeds ends every
// bench.
func (r *Router) ClearBench(ref integrations.Ref) { r.bench.clearTransient(ref) }

// ObserveProbe is the body of the core.integrations.health task's per-probe
// duty: wire it to integrations.Options.Probed. A probe that answers
// HealthOK for an ai instance clears its failure bench (ClearBench), so an
// endpoint that recovered returns before its cooldown runs out, even though
// the Registry reports no status change for it. Any other answer, and any
// other kind, changes nothing.
func (r *Router) ObserveProbe(ref integrations.Ref, h integrations.Health) {
	if ref.Kind == integrations.KindAI && h.Status == integrations.HealthOK {
		r.ClearBench(ref)
	}
}

// BenchedUntil is when ref's cooldown ends; ok is false when it is not benched.
func (r *Router) BenchedUntil(ref integrations.Ref) (time.Time, bool) {
	u := r.bench.benchedUntil(ref)
	return u, !u.IsZero()
}
