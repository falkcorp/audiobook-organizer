// file: internal/plugins/maintenance/orphan_book_files.go
// version: 2.4.0
// guid: 9d2c4f6a-8e1b-4c5d-9a7b-3e5f1a2c4b6d
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
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

// orphanBookFilesCleanupHelp is the def's Description, which the native kind
// carries as its Help.
const orphanBookFilesCleanupHelp = "Report-only: counts book_file rows whose book_id no longer references an existing book. Never deletes; see maintenance.orphan-book-files-repoint-plan for where each should go."

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
		Description:     orphanBookFilesCleanupHelp,
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "maintenance.orphan-book-files-cleanup",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead},
		// Explicit so the def does not rely on the empty-means-settings.manage
		// default (08-X2); the scheduler now enqueues it nightly.
		Permissions: []auth.Permission{auth.PermSettingsManage},
		Run:         p.runOrphanBookFilesCleanup,
	}
}

func (p *Plugin) runOrphanBookFilesCleanup(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params OrphanBookFilesCleanupParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	env := orphanBookFilesCleanupEnv{t6g6Env: t6g6HeadEnv(reporter, false)}
	// A nil OpsStore leaves env.Store nil (a typed-nil adapter would hide it), so
	// the core's "database not initialized" check still fires, after the delete
	// refusal as before.
	if store := p.deps.OpsStore(); store != nil {
		env.Store = t6g6HeadStore{OpsStore: store}
	}
	return orphanBookFilesCleanupCore(ctx, params, env)
}

// orphanBookFilesCleanupKind is rule R3's kind for the def above.
const orphanBookFilesCleanupKind = tasks.Kind("books.maintenance.orphan-book-files-cleanup")

// orphanBookFilesCleanupStore is the reads the scan makes. The op writes
// nothing, so it has no writer.
type orphanBookFilesCleanupStore interface {
	// AllBookFilesCoreIncludingRetired is every book_file row, retired ones
	// included (database.AllBookFilesCoreIncludingRetired over the store).
	AllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error)
	GetAllBooksCoreComplete(limit, offset int) ([]database.BookCore, error)
	ListSoftDeletedBooks(limit, offset int, olderThan *time.Time) ([]database.Book, error)
}

// orphanBookFilesCleanupEnv is what the core takes from its shell.
type orphanBookFilesCleanupEnv struct {
	t6g6Env
	Store orphanBookFilesCleanupStore
}

// orphanBookFilesCleanupParallelism is the op's concurrency. HEAD declared none
// and the scan is one pass of set differences.
func orphanBookFilesCleanupParallelism() tasks.Parallelism {
	return tasks.Sequential("HEAD declared no concurrency; the scan is one pass of set differences over the book_file and book lists")
}

// orphanBookFilesCleanupNative is the native kind. It only reports, so it is
// ReadOnly and its Preview and Live runs are the same.
func (p *Plugin) orphanBookFilesCleanupNative(deps t6g6Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := t6g6Common(orphanBookFilesCleanupKind, sched)
	c.Title = "Orphan book_file scan"
	c.Help = orphanBookFilesCleanupHelp
	c.Effects = tasks.ReadOnly()
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityNormal
	c.ConcurrencyKey = tasks.Key("books.maintenance.orphan-book-files-cleanup")
	c.Timeout = 30 * time.Minute
	return tasks.Task(orphanBookFilesCleanupKind, tasks.TaskSpec[OrphanBookFilesCleanupParams, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, params OrphanBookFilesCleanupParams) (struct{}, error) {
			env := orphanBookFilesCleanupEnv{t6g6Env: t6g6NativeEnv(rc)}
			if deps.Store != nil {
				env.Store = deps.Store
			}
			return struct{}{}, orphanBookFilesCleanupCore(rc.Context(), params, env)
		},
	})
}

