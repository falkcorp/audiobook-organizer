// file: internal/plugins/maintenance/orphan_book_files.go
// version: 1.2.0
// guid: 9d2c4f6a-8e1b-4c5d-9a7b-3e5f1a2c4b6d
// last-edited: 2026-07-07

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
	// Delete, when true, removes each detected orphan book_file row via
	// Store.DeleteBookFile. When false (default), the op is a dry run.
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
	for i, of := range orphans {
		if ctx.Err() != nil {
			_ = reporter.Log(slog.LevelWarn, "Orphan delete cancelled",
				slog.Int("deleted", deleted),
				slog.Int("failed", failed),
				slog.Int("remaining", total-i),
			)
			return ctx.Err()
		}
		if err := store.DeleteBookFile(of.ID); err != nil {
			failed++
			_ = reporter.Log(slog.LevelWarn, "Failed to delete orphan book_file",
				slog.String("book_file_id", of.ID),
				slog.String("book_id", of.BookID),
				slog.String("file_path", of.FilePath),
				slog.String("error", err.Error()),
			)
			continue
		}
		deleted++
		prog.StepN(i+1, fmt.Sprintf("Deleting orphan book_files: %d/%d", i+1, total))
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
// anything — callers that want to delete iterate over the result and call
// Store.DeleteBookFile themselves.
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
