// file: internal/transcribe/inflight.go
// version: 1.0.0
// guid: 55d73cef-7ffe-4cc4-bc48-434789153386
// last-edited: 2026-08-31

package transcribe

import (
	"context"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// Endpoint.Concurrency is enforced HERE, not in allocateJobs.
//
// allocateJobs uses Concurrency as an allocation *weight* -- how big a slice of
// the job list an endpoint is offered per pass -- and its loop repeats until
// every job is assigned. That makes it a ratio between endpoints, never a
// ceiling: with a single endpoint it simply runs more passes and hands over the
// whole list. Worse, the weight is per-dispatch, and callers dispatch
// independently (the intro-transcribe op runs introTranscribePageConc pages in
// parallel, each with its own dispatch), so N callers produced N concurrent
// requests at one server no matter what the operator configured.
//
// The cap therefore has to live at the last shared choke point -- the HTTP
// request itself -- in state that outlives any one dispatch. This registry is
// process-wide and keyed by endpoint URL so every caller contends for the same
// slots.
type slotPool struct {
	limit int
	ch    chan struct{}
}

var (
	inflightMu    sync.Mutex
	inflightPools = map[string]*slotPool{}

	// poolWide caps TOTAL simultaneous requests across every endpoint. The
	// per-endpoint cap answers "how much can this box take?"; this answers
	// "how much am I willing to have outstanding at once?" -- which is a
	// different question, and not the sum of the first: an operator may have
	// four willing servers and still want only two requests in the air
	// (bandwidth, cost, or leaving the machine usable).
	poolWideMu sync.Mutex
	poolWide   *slotPool
)

// acquirePoolWide takes a slot from the global cap. A limit < 1 means
// unlimited, and returns a no-op release rather than an unbounded channel.
func acquirePoolWide(ctx context.Context, limit int) (func(), error) {
	if limit < 1 {
		return func() {}, nil
	}

	poolWideMu.Lock()
	if poolWide == nil || poolWide.limit != limit {
		poolWide = &slotPool{limit: limit, ch: make(chan struct{}, limit)}
	}
	pool := poolWide
	poolWideMu.Unlock()

	select {
	case pool.ch <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-pool.ch }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// acquireInFlight blocks until this endpoint has a free request slot, or ctx is
// done. It returns a release function that is safe to call more than once.
//
// A changed limit (config reload) replaces the pool. Callers already holding a
// slot release into the pool they took it from -- captured in the closure, not
// looked up again -- so a swap can never make a release land in the wrong pool
// or leak a slot from the new one.
func acquireInFlight(ctx context.Context, url string, limit int) (func(), error) {
	if limit < 1 {
		limit = 1
	}

	// ALWAYS pool-wide first, then per-endpoint. Two counting semaphores taken
	// in a fixed global order cannot deadlock; taking them in caller-dependent
	// order could. This is why both live behind one function.
	releasePool, err := acquirePoolWide(ctx, config.AppConfig.WhisperMaxInFlight)
	if err != nil {
		return nil, err
	}

	inflightMu.Lock()
	pool, ok := inflightPools[url]
	if !ok || pool.limit != limit {
		pool = &slotPool{limit: limit, ch: make(chan struct{}, limit)}
		inflightPools[url] = pool
	}
	inflightMu.Unlock()

	select {
	case pool.ch <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() {
				<-pool.ch
				releasePool()
			})
		}, nil
	case <-ctx.Done():
		// Do not strand the pool-wide slot we already hold.
		releasePool()
		return nil, ctx.Err()
	}
}

// inFlightDepth reports how many slots are currently held for url. Test and
// telemetry helper; returns 0 for an endpoint that has never been dispatched to.
func inFlightDepth(url string) int {
	inflightMu.Lock()
	defer inflightMu.Unlock()
	if pool, ok := inflightPools[url]; ok {
		return len(pool.ch)
	}
	return 0
}
