// file: internal/aidispatch/inflight.go
// version: 1.1.0
// guid: 78ddb327-a1f6-4c52-bcce-bd87a4eaf13c
// last-edited: 2026-09-19

package aidispatch

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// Per-endpoint in-flight caps, lifted unchanged from internal/transcribe and
// now keyed by endpoint ID instead of URL.
//
// The cap has to live at the last shared choke point -- the request itself --
// in state that outlives any one dispatch. An allocation weight is a ratio
// between endpoints, never a ceiling, and callers dispatch independently, so
// N callers would otherwise produce N concurrent requests at one server no
// matter what the operator configured. The registry is therefore process-wide
// and every caller contends for the same slots.
//
// 🔴 The two limits are deliberately ASYMMETRIC and both callers depend on it:
//   - a per-endpoint limit < 1 means 1 (EffectiveConcurrency). An endpoint
//     with Concurrency 0 is a configured server; "0" is an unset field, and
//     treating it as unlimited would remove the cap from every row that never
//     set one.
//   - a TotalCap.Limit < 1 means UNLIMITED. whisper_max_in_flight 0 is the
//     documented "no pool-wide cap" setting.
type slotPool struct {
	limit int
	ch    chan struct{}
}

// TotalCap caps the total simultaneous requests across every endpoint in one
// group (e.g. "whisper"). The per-endpoint cap answers "how much can this box
// take?"; this answers "how much am I willing to have outstanding at once?",
// which is not the sum of the first.
type TotalCap struct {
	Group string
	Limit int
}

// Slots is an in-flight registry. The package default instance is the one
// production uses; separate instances exist so tests can run in isolation.
type Slots struct {
	mu     sync.Mutex
	pools  map[string]*slotPool
	totMu  sync.Mutex
	totals map[string]*slotPool
}

// NewSlots returns an empty registry.
func NewSlots() *Slots {
	return &Slots{pools: map[string]*slotPool{}, totals: map[string]*slotPool{}}
}

var defaultSlots = NewSlots()

// DefaultSlots is the process-wide registry.
func DefaultSlots() *Slots { return defaultSlots }

// EffectiveConcurrency maps a configured per-endpoint concurrency to the slot
// count actually enforced: values < 1 mean 1.
func EffectiveConcurrency(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// LegacyURLID is the endpoint ID used for a caller that still identifies
// endpoints by URL (internal/transcribe until PR 3). The prefix keeps it from
// ever colliding with a configured ai_endpoints ID.
func LegacyURLID(url string) string { return "url:" + url }

// totalPool returns the group-wide pool for total, creating it on first use.
// Callers must have checked total.Limit >= 1.
func (s *Slots) totalPool(total TotalCap) *slotPool {
	s.totMu.Lock()
	defer s.totMu.Unlock()
	pool := s.totals[total.Group]
	if pool == nil {
		pool = &slotPool{limit: total.Limit, ch: make(chan struct{}, total.Limit)}
		s.totals[total.Group] = pool
	} else if pool.limit != total.Limit {
		// Same reasoning as the per-endpoint pool: replacing it installs an
		// empty channel and silently removes the cap. Takes effect at restart.
		slog.Warn("aidispatch: pool-wide in-flight cap changed; keeping the established cap until restart",
			"group", total.Group, "established", pool.limit, "requested", total.Limit)
	}
	return pool
}

// endpointPool returns id's per-endpoint pool, creating it on first use.
func (s *Slots) endpointPool(id string, limit int) *slotPool {
	s.mu.Lock()
	defer s.mu.Unlock()
	pool, ok := s.pools[id]
	if !ok {
		pool = &slotPool{limit: limit, ch: make(chan struct{}, limit)}
		s.pools[id] = pool
	} else if pool.limit != limit {
		// NEVER replace the pool on a differing limit. Replacing installs a
		// fresh EMPTY channel, so if two callers disagree about the limit for
		// one endpoint -- the same box listed twice, or a config edit racing
		// an in-flight dispatch -- every acquire re-creates the pool and admits
		// immediately. The cap silently disappears while every signal still
		// says it is working. A changed concurrency takes effect at restart.
		slog.Warn("aidispatch: conflicting in-flight limits for one endpoint; keeping the established one",
			"endpoint", id, "established", pool.limit, "requested", limit)
	}
	return pool
}

// acquireTotal takes a slot from the group-wide cap. A limit < 1 means
// unlimited and returns a no-op release rather than an unbounded channel.
func (s *Slots) acquireTotal(ctx context.Context, total TotalCap) (func(), error) {
	if total.Limit < 1 {
		return func() {}, nil
	}
	pool := s.totalPool(total)
	select {
	case pool.ch <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-pool.ch }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Acquire blocks until endpoint id has a free request slot (and the group-wide
// cap has room), or ctx is done. The returned release is safe to call more
// than once. A failure wraps ErrSlotWait and names the endpoint.
func (s *Slots) Acquire(ctx context.Context, id string, limit int, total TotalCap) (func(), error) {
	pool := s.endpointPool(id, EffectiveConcurrency(limit))

	// Per-endpoint slot FIRST, group-wide second. The group-wide cap is the
	// scarce SHARED resource; holding it while parking on a busy endpoint's
	// queue starves every other endpoint, the precise inverse of what it is
	// for. Blocking while holding the local slot only delays the endpoint we
	// are already queued on. (Either order is deadlock-free, so deadlock is
	// not what decides this.)
	select {
	case pool.ch <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("%w for %s: %w", ErrSlotWait, id, ctx.Err())
	}

	releaseTotal, err := s.acquireTotal(ctx, total)
	if err != nil {
		<-pool.ch // or the endpoint slot leaks for the life of the process
		return nil, fmt.Errorf("%w for %s: %w", ErrSlotWait, id, err)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			<-pool.ch
			releaseTotal()
		})
	}, nil
}

// TryAcquire is Acquire without waiting: it takes a slot only if endpoint id
// AND the group-wide cap both have one free right now, and reports whether it
// did. It is what lets Call fill FREE slots across endpoints (design step 2a)
// instead of queueing on the preferred endpoint while a peer sits idle.
func (s *Slots) TryAcquire(id string, limit int, total TotalCap) (func(), bool) {
	pool := s.endpointPool(id, EffectiveConcurrency(limit))
	select {
	case pool.ch <- struct{}{}:
	default:
		return nil, false
	}
	releaseTotal := func() {}
	if total.Limit >= 1 {
		tp := s.totalPool(total)
		select {
		case tp.ch <- struct{}{}:
			var once sync.Once
			releaseTotal = func() { once.Do(func() { <-tp.ch }) }
		default:
			<-pool.ch
			return nil, false
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			<-pool.ch
			releaseTotal()
		})
	}, true
}

// Depth reports how many slots are currently held for id (0 if never used).
func (s *Slots) Depth(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pool, ok := s.pools[id]; ok {
		return len(pool.ch)
	}
	return 0
}

// HasPool reports whether id has ever been dispatched to.
func (s *Slots) HasPool(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.pools[id]
	return ok
}

// Reset discards every pool. Only for tests: a caller holding a slot releases
// into the pool it took it from (captured in its closure), so a reset can
// never make a release land in the wrong pool, but the cap it enforced is gone.
func (s *Slots) Reset() {
	s.mu.Lock()
	s.pools = map[string]*slotPool{}
	s.mu.Unlock()
	s.totMu.Lock()
	s.totals = map[string]*slotPool{}
	s.totMu.Unlock()
}
