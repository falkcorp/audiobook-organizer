// file: internal/server/embedding_backfill.go
// version: 1.9.0
// guid: a1b2c3d4-e5f6-7a8b-9c0d-e1f2a3b4c5d6
// last-edited: 2026-07-04

package server

import (
	"context"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/dedup"
)

// backfillVersionMarker is delegated to the domain package.
var backfillVersionMarker = dedup.BackfillVersionMarker

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
	offset := 0

	// Honest counters: the previous version of this loop reported
	// "N books embedded" for every successful EmbedBook return, which
	// included non-primary skips, empty-title skips, and cached-hash
	// no-ops. A re-run against a stable library would report ~24K
	// "embedded" books even though zero API calls had been made and
	// roughly half the records were non-primary version siblings the
	// scorer never touches. We now count each EmbedStatus into its own
	// bucket and log a breakdown at the end.
	var (
		statEmbedded          int
		statCached            int
		statSkippedNonPrimary int
		statSkippedEmptyTitle int
		statErrors            int
	)
	visited := 0
	nextProgressAt := 500

	// Backfill books in batches
	for {
		// Honor shutdown — if the server is stopping, bail out fast so
		// we're not holding iterators / DB connections when CloseStore
		// runs. The marker stays unset on cancel, which is the right
		// thing: we want the next startup to resume the backfill.
		if ctx.Err() != nil {
			slog.Info("Embedding backfill canceled during book loop at offset", "offset", offset, "ctx", ctx.Err())
			return
		}
		books, err := store.GetAllBooks(100, offset)
		if err != nil || len(books) == 0 {
			break
		}
		for _, book := range books {
			if ctx.Err() != nil {
				slog.Info("Embedding backfill canceled mid-batch at offset", "offset", offset)
				return
			}
			status, err := s.dedupEngine.EmbedBook(ctx, book.ID)
			if err != nil {
				slog.Warn("backfill embed book", "book", book.ID, "err", err)
				statErrors++
				visited++
				continue
			}
			switch status {
			case dedup.EmbedStatusEmbedded:
				statEmbedded++
			case dedup.EmbedStatusCached:
				statCached++
			case dedup.EmbedStatusSkippedNonPrimary:
				statSkippedNonPrimary++
			case dedup.EmbedStatusSkippedEmptyTitle:
				statSkippedEmptyTitle++
			}
			visited++
		}
		offset += len(books)
		if visited >= nextProgressAt {
			slog.Info("Embedding backfill progress books visited (embedded cached skipped_non_primary skipped_empty_title)", "visited", visited, "statEmbedded", statEmbedded, "statCached", statCached, "statSkippedNonPrimary", statSkippedNonPrimary, "statSkippedEmptyTitle", statSkippedEmptyTitle)
			for nextProgressAt <= visited {
				nextProgressAt += 500
			}
		}
	}
	slog.Info("Book backfill complete visited embedded cached skipped_non_primary skipped_empty_title errors", "visited", visited, "statEmbedded", statEmbedded, "statCached", statCached, "statSkippedNonPrimary", statSkippedNonPrimary, "statSkippedEmptyTitle", statSkippedEmptyTitle, "statErrors", statErrors)

	// Backfill authors
	authorCount := 0
	authors, err := store.GetAllAuthors()
	if err != nil {
		slog.Warn("backfill failed to get authors", "err", err)
	} else {
		for _, author := range authors {
			if ctx.Err() != nil {
				slog.Info("Embedding backfill canceled during author loop after authors", "authorCount", authorCount)
				return
			}
			if err := s.dedupEngine.EmbedAuthor(ctx, author.ID); err != nil {
				slog.Warn("backfill embed author", "author", author.ID, "err", err)
			} else {
				authorCount++
			}
		}
	}
	slog.Info("Embedded authors", "authorCount", authorCount)

	slog.Info("Embedding backfill complete books (embedded, cached), authors", "visited", visited, "statEmbedded", statEmbedded, "statCached", statCached, "authorCount", authorCount)

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
