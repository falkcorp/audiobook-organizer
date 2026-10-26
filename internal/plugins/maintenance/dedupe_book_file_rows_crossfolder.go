// file: internal/plugins/maintenance/dedupe_book_file_rows_crossfolder.go
// version: 1.4.0
// guid: 65b43ad1-649a-4077-856f-593cb93f5cbd
// last-edited: 2026-10-26

package maintenance

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	ulid "github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// This file holds the two explicitly-scoped deletion modes of
// maintenance.dedupe-book-file-rows:
//
//   - cross_folder: a MISSING row is deleted when EXACTLY ONE present row in the
//     same book has the same basename (case-sensitive) and the same size. This is
//     the one owner-approved exception (2026-09-25, repeated 2026-09-26) to
//     "never delete book_file rows as a repair". Zero twins, two or more twins,
//     a size mismatch, a row with no recorded size, or a path under the iTunes
//     tree all SKIP with a reason — this op deletes rows, it does not guess.
//
//   - remove_row_ids: named rows are removed when another present row in the
//     same book carries an identical file hash, or when the caller passes
//     confirmed_duplicate:true. Only the ROW goes; the file on disk is never
//     touched (DeleteBookFilesByIDs is a pure database delete).
//
// Both are preview by default, like the rest of the op: nothing is written
// unless the call carries "apply": true.

// Decision modes and actions written to the decisions report.
const (
	decisionModeCrossFolder = "cross_folder"
	decisionModeRemoveRow   = "remove_row"
	// The op's older deletion shapes, recorded alongside cross_folder so a
	// tracked preview lists every row the apply would delete.
	decisionModeExactDuplicate = "exact_duplicate"
	decisionModeSameFolder     = "same_folder"

	decisionWouldDelete = "would_delete"
	decisionDeleted     = "deleted"
	decisionSkip        = "skip"
	decisionFailed      = "failed"
)

// bookFileRowDecision is one row's outcome in a cross_folder or remove_row_ids
// run. Every row the modes considered gets exactly one, so the preview an owner
// approves an apply from lists each deletion AND each refusal.
type bookFileRowDecision struct {
	Mode      string
	Action    string
	BookID    string
	RowID     string
	Path      string
	Size      int64 // the row's recorded size_bytes; the apply-time twin check compares against it
	TwinRowID string
	TwinPath  string
	Reason    string
}

// line renders the decision for the op's log, one row per line.
func (d bookFileRowDecision) line() string {
	s := fmt.Sprintf("%s %s: book=%s row=%s path=%q", d.Mode, d.Action, d.BookID, d.RowID, d.Path)
	if d.TwinRowID != "" || d.TwinPath != "" {
		s += fmt.Sprintf(" twin=%s twin_path=%q", d.TwinRowID, d.TwinPath)
	}
	if d.Reason != "" {
		s += " reason=" + d.Reason
	}
	return s
}

// crossFolderApplyStat is the stat used by the APPLY-TIME re-check of a planned
// cross-folder deletion (and of a remove_row_ids hash twin). It is a variable
// only so a test can make a file reappear between the plan and the delete;
// production never reassigns it.
var crossFolderApplyStat = os.Stat

