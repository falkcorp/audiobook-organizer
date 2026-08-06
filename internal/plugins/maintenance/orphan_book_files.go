// file: internal/plugins/maintenance/orphan_book_files.go
// version: 1.3.0
// guid: 9d2c4f6a-8e1b-4c5d-9a7b-3e5f1a2c4b6d
// last-edited: 2026-08-06

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// OrphanBookFilesCleanupParams are the JSON parameters for the orphan
// book_files cleanup op. When Delete is false (default), the op reports the
// count of orphan rows but does not modify the database.
type OrphanBookFilesCleanupParams struct {
	// Delete, when true, removes the detected orphan book_file rows via
	// Store.DeleteBookFilesByIDs. When false (default), the op is a dry run.
	Delete bool `json:"delete"`
}

// orphanBookFilesCleanupDef registers the maintenance.orphan-book-files-cleanup
// OperationDef. It runs nightly during the maintenance window (02:15 daily) so
// it sits between purge-old-logs (02:00 Sun) and purge-deleted (03:00) without
// competing for the same minute.
func (p *Plugin) orphanBookFilesCleanupDef() sdk.OperationDef {
	sched := "15 2 * * *" // 02:15 daily — nightly maintenance window
	return sdk.OperationDef{
		ID:              "maintenance.orphan-book-files-cleanup",
		Plugin:          "maintenance",
		DisplayName:     "Orphan book_file cleanup",
		Description:     "Detects book_file rows whose book_id no longer references an existing book. Reports the count by default; pass {\"delete\": true} to remove the orphans.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "maintenance.orphan-book-files-cleanup",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runOrphanBookFilesCleanup,
	}
}

func (p *Plugin) runOrphanBookFilesCleanup(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params OrphanBookFilesCleanupParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	_ = reporter.Log(slog.LevelInfo, "Starting orphan book_file scan",
		slog.Bool("delete", params.Delete),
	)
	scanProg := sdk.NewProgress(reporter, 0)
	scanProg.Start("Scanning book_files for orphan rows...")

	orphans, totalFiles, totalBooks, err := findOrphanBookFiles(ctx, store)
	if err != nil {
		return fmt.Errorf("scan failed: %w", err)
	}

	_ = reporter.Log(slog.LevelInfo, "Orphan scan complete",
		slog.Int("orphan_count", len(orphans)),
		slog.Int("total_book_files", totalFiles),
		slog.Int("total_books", totalBooks),
	)

	if !params.Delete || len(orphans) == 0 {
		msg := fmt.Sprintf("Orphan book_file scan: %d orphan(s) detected (report-only)", len(orphans))
		if params.Delete && len(orphans) == 0 {
			msg = "Orphan book_file cleanup: no orphans found, nothing to delete"
		}
		_ = reporter.Log(slog.LevelInfo, msg)
		scanProg.Done(msg)
		return nil
	}

	// Delete pass — now we know N.
	total := len(orphans)
	prog := sdk.NewProgress(reporter, total)
	prog.Start(fmt.Sprintf("Found %d orphan book_file row(s) out of %d (valid books: %d)",
		total, totalFiles, totalBooks))
	var deleted, failed int

	// Deletes go through Store.DeleteBookFilesByIDs in chunks rather than one
	// DeleteBookFile per row. Per-row deletion costs ~1.35s of FIXED overhead each
	// (a Sync commit, a full book-version snapshot, two memdb write transactions,
	// and an aggregate recompute — all per-book work being run per row), which on a
	// multi-thousand-row orphan sweep is hours of pure waste.
	//
	// ⚠️ THIS PATH HAS NO TRAILING RecomputeBookAggregates of its own, unlike
	// dedupe-book-file-rows. It relies entirely on DeleteBookFilesByIDs notifying
	// once per affected book. Do not "optimise" that notification away.
	//
	// (In practice a genuine orphan's owning book does not exist — that is what
	// makes it an orphan — so the recompute finds no book and returns early. The
	// notification still has to happen: findOrphanBookFiles decides orphanhood from
	// a snapshot, and if a book turns out to exist after all, its aggregates must be
	// corrected rather than left counting rows that are gone.)
	//
	// WHY CHUNKED and not one call for the whole list: DeleteBookFilesByIDs is
	// fail-closed, so a single unresolvable ID aborts its entire call. Chunking
	// bounds that blast radius to one chunk, which the next run picks up anyway
	// because findOrphanBookFiles rescans from scratch. Chunking also preserves the
	// two things the per-row loop gave us for free — a cancellation check and a
	// progress tick often enough to keep the stuck-op watchdog fed.
	const orphanDeleteChunk = 500
	for start := 0; start < total; start += orphanDeleteChunk {
		if ctx.Err() != nil {
			_ = reporter.Log(slog.LevelWarn, "Orphan delete cancelled",
				slog.Int("deleted", deleted),
				slog.Int("failed", failed),
				slog.Int("remaining", total-start),
			)
			return ctx.Err()
		}
		end := min(start+orphanDeleteChunk, total)
		chunk := orphans[start:end]

		ids := make([]string, 0, len(chunk))
		for i := range chunk {
			ids = append(ids, chunk[i].ID)
		}

		if err := store.DeleteBookFilesByIDs(ids); err != nil {
			// Fail-closed: nothing in this chunk was deleted, so count the whole
			// chunk as failed rather than guessing at a partial result.
			failed += len(ids)
			_ = reporter.Log(slog.LevelWarn, "Failed to delete orphan book_file chunk",
				slog.Int("chunk_start", start),
				slog.Int("chunk_size", len(ids)),
				slog.String("first_book_file_id", chunk[0].ID),
				slog.String("first_book_id", chunk[0].BookID),
				slog.String("first_file_path", chunk[0].FilePath),
				slog.String("error", err.Error()),
			)
			continue
		}
		deleted += len(ids)
		prog.StepN(end, fmt.Sprintf("Deleting orphan book_files: %d/%d", end, total))
	}

	final := fmt.Sprintf("Orphan book_file cleanup: deleted %d, failed %d (of %d detected)",
		deleted, failed, total)
	_ = reporter.Log(slog.LevelInfo, final)
	prog.Done(final)
	return nil
}

