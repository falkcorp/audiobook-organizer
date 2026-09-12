// file: internal/plugins/maintenance/dedupe_book_file_rows.go
// version: 1.6.0
// guid: 1c7f4b93-6a05-42e8-9d31-8b0e5a2f7c46
// last-edited: 2026-09-11

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	ulid "github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// DedupeBookFileRowsParams are the JSON parameters for the duplicate book_file
// row cleanup.
type DedupeBookFileRowsParams struct {
	// Apply, when true, deletes the redundant rows. Default false — dry run.
	// Mirrors maintenance.title-repair's convention: a destructive op must be
	// asked for explicitly, never reached by forgetting a flag.
	Apply bool `json:"apply"`
	// Limit caps how many BOOKS are processed (0 = all). Useful for a canary.
	Limit int `json:"limit,omitempty"`
	// ReportPath overrides where the per-book TSV lands. Empty means a derived
	// path under reports/. The report is written on EVERY run, dry or apply,
	// so an operator always has the census the summary line only samples.
	ReportPath string `json:"reportPath,omitempty"`
}

func (p *Plugin) dedupeBookFileRowsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.dedupe-book-file-rows",
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "maintenance",
		DisplayName:     "De-duplicate book_file rows",
		Description:     "Finds books holding MORE THAN ONE book_file row for the same file_path and removes the redundant rows, then recomputes the book's aggregates. Duplicated rows inflate a book's total duration and file size by the duplication factor. Dry-run by default — pass {\"apply\": true} to delete.",
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.dedupe-book-file-rows",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		// The registry watchdog cancels an op that goes ProgressTimeout without an
		// UpdateProgress stamp (default 5m — see
		// internal/operations/registry/watchdog.go). The first full production run
		// was killed at book 19/194 by exactly that:
		//
		//   registry: strike recorded kind=stuck message="no progress for 5m12s"
		//   registry: canceling stuck op
		//
		// Per-book cost is highly variable — a book with 47 duplicate rows does far
		// more work than one with 2 — so a single heavy book could exceed the window
		// on its own. Liveness is now primarily handled by RunItems, which stamps
		// UpdateProgress on every book completion; this override remains as defence
		// in depth for one book pathological enough to outlast even that, and it
		// matches the precedent malformed-m4b-transcode (backfill.go) set for a
		// slow-but-healthy per-item op.
		ProgressTimeout: 30 * time.Minute,
		// DEFENCE IN DEPTH, NOT THE GUARD. Dispatcher Gate 4
		// (internal/operations/registry/dispatcher.go) defers DISPATCH while a
		// named def is RUNNING. It cannot see a QUEUED library.scan, and it
		// cannot be conditional on params.Apply — so it would also hold back a
		// harmless dry run. The real protection is the in-run refusal at the top
		// of runDedupeBookFileRows; this only narrows the window at dispatch.
		DependsOn:    []string{"library.scan"},
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:          p.runDedupeBookFileRows,
	}
}

// dupGroup is one (book, file_path) that has more than one row.
type dupGroup struct {
	bookID   string
	filePath string
	rowIDs   []string // every row for this path, keeper first after ranking
}

// dupeReportRow is one affected book's line in the per-book TSV.
type dupeReportRow struct {
	BookID   string
	Title    string
	Rows     int  // book_file rows with a non-empty path
	Distinct int  // distinct file paths among them
	DupRows  int  // Rows - Distinct: what a full apply would remove
	DupHasFP bool // at least one REDUNDANT row carries an AcoustID fingerprint
}