// planCrossFolderPurges decides, for each MISSING row in candidates, whether it
// may be deleted as a superseded copy of a present row elsewhere in the book.
//
// candidates are the rows still eligible (the caller has already removed rows
// the same-folder fold claimed); all is every row of the book, used to find
// twins. present maps a path to whether it exists as a regular file.
//
// A twin is a row, other than the candidate, whose file is PRESENT, whose
// basename equals the candidate's exactly (case-sensitive), and whose recorded
// size equals the candidate's. The count is of ROWS, not paths, which is the
// literal owner rule: two rows at one present path count as two twins and the
// candidate is skipped until the exact-duplicate pass has collapsed them.
func planCrossFolderPurges(bookID string, candidates, all []database.BookFile, present map[string]bool) (purges, skips []bookFileRowDecision) {
	sorted := append([]database.BookFile(nil), candidates...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for _, f := range sorted {
		if strings.TrimSpace(f.FilePath) == "" || present[f.FilePath] {
			continue // only a missing row is ever a cross-folder candidate
		}
		d := bookFileRowDecision{
			Mode: decisionModeCrossFolder, BookID: bookID, RowID: f.ID, Path: f.FilePath, Size: f.FileSize,
		}
		skip := func(reason string) {
			d.Action, d.Reason = decisionSkip, reason
			skips = append(skips, d)
		}
		if pathutil.UnderFrozenITunesTree(f.FilePath) {
			skip("row is under the iTunes tree (books/itunes/**), which is never mutated")
			continue
		}
		if f.FileSize <= 0 {
			skip("row has no recorded size, so the same-size gate cannot be applied")
			continue
		}
		base := filepath.Base(f.FilePath)
		var sameBase, twins []database.BookFile
		for _, g := range all {
			if g.ID == f.ID || g.FilePath == f.FilePath || !present[g.FilePath] {
				continue
			}
			if filepath.Base(g.FilePath) != base {
				continue
			}
			sameBase = append(sameBase, g)
			if g.FileSize == f.FileSize {
				twins = append(twins, g)
			}
		}
		switch {
		case len(twins) == 0 && len(sameBase) == 0:
			skip("no present row in the book has the same basename")
			continue
		case len(twins) == 0:
			skip(fmt.Sprintf("%d present row(s) share the basename but none has size %d", len(sameBase), f.FileSize))
			continue
		case len(twins) > 1:
			ids := make([]string, 0, len(twins))
			for _, t := range twins {
				ids = append(ids, t.ID)
			}
			sort.Strings(ids)
			skip(fmt.Sprintf("%d present rows share the basename and size (%s); which one it was is a guess",
				len(twins), strings.Join(ids, ",")))
			continue
		}
		twin := twins[0]
		d.TwinRowID, d.TwinPath = twin.ID, twin.FilePath
		if pathutil.UnderFrozenITunesTree(twin.FilePath) {
			skip("twin is under the iTunes tree (books/itunes/**), which is never mutated")
			continue
		}
		d.Action = decisionWouldDelete
		purges = append(purges, d)
	}
	return purges, skips
}

// verifyCrossFolderPurge is the APPLY-TIME confirmation for a planned
// deletion: the row's path must still be absent and the twin's path must still
// be a regular file of the recorded size. Anything else — including a stat
// error that is not "does not exist" — refuses, because an unreadable path is
// not proof of an absent one.
func verifyCrossFolderPurge(d bookFileRowDecision, stat func(string) (os.FileInfo, error)) (bool, string) {
	if _, err := stat(d.Path); err == nil {
		return false, "apply-time check: the row's file exists again"
	} else if !os.IsNotExist(err) {
		return false, fmt.Sprintf("apply-time check: cannot confirm the row's file is absent: %v", err)
	}
	fi, err := stat(d.TwinPath)
	if err != nil {
		return false, fmt.Sprintf("apply-time check: twin file is not readable: %v", err)
	}
	if !fi.Mode().IsRegular() {
		return false, "apply-time check: twin path is not a regular file"
	}
	if fi.Size() != d.Size {
		return false, fmt.Sprintf("apply-time check: twin size on disk is %d, the row records %d", fi.Size(), d.Size)
	}
	return true, ""
}

// journalAndDeleteBookFiles writes one undo-ledger row per book_file row and
// only then deletes them in one batch.
//
// 🔴 JOURNAL FIRST. The ledger row must exist BEFORE the delete commits: a
// journal written afterwards is lost in exactly the case it exists for — the
// process dying between the two. A journal write that FAILS aborts the whole
// delete; an unreplayable deletion is worse than a row that survives to the
// next run, and every caller is idempotent, so the cost of skipping is one
// more run.
//
// The full row goes into OldValue as JSON, not just its id: an id cannot be
// replayed back into a row, and replay is the only rollback this path has.
//
// DeleteBookFilesByIDs is fail-closed: a call of up to 64 rows (its commit chunk)
// deletes every row or none, and a larger one decides every refusal before its
// first chunk commits, so only a later chunk losing its compare-and-swap rounds
// can leave the call partly done, in which case it returns the rows that are gone
// with the error. Two things follow.
//
//   - A journal row whose row was NOT deleted (rows kept changing under the
//     delete, a row moved to another book since it was judged under bookID, an
//     id no longer resolves, a chunk after the one that failed) describes a
//     deletion that did not happen. It is VOIDED (OperationChange.Voided: no
//     revert, count or preflight sees it), so a re-run, which journals the same
//     rows again under new ids, does not leave two ledger rows for one deletion
//     and the ledger never claims a delete that did not occur. The rows the call
//     returned as deleted keep theirs, whatever the error, and so do all of them
//     when only the fsync failed (ErrBookFileDurabilityUnknown) on a call that
//     fit one chunk: that delete WAS applied.
//   - The rows journaled first are the caller's earlier read. The store
//     re-reads each row at its commit, so a row a writer changed in between is
//     deleted in its changed form; the rows it returns are what was actually
//     deleted, and a journal row whose pre-image differs from them is rewritten
//     (same id) to the bytes that were deleted, so a replay restores those.
func journalAndDeleteBookFiles(store OpsStore, opID, bookID string, rows []database.BookFile) error {
	if len(rows) == 0 {
		return nil
	}
	refs := make([]database.BookFileRef, 0, len(rows))
	journaled := make(map[string]*database.OperationChange, len(rows))
	for i := range rows {
		blob, merr := json.Marshal(rows[i])
		if merr != nil {
			// A row we cannot serialize is a row we cannot replay: do not delete it.
			return errors.Join(fmt.Errorf("serialize row %s for the undo ledger: %w", rows[i].ID, merr), voidDeleteJournal(store, journaled))
		}
		change := &database.OperationChange{
			ID:          ulid.Make().String(),
			OperationID: opID,
			BookID:      bookID,
			ChangeType:  "book_file_delete",
			FieldName:   rows[i].ID,
			OldValue:    string(blob), // the ENTIRE row, so the deletion can be replayed back
			NewValue:    "",
		}
		if jerr := store.CreateOperationChange(change); jerr != nil {
			return errors.Join(fmt.Errorf("journal row %s: %w", rows[i].ID, jerr), voidDeleteJournal(store, journaled))
		}
		journaled[rows[i].ID] = change
		refs = append(refs, database.BookFileRef{BookID: bookID, FileID: rows[i].ID})
	}
	deleted, derr := store.DeleteBookFilesByIDs(refs)
	var refreshErrs []error
	if derr != nil {
		// The rows it returns are deleted (a call of more than one chunk can fail
		// in a later chunk after earlier ones committed); every other row is not,
		// whether the error was a refusal or only an fsync failure that stopped
		// the remaining chunks. Their journal rows describe deletions that did not
		// happen.
		gone := make(map[string]bool, len(deleted))
		for i := range deleted {
			gone[deleted[i].ID] = true
		}
		untouched := make(map[string]*database.OperationChange, len(journaled))
		for id, change := range journaled {
			if !gone[id] {
				untouched[id] = change
			}
		}
		if verr := voidDeleteJournal(store, untouched); verr != nil {
			refreshErrs = append(refreshErrs, verr)
		}
	}
	for i := range deleted {
		change := journaled[deleted[i].ID]
		if change == nil {
			continue
		}
		blob, merr := json.Marshal(deleted[i])
		if merr != nil {
			refreshErrs = append(refreshErrs, fmt.Errorf("serialize deleted row %s: %w", deleted[i].ID, merr))
			continue
		}
		if string(blob) == change.OldValue {
			continue
		}
		fresh := *change
		fresh.OldValue = string(blob)
		if jerr := store.CreateOperationChange(&fresh); jerr != nil {
			refreshErrs = append(refreshErrs, fmt.Errorf("rewrite journal row %s with the deleted bytes: %w", deleted[i].ID, jerr))
		}
	}
	if derr != nil {
		return errors.Join(append([]error{fmt.Errorf("batched delete of %d rows: %w", len(refs), derr)}, refreshErrs...)...)
	}
	return errors.Join(refreshErrs...)
}

// voidDeleteJournal takes back the ledger rows written for a delete that did not
// happen. A void that cannot be written is returned: a live row for a deletion
// that never occurred is worse than a failed op.
func voidDeleteJournal(store OpsStore, journaled map[string]*database.OperationChange) error {
	now := time.Now()
	var errs []error
	for _, change := range journaled {
		v := *change
		v.RevertedAt, v.Voided = &now, true
		if err := store.CreateOperationChange(&v); err != nil {
			errs = append(errs, fmt.Errorf("void journal row %s: %w", v.ID, err))
		}
	}
	return errors.Join(errs...)
}

// decisionsReportPath derives the per-row decisions TSV from the per-book one,
// so both land side by side and an overridden reportPath moves both.
func decisionsReportPath(bookReport string) string {
	return strings.TrimSuffix(bookReport, ".tsv") + "-decisions.tsv"
}

// writeRowDecisionsReport dumps every cross_folder / remove_row decision as a
// TSV, sorted for diffability. Zero rows still writes the header.
func writeRowDecisionsReport(path string, rows []bookFileRowDecision) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o775); err != nil {
			return err
		}
	}
	out := append([]bookFileRowDecision(nil), rows...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].BookID != out[j].BookID {
			return out[i].BookID < out[j].BookID
		}
		return out[i].RowID < out[j].RowID
	})
	clean := strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace
	var b strings.Builder
	b.WriteString("mode\taction\tbook_id\trow_id\tpath\tsize_bytes\ttwin_row_id\ttwin_path\treason\n")
	for _, r := range out {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			r.Mode, r.Action, clean(r.BookID), clean(r.RowID), clean(r.Path), r.Size,
			clean(r.TwinRowID), clean(r.TwinPath), clean(r.Reason))
	}
	return os.WriteFile(path, []byte(b.String()), 0o664)
}

