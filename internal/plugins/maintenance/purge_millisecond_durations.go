// file: internal/plugins/maintenance/purge_millisecond_durations.go
// version: 1.0.0
// guid: 7ad86e89-caff-4b83-8cdb-ec0403de1d98
// last-edited: 2026-08-04

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// PurgeMillisecondDurationsParams are the JSON parameters for the ms→seconds
// backfill.
type PurgeMillisecondDurationsParams struct {
	// Apply, when true, writes the corrected durations. Default false — dry run.
	// Same convention as maintenance.dedupe-book-file-rows and title-repair: a
	// destructive op must be asked for explicitly, never reached by forgetting a flag.
	Apply bool `json:"apply"`
	// Limit caps how many BOOKS are processed (0 = all). Useful for a canary.
	Limit int `json:"limit,omitempty"`
}

func (p *Plugin) purgeMillisecondDurationsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.purge-millisecond-durations",
		Plugin:      "maintenance",
		DisplayName: "Convert millisecond book_file durations to seconds",
		Description: "BookFile.Duration is seconds by convention, but an old iTunes import path stored " +
			"milliseconds (TotalTime) without dividing by 1000. Those rows inflate every duration-derived " +
			"number by ~1000×. This finds them by implied bitrate — a duration is only treated as " +
			"milliseconds when reading it as seconds implies an impossible sub-4 kbps file AND dividing by " +
			"1000 lands back in a plausible audio band — and rewrites them as seconds, then recomputes the " +
			"book's aggregates. Dry-run by default; pass {\"apply\": true} to write.",
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.purge-millisecond-durations",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		// Same reasoning as dedupe-book-file-rows: RunItems stamps progress per book
		// completion, but one pathological book must not be mistaken for a hang by the
		// watchdog's 5-minute default.
		ProgressTimeout: 30 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runPurgeMillisecondDurations,
	}
}

// msFix is one row that needs converting, carried from the cheap discovery pass
// to the full-fidelity repair pass.
type msFix struct {
	fileID string
	oldDur int
	newDur int
}