// orphanBookFilesCleanupCore is the body of runOrphanBookFilesCleanup.
func orphanBookFilesCleanupCore(ctx context.Context, params OrphanBookFilesCleanupParams, env orphanBookFilesCleanupEnv) error {
	if params.Delete {
		return fmt.Errorf("the delete mode of this op was removed: it deleted book_file rows, which must be repointed instead; run maintenance.orphan-book-files-repoint-plan for a dry-run plan")
	}
	if env.Store == nil {
		return fmt.Errorf("database not initialized")
	}
	env.Log(slog.LevelInfo, "Starting orphan book_file scan (report-only)")
	scanProg := t6g6NewProgress(env.t6g6Env, 0)
	scanProg.Start("Scanning book_files for orphan rows...")

	orphans, totalFiles, ownerCount, err := t6g6FindOrphanBookFilesVia(ctx, env.Store)
	if err != nil {
		return fmt.Errorf("scan failed: %w", err)
	}

	env.Log(slog.LevelInfo, "Orphan scan complete",
		slog.Int("orphan_count", len(orphans)),
		slog.Int("total_book_files", totalFiles),
		// live + soft-deleted: every book that could still own a file row.
		slog.Int("owning_books", ownerCount),
	)
	msg := fmt.Sprintf("Orphan book_file scan: %d orphan(s) detected (report-only)", len(orphans))
	env.Log(slog.LevelInfo, msg)
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
	return t6g6FindOrphanBookFilesVia(ctx, t6g6OrphanScanShim{store})
}

// t6g6OrphanScanShim gives an orphanFileScanner the retired-inclusive file list
// the Via scan reads: database.AllBookFilesCoreIncludingRetired over the store,
// which sees through decorators.
type t6g6OrphanScanShim struct{ orphanFileScanner }

func (s t6g6OrphanScanShim) AllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error) {
	return database.AllBookFilesCoreIncludingRetired(s.orphanFileScanner)
}

// t6g6FindOrphanBookFilesVia is findOrphanBookFiles over the reads the shell
// hands the core (see it for what the scan decides and why each read fails
// closed).
func t6g6FindOrphanBookFilesVia(ctx context.Context, store orphanBookFilesCleanupStore) (orphans []database.BookFileCore, totalFiles int, ownerCount int, err error) {
	if ctx.Err() != nil {
		return nil, 0, 0, ctx.Err()
	}
	// Retired rows included: a retired row is keyed under its holder, so one whose
	// holder is gone is an orphan like any other row.
	files, ferr := store.AllBookFilesCoreIncludingRetired()
	if ferr != nil {
		return nil, 0, 0, fmt.Errorf("GetAllBookFilesCore: %w", ferr)
	}
	if ctx.Err() != nil {
		return nil, 0, 0, ctx.Err()
	}
	// limit=0 means "all" — the unbounded form used across this plugin.
	//
	// Complete, not plain GetAllBooksCore. This list is a membership set whose
	// ABSENCES define the orphans, so an undercount is a wrong report: one book
	// row missing from a partially warmed or lossy memdb removes that book from
	// `valid` below and every file row it owns is reported as an orphan. The op
	// is report-only, but its output is what a repoint plan is built from.
	//
	// Fail CLOSED on error, exactly as the soft-deleted union below does: the
	// PebbleStore form recovers a tainted memdb by rescanning Pebble, so an error
	// reaching here means no source could produce a trustworthy set.
	books, berr := store.GetAllBooksCoreComplete(0, 0)
	if berr != nil {
		return nil, 0, 0, fmt.Errorf("GetAllBooksCoreComplete (defines which book_file rows are orphans): %w", berr)
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
	// every book_file whose owner is absent as an orphan, and a restorable
	// book's files must not be reported as one.
	//
	// Until 2026-08-13 this was accidentally correct: the memdb implementation
	// of GetAllBooksCore leaked soft-deleted rows, so they landed in `valid` by
	// way of a bug. Fixing that bug is what made this union load-bearing —
	// without it the very first orphan-cleanup run after the fix would have
	// flagged (back then, deleted) the file rows of every book soft-deleted by
	// the July dedup drain. See TestFindOrphanBookFiles_SoftDeletedBooksKeepTheirFiles.
	softDeleted, serr := store.ListSoftDeletedBooks(0, 0, nil)
	if serr != nil {
		// Fail CLOSED: without the soft-deleted set this scan cannot tell a
		// restorable book's files from real orphans. Refuse rather than
		// over-report.
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
