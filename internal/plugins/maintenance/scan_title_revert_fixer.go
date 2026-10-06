// file: internal/plugins/maintenance/scan_title_revert_fixer.go
// version: 1.0.0
// guid: beeafd36-1bf8-48f8-b62c-bd5e1be4135c
// last-edited: 2026-10-06

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// scanTitleRevertFixerID is the Repairs-lane id of the scan title revert.
const scanTitleRevertFixerID = "maintenance.scan-title-revert"

// Skip kinds of a scan-title-revert row. A held row is listed with its reason
// and never written.
const (
	strSkipGone          = "gone"
	strSkipNoSnapshot    = "no_snapshot"
	strSkipSame          = "same"
	strSkipOldTitleEmpty = "old_title_empty"
	strSkipChangedSince  = "changed_since_scan"
	strSkipLocked        = "locked"
	strSkipITunes        = "itunes"
)

// scanTitleRevertFixer puts back the titles one library scan rewrote.
//
// The nightly library.scan of 2026-10-05 rewrote existing books' titles from
// their file tags and names before #3799 stopped a rescan from touching an
// existing book's identity. Its change journal recorded none of those writes,
// so the scan op's own revert cannot undo them. Every book write, though,
// leaves a CoW snapshot: book_ver:<id>:<unixnano> holds the row as it was
// BEFORE the write it is stamped with (PebbleStore.UpdateBook marshals the old
// row under time.Now()). So the row as it stood when the scan started is the
// EARLIEST snapshot stamped at or after the scan's start.
//
// The fixer is input-driven, not a library sweep: its params name the books
// (book_ids, the owner's approved list) and the scan's start (since,
// RFC3339); source_op_id is recorded on each row for provenance only. One row
// per listed book proposes the snapshot's title over the current one. It is
// held when:
//   - gone: the book is deleted or merged away;
//   - itunes: the book carries an iTunes persistent id (on the book, on a
//     book_file row or as a live iTunes external id); the iTunes library is
//     hands-off, as in the folder-books and fragment fixers;
//   - locked: the title carries a user override (or a repair's lock);
//   - no_snapshot: no snapshot was stamped at or after since;
//   - old_title_empty: the snapshot's title is blank;
//   - same: the snapshot's title is the current title;
//   - changed_since_scan: the current title is not the one the scan wrote,
//     i.e. someone edited it since. The scan's value is the first title after
//     the found snapshot that differs from it (strScanWrote), or the live
//     title when none does;
//   - skipped_owner_manual: the title to restore marks Doctor Who / Big
//     Finish / Torchwood (the framework guard reads only the current title).
//
// The framework guards (books/itunes/** paths, Doctor Who / Big Finish /
// Torchwood) run over every row as for every fixer.
//
// Apply writes the title only, through the Writer's ModifyBook (a
// compare-and-set against the planned current title and a fresh lock check,
// inside the write). Writer.Modify records the metadata-history row after the
// write, and the title is then journaled under the apply op, so both "undo
// last apply" and the op revert restore the scan's title. The title is NOT
// locked: #3799 keeps a rescan off an existing book's title, and a lock would
// also stop the owner's later metadata apply from setting it. After the run,
// one FORCED metadata candidate fetch is enqueued for the changed books
// (AfterApply): the title is a search input and the scan's write dropped
// their candidates. Fetch only; nothing is applied.
//
// CONCURRENCY: Plan reads the listed books on a bounded RunItems pool. Apply
// rows are one book each (Row.BookIDs is the row's own id), so the engine's
// partitioning puts every row in its own partition and its bounded pool
// never has two workers writing one book.
type scanTitleRevertFixer struct{ p *Plugin }

func newScanTitleRevertFixer(p *Plugin) *scanTitleRevertFixer { return &scanTitleRevertFixer{p: p} }

var (
	_ repairs.Fixer        = (*scanTitleRevertFixer)(nil)
	_ repairs.AfterApplier = (*scanTitleRevertFixer)(nil)
)

