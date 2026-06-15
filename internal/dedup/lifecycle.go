// file: internal/dedup/lifecycle.go
// version: 1.3.0
// guid: 3e4f5a6b-7c8d-9e0f-a1b2-c3d4e5f60718
// last-edited: 2026-06-14

// Lifecycle methods on *dedup.Engine that the serviceregistry container
// picks up via interface satisfaction. PostInit subscribes to lifecycle
// events on the plugin event bus so the engine reacts to book imports
// from any source (filesystem watcher, iTunes importer, manual upload)
// without a server-bound closure callback.
//
// Replaces server.fireDedupOnImport, which was wired via a closure in
// itunesservice.Deps.OnBookCreated (removed in PLUGIN-DECOUPLE).

package dedup

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// defaultHydrationStopTimeout is the default maximum time Stop waits for
// the chromem hydration goroutine to finish after its context is canceled.
// Five seconds is generous — the goroutine's inner iteration is bounded by
// bgCtx, so it should drain within a Pebble-read round-trip.
const defaultHydrationStopTimeout = 5 * time.Second

// dedupChromemLazy reports whether the eager HydrateChromem at startup
// should be skipped. Controlled by env var DEDUP_CHROMEM_LAZY (default
// false / eager). When true, the chromem store stays empty and
// FindSimilar in engine.go falls back to the SQLite linear-scan path
// (EmbeddingStore.FindSimilar at internal/database/embedding_store.go).
//
// Tradeoff: skipping hydrate saves ~6GB heap on the 392K-book / 42K-
// embedding production library, but each dedup FindSimilar goes from
// chromem ANN (<10ms) to SQLite full-scan + cosine (~50-200ms). Dedup
// queries are rare (operator-triggered scans, dedup-on-import), so the
// memory savings dominate for memory-constrained deployments.
func dedupChromemLazy() bool {
	v := os.Getenv("DEDUP_CHROMEM_LAZY")
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false
	}
	return b
}

// stopTimeout returns the effective join-timeout for Stop().  Tests can
// override Engine.stopTimeout_ to shrink this; production always gets
// defaultHydrationStopTimeout.
func (de *Engine) stopTimeout() time.Duration {
	if de.stopTimeout_ > 0 {
		return de.stopTimeout_
	}
	return defaultHydrationStopTimeout
}