// rankKeeper orders rows so the BEST-EVIDENCED row sorts first and therefore
// survives.
//
// 🔴 THE ORDER HERE IS DATA-LOSS-CRITICAL. This repo has already shipped bugs
// that wiped AcoustIDFingerprint (see the UpdateBookFile fingerprint-wipe
// incident). Deleting the wrong twin would destroy a fingerprint that took a
// full-file decode to produce, and nothing downstream would report it — the book
// would simply stop matching in dedup.
//
// Preference order, most to least important:
//  1. has an AcoustID fingerprint  — expensive to recompute, impossible to guess
//  2. has a non-zero duration      — the field this whole cleanup exists to fix
//  3. has a file hash              — used by integrity checks
//  4. lexicographically smallest ID — arbitrary but STABLE, so a dry run and the
//     apply that follows it choose the same keeper
func rankKeeper(files []database.BookFile) []database.BookFile {
	out := append([]database.BookFile(nil), files...)
	score := func(f database.BookFile) (int, int, int) {
		fp, dur, hash := 0, 0, 0
		if len(f.AcoustIDFingerprint) > 0 {
			fp = 1
		}
		if f.Duration > 0 {
			dur = 1
		}
		if strings.TrimSpace(f.FileHash) != "" {
			hash = 1
		}
		return fp, dur, hash
	}
	sort.SliceStable(out, func(i, j int) bool {
		fi, di, hi := score(out[i])
		fj, dj, hj := score(out[j])
		if fi != fj {
			return fi > fj
		}
		if di != dj {
			return di > dj
		}
		if hi != hj {
			return hi > hj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// mergeMissingFields fills fields the keeper lacks from the twins about to be
// deleted, and reports whether anything was salvaged.
//
// Strictly additive: a field the keeper already has is never touched, so the
// merge can only ever recover data, never degrade it. The twins are scanned in
// ranked order, so the best-evidenced donor is consulted first.
func mergeMissingFields(keeper database.BookFile, twins []database.BookFile) (database.BookFile, bool) {
	changed := false
	for i := range twins {
		t := twins[i]
		if keeper.Duration <= 0 && t.Duration > 0 {
			keeper.Duration = t.Duration
			changed = true
		}
		if len(keeper.AcoustIDFingerprint) == 0 && len(t.AcoustIDFingerprint) > 0 {
			keeper.AcoustIDFingerprint = t.AcoustIDFingerprint
			changed = true
		}
		if strings.TrimSpace(keeper.FileHash) == "" && strings.TrimSpace(t.FileHash) != "" {
			keeper.FileHash = t.FileHash
			changed = true
		}
		if keeper.FileSize <= 0 && t.FileSize > 0 {
			keeper.FileSize = t.FileSize
			changed = true
		}
		if keeper.AcoustIDFingerprintDurationSec <= 0 && t.AcoustIDFingerprintDurationSec > 0 {
			keeper.AcoustIDFingerprintDurationSec = t.AcoustIDFingerprintDurationSec
			changed = true
		}
	}
	return keeper, changed
}

func (p *Plugin) runDedupeBookFileRows(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params DedupeBookFileRowsParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	log := reporter.Logger()
	log.Info("dedupe-book-file-rows: starting", "apply", params.Apply, "limit", params.Limit)

	// 🔴 FAIL CLOSED. A library.scan rewrites the same book_file rows this op
	// deletes, so a concurrent scan can resurrect a row we just collapsed or
	// mutate the keeper mid-flight. Applies refuse; a dry run is read-only and is
	// allowed through so an operator can still take the census during a scan.
	//
	// This is a POINT-IN-TIME check, not a lock: a scan enqueued one second later
	// is not caught. DependsOn (dedupeBookFileRowsDef) narrows that window at
	// dispatch. The op is not scan-proof; it is scan-aware.
	if params.Apply {
		qs := p.deps.OperationQueueStore()
		if qs == nil {
			return fmt.Errorf("dedupe-book-file-rows: cannot verify no library.scan is active; refusing to apply")
		}
		active, aerr := qs.ListActiveOperationsV2()
		if aerr != nil {
			// Fail CLOSED: an unreadable queue is not proof of an idle queue.
			return fmt.Errorf("dedupe-book-file-rows: cannot list active operations; refusing to apply: %w", aerr)
		}
		for _, op := range active {
			if op.DefID == "library.scan" && (op.Status == "running" || op.Status == "queued") {
				// A ZOMBIE 'running' ROW WITH NO LIVE HANDLE ALSO LANDS HERE, and
				// that is the safe direction — say so, or the next operator reads
				// this as the op being broken instead of the queue being stale.
				return fmt.Errorf(
					"dedupe-book-file-rows: library.scan is %s (op %s); refusing to apply — a running scan "+
						"rewrites the rows this op deletes. If that scan is a stale/zombie row with no live "+
						"handle, clear it before re-running rather than bypassing this check",
					op.Status, op.ID)
			}
		}
	}

	// PASS 1 — cheap. The Core projection is a memdb read, so it is fast enough
	// to sweep the whole library, and file_path is all we need to FIND duplicates.
	//
	// It is deliberately NOT used to DECIDE anything: the Core projection does not
	// carry AcoustIDFingerprint, so choosing a keeper from it would be choosing
	// blind on the one field we must not lose.
	cores, err := store.GetAllBookFilesCore()
	if err != nil {
		return fmt.Errorf("scan book_files: %w", err)
	}
	byBookPath := map[string][]string{}
	for i := range cores {
		c := cores[i]
		if c.BookID == "" || strings.TrimSpace(c.FilePath) == "" {
			continue // orphan / pathless rows belong to orphan-book-files-cleanup
		}
		key := c.BookID + "\x00" + c.FilePath
		byBookPath[key] = append(byBookPath[key], c.ID)
	}

	affected := map[string][]dupGroup{} // bookID -> duplicate groups
	dupRows := 0
	for key, ids := range byBookPath {
		if len(ids) < 2 {
			continue
		}
		parts := strings.SplitN(key, "\x00", 2)
		g := dupGroup{bookID: parts[0], filePath: parts[1], rowIDs: ids}
		affected[g.bookID] = append(affected[g.bookID], g)
		dupRows += len(ids) - 1
	}

	bookIDs := make([]string, 0, len(affected))
	for id := range affected {
		bookIDs = append(bookIDs, id)
	}
	sort.Strings(bookIDs) // deterministic order so runs are comparable
	if params.Limit > 0 && len(bookIDs) > params.Limit {
		bookIDs = bookIDs[:params.Limit]
	}

	log.Info("dedupe-book-file-rows: scan complete",
		"total_rows", len(cores),
		"books_affected", len(affected),
		"redundant_rows", dupRows)

	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"%d book(s) hold duplicate book_file rows (%d redundant rows)", len(affected), dupRows))

	// 🔒 Counters are touched by every worker — see the pool below. A single mutex
	// is right here: contention is negligible against per-book DB work, and the
	// alternative (five atomics plus a locked slice) buys nothing but subtlety.
	var mu sync.Mutex
	var deleted, wouldDelete, failed, recomputed, salvaged int
	var examples []string
	var reportRows []dupeReportRow // appended by every worker: always under mu

	// PASS 2 — per affected book only, so the expensive full-fidelity read is paid
	// for the handful of books that need it rather than the whole library.
	//
	// ⚡ PARALLEL BY BOOK. This was a plain sequential `for range bookIDs`, which
	// CLAUDE.md's concurrency rule forbids for a whole-library loop doing per-item
	// DB work — and the first full production run proved why: ~1.7 minutes per book
	// meant 176 books could not finish inside the op's own 2-hour timeout.
	//
	// 🔑 SAFE BECAUSE THE PARTITION IS DISJOINT. Every unit of work is one bookID,
	// and a book_file row belongs to exactly one book, so two workers can never
	// touch the same row, the same keeper decision, or the same
	// RecomputeBookAggregates target. This is the partition-into-disjoint-sets case
	// the concurrency rule calls for, NOT a naive fan-out over shared state.
	//
	// RunItems also fixes the liveness problem properly: it stamps UpdateProgress as
	// each book COMPLETES, via a monotonic atomic counter that stays ordered even
	// when books finish out of order. The stuck-op watchdog therefore sees progress
	// on every completion rather than once per sequential step.
	workers := min(runtime.NumCPU(), len(bookIDs))
	log.Info("dedupe-book-file-rows: processing books in parallel",
		"books", len(bookIDs), "workers", workers)

	// Captured ONCE, outside the workers: every journal row this run writes is
	// tagged with the same operation id so GetOperationChanges(opID) returns the
	// whole deletion set. ReporterOpID degrades to "" for a reporter that cannot
	// say (a fake, by design) — that is a WARN, never a refusal: the rows still
	// carry the full BookFile in OldValue and stay reachable via GetBookChanges.
	opID := registry.ReporterOpID(reporter)
	if opID == "" {
		log.Warn("dedupe-book-file-rows: no operation id; journal rows will be uncorrelated")
	}

	runErr := registry.RunItems(ctx, reporter, bookIDs, func(ctx context.Context, bookID string) error {
		// GetBookFiles reads Pebble directly (raw prefix iteration), NOT memdb, so
		// AcoustIDFingerprint is present and un-stripped here. That is exactly why
		// the keeper decision happens in this pass and not the previous one.
		files, ferr := store.GetBookFiles(bookID)
		if ferr != nil {
			mu.Lock()
			failed++
			mu.Unlock()
			log.Warn("dedupe-book-file-rows: GetBookFiles failed", "book_id", bookID, "err", ferr)
			// nil, not the error: one unreadable book must not abort the sweep.
			return nil
		}
		byPath := map[string][]database.BookFile{}
		for fi := range files {
			f := files[fi]
			if strings.TrimSpace(f.FilePath) == "" {
				continue
			}
			byPath[f.FilePath] = append(byPath[f.FilePath], f)
		}
		totalRows := 0
		for _, rs := range byPath {
			totalRows += len(rs)
		}
		distinct := len(byPath)
		dupHasFP := false

		changedThisBook := false
		// Redundant row IDs accumulate here and are deleted in ONE batch call after
		// the group loop, instead of one DeleteBookFile per row.
		//
		// 🔴 THE SALVAGE WRITE BELOW MUST NOT JOIN THIS BATCH. Rescued keeper fields
		// have to be COMMITTED BEFORE their donor rows are deleted, and the whole
		// point of the "if the salvage write fails, skip this group" escape is that
		// the donors are still there to try again from. Folding the salvage into an
		// atomic delete batch silently removes that escape: the group would either
		// commit both or neither, which sounds safer but actually means a keeper
		// whose salvage write failed can no longer be repaired from its twins on the
		// next run, because "neither" is indistinguishable from "nothing to do".
		// Losing a duration is recoverable; losing it while also deleting the only
		// other copy is not. That asymmetry is why the two commits stay separate and
		// ordered. This is the repo's dominant incident class (fingerprint /
		// Author / Series wipes on write-back) — do not "simplify" this.
		//
		// Accumulating across groups keeps that ordering STRONGER, not weaker: every
		// salvage in this book commits before any donor in this book is deleted, and
		// a group whose salvage failed simply never enters the accumulator.
		//
		// pendingRows holds the SAME rows as pendingDeletes, in the same order,
		// because a delete has to be journaled with its whole content and an ID
		// alone cannot be replayed back into a row. The two are appended together
		// in one place below, AFTER the salvage-failure escape — appending
		// anywhere else desynchronises them and would journal a row that was
		// never deleted (or worse, delete one that was never journaled).
		var pendingDeletes []string
		var pendingRows []database.BookFile
		for path, rows := range byPath {
			if len(rows) < 2 {
				continue
			}
			ranked := rankKeeper(rows)
			keeper, redundant := ranked[0], ranked[1:]
			// Read BEFORE mergeMissingFields, which may copy a twin's fingerprint
			// onto the keeper: the report column is about the rows being removed.
			for ri := 0; ri < len(redundant) && !dupHasFP; ri++ {
				dupHasFP = len(redundant[ri].AcoustIDFingerprint) > 0
			}

			// 🔴 MERGE, DON'T JUST PICK. Ranking alone chooses a whole ROW, so a
			// keeper that carries a fingerprint but no duration silently loses the
			// duration held by one of its twins.
			//
			// This was originally written up as an observed loss on "The Trapped Mind
			// Project", which read 0.00h after its 130 rows were collapsed. That was
			// WRONG and is retracted: the book's entire audio is a 13.5-second,
			// 91,958-byte MP3, the surviving row matches the file exactly, and 0.00h
			// is simply what 13 seconds renders as. A later full-library dry run
			// confirmed it — "would salvage fields on 0 keepers" across all 194 books.
			//
			// The guard stays because the hazard is real even though this was not an
			// instance of it. Treat it as defence, not as a fix for a known incident.
			//
			// So salvage every field the keeper is missing before its twins are
			// deleted. Only ever FILLS empty fields — a value the keeper already has
			// always wins, so this can never overwrite good data with worse.
			merged, changed := mergeMissingFields(keeper, redundant)
			keeper = merged

			mu.Lock()
			if len(examples) < 10 {
				examples = append(examples, fmt.Sprintf("%s: %d rows for %q (keeping %s)",
					bookID, len(rows), shortPath(path), keeper.ID))
			}
			mu.Unlock()

			if !params.Apply {
				mu.Lock()
				wouldDelete += len(redundant)
				if changed {
					salvaged++
				}
				mu.Unlock()
				continue
			}

			// Persist the salvaged fields BEFORE deleting the donors. If the write
			// fails we skip this group entirely rather than delete rows whose data
			// was never rescued — losing a duration is recoverable, losing it while
			// also deleting the only copy is not.
			if changed {
				if uerr := store.UpdateBookFile(keeper.ID, &keeper); uerr != nil {
					mu.Lock()
					failed++
					mu.Unlock()
					log.Warn("dedupe-book-file-rows: could not persist salvaged fields; leaving this group intact",
						"book_id", bookID, "keeper", keeper.ID, "err", uerr)
					continue
				}
				mu.Lock()
				salvaged++
				mu.Unlock()
			}

			// Salvage for this group is committed; its donors may now be queued.
			for ri := range redundant {
				pendingDeletes = append(pendingDeletes, redundant[ri].ID)
				pendingRows = append(pendingRows, redundant[ri])
			}
		}

		// Report row for this book, built from the Pebble read above so it
		// describes the same rows the keeper decision saw. Recorded in dry runs and
		// applies alike, before any delete, so an apply's report says what it
		// FOUND, not what survived. A failed title lookup leaves the title empty:
		// the report is diagnostic and must never abort a book.
		if totalRows > distinct {
			title := ""
			if b, berr := store.GetBookByID(bookID); berr == nil && b != nil {
				title = b.Title
			}
			mu.Lock()
			reportRows = append(reportRows, dupeReportRow{
				BookID: bookID, Title: title, Rows: totalRows, Distinct: distinct,
				DupRows: totalRows - distinct, DupHasFP: dupHasFP,
			})
			mu.Unlock()
		}

		// One batched delete for the whole book. DeleteBookFilesByIDs is fail-closed
		// on unresolvable IDs, so a partial delete cannot happen here — either every
		// queued row goes or none do. If none do, the rows survive and the next run
		// of this op re-reads them and collapses them again, so the failure costs a
		// re-run and nothing else.
		if len(pendingDeletes) > 0 {
			// 🔴 JOURNAL FIRST. The ledger row must exist BEFORE the delete
			// commits: a journal written afterwards is lost in exactly the case it
			// exists for — the process dying between the two. A journal write that
			// FAILS aborts this book's delete entirely; an unreplayable deletion is
			// worse than a duplicate row that survives to the next run, and this op
			// is idempotent, so the cost of skipping is one more run.
			//
			// The full row goes into OldValue as JSON, not just its id: an id
			// cannot be replayed back into a row, and replay is the only rollback
			// this path has (`git revert` restores code, never data).
			journalOK := true
			for ri := range pendingRows {
				blob, merr := json.Marshal(pendingRows[ri])
				if merr != nil {
					// A row we cannot serialize is a row we cannot replay. Same
					// verdict as a failed write: do not delete it.
					log.Warn("dedupe-book-file-rows: could not serialize row for the undo ledger; leaving this book's rows intact",
						"book_id", bookID, "row", pendingRows[ri].ID, "err", merr)
					journalOK = false
					break
				}
				if jerr := store.CreateOperationChange(&database.OperationChange{
					ID:          ulid.Make().String(),
					OperationID: opID,
					BookID:      bookID,
					ChangeType:  "book_file_delete",
					FieldName:   pendingRows[ri].ID,
					OldValue:    string(blob), // the ENTIRE row, so the deletion can be replayed back
					NewValue:    "",
				}); jerr != nil {
					log.Warn("dedupe-book-file-rows: journal write failed; leaving this book's rows intact",
						"book_id", bookID, "row", pendingRows[ri].ID, "err", jerr)
					journalOK = false
					break
				}
			}

			if !journalOK {
				// Counted as a failure, but the sweep continues: one book's
				// unwritable ledger must not abandon the other 175.
				mu.Lock()
				failed++
				mu.Unlock()
			} else if derr := store.DeleteBookFilesByIDs(pendingDeletes); derr != nil {
				mu.Lock()
				failed++
				mu.Unlock()
				log.Warn("dedupe-book-file-rows: batched delete failed; leaving this book's rows intact",
					"book_id", bookID, "rows", len(pendingDeletes), "err", derr)
			} else {
				mu.Lock()
				deleted += len(pendingDeletes)
				mu.Unlock()
				changedThisBook = true
			}
		}

		// Totals are a plain sum over the surviving rows, so they are only correct
		// once the redundant rows are gone — recompute AFTER deleting, per book.
		// Safe to do inside a worker: this book's rows belong to no other worker.
		//
		// This LOOKS redundant now that DeleteBookFilesByIDs already notifies once
		// per affected book, and it is nearly free rather than actually redundant:
		// RecomputeBookAggregates early-returns when neither Duration nor FileSize
		// changed, which is exactly the state the batch delete just left the book
		// in. Keep it anyway — it is what feeds the `recomputed` counter this op
		// reports, and it is the only recompute that happens on the paths where the
		// batch delete was skipped. Removing it would silently zero a counter the
		// op's output is read for.
		if params.Apply && changedThisBook {
			if rerr := store.RecomputeBookAggregates(bookID); rerr != nil {
				mu.Lock()
				failed++
				mu.Unlock()
				log.Warn("dedupe-book-file-rows: RecomputeBookAggregates failed",
					"book_id", bookID, "err", rerr)
			} else {
				mu.Lock()
				recomputed++
				mu.Unlock()
			}
		}
		// No explicit progress call: RunItems stamps UpdateProgress as each book
		// completes, using a monotonic counter that stays ordered even when books
		// finish out of order. That is also what keeps the stuck-op watchdog fed.
		return nil
	}, registry.RunItemsOptions{
		Concurrency: workers,
		// ErrModeCollect: a single failing book must not abandon the other 175.
		// Per-book failures are already counted and logged above, and the callback
		// returns nil for them, so this mainly governs cancellation semantics.
		ErrMode: registry.ErrModeCollect,
		Label: func(i, total int) string {
			return fmt.Sprintf("book %d/%d", i+1, total)
		},
	})
	if runErr != nil && ctx.Err() != nil {
		// Cancelled or timed out. Everything already committed stays correct — books
		// are independent and the op is idempotent — so report and let a re-run
		// pick up the remainder rather than pretending the work was lost.
		log.Warn("dedupe-book-file-rows: cancelled", "deleted", deleted, "books", len(bookIDs))
		return ctx.Err()
	}
	if runErr != nil {
		log.Warn("dedupe-book-file-rows: some books failed", "err", runErr)
	}

	// Per-book census. Workers finish out of order, so sort by book ID: the
	// file has to be diffable between a dry run and the apply that follows it.
	reportPath := params.ReportPath
	if reportPath == "" {
		mode := "dryrun"
		if params.Apply {
			mode = "apply"
		}
		reportPath = filepath.Join("reports", "dedupe-book-file-rows-"+mode+".tsv")
	}
	sort.Slice(reportRows, func(i, j int) bool { return reportRows[i].BookID < reportRows[j].BookID })
	// A Limit-capped run covers only the processed subset; say so, or a canary
	// report reads as the whole census. Books whose GetBookFiles failed have no
	// row either, so report rows and books_affected can legitimately differ.
	limited := len(bookIDs) < len(affected)
	reportNote := fmt.Sprintf("report: %s (%d books)", reportPath, len(reportRows))
	if werr := writeDupeRowsReport(reportPath, reportRows); werr != nil {
		log.Warn("dedupe-book-file-rows: could not write report", "path", reportPath, "err", werr, "rows", len(reportRows))
		reportNote = "report: FAILED to write " + reportPath
	} else {
		log.Info("dedupe-book-file-rows: wrote report", "path", reportPath, "rows", len(reportRows),
			"books_processed", len(bookIDs), "books_affected", len(affected), "limited", limited)
	}
	if limited {
		reportNote += fmt.Sprintf(", limit %d of %d affected books", len(bookIDs), len(affected))
	}

	verb := fmt.Sprintf("would delete %d (would salvage fields on %d keepers)", wouldDelete, salvaged)
	if params.Apply {
		verb = fmt.Sprintf("deleted %d (salvaged fields on %d keepers, recomputed %d books)",
			deleted, salvaged, recomputed)
	}
	// ⚠️ Operational note, learned the hard way on the first canary: the corrected
	// totals are NOT visible until memdb catches up. Immediately after an apply the
	// list projection still reported the pre-delete duration and file count, and a
	// service restart was what surfaced the (already correct) values. Say so, or
	// the next operator concludes the op did nothing.
	summary := fmt.Sprintf(
		"dedupe-book-file-rows: %d rows scanned, %d books affected, %d redundant rows, %s, failed %d "+
			"| %s | NOTE: corrected totals may not appear until memdb refreshes (restart) | e.g. %s",
		len(cores), len(affected), dupRows, verb, failed, reportNote, strings.Join(examples, "; "))
	_ = reporter.Log(slog.LevelInfo, summary)
	_ = reporter.UpdateProgress(len(bookIDs), len(bookIDs), summary)
	return nil
}

// writeDupeRowsReport dumps EVERY affected book and its duplication shape as a
// TSV. Written on dry runs as well as applies: the dry run is the artifact an
// owner approves an apply from, and a log line capped at ten examples is not
// that. Zero rows still writes the header, so "ran and found nothing" is
// distinguishable from "did not run". Shaped after writeReapReport.
func writeDupeRowsReport(path string, rows []dupeReportRow) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o775); err != nil {
			return err
		}
	}
	// Titles are user data; a tab or newline would silently shift columns.
	clean := strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace
	var b strings.Builder
	b.WriteString("book_id\ttitle\trows\tdistinct\tdup_rows\thas_fingerprint_on_dupe\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "%s\t%s\t%d\t%d\t%d\t%t\n",
			clean(r.BookID), clean(r.Title), r.Rows, r.Distinct, r.DupRows, r.DupHasFP)
	}
	return os.WriteFile(path, []byte(b.String()), 0o664)
}

// shortPath trims a long path to its last two segments for log readability.
func shortPath(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) <= 2 {
		return p
	}
	return ".../" + strings.Join(parts[len(parts)-2:], "/")
}
