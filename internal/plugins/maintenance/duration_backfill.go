// file: internal/plugins/maintenance/duration_backfill.go
// version: 1.5.0
// guid: 7e4b2a90-3c61-4d58-8f29-6a1c0e5b9d83
// last-edited: 2026-07-05

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// CONS-16: the iTunes importer historically stored per-file BookFile.Duration in
// milliseconds (iTunes TotalTime units) instead of seconds. RecomputeBookAggregates
// then summed those inflated values and overwrote the correct seconds-valued
// Book.Duration, mislabeling real books as dedup candidates. This maintenance op
// heals existing rows. The importer bug itself is fixed at the 3 write sites
// (see internal/itunes/service/importer.go trackDurationSeconds), and new writes
// are guarded at the store chokepoint (CONS-18, database.DurationLooksLikeMillis).
//
// The ms-detection predicate now lives in the database package so the store gate
// and this backfill share one implementation.
func durationLooksLikeMillis(fileSize int64, durationSec int) bool {
	return database.DurationLooksLikeMillis(fileSize, durationSec)
}

type durationBackfillParams struct {
	DryRun bool `json:"dryRun"`
}

// durationFix records a single BookFile whose Duration must be divided by 1000.
type durationFix struct {
	file        database.BookFile
	newDuration int
}

func (p *Plugin) durationBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.duration-backfill",
		Plugin:          "maintenance",
		DisplayName:     "Fix millisecond-valued book file durations",
		Description:     "Scans all BookFiles for durations mistakenly stored in milliseconds (CONS-16: legacy iTunes import bug) and divides them back to seconds, then recomputes affected book aggregates. Detection uses the file's implied bitrate so genuine durations are never touched. Default dry-run previews changes; set dryRun=false to apply.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.duration-backfill",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         60 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runDurationBackfill,
	}
}