// PostInit wires the dedup engine into the rest of the container. Called
// by Container.PostInit after Build completes. Three steps:
//
//  1. Subscribe to plugin.EventBookImported via the eventbus so any source
//     (iTunes import, filesystem watcher, manual upload) triggers a dedup
//     check on the new book.
//  2. Pull chromem-go ANN store (optional, soft) and wire it via
//     SetChromemStore. Launch the chromem hydration goroutine on the
//     engine's bg-context.
//  3. Pull AIJobsStore (interface assertion against the main store) and
//     wire it for async dedup review batches. Register the engine as the
//     dedup verdict applier for batch callbacks.
//
// Safe to call when the engine is nil (Build returns nil when API key
// isn't configured — typed-nil receiver allowed by Go method dispatch on
// pointer types only when method has a nil-check, which we do).
func (de *Engine) PostInit(ctx context.Context, c *serviceregistry.Container) error {
	if de == nil {
		return nil
	}
	// Initialise the engine's own bg-context for subscriber goroutines.
	// The event bus's Publish runs each handler in its own goroutine with
	// the publisher's ctx; we ignore that ctx in favor of our own so a
	// canceled publisher (e.g. test-only request ctx) doesn't kill our
	// dedup check mid-flight.
	de.bgMu.Lock()
	if de.bgCtx == nil {
		de.bgCtx, de.bgCancel = context.WithCancel(context.Background())
	}
	bgCtx := de.bgCtx
	de.bgMu.Unlock()

	// Step 1 — event bus subscription
	if bus, ok := serviceregistry.TryGet[*plugin.EventBus](c, "eventbus"); ok && bus != nil {
		bus.Subscribe(plugin.EventBookImported, de.onBookImported)
		slog.Info("[dedup] PostInit subscribed to EventBookImported")
	} else {
		slog.Info("[dedup] PostInit eventbus not available, skipping dedup-on-import subscription")
	}

	// Step 2 — vector ANN store (chromem or hnsw, per config) + hydrate.
	// Retrieved as the VectorANNStore interface so either backend works; the
	// service Build returns an untyped nil when disabled, so ok=false here.
	if chromemStore, ok := serviceregistry.TryGet[database.VectorANNStore](c, "chromemstore"); ok && chromemStore != nil {
		de.SetChromemStore(chromemStore)
		slog.Info("[INFO] vector ANN store active for dedup Layer 2")
		if dedupChromemLazy() {
			// Skip the eager hydrate. Chromem stays empty; FindSimilar in
			// engine.go falls back to the SQLite linear-scan path via
			// EmbeddingStore.FindSimilar (slower per-query but no upfront
			// ~6GB heap from mirroring 42K book vectors into memory).
			slog.Info("chromem hydrate skipped (DEDUP_CHROMEM_LAZY=true) — dedup FindSimilar will use SQLite linear-scan fallback")
		} else {
			// Hydrate asynchronously on the engine's bg-context.
			// Add(1) under bgMu BEFORE launching so Stop can't race the
			// bgWg.Wait() call: if Stop runs first, bgCancel fires, and
			// the goroutine exits quickly; if the goroutine runs first,
			// bgWg.Done() will already be wired before Stop's Wait.
			de.bgMu.Lock()
			de.bgWg.Add(1)
			de.bgMu.Unlock()
			go func() {
				defer de.bgWg.Done()
				hCtx, cancel := context.WithTimeout(bgCtx, 30*time.Minute)
				defer cancel()
				books, authors, err := de.HydrateChromem(hCtx)
				if err != nil {
					slog.Warn("chromem hydrate finished with error", "err", err, "books", books, "authors", authors)
					return
				}
				slog.Info("chromem hydrate complete", "books", books, "authors", authors)
			}()
		}
	}

	// Step 3 — aijobs store + verdict applier
	if jobs, ok := serviceregistry.TryGet[database.AIJobsStore](c, "aijobsstore"); ok && jobs != nil {
		de.SetAIJobsStore(jobs)
		ai.SetDedupVerdictApplier(de)
		slog.Info("[INFO] Dedup async review (aijobs) wired")
	}

	// Step 4 — ISBN/ASIN secondary index store.
	// *database.PebbleStore implements ISBNIndexStore (IsISBNIndexBuilt +
	// GetBookIDsByISBNASIN). When the index has been built, checkExactISBN
	// switches to the O(matches) path; otherwise it falls back to GetAllBooks.
	if ps, ok := de.bookStore.(*database.PebbleStore); ok {
		de.SetISBNIndexStore(ps)
		slog.Info("[dedup] PostInit wired ISBNIndexStore (checkExactISBN will use indexed path when flag is set)")
	}

	return nil
}

// onBookImported is the EventHandler bound to EventBookImported. It runs
// in a goroutine spawned by EventBus.Publish; we ignore the publisher's
// ctx in favor of the engine's bg-context so external cancellation
// (e.g. an HTTP request that just finished) doesn't cut the dedup check
// off mid-stream.
func (de *Engine) onBookImported(_ context.Context, evt plugin.Event) error {
	if evt.BookID == "" {
		return nil
	}
	de.bgMu.RLock()
	bgCtx := de.bgCtx
	de.bgMu.RUnlock()
	if bgCtx == nil {
		bgCtx = context.Background()
	}
	if _, err := de.CheckBook(bgCtx, evt.BookID); err != nil {
		slog.Warn("dedup-on-import CheckBook()", "evt", evt.BookID, "err", err)
	}
	return nil
}

// Stop releases the engine's background-context resources. Called by
// Container.Stop. Safe to call multiple times.
//
// After canceling the context it waits up to hydrationStopTimeout for the
// hydration goroutine to drain its current Pebble read, then warns and
// returns so the overall server shutdown is never hung indefinitely.
func (de *Engine) Stop(ctx context.Context) error {
	if de == nil {
		return nil
	}
	de.bgMu.Lock()
	if de.bgCancel != nil {
		de.bgCancel()
		de.bgCancel = nil
		de.bgCtx = nil
	}
	de.bgMu.Unlock()

	// Join the hydration goroutine with a bounded timeout so that the store
	// can safely close after Stop returns.  We do this OUTSIDE bgMu to avoid
	// holding the mutex across the entire wait duration.
	timeout := de.stopTimeout()
	done := make(chan struct{})
	go func() {
		de.bgWg.Wait()
		close(done)
	}()
	select {
	case <-done:
		// goroutine exited cleanly
	case <-time.After(timeout):
		slog.Warn("[dedup] hydration goroutine did not stop within timeout — proceeding with shutdown",
			"timeout", timeout)
	}
	return nil
}