func (f *scanTitleRevertFixer) ID() string    { return scanTitleRevertFixerID }
func (f *scanTitleRevertFixer) Title() string { return "Revert titles a scan rewrote" }
func (f *scanTitleRevertFixer) Description() string {
	return "Puts back the titles a library scan rewrote, for an explicit list of books. Params: book_ids (the books), " +
		"since (RFC3339, when the scan started) and optionally source_op_id (the scan op, recorded on each row). " +
		"The title restored is the one in the book's earliest version snapshot taken at or after since: the row as " +
		"it stood when the scan started. Held: deleted or merged books, iTunes books, locked titles, books with no " +
		"snapshot since then, blank or unchanged titles, and titles edited after the scan. Writes the title only " +
		"(undoable with the apply operation's revert or \"undo last apply\"), then starts a forced metadata " +
		"candidate fetch for the changed books; nothing is applied."
}

// strParams are the fixer's params.
type strParams struct {
	BookIDs    []string `json:"book_ids"`
	Since      string   `json:"since"`
	SourceOpID string   `json:"source_op_id,omitempty"`

	ids   []string
	since time.Time
}

// parseSTRParams reads and checks the params: at least one book id and a
// since that parses as RFC3339.
func parseSTRParams(raw json.RawMessage) (strParams, error) {
	var p strParams
	if len(raw) == 0 || string(raw) == "null" {
		return p, fmt.Errorf("%s: params are required: book_ids and since", scanTitleRevertFixerID)
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, fmt.Errorf("%s: parse params: %w", scanTitleRevertFixerID, err)
	}
	seen := map[string]bool{}
	for _, id := range p.BookIDs {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			p.ids = append(p.ids, id)
		}
	}
	sort.Strings(p.ids)
	if len(p.ids) == 0 {
		return p, fmt.Errorf("%s: book_ids is required (the books whose titles are reverted; never the whole library)", scanTitleRevertFixerID)
	}
	since, err := time.Parse(time.RFC3339, strings.TrimSpace(p.Since))
	if err != nil {
		return p, fmt.Errorf("%s: since must be an RFC3339 time (the scan's start): %w", scanTitleRevertFixerID, err)
	}
	p.since = since
	p.SourceOpID = strings.TrimSpace(p.SourceOpID)
	return p, nil
}

// Plan builds one row per listed book.
func (f *scanTitleRevertFixer) Plan(ctx context.Context, raw json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	if f.p.deps == nil || f.p.deps.OpsStore() == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	params, err := parseSTRParams(raw)
	if err != nil {
		return nil, err
	}
	rows := make([]repairs.Row, len(params.ids))
	var done atomic.Int64
	// Each worker writes only rows[i] for its own i; the ids are unique.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(params.ids)), func(_ context.Context, i int) error {
		defer done.Add(1)
		rows[i] = f.rowOrError(params.ids[i], params)
		return nil
	}, registry.RunItemsOptions{
		Concurrency: titleRepairWorkers(),
		ErrMode:     registry.ErrModeCollect,
		// Label runs inside the workers: it reads only the atomic.
		Label: func(_, total int) string { return fmt.Sprintf("Scan title revert %d/%d", done.Load(), total) },
	})
	if runErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, runErr
	}
	return rows, nil
}

// rowOrError is evaluate, with a read failure listed as an "error" row.
func (f *scanTitleRevertFixer) rowOrError(id string, params strParams) repairs.Row {
	r, err := f.evaluate(id, params)
	if err != nil {
		r = repairs.Row{RowID: id, BookIDs: []string{id}, Skipped: "error", SkipReason: err.Error(),
			Reason: err.Error(), Risk: repairs.RiskReview}
		r.Fingerprint = strFingerprint(r, "error")
	}
	return r
}