func (p *Plugin) runPurgeMillisecondDurations(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params PurgeMillisecondDurationsParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	log := reporter.Logger()
	log.Info("purge-millisecond-durations: starting", "apply", params.Apply, "limit", params.Limit)

	// PASS 1 — cheap discovery. The Core projection is a memdb read and already
	// carries Duration and FileSize, which is everything DurationLooksLikeMillis
	// needs. Sweeping the whole library here is fine; it is the per-row REPAIR that
	// must be full-fidelity.
	cores, err := store.GetAllBookFilesCore()
	if err != nil {
		return fmt.Errorf("scan book_files: %w", err)
	}

	byBook := map[string][]msFix{}
	candidates := 0
	for i := range cores {
		c := cores[i]
		if c.BookID == "" {
			continue // orphan rows belong to orphan-book-files-cleanup
		}
		// 🔑 THE ONLY TEST. DurationLooksLikeMillis fires only when reading the value
		// as seconds implies an impossible sub-4 kbps file AND dividing by 1000 lands
		// back inside a plausible audio band. A genuinely low-bitrate clip is rejected
		// because its /1000 would imply an absurd multi-Mbps rate. This is the same
		// predicate the write chokepoint (normalizeBookFileDuration) and the read path
		// (NormalizeDurationSec) already use, so this backfill cannot disagree with
		// what the rest of the system believes.
		if !database.DurationLooksLikeMillis(c.FileSize, c.Duration) {
			continue
		}
		byBook[c.BookID] = append(byBook[c.BookID], msFix{
			fileID: c.ID, oldDur: c.Duration,
			newDur: database.NormalizeDurationSec(c.FileSize, c.Duration),
		})
		candidates++
	}

	bookIDs := make([]string, 0, len(byBook))
	for id := range byBook {
		bookIDs = append(bookIDs, id)
	}
	sort.Strings(bookIDs) // deterministic order so a dry run and its apply agree
	if params.Limit > 0 && len(bookIDs) > params.Limit {
		bookIDs = bookIDs[:params.Limit]
	}

	log.Info("purge-millisecond-durations: scan complete",
		"total_rows", len(cores), "books_affected", len(byBook), "ms_rows", candidates)

	if len(bookIDs) == 0 {
		summary := fmt.Sprintf(
			"purge-millisecond-durations: %d rows scanned, 0 millisecond durations found — nothing to do",
			len(cores))
		_ = reporter.Log(slog.LevelInfo, summary)
		_ = reporter.UpdateProgress(1, 1, summary)
		return nil
	}

	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"%d book(s) hold %d millisecond-valued durations", len(byBook), candidates))

	// 🔒 Shared across workers — see the pool below.
	var mu sync.Mutex
	var fixed, wouldFix, failed, recomputed, skipped int
	var examples []string

	// PASS 2 — full fidelity, per affected book. Parallel by book: a book_file row
	// belongs to exactly one book, so workers never touch the same row or the same
	// RecomputeBookAggregates target (same disjoint-partition argument as
	// dedupe-book-file-rows).
	workers := runtime.NumCPU()
	if workers > len(bookIDs) {
		workers = len(bookIDs)
	}
	log.Info("purge-millisecond-durations: repairing in parallel",
		"books", len(bookIDs), "workers", workers)

	runErr := registry.RunItems(ctx, reporter, bookIDs, func(ctx context.Context, bookID string) error {
		// GetBookFiles reads Pebble directly, NOT memdb. That matters: the memdb
		// projection strips AcoustIDFingerprint, and UpdateBookFile writes the whole
		// struct — so repairing from a memdb copy would wipe every fingerprint on the
		// row. This repo has already shipped that exact bug once.
		files, ferr := store.GetBookFiles(bookID)
		if ferr != nil {
			mu.Lock()
			failed++
			mu.Unlock()
			log.Warn("purge-millisecond-durations: GetBookFiles failed", "book_id", bookID, "err", ferr)
			return nil // one unreadable book must not abort the sweep
		}

		changedThisBook := false
		for fi := range files {
			f := files[fi]

			// Re-test against the FULL-FIDELITY row rather than trusting PASS 1. The
			// memdb snapshot can be stale (it has been, today), and a row that has
			// since been corrected must not be divided a second time — that would turn
			// a good 9,906-second duration into 9 seconds.
			if !database.DurationLooksLikeMillis(f.FileSize, f.Duration) {
				mu.Lock()
				skipped++
				mu.Unlock()
				continue
			}

			oldDur := f.Duration
			newDur := database.NormalizeDurationSec(f.FileSize, f.Duration)

			mu.Lock()
			if len(examples) < 10 {
				examples = append(examples, fmt.Sprintf("%s: %ds → %ds (%s)",
					bookID, oldDur, newDur, shortPath(f.FilePath)))
			}
			mu.Unlock()

			if !params.Apply {
				mu.Lock()
				wouldFix++
				mu.Unlock()
				continue
			}

			f.Duration = newDur
			if uerr := store.UpdateBookFile(f.ID, &f); uerr != nil {
				mu.Lock()
				failed++
				mu.Unlock()
				log.Warn("purge-millisecond-durations: write failed; leaving the row as-is",
					"book_id", bookID, "file_id", f.ID, "err", uerr)
				continue
			}
			mu.Lock()
			fixed++
			mu.Unlock()
			changedThisBook = true
		}

		// Book.Duration is a sum over the rows, so it is only right once the rows are.
		if params.Apply && changedThisBook {
			if rerr := store.RecomputeBookAggregates(bookID); rerr != nil {
				mu.Lock()
				failed++
				mu.Unlock()
				log.Warn("purge-millisecond-durations: RecomputeBookAggregates failed",
					"book_id", bookID, "err", rerr)
			} else {
				mu.Lock()
				recomputed++
				mu.Unlock()
			}
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: workers,
		ErrMode:     registry.ErrModeCollect,
		Label: func(i, total int) string {
			return fmt.Sprintf("book %d/%d", i+1, total)
		},
	})
	if runErr != nil && ctx.Err() != nil {
		log.Warn("purge-millisecond-durations: cancelled", "fixed", fixed, "books", len(bookIDs))
		return ctx.Err()
	}
	if runErr != nil {
		log.Warn("purge-millisecond-durations: some books failed", "err", runErr)
	}

	verb := fmt.Sprintf("would convert %d row(s)", wouldFix)
	if params.Apply {
		verb = fmt.Sprintf("converted %d row(s), recomputed %d book(s)", fixed, recomputed)
	}
	// Same operational caveat as the dedupe op: the corrected totals are not visible
	// on the memdb-backed read path until it refreshes.
	summary := fmt.Sprintf(
		"purge-millisecond-durations: %d rows scanned, %d book(s) affected, %d ms row(s), %s, "+
			"skipped %d (already seconds), failed %d "+
			"| NOTE: corrected totals may not appear until memdb refreshes (restart) | e.g. %s",
		len(cores), len(byBook), candidates, verb, skipped, failed, strings.Join(examples, "; "))
	_ = reporter.Log(slog.LevelInfo, summary)
	_ = reporter.UpdateProgress(len(bookIDs), len(bookIDs), summary)
	return nil
}