// emitRowDecisions logs every decision as its own line — the preview IS this
// list, so it is never sampled — writes the decisions TSV, and returns a short
// tally for the summary line.
func (p *Plugin) emitRowDecisions(decisions []bookFileRowDecision, path string, reporter sdk.Reporter) string {
	sorted := append([]bookFileRowDecision(nil), decisions...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].BookID != sorted[j].BookID {
			return sorted[i].BookID < sorted[j].BookID
		}
		return sorted[i].RowID < sorted[j].RowID
	})
	tally := map[string]int{}
	for _, d := range sorted {
		tally[d.Action]++
		_ = reporter.Log(slog.LevelInfo, "dedupe-book-file-rows: "+d.line())
	}
	note := fmt.Sprintf("%d would delete, %d deleted, %d skipped, %d failed",
		tally[decisionWouldDelete], tally[decisionDeleted], tally[decisionSkip], tally[decisionFailed])
	if err := writeRowDecisionsReport(path, sorted); err != nil {
		reporter.Logger().Warn("dedupe-book-file-rows: could not write decisions report", "path", path, "err", err)
		return note + ", decisions report: FAILED to write " + path
	}
	return note + ", decisions report: " + path
}

// finishRemoveNamedRows runs the remove_row_ids mode and reports it the same
// way the sweep reports: per-row log lines, a decisions TSV, a summary line.
func (p *Plugin) finishRemoveNamedRows(store OpsStore, params DedupeBookFileRowsParams, cores []database.BookFileCore,
	opID, reportPath string, reporter sdk.Reporter) error {
	res := removeNamedBookFileRows(store, params, cores, opID, reporter.Logger())
	note := p.emitRowDecisions(res.decisions, decisionsReportPath(reportPath), reporter)
	mode := "preview"
	if params.Apply {
		mode = "apply"
	}
	summary := fmt.Sprintf(
		"dedupe-book-file-rows remove_row_ids (%s, confirmed_duplicate=%t): %d named, %d deleted, recomputed %d books, failed %d | %s "+
			"| files on disk are never touched | NOTE: corrected totals may not appear until memdb refreshes (restart)",
		mode, params.ConfirmedDuplicate, len(params.RemoveRowIDs), res.deleted, res.recomputed, res.failed, note)
	_ = reporter.Log(slog.LevelInfo, summary)
	_ = reporter.UpdateProgress(1, 1, summary)
	return nil
}