// Replan re-reads the book and its snapshots with the plan's own params.
func (f *scanTitleRevertFixer) Replan(_ context.Context, raw json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	if f.p.deps == nil || f.p.deps.OpsStore() == nil {
		return repairs.Row{}, fmt.Errorf("database not initialized")
	}
	params, err := parseSTRParams(raw)
	if err != nil {
		return repairs.Row{}, err
	}
	return f.evaluate(planned.RowID, params)
}

// strDecision is what Apply writes for one row: the title it expects to find
// and the one it restores.
type strDecision struct {
	bookID, current, restore string
}

// strSnapshotsSince finds, among a book's snapshots, the earliest stamped at
// or after since (the row when the scan started) and every snapshot after it,
// oldest first (the rows after each later write). GetBookSnapshots is asked
// for every snapshot (limit 0) and the result is re-sorted here, so neither
// its order nor a limit can drop the one wanted.
func strSnapshotsSince(snaps []database.BookSnapshot, since time.Time) (found *database.BookSnapshot, later []database.BookSnapshot) {
	sorted := slices.Clone(snaps)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Timestamp.Before(sorted[j].Timestamp) })
	for i := range sorted {
		if sorted[i].Timestamp.Before(since) {
			continue
		}
		return &sorted[i], sorted[i+1:]
	}
	return nil, nil
}

// strScanWrote is the title the scan wrote over restore: the title of the
// first snapshot after the found one whose title differs from restore (each
// later snapshot holds the row before a later write, so that is the row the
// first title-changing write left), or the live title when no later snapshot
// differs (that write was the book's last title change). A write of another
// field first -- the scan's own, an AI re-save, any op -- leaves restore in
// the next snapshot and is passed over. at names where it was read.
func strScanWrote(later []database.BookSnapshot, restore, live string) (title, at string, err error) {
	for i := range later {
		t, terr := strSnapshotTitle(&later[i])
		if terr != nil {
			return "", "", terr
		}
		if t != restore {
			return t, "snapshot " + later[i].Timestamp.UTC().Format(time.RFC3339Nano), nil
		}
	}
	return live, "the live row", nil
}

// strSnapshotTitle decodes a snapshot's book row and returns its title.
func strSnapshotTitle(s *database.BookSnapshot) (string, error) {
	var b database.Book
	if err := json.Unmarshal(s.Data, &b); err != nil {
		return "", fmt.Errorf("decode snapshot %s@%d: %w", s.BookID, s.Timestamp.UnixNano(), err)
	}
	return b.Title, nil
}

// strITunesWhy says why the book belongs to the iTunes library, or "".
func strITunesWhy(store OpsStore, b *database.Book) (string, error) {
	if b.ITunesPersistentID != nil && strings.TrimSpace(*b.ITunesPersistentID) != "" {
		return "the book carries iTunes id " + *b.ITunesPersistentID, nil
	}
	files, err := store.GetBookFiles(b.ID)
	if err != nil {
		return "", fmt.Errorf("read files of %s: %w", b.ID, err)
	}
	for _, bf := range files {
		if strings.TrimSpace(bf.ITunesPersistentID) != "" {
			return fmt.Sprintf("file row %s carries iTunes id %s", bf.ID, bf.ITunesPersistentID), nil
		}
	}
	exts, err := store.GetExternalIDsForBook(b.ID)
	if err != nil {
		return "", fmt.Errorf("read external ids of %s: %w", b.ID, err)
	}
	for _, e := range exts {
		if e.Source == "itunes" && !e.Tombstoned {
			return "the book carries iTunes external id " + e.ExternalID, nil
		}
	}
	return "", nil
}

// strTitleLock reports whether the title is locked, and why in words.
func strTitleLock(store OpsStore, bookID string) (bool, string, error) {
	locked, err := database.LockedUserFields(store, bookID)
	if err != nil {
		return false, "", fmt.Errorf("read field locks of %s: %w", bookID, err)
	}
	if !locked[database.FieldKeyTitle] {
		return false, "", nil
	}
	states, err := store.GetMetadataFieldStates(bookID)
	if err != nil {
		return false, "", fmt.Errorf("read field states of %s: %w", bookID, err)
	}
	_, why := lockHold(fieldStateOf(states, database.FieldKeyTitle), "title")
	return true, why, nil
}

