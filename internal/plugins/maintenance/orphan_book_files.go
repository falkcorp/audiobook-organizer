// file: internal/plugins/maintenance/orphan_book_files.go
// version: 2.1.0
// guid: 9d2c4f6a-8e1b-4c5d-9a7b-3e5f1a2c4b6d
// last-edited: 2026-09-19

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
// book_files scan.
//
// Delete is accepted only so an old caller gets a clear refusal instead of a
// silent report: the delete mode was REMOVED on 2026-09-19. It hard-deleted
// the orphan rows, and a book_file row is the only record tying an audio file
// to the library — the standing rule is never delete book_file rows as a
// repair, repoint them. Use maintenance.orphan-book-files-repoint-plan (dry
// run) to see where each orphan should go.
type OrphanBookFilesCleanupParams struct {
	Delete bool `json:"delete"`
}

// orphanBookFilesCleanupDef registers the maintenance.orphan-book-files-cleanup
// OperationDef. It runs nightly during the maintenance window (02:15 daily) so
// it sits between purge-old-logs (02:00 Sun) and purge-deleted (03:00) without
// competing for the same minute. Report-only.
func (p *Plugin) orphanBookFilesCleanupDef() sdk.OperationDef {
	sched := "15 2 * * *" // 02:15 daily — nightly maintenance window
	return sdk.OperationDef{
		ID:              "maintenance.orphan-book-files-cleanup",
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Orphan book_file scan",
		Description:     "Report-only: counts book_file rows whose book_id no longer references an existing book. Never deletes; see maintenance.orphan-book-files-repoint-plan for where each should go.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "maintenance.orphan-book-files-cleanup",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead},
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
	if params.Delete {
		return fmt.Errorf("the delete mode of this op was removed: it deleted book_file rows, which must be repointed instead; run maintenance.orphan-book-files-repoint-plan for a dry-run plan")
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	_ = reporter.Log(slog.LevelInfo, "Starting orphan book_file scan (report-only)")
	scanProg := sdk.NewProgress(reporter, 0)
	scanProg.Start("Scanning book_files for orphan rows...")

	orphans, totalFiles, ownerCount, err := findOrphanBookFiles(ctx, store)
	if err != nil {
		return fmt.Errorf("scan failed: %w", err)
	}

	_ = reporter.Log(slog.LevelInfo, "Orphan scan complete",
		slog.Int("orphan_count", len(orphans)),
		slog.Int("total_book_files", totalFiles),
		// live + soft-deleted: every book that could still own a file row.
		slog.Int("owning_books", ownerCount),
	)
	msg := fmt.Sprintf("Orphan book_file scan: %d orphan(s) detected (report-only)", len(orphans))
	_ = reporter.Log(slog.LevelInfo, msg)
	scanProg.Done(msg)
	return nil
}

// findOrphanBookFiles returns every BookFile whose BookID does not match any
// book that could still own it. Returns the orphan slice, the total number of
// book_files scanned, and the number of owning book IDs considered.
//
// The file list comes from Store.GetAllBookFilesCore, which takes the memdb
// fastpath and returns a projection without per-row decoding cost. The two
// getters that build the OWNER set — GetAllBooksCoreComplete and
// ListSoftDeletedBooks — take that fastpath only while memdb can vouch for
// being complete, and fall back to the authoritative Pebble scan otherwise.
// That is not an optimisation detail: this scan's absences decide what is
// reported (and planned for repointing) as an orphan, so a short answer
// misreports live rows. Until 2026-09-19 they authorized a hard delete of the
// rows; that mode is gone, but the fail-closed reads stay — the comments below
// that speak of deletion describe why they were made strict.
//
// "Could still own it" is deliberately wider than "live": a soft-deleted book
// is restorable and still owns its files, so ownerCount is live + soft-deleted
// and will exceed the library's book count. That is the correct denominator
// for this scan and the one it reports.
//
// This is the testable core of runOrphanBookFilesCleanup and of the repoint
// plan. It never deletes anything, and no caller does any more.
func findOrphanBookFiles(ctx context.Context, store orphanFileScanner) (orphans []database.BookFileCore, totalFiles int, ownerCount int, err error) {
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
	// limit=0 means "all" — the unbounded form used across this plugin.
	//
	// Complete, not plain GetAllBooksCore. This list is a membership set whose
	// ABSENCES authorize a hard delete, so an undercount is not a smaller report,
	// it is book_file rows destroyed. One book row missing from a partially warmed
	// or lossy memdb removes that book from `valid` below, and every file row it
	// owns is deleted while the book survives as a fileless shell.
	//
	// Fail CLOSED on error, exactly as the soft-deleted union below does: the
	// PebbleStore form recovers a tainted memdb by rescanning Pebble, so an error
	// reaching here means no source could produce a trustworthy set — and this
	// scan's caller feeds what it returns straight to DeleteBookFilesByIDs.
	books, berr := store.GetAllBooksCoreComplete(0, 0)
	if berr != nil {
		return nil, 0, 0, fmt.Errorf("GetAllBooksCoreComplete (decides which book_file rows are deleted): %w", berr)
	}
	valid := make(map[string]struct{}, len(books))
	for _, b := range books {
		valid[b.ID] = struct{}{}
	}

	// A soft-deleted book still OWNS its book_files. It is in the trash, not
	// gone: POST /api/v1/audiobooks/:id/restore brings it back, and a restore
	// whose file rows were deleted underneath it restores an empty shell.
	//
	// GetAllBooksCore deliberately excludes soft-deleted rows, so they must be
	// added back explicitly here — this scan is a set-difference that treats
	// every book_file whose owner is absent as garbage, and callers feed the
	// result straight to DeleteBookFilesByIDs.
	//
	// Until 2026-08-13 this was accidentally correct: the memdb implementation
	// of GetAllBooksCore leaked soft-deleted rows, so they landed in `valid` by
	// way of a bug. Fixing that bug is what made this union load-bearing —
	// without it the very first orphan-cleanup run after the fix would have
	// deleted the file rows of all 3,953 books soft-deleted by the July dedup
	// drain. See TestFindOrphanBookFiles_SoftDeletedBooksKeepTheirFiles.
	softDeleted, serr := store.ListSoftDeletedBooks(0, 0, nil)
	if serr != nil {
		// Fail CLOSED: without the soft-deleted set this scan cannot tell a
		// restorable book's files from real garbage, and the caller deletes
		// what it returns. Refuse rather than under-report.
		return nil, 0, 0, fmt.Errorf("ListSoftDeletedBooks (needed to protect restorable books): %w", serr)
	}
	for i := range softDeleted {
		valid[softDeleted[i].ID] = struct{}{}
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
	// Report the size of the set that actually decided orphanhood, not just
	// the live books. `valid` is live + soft-deleted, so returning len(books)
	// here would log a denominator smaller than the one used above — an
	// operator reading "N orphans out of M books" would be reading the wrong M
	// and could not reconcile it against the library count.
	return orphans, len(files), len(valid), nil
}