// removeRowsResult is what the remove_row_ids pass reports back to the op.
type removeRowsResult struct {
	decisions  []bookFileRowDecision
	deleted    int
	failed     int
	recomputed int
}

// removeNamedBookFileRows runs the remove_row_ids mode.
//
// The input is an explicit, operator-typed list of row IDs, not a
// library-scale collection, so the loop is sequential on purpose: the
// concurrency rule targets whole-library sweeps, and a handful of named rows
// gains nothing from a pool but ordering noise in the report.
//
// Per book, in order: gate every named row, refuse the book outright if the
// removal would leave it with no rows, then (apply only) re-stat each hash
// twin, salvage the removed row's fields onto its hash twin and carry its
// fingerprint windows there, journal, delete, and recompute the book's
// aggregates. A confirmed_duplicate removal with no hash twin has no row that
// provably holds the same bytes, so nothing is salvaged or carried from it.
func removeNamedBookFileRows(store OpsStore, params DedupeBookFileRowsParams, cores []database.BookFileCore,
	opID string, log *slog.Logger) removeRowsResult {
	var res removeRowsResult
	bookOf := make(map[string]string, len(cores))
	for i := range cores {
		bookOf[cores[i].ID] = cores[i].BookID
	}
	scope := map[string]bool{}
	for _, id := range params.BookIDs {
		if id = strings.TrimSpace(id); id != "" {
			scope[id] = true
		}
	}

	byBook := map[string][]string{}
	seen := map[string]bool{}
	for _, raw := range params.RemoveRowIDs {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		bookID, ok := bookOf[id]
		if !ok || bookID == "" {
			res.decisions = append(res.decisions, bookFileRowDecision{Mode: decisionModeRemoveRow,
				Action: decisionSkip, RowID: id, Reason: "no such book_file row"})
			continue
		}
		if len(scope) > 0 && !scope[bookID] {
			res.decisions = append(res.decisions, bookFileRowDecision{Mode: decisionModeRemoveRow,
				Action: decisionSkip, BookID: bookID, RowID: id, Reason: "row's book is outside the book_ids scope"})
			continue
		}
		byBook[bookID] = append(byBook[bookID], id)
	}
	books := make([]string, 0, len(byBook))
	for b := range byBook {
		books = append(books, b)
	}
	sort.Strings(books)

	for _, bookID := range books {
		files, err := store.GetBookFiles(bookID)
		if err != nil {
			res.failed++
			for _, id := range byBook[bookID] {
				res.decisions = append(res.decisions, bookFileRowDecision{Mode: decisionModeRemoveRow,
					Action: decisionFailed, BookID: bookID, RowID: id, Reason: "GetBookFiles: " + err.Error()})
			}
			continue
		}
		targets := map[string]bool{}
		for _, id := range byBook[bookID] {
			targets[id] = true
		}
		byID := make(map[string]database.BookFile, len(files))
		for _, f := range files {
			byID[f.ID] = f
		}

		type approved struct {
			d    bookFileRowDecision
			row  database.BookFile
			twin *database.BookFile // hash twin; nil for a confirmed_duplicate-only removal
		}
		var ok []approved
		ids := append([]string(nil), byBook[bookID]...)
		sort.Strings(ids)
		for _, id := range ids {
			f, found := byID[id]
			d := bookFileRowDecision{Mode: decisionModeRemoveRow, BookID: bookID, RowID: id}
			if !found {
				d.Action, d.Reason = decisionSkip, "row is no longer in the book"
				res.decisions = append(res.decisions, d)
				continue
			}
			d.Path, d.Size = f.FilePath, f.FileSize
			if pathutil.UnderFrozenITunesTree(f.FilePath) {
				d.Action, d.Reason = decisionSkip, "row is under the iTunes tree (books/itunes/**), which is never mutated"
				res.decisions = append(res.decisions, d)
				continue
			}
			// Hash twin: another row, NOT itself named for removal (two copies
			// both listed must not remove each other's justification), with an
			// identical non-empty hash AND an identical recorded size, whose file
			// is present. The size check is belt and braces: the current digest
			// (filehash.BookFileHash) already folds the size in, but rows hashed
			// under an older, unrecorded kind may not have.
			var twin *database.BookFile
			if h := strings.TrimSpace(f.FileHash); h != "" {
				cands := make([]database.BookFile, 0, 2)
				for _, g := range files {
					if g.ID == f.ID || targets[g.ID] || strings.TrimSpace(g.FileHash) != h || g.FileSize != f.FileSize {
						continue
					}
					if fi, serr := os.Stat(g.FilePath); serr == nil && fi.Mode().IsRegular() {
						cands = append(cands, g)
					}
				}
				sort.Slice(cands, func(i, j int) bool { return cands[i].ID < cands[j].ID })
				if len(cands) > 0 {
					t := cands[0]
					twin = &t
				}
			}
			switch {
			case twin != nil:
				d.TwinRowID, d.TwinPath = twin.ID, twin.FilePath
				d.Reason = "identical file hash with a present row"
			case params.ConfirmedDuplicate:
				d.Reason = "confirmed_duplicate"
			default:
				d.Action = decisionSkip
				d.Reason = "no other present row has an identical file hash and size; pass confirmed_duplicate:true only if a human has confirmed the duplicate"
				res.decisions = append(res.decisions, d)
				continue
			}
			ok = append(ok, approved{d: d, row: f, twin: twin})
		}
		if len(ok) == 0 {
			continue
		}
		// Never empty a book: a book with no rows has no audio at all, and that
		// is not a duplicate cleanup.
		if len(files)-len(ok) < 1 {
			for _, a := range ok {
				a.d.Action, a.d.Reason = decisionSkip, "removing every named row would leave the book with no rows"
				res.decisions = append(res.decisions, a.d)
			}
			continue
		}
		if !params.Apply {
			for _, a := range ok {
				a.d.Action = decisionWouldDelete
				res.decisions = append(res.decisions, a.d)
			}
			continue
		}

		var rows []database.BookFile
		var pending []bookFileRowDecision
		for _, a := range ok {
			if a.twin != nil {
				fi, serr := crossFolderApplyStat(a.twin.FilePath)
				if serr != nil || !fi.Mode().IsRegular() {
					a.d.Action, a.d.Reason = decisionSkip, "apply-time check: the hash twin's file is no longer present"
					res.decisions = append(res.decisions, a.d)
					continue
				}
				// Same bytes (identical hash), so the removed row's evidence
				// describes the twin too: fill what the twin lacks, and move
				// the fingerprint windows before the delete cascades them.
				if merged, changed := mergeMissingFields(*a.twin, []database.BookFile{a.row}); changed {
					if uerr := store.UpdateBookFile(merged.ID, &merged); uerr != nil {
						res.failed++
						a.d.Action, a.d.Reason = decisionFailed, "could not salvage fields onto the hash twin: "+uerr.Error()
						res.decisions = append(res.decisions, a.d)
						continue
					}
				}
				if _, cerr := store.CarryOverFingerprintWindows(
					[]database.FingerprintWindowRef{database.FileWindowRef(a.row.ID)},
					database.FileWindowRef(a.twin.ID)); cerr != nil {
					res.failed++
					a.d.Action, a.d.Reason = decisionFailed, "could not carry fingerprint windows to the hash twin: "+cerr.Error()
					res.decisions = append(res.decisions, a.d)
					continue
				}
			}
			rows = append(rows, a.row)
			pending = append(pending, a.d)
		}
		if len(rows) == 0 {
			continue
		}
		if derr := journalAndDeleteBookFiles(store, opID, bookID, rows); derr != nil {
			res.failed++
			log.Warn("dedupe-book-file-rows: remove_row_ids delete failed; leaving this book's rows intact",
				"book_id", bookID, "rows", len(rows), "err", derr)
			for _, d := range pending {
				d.Action, d.Reason = decisionFailed, derr.Error()
				res.decisions = append(res.decisions, d)
			}
			continue
		}
		res.deleted += len(rows)
		for _, d := range pending {
			d.Action = decisionDeleted
			res.decisions = append(res.decisions, d)
		}
		if rerr := store.RecomputeBookAggregates(bookID); rerr != nil {
			res.failed++
			log.Warn("dedupe-book-file-rows: RecomputeBookAggregates failed", "book_id", bookID, "err", rerr)
		} else {
			res.recomputed++
		}
	}
	return res
}