// evaluate builds the row of one listed book. Plan and Replan both call it,
// so a row planned and re-planned from the same state carry the same
// fingerprint.
func (f *scanTitleRevertFixer) evaluate(id string, params strParams) (repairs.Row, error) {
	store := f.p.deps.OpsStore()
	r := repairs.Row{RowID: id, BookIDs: []string{id}, Risk: repairs.RiskLow}
	if params.SourceOpID != "" {
		r.Evidence = append(r.Evidence, "source operation "+params.SourceOpID)
	}
	held := func(kind, why, extra string) (repairs.Row, error) {
		r.Skipped, r.SkipReason, r.Reason = kind, why, why
		r.Fingerprint = strFingerprint(r, extra)
		return r, nil
	}
	b, err := store.GetBookByID(id)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read book %s: %w", id, err)
	}
	if b == nil || b.IsSoftDeleted() || (b.MergedIntoBookID != nil && *b.MergedIntoBookID != "") {
		return held(strSkipGone, "the book no longer exists (deleted or merged away)", "gone")
	}
	r.Title = b.Title
	r.Current = map[string]string{"title": b.Title}
	if b.AuthorID != nil {
		a, aerr := store.GetAuthorByID(*b.AuthorID)
		if aerr != nil {
			return repairs.Row{}, fmt.Errorf("read author %d: %w", *b.AuthorID, aerr)
		}
		if a != nil {
			r.Author = a.Name
		}
	}

	itunesWhy, err := strITunesWhy(store, b)
	if err != nil {
		return repairs.Row{}, err
	}
	if itunesWhy != "" {
		return held(strSkipITunes, itunesWhy+"; the iTunes library is hands-off", b.Title)
	}
	locked, lockWhy, err := strTitleLock(store, id)
	if err != nil {
		return repairs.Row{}, err
	}
	if locked {
		return held(strSkipLocked, lockWhy, b.Title)
	}

	// Every snapshot (limit 0): a book written often since the scan must not
	// have the one at the scan's start cut off.
	snaps, err := store.GetBookSnapshots(id, 0)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read snapshots of %s: %w", id, err)
	}
	found, later := strSnapshotsSince(snaps, params.since)
	if found == nil {
		return held(strSkipNoSnapshot, fmt.Sprintf("the book has no version snapshot at or after %s: nothing wrote it since then",
			params.since.UTC().Format(time.RFC3339)), b.Title)
	}
	restore, err := strSnapshotTitle(found)
	if err != nil {
		return repairs.Row{}, err
	}
	scanWrote, afterAt, err := strScanWrote(later, restore, b.Title)
	if err != nil {
		return repairs.Row{}, err
	}
	foundAt := found.Timestamp.UTC().Format(time.RFC3339Nano)
	r.Current["snapshot_at"] = foundAt
	r.Evidence = append(r.Evidence,
		fmt.Sprintf("snapshot %s (the row before the first write at or after %s) has title %q",
			foundAt, params.since.UTC().Format(time.RFC3339), restore),
		fmt.Sprintf("the first write after it to change the title left %q (read from %s)", scanWrote, afterAt))
	// Title values and the found snapshot only: a later write of another
	// field (the six-hourly ASIN backfill) adds snapshots but moves none of
	// these, so it does not refuse the row as changed_since_plan.
	extra := strings.Join([]string{b.Title, foundAt, restore, scanWrote}, "\n")

	switch {
	case strings.TrimSpace(restore) == "":
		return held(strSkipOldTitleEmpty, "the snapshot's title is blank; there is nothing to restore", extra)
	case restore == b.Title:
		return held(strSkipSame, "the title is already the snapshot's", extra)
	case scanWrote != b.Title:
		return held(strSkipChangedSince, fmt.Sprintf("the title was changed after the scan wrote %q; that later edit stands", scanWrote), extra)
	}
	// The framework guard reads the CURRENT title; the one restored is
	// evidence too ("of 12" may become "Doctor Who: ...").
	if kind, why := repairs.GuardBookTitle(id, restore); kind != "" {
		return held(kind, why, extra)
	}
	r.Proposed = map[string]string{"title": restore}
	r.Reason = fmt.Sprintf("the scan rewrote the title %q as %q; it is put back", restore, b.Title)
	r.Detail = &strDecision{bookID: id, current: b.Title, restore: restore}
	r.Fingerprint = strFingerprint(r, extra)
	return r, nil
}

