// file: internal/server/embedding_backfill.go
// version: 1.11.0
// guid: a1b2c3d4-e5f6-7a8b-9c0d-e1f2a3b4c5d6
// last-edited: 2026-07-07

package server

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// backfillVersionMarker is delegated to the domain package.
var backfillVersionMarker = dedup.BackfillVersionMarker

// embeddingBackfillConcurrency bounds the startup embedding backfill's
// worker pool (CONC-6). EmbedBook and EmbedAuthor both ultimately call out to
// the configured embedding backend (local Ollama or OpenAI), which is
// network/rate-limited rather than CPU-bound, so this is a small fixed knob
// rather than runtime.NumCPU() — the same reasoning applied to the other
// network-bound external-call hotspots in
// docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md (fix
// pattern #4).
const embeddingBackfillConcurrency = 4

// embeddingBackfillReporter is a minimal registry.Reporter adapter for the
// startup embedding backfill loop. This loop runs as a plain background
// goroutine kicked off from server.go rather than through the operation
// registry, so — unlike op-registry plugins such as
// internal/plugins/acoustid/backfill.go, which receive a real sdk.Reporter —
// there is no Reporter already in scope here.
//
// Checkpoint/RunPhase/Trigger/Logger/SetCurrentItem are inert no-ops: this
// loop has no checkpoint/resume state (the whole-backfill version marker in
// runEmbeddingBackfill already makes the expensive work idempotent across
// restarts) and no SSE/UI surface to update mid-run. UpdateProgress/
// IsCanceled/Log delegate to slog and the loop's own ctx (nil-safe) so
// interval progress logging and cancellation still work under
// registry.RunItems.
type embeddingBackfillReporter struct {
	ctx context.Context
	// logEvery gates UpdateProgress logging to roughly one line per N
	// completed items (0 disables interval logging). Mirrors the previous
	// nextProgressAt bucket-logging behavior without needing a shared
	// mutable counter across goroutines: current is unique per item.
	logEvery int
}

func (r *embeddingBackfillReporter) UpdateProgress(current, total int, message string) error {
	if r.logEvery > 0 && (current%r.logEvery == 0 || current == total) {
		slog.Info(message, "current", current, "total", total)
	}
	return nil
}

func (r *embeddingBackfillReporter) Log(level slog.Level, message string, attrs ...slog.Attr) error {
	slog.Default().LogAttrs(context.Background(), level, message, attrs...)
	return nil
}

func (r *embeddingBackfillReporter) Logger() *slog.Logger { return slog.Default() }

func (r *embeddingBackfillReporter) Checkpoint(state any) error { return nil }

func (r *embeddingBackfillReporter) IsCanceled() bool {
	return r.ctx != nil && r.ctx.Err() != nil
}

func (r *embeddingBackfillReporter) RunPhase(ctx context.Context, name string, fn func(context.Context, registry.Reporter) error) error {
	return fn(ctx, r)
}

func (r *embeddingBackfillReporter) Trigger(ctx context.Context, eventName string, payload any) error {
	return nil
}

func (r *embeddingBackfillReporter) SetCurrentItem(label string) {}

// embeddingBackfillStats is the honest per-status breakdown for the book
// backfill loop. Kept as its own type (rather than five return values) so
// embedBooksConcurrent can be unit tested against a fake embedFn without a
// real dedup.Engine/store.
type embeddingBackfillStats struct {
	Embedded          int
	Cached            int
	SkippedNonPrimary int
	SkippedEmptyTitle int
	Errors            int
	Visited           int
}

