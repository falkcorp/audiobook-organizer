// file: internal/transcribe/inflight.go
// version: 1.1.0
// guid: 55d73cef-7ffe-4cc4-bc48-434789153386
// last-edited: 2026-08-31

package transcribe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// ErrSlotWait marks a failure to ACQUIRE a slot, as distinct from a failure of
// the endpoint itself. Nothing was sent, so callers must not treat it as
// evidence the server is unhealthy -- see the dispatcher's cooldown handling.
var ErrSlotWait = errors.New("in-flight slot not acquired")

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
	if poolWide == nil {
		poolWide = &slotPool{limit: limit, ch: make(chan struct{}, limit)}
	} else if poolWide.limit != limit {
		// Same reasoning as the per-endpoint pool: replacing it installs an
		// empty channel and silently removes the cap. Takes effect at restart.
		slog.Warn("transcribe: whisper_max_in_flight changed; keeping the established cap until restart",
			"established", poolWide.limit, "requested", limit)
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

	inflightMu.Lock()
	pool, ok := inflightPools[url]
	if !ok {
		pool = &slotPool{limit: limit, ch: make(chan struct{}, limit)}
		inflightPools[url] = pool
	} else if pool.limit != limit {
		// NEVER replace the pool on a differing limit. Replacing installs a
		// fresh EMPTY channel, so if two callers disagree about the limit for
		// one URL -- the same box listed twice, or a config edit racing an
		// in-flight dispatch -- every acquire re-creates the pool and admits
		// immediately. The cap silently disappears while every signal still
		// says it is working. Changing an endpoint's concurrency therefore
		// takes effect at restart.
		slog.Warn("transcribe: conflicting in-flight limits for one endpoint; keeping the established one",
			"url", url, "established", pool.limit, "requested", limit)
	}
	inflightMu.Unlock()

	// Per-endpoint slot FIRST, pool-wide second. The pool-wide cap is the
	// scarce SHARED resource; holding it while parking on a busy endpoint's
	// queue starves every other endpoint, which is the precise inverse of what
	// it is for. Blocking while holding the local slot only ever delays the
	// endpoint we are already queued on. (Either order is deadlock-free, so
	// deadlock is not what decides this.)
	select {
	case pool.ch <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("%w for %s: %w", ErrSlotWait, url, ctx.Err())
	}

	releasePool, err := acquirePoolWide(ctx, config.AppConfig.WhisperMaxInFlight)
	if err != nil {
		<-pool.ch // or the endpoint slot leaks for the life of the process
		return nil, fmt.Errorf("%w for %s: %w", ErrSlotWait, url, err)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			<-pool.ch
			releasePool()
		})
	}, nil
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