// strFingerprint hashes a row's decision and its inputs.
func strFingerprint(r repairs.Row, extra string) string {
	sum := sha256.Sum256([]byte(r.RowID + "\n" + r.Skipped + "\n" + r.Proposed["title"] + "\n" + extra))
	return hex.EncodeToString(sum[:])[:32]
}

// Apply writes the title, and only the title, while it is still the planned
// current one and no lock or iTunes id has appeared.
//
// As in the author-named-series fixer, the undo rows follow the write they
// describe: the metadata-history row by Writer.Modify itself, the op-journal
// row once Modify has committed. A journal row for a write that never
// happened is the ledger-before-write bug class; a journal failure after the
// write is reported partially_applied, and "undo last apply" still restores
// the book from its history row.
func (f *scanTitleRevertFixer) Apply(_ context.Context, w *repairs.Writer, fresh repairs.Row) error {
	d, ok := fresh.Detail.(*strDecision)
	if !ok || d == nil {
		return fmt.Errorf("%s: row %s carries no decision", scanTitleRevertFixerID, fresh.RowID)
	}
	store := f.p.deps.OpsStore()
	changed, err := w.Modify(d.bookID, func(cur *database.Book) error {
		if cur.Title != d.current {
			return fmt.Errorf("%w: title is now %q", repairs.ErrChangedSincePlan, cur.Title)
		}
		if cur.ITunesPersistentID != nil && strings.TrimSpace(*cur.ITunesPersistentID) != "" {
			return fmt.Errorf("%w: the book now carries an iTunes id", repairs.ErrChangedSincePlan)
		}
		locked, lerr := database.LockedUserFields(store, d.bookID)
		if lerr != nil {
			return fmt.Errorf("read field locks of %s: %w", d.bookID, lerr)
		}
		if locked[database.FieldKeyTitle] {
			return fmt.Errorf("%w: the title is now locked", repairs.ErrChangedSincePlan)
		}
		cur.Title = d.restore
		return nil
	})
	if err != nil {
		return err
	}
	if !slices.Contains(changed, "title") {
		return fmt.Errorf("book %s: the write committed but recorded no title change", d.bookID)
	}
	if jerr := w.Journal(d.bookID, "metadata_update", "title", d.current, d.restore); jerr != nil {
		return fmt.Errorf("%w: book %s title restored but not journaled: %w", repairs.ErrPartiallyApplied, d.bookID, jerr)
	}
	return nil
}

// AfterApply enqueues one FORCED metadata candidate fetch for the books whose
// title was restored. Forced, unlike the author-named-series follow-up: the
// title is a search input, so the providers are asked a new question, and
// the scan's write dropped these books' candidates. The fetch only stores
// candidates for review; nothing is applied.
func (f *scanTitleRevertFixer) AfterApply(ctx context.Context, bookIDs []string) (string, error) {
	if f.p.deps == nil {
		return "", errNoEnqueuer
	}
	opID, err := f.p.deps.EnqueueOp(ctx, metabatch.CandidateFetchDefID, metabatch.FetchOpParams{
		BookIDs: bookIDs, TotalBooks: len(bookIDs), Force: true,
	})
	if err != nil {
		return "", fmt.Errorf("enqueue %s for %d books: %w", metabatch.CandidateFetchDefID, len(bookIDs), err)
	}
	return opID, nil
}