func (p *Plugin) runDurationBackfill(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params := durationBackfillParams{DryRun: true} // safe default
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}

	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	if params.DryRun {
		_ = reporter.Log(slog.LevelInfo, "DRY RUN — no changes will be written")
	}

	totalBooks, countErr := store.CountPrimaryBooks()
	if countErr != nil || totalBooks <= 0 {
		totalBooks = 0
	}
	_ = reporter.UpdateProgress(0, totalBooks, "Phase 1/2: scanning book file durations…")

	const pageSize = 500
	offset := 0
	// Gather all books first via pagination.
	var allBooks []database.Book
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		books, err := store.GetAllBooks(pageSize, offset)
		if err != nil {
			return fmt.Errorf("GetAllBooks offset=%d: %w", offset, err)
		}
		if len(books) == 0 {
			break
		}
		allBooks = append(allBooks, books...)
		offset += len(books)
		if len(books) < pageSize {
			break
		}
	}

	// Fixes grouped per book, preserving book discovery order so Phase 2 can
	// recompute each affected book's aggregates exactly once. Guarded by mu since
	// multiple workers will update these concurrently via registry.RunItems.
	var mu sync.Mutex
	bookOrder := make([]string, 0)
	fixesByBook := make(map[string][]durationFix)

	// Parallelize the per-book GetBookFiles and duration analysis via registry.RunItems.
	// Each worker independently checks a book's files and accumulates fixes; the shared
	// maps are guarded by mu so the parallel version produces identical output to serial.
	scanned := 0
	err := registry.RunItems(ctx, reporter, allBooks, func(ctx context.Context, book database.Book) error {
		files, ferr := store.GetBookFiles(book.ID)
		if ferr != nil {
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
				"book %s: GetBookFiles failed: %v", book.ID, ferr))
			return nil // non-fatal: skip this book
		}

		var bookFixes []durationFix
		for _, f := range files {
			if durationLooksLikeMillis(f.FileSize, f.Duration) {
				bookFixes = append(bookFixes, durationFix{
					file:        f,
					newDuration: f.Duration / 1000,
				})
			}
		}

		// Atomically update shared state if this book has any fixes.
		if len(bookFixes) > 0 {
			mu.Lock()
			bookOrder = append(bookOrder, book.ID)
			fixesByBook[book.ID] = bookFixes
			mu.Unlock()
		}

		return nil
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(),
		Label: func(i, total int) string {
			mu.Lock()
			curBooks := len(bookOrder)
			mu.Unlock()
			return fmt.Sprintf("Books %d/%d (found %d with ms durations)",
				i+1, total, curBooks)
		},
	})
	if err != nil {
		return fmt.Errorf("parallel GetBookFiles scan: %w", err)
	}
	scanned = len(allBooks)

	totalFiles := 0
	for _, fixes := range fixesByBook {
		totalFiles += len(fixes)
	}

	if totalFiles == 0 {
		suffix := ""
		if params.DryRun {
			suffix = " (dry run)"
		}
		result := fmt.Sprintf("Scanned %d books: 0 files need duration correction%s", scanned, suffix)
		_ = reporter.Log(slog.LevelInfo, result)
		_ = reporter.UpdateProgress(1, 1, result)
		return nil
	}

	_ = reporter.UpdateProgress(0, totalFiles, fmt.Sprintf(
		"Phase 2: correcting %d file durations across %d books…", totalFiles, len(bookOrder)))

	var fixedFiles, errCount int

	// Logging is best-effort and time-batched: progress (a cheap counter) updates
	// continuously, but file-level detail is flushed at most once per logInterval
	// so a 175K-row run can't flood the activity store. Examples are kept in a
	// small ring and emitted with each heartbeat.
	const (
		writeBatchSize = 1000             // BookFiles per BatchUpsert commit
		logInterval    = 15 * time.Second // heartbeat cadence for detail logs
		exampleCount   = 5                // sample corrections per heartbeat
	)
	lastLog := time.Now()
	heartbeat := func(force bool, phase string, cur, total int, examples []string) {
		if !force && time.Since(lastLog) < logInterval {
			return
		}
		msg := fmt.Sprintf("%s: %d/%d", phase, cur, total)
		if len(examples) > 0 {
			msg += " — e.g. " + strings.Join(examples, "; ")
		}
		_ = reporter.Log(slog.LevelInfo, msg)
		_ = reporter.UpdateProgress(cur, total, fmt.Sprintf("%s: %d/%d", phase, cur, total))
		lastLog = time.Now()
	}

	if params.DryRun {
		// Preview only: count and show a handful of examples, no writes.
		examples := make([]string, 0, exampleCount)
		for _, bookID := range bookOrder {
			for _, fix := range fixesByBook[bookID] {
				if len(examples) < exampleCount {
					examples = append(examples, fmt.Sprintf("%s %dms→%ds", fix.file.ID, fix.file.Duration, fix.newDuration))
				}
				fixedFiles++
			}
		}
		result := fmt.Sprintf("DRY RUN — would correct %d file durations across %d books. Examples: %s",
			fixedFiles, len(bookOrder), strings.Join(examples, "; "))
		_ = reporter.Log(slog.LevelInfo, result)
		_ = reporter.UpdateProgress(totalFiles, totalFiles, result)
		return nil
	}

	// Phase 2a: write corrected durations in batches. BatchUpsertBookFiles does
	// NOT recompute book aggregates per file — that's the whole point: a per-file
	// recompute (175K of them, each re-summing the book) is what made the naive
	// version take hours. We recompute once per book in Phase 2b instead.
	pending := make([]*database.BookFile, 0, writeBatchSize)
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		if err := store.BatchUpsertBookFiles(pending); err != nil {
			return err
		}
		pending = pending[:0]
		return nil
	}

	for _, bookID := range bookOrder {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		for _, fix := range fixesByBook[bookID] {
			corrected := fix.file // copy; take address of the loop-local
			corrected.Duration = fix.newDuration
			pending = append(pending, &corrected)
			fixedFiles++
			if len(pending) >= writeBatchSize {
				if err := flush(); err != nil {
					return fmt.Errorf("batch write at file %d: %w", fixedFiles, err)
				}
			}
			heartbeat(false, "Phase 2a writing durations", fixedFiles, totalFiles, nil)
		}
	}
	if err := flush(); err != nil {
		return fmt.Errorf("final batch write: %w", err)
	}
	heartbeat(true, "Phase 2a writing durations", fixedFiles, totalFiles, nil)

	// Phase 2b: heal the denormalized Book.Duration / Book.FileSize aggregates,
	// exactly once per affected book.
	healed := 0
	for _, bookID := range bookOrder {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := store.RecomputeBookAggregates(bookID); err != nil {
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
				"book %s: RecomputeBookAggregates failed: %v", bookID, err))
			errCount++
		}
		healed++
		heartbeat(false, "Phase 2b healing book aggregates", healed, len(bookOrder), nil)
	}

	result := fmt.Sprintf("Scanned %d books: %d file durations corrected across %d books, %d errors",
		scanned, fixedFiles, len(bookOrder), errCount)
	_ = reporter.Log(slog.LevelInfo, result)
	_ = reporter.UpdateProgress(totalFiles, totalFiles, result)

	if errCount > 0 {
		return fmt.Errorf("%d errors during duration backfill (see op log for details)", errCount)
	}
	return nil
}