// embedBooksConcurrent runs embedFn over books via registry.RunItems with a
// bounded worker pool, aggregating per-status counts under a mutex (the only
// shared mutable state in this loop — everything else is per-worker-local or
// delegated to embedFn, which the brief confirms has no cross-book shared
// state of concern). Returns ctx.Err() when canceled mid-run; otherwise nil.
//
// Extracted from runEmbeddingBackfill so the concurrency behavior (parallel
// output identical to serial output, order-independent) can be exercised in
// a unit test with a fake embedFn instead of a live dedup.Engine.
func embedBooksConcurrent(ctx context.Context, books []database.BookCore, concurrency int, embedFn func(ctx context.Context, bookID string) (dedup.EmbedStatus, error)) (embeddingBackfillStats, error) {
	var (
		mu    sync.Mutex
		stats embeddingBackfillStats
	)
	reporter := &embeddingBackfillReporter{ctx: ctx, logEvery: 500}

	err := registry.RunItems(ctx, reporter, books, func(ctx context.Context, book database.BookCore) error {
		status, embedErr := embedFn(ctx, book.ID)

		mu.Lock()
		defer mu.Unlock()
		stats.Visited++
		if embedErr != nil {
			slog.Warn("backfill embed book", "book", book.ID, "err", embedErr)
			stats.Errors++
			return nil
		}
		switch status {
		case dedup.EmbedStatusEmbedded:
			stats.Embedded++
		case dedup.EmbedStatusCached:
			stats.Cached++
		case dedup.EmbedStatusSkippedNonPrimary:
			stats.SkippedNonPrimary++
		case dedup.EmbedStatusSkippedEmptyTitle:
			stats.SkippedEmptyTitle++
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: concurrency,
		Label: func(i, total int) string {
			mu.Lock()
			s := stats
			mu.Unlock()
			return fmt.Sprintf("Embedding backfill progress books visited=%d/%d embedded=%d cached=%d skipped_non_primary=%d skipped_empty_title=%d errors=%d",
				i, total, s.Embedded, s.Cached, s.SkippedNonPrimary, s.SkippedEmptyTitle, s.Errors)
		},
	})
	// RunItems has joined every worker goroutine by the time it returns
	// (both runItemsSeq and runItemsPar block on completion/wg.Wait()), so
	// reading stats without the mutex here is safe.
	return stats, err
}

// embedAuthorsConcurrent is the author-loop analog of embedBooksConcurrent.
// EmbedAuthor's success/failure is boolean (no status enum), so the only
// shared mutable state is the completed-count.
func embedAuthorsConcurrent(ctx context.Context, authors []database.Author, concurrency int, embedFn func(ctx context.Context, authorID int) error) (int, error) {
	var (
		mu    sync.Mutex
		count int
	)
	reporter := &embeddingBackfillReporter{ctx: ctx, logEvery: 500}

	err := registry.RunItems(ctx, reporter, authors, func(ctx context.Context, author database.Author) error {
		if embedErr := embedFn(ctx, author.ID); embedErr != nil {
			slog.Warn("backfill embed author", "author", author.ID, "err", embedErr)
			return nil
		}
		mu.Lock()
		count++
		mu.Unlock()
		return nil
	}, registry.RunItemsOptions{
		Concurrency: concurrency,
		Label: func(i, total int) string {
			mu.Lock()
			c := count
			mu.Unlock()
			return fmt.Sprintf("Embedding backfill progress authors visited=%d/%d embedded=%d", i, total, c)
		},
	})
	return count, err
}

// runEmbeddingBackfill embeds all books and authors on first startup and
// re-runs once after each backfill version bump.
func (s *Server) runEmbeddingBackfill() {
	store := s.Store()
	if store == nil || s.dedupEngine == nil {
		return
	}

	// Check if backfill already done at the current version
	if setting, err := store.GetSetting(backfillVersionMarker); err == nil && setting != nil && setting.Value == "true" {
		slog.Info("Embedding backfill already complete (), skipping", "backfillVersionMarker", backfillVersionMarker)
		return
	}
	slog.Info("Starting embedding backfill ()...", "backfillVersionMarker", backfillVersionMarker)

	// Use the server's background context so Shutdown can cancel this
	// goroutine instead of leaving it iterating Pebble while CloseStore
	// tries to tear down the database. If bgCtx is nil (e.g. unit tests
	// instantiating Server without NewServer), fall back to Background.
	ctx := s.bgCtx
	if ctx == nil {
		ctx = context.Background()
	}

	if ctx.Err() != nil {
		slog.Info("Embedding backfill canceled before book loop started", "ctx", ctx.Err())
		return
	}

	// Fetch the whole library up front (limit=0 is the documented "fetch
	// all" sentinel — see internal/plugins/dedup/embed_scan.go's identical
	// GetAllBooks(0, 0) call) so the book loop is a single registry.RunItems
	// call rather than a nested per-page loop. Swallow the error exactly as
	// the previous pagination loop did (it only used the error to stop
	// fetching, never logged it).
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		books = nil
	}

	// Honest counters: the previous version of this loop reported
	// "N books embedded" for every successful EmbedBook return, which
	// included non-primary skips, empty-title skips, and cached-hash
	// no-ops. A re-run against a stable library would report ~24K
	// "embedded" books even though zero API calls had been made and
	// roughly half the records were non-primary version siblings the
	// scorer never touches. We now count each EmbedStatus into its own
	// bucket and log a breakdown at the end.
	stats, runErr := embedBooksConcurrent(ctx, books, embeddingBackfillConcurrency, s.dedupEngine.EmbedBook)
	if runErr != nil && ctx.Err() != nil {
		slog.Info("Embedding backfill canceled during book loop", "visited", stats.Visited, "ctx", ctx.Err())
		return
	}
	slog.Info("Book backfill complete visited embedded cached skipped_non_primary skipped_empty_title errors", "visited", stats.Visited, "statEmbedded", stats.Embedded, "statCached", stats.Cached, "statSkippedNonPrimary", stats.SkippedNonPrimary, "statSkippedEmptyTitle", stats.SkippedEmptyTitle, "statErrors", stats.Errors)

	// Backfill authors
	authorCount := 0
	authors, err := store.GetAllAuthors()
	if err != nil {
		slog.Warn("backfill failed to get authors", "err", err)
	} else if ctx.Err() != nil {
		slog.Info("Embedding backfill canceled during author loop after authors", "authorCount", authorCount)
		return
	} else {
		var runErr error
		authorCount, runErr = embedAuthorsConcurrent(ctx, authors, embeddingBackfillConcurrency, s.dedupEngine.EmbedAuthor)
		if runErr != nil && ctx.Err() != nil {
			slog.Info("Embedding backfill canceled during author loop after authors", "authorCount", authorCount)
			return
		}
	}
	slog.Info("Embedded authors", "authorCount", authorCount)

	slog.Info("Embedding backfill complete books (embedded, cached), authors", "visited", stats.Visited, "statEmbedded", stats.Embedded, "statCached", stats.Cached, "authorCount", authorCount)

	// Persist the backfill marker NOW, before FullScan runs. The embedding
	// work itself is complete; FullScan is a follow-up exact-match/similarity
	// pass that happens to live in the same function. It used to come right
	// before the SetSetting call, which meant any crash or panic during
	// FullScan — and we've been hitting a Pebble "element has outstanding
	// references" panic pretty reliably — would leave the marker unset and
	// the next restart would pointlessly re-embed 24K books from the cached
	// text_hash (fast but not free, and it blocks the API while it runs).
	// Writing the marker here makes the expensive part idempotent across
	// crashes. If FullScan fails, the user can still trigger a Re-scan from
	// the UI; they just won't re-pay for embedding work that's already done.
	if err := store.SetSetting(backfillVersionMarker, "true", "bool", false); err != nil {
		slog.Warn("failed to persist backfill marker — backfill will re-run next startup", "err", err)
	} else {
		slog.Info("Embedding backfill marker persisted ()", "backfillVersionMarker", backfillVersionMarker)
	}

	// Purge stale candidates from any previous scan before running a new
	// one. This is what cleans up the 16K+ non-primary / same-group rows
	// left over from pre-fix backfills — on subsequent startups, the
	// cleanup is a no-op because FullScan won't create those rows anymore.
	if deleted, err := s.dedupEngine.PurgeStaleCandidates(ctx); err != nil {
		slog.Warn("backfill purge stale candidates error", "err", err)
	} else if deleted > 0 {
		slog.Info("Purged stale dedup candidate(s) before initial scan", "deleted", deleted)
	}

	// Run full dedup scan with a bucket-crossing progress logger (see
	// newDedupScanProgressLogger for why a naive `done%N == 0` check fails).
	// Wrapped in defer/recover because this is the block that has been
	// panicking with a Pebble ref-count error mid-scan. A panic here should
	// leave the process running — the marker is already set above, the
	// dedup queue is usable, and the crash was killing the whole server on
	// startup which meant the user couldn't even reach the UI to investigate.
	slog.Info("Running initial dedup scan...")
	func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("Initial dedup scan panicked — backfill marker is already set, server will continue", "r", r)
			}
		}()
		progressFn := newDedupScanProgressLogger(1000, func(format string, args ...any) {
			slog.DebugContext(context.Background(), "progress", "args", args)
		})
		if err := s.dedupEngine.FullScan(ctx, progressFn); err != nil {
			slog.Warn("Initial dedup scan failed", "err", err)
		}
	}()

	slog.Info("Embedding backfill and initial dedup scan complete ()", "backfillVersionMarker", backfillVersionMarker)
}

// newDedupScanProgressLogger is a thin wrapper around the domain implementation.
func newDedupScanProgressLogger(interval int, logf func(format string, args ...any)) func(phase string, done, total int) {
	return dedup.NewDedupScanProgressLogger(interval, logf)
}