// findOrphanBookFiles returns every BookFile whose BookID does not match any
// existing book ID. Returns the orphan slice, the total number of book_files
// scanned, and the number of valid book IDs found. The scan uses the memdb
// fastpath via Store.GetAllBookFilesCore / Store.GetAllBooks; both calls
// return projections of the underlying tables without per-row decoding cost.
//
// This is the testable core of runOrphanBookFilesCleanup. It does not delete
// anything — callers that want to delete pass the resulting IDs to
// Store.DeleteBookFilesByIDs themselves, in chunks.
func findOrphanBookFiles(ctx context.Context, store database.Store) (orphans []database.BookFileCore, totalFiles int, totalBooks int, err error) {
	if ctx.Err() != nil {
		return nil, 0, 0, ctx.Err()
	}
	files, ferr := store.GetAllBookFilesCore()
	if ferr != nil {
		return nil, 0, 0, fmt.Errorf("GetAllBookFilesCore: %w", ferr)
	}
	if ctx.Err() != nil {
		return nil, 0, 0, ctx.Err()
	}
	// GetAllBooksCore(0, 0) is the unbounded form across the existing maintenance
	// plugin (see pebble_store.go:9210, 9256, 9318) — limit=0 means "all".
	books, berr := store.GetAllBooksCore(0, 0)
	if berr != nil {
		return nil, 0, 0, fmt.Errorf("GetAllBooksCore: %w", berr)
	}
	valid := make(map[string]struct{}, len(books))
	for _, b := range books {
		valid[b.ID] = struct{}{}
	}
	orphans = make([]database.BookFileCore, 0)
	for _, f := range files {
		if ctx.Err() != nil {
			return nil, 0, 0, ctx.Err()
		}
		if f.BookID == "" {
			// Empty book_id is its own kind of broken row, but treat it as
			// an orphan so it surfaces in the count.
			orphans = append(orphans, f)
			continue
		}
		if _, ok := valid[f.BookID]; !ok {
			orphans = append(orphans, f)
		}
	}
	return orphans, len(files), len(books), nil
}
