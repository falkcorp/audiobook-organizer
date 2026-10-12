// file: internal/plugins/maintenance/split_books_fixer.go
// version: 1.0.1
// guid: b55e5de5-a785-4658-9cb0-ab1d3f20a781
// last-edited: 2026-10-12

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// splitBooksFixerID is the Repairs-lane id of the split-book fixer. It
// replaces the old Split Books tab of the dedup page (owner decision D21:
// port it as a fixer, then measure it against the fragment fixer's
// "no parent" rows before deciding whether to retire either).
const splitBooksFixerID = "dedup.split-books"

// Skip kinds of a split-book row (the framework adds its own guard kinds).
const (
	// sbSkipGone: a member book is gone or already retired since the
	// detector listed it.
	sbSkipGone = "skipped_member_gone"
	// sbSkipResolved: a re-plan no longer finds the cluster (merged,
	// moved or changed by another writer).
	sbSkipResolved = "resolved"
	// sbSkipError: a member could not be read, so the row cannot be decided.
	sbSkipError = "error"
)

// sbChangeType is the op-journal change type of the two ledger rows an apply
// writes on the keep book: "row:<row id>" (the decision, journaled before the
// merge) and "combine_journal" (the merge's undo journal id, after it). The
// op revert has no case for it, so it is record-only
// (undo.NotRestorableLabel): the merge is undone through its combine journal
// (merge.Service.UndoCombine, POST /merge/undo/:journal_id), not through the
// apply operation's revert.
const sbChangeType = dedup.SplitBookMergeJournalOrigin

// splitBooksFixer merges split-book clusters: one book stored as several
// short books (one per chapter file) in one folder, as
// dedup.DetectSplitBookCandidates finds them, which is the same detector the
// dedup.split-book-scan op persists for the old tab and the
// merge-split-books CLI (both unchanged).
//
// ROWS. One row per detected cluster, Class = the detector's shape (parent,
// grandparent, parent-relaxed). Its members are the cluster's books: the
// suggested keep (the lowest id, the detector's BookIDs[0], the same default
// the old tab and the merge route use) has Role "keep", every other book
// Role "fragment". A cluster with any iTunes book is skipped with the reason
// "iTunes": the predicate is duplicate-copies' (itunesCopyWhy: a book or row
// iTunes id, a row iTunes path, an un-tombstoned itunes external id, an
// iTunes Media folder, books/itunes/**), plus the merge's own apply-time
// refusal (merge.GuardITunesProtected), so the plan never offers a row the
// merge would refuse. A path whose location cannot be settled skips the row
// (fail closed).
//
// APPLY PATH (03-PR9c, the brief's DISCREPANCY). The spec named an op
// "dedup.split-book-merge"; no such op exists (op_ids.golden has only
// dedup.split-book-bulk-merge, whose ConcurrencyKey carries that name). The
// bulk op is not used: it is an enqueued batch with its own dry-run default,
// it deletes candidate-store rows, and it writes nothing to the Repairs
// journal. Apply calls the single-merge core the merge route
// (MergeSplitBookCandidate) and the bulk op both call,
// dedup.MergeSplitBookClusterWithOptions, for exactly one cluster: files are
// MOVED onto the keep (MoveBookFilesToBook repoints book_file rows; none is
// ever deleted), external ids and listening progress follow, the other books
// are soft-deleted, and the keep takes the suggested title unless its title
// is locked. The core writes its own undo journal (combine journal).
//
// The core writes through the store, not through repairs.Writer, so the
// Writer's per-write fence and lease checks do not cover it. Precheck closes
// that: it runs under the merge lock (the core takes merge.LockMergeRMW
// itself; Apply must not hold it, the mutex is not re-entrant), immediately
// before the core reads anything, and beats the Writer (fence, then lease)
// before re-checking every member against the row. A run stopped or a lease
// lost while the core waited for the lock refuses the row with nothing
// written.
//
// JOURNAL. The decision is journaled on the keep (sbChangeType,
// "row:<row id>") BEFORE the merge, voided if the merge refuses, and the
// combine journal id after it, so the Repairs journal records every merge
// and names its undo.
//
// RESUME. A merge cut off part-way (a lost lease, a cancel, a crash) is not
// resumed as such: the re-plan sees the sources it already retired, the
// fingerprint differs, and the row settles changed_since_plan. The combine
// journal names every step already made (it is written before each one), so
// undoing it reverses the partial merge; a fresh plan offers what is left.
//
// Written Evaluate-style for the ops v3 port: evaluate decides ONE cluster
// from reads it makes itself, with no writer; Plan and Replan differ only in
// how many clusters they decide.
type splitBooksFixer struct{ p *Plugin }

func newSplitBooksFixer(p *Plugin) *splitBooksFixer { return &splitBooksFixer{p: p} }

var (
	_ repairs.Fixer       = (*splitBooksFixer)(nil)
	_ repairs.ApplyScoped = (*splitBooksFixer)(nil)
)

func (f *splitBooksFixer) ID() string    { return splitBooksFixerID }
func (f *splitBooksFixer) Title() string { return "Split books" }
func (f *splitBooksFixer) Description() string {
	return "One book stored as several short books, one per chapter file, in one folder (three or more " +
		"numbered files in sequence). Applying a row merges the cluster into its suggested keep book: every " +
		"file row moves onto it (none is deleted), listening progress and external ids follow, the other " +
		"books are retired, and the keep takes the suggested title unless its title is locked. A cluster " +
		"with an iTunes book is skipped. A merge is undone from its merge undo journal (the row's " +
		"\"combine_journal\" entry in the apply operation's changes), not by reverting the apply operation. " +
		"The fragment fixer's \"no parent\" rows can cover the same clusters; run both trials and compare."
}

// sbState is what Replan needs from plan time: which cluster the row is.
type sbState struct {
	Shape   string   `json:"shape"`
	Folder  string   `json:"folder"`
	BookIDs []string `json:"book_ids"`
}

// sbDecision is what Apply writes for one fresh row, carried in Row.Detail.
type sbDecision struct {
	rowID string
	keep  string
	srcs  []string
	title string
	// paths and files are every member's book path and sorted book_file ids
	// as evaluate read them; Precheck refuses the merge when any moved.
	paths map[string]string
	files map[string][]string
}

// sbReader is what evaluate reads per member: the book and its files (the
// merge's iTunes guard reads the same two), its external ids (the iTunes
// predicate) and the keep's field locks (the title proposal).
type sbReader interface {
	merge.ITunesGuardStore
	database.MetadataFieldStateReader
	GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error)
}

// mergeStore returns the store the detector and the merge core run on. The
// core needs the whole dedup.Store; production's OpsStore is the server's
// database.Store, which satisfies it (as merge-chapter-groups asserts). The
// assertion is on the store itself, not through AsCapability, so a decorator
// (the search index's) is never unwrapped past.
func (f *splitBooksFixer) mergeStore() (dedup.Store, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	ds, ok := store.(dedup.Store)
	if !ok {
		return nil, fmt.Errorf("%s: store %T does not implement the split-book merge surface; refusing", splitBooksFixerID, store)
	}
	return ds, nil
}

// sbSnapshot is the detector's answer at one moment, indexed by row id.
// Read-only once built.
type sbSnapshot struct {
	store dedup.Store
	res   *repairs.PathResolver
	cands []dedup.SplitBookCandidate
	byRow map[string]*dedup.SplitBookCandidate
}

// loadSnapshot runs the detector over the whole library. progress, if set,
// is told how many books have been read.
func (f *splitBooksFixer) loadSnapshot(ctx context.Context, progress func(loaded int)) (*sbSnapshot, error) {
	ds, err := f.mergeStore()
	if err != nil {
		return nil, err
	}
	cands, err := dedup.DetectSplitBookCandidates(ctx, ds, progress)
	if err != nil {
		return nil, fmt.Errorf("%s: detect split-book clusters: %w", splitBooksFixerID, err)
	}
	snap := &sbSnapshot{store: ds, res: repairs.NewPathResolver(), cands: cands,
		byRow: make(map[string]*dedup.SplitBookCandidate, len(cands))}
	for i := range cands {
		snap.byRow[sbRowID(cands[i])] = &cands[i]
	}
	return snap, nil
}

// sbRowID is a cluster's stable row id: its shape and folder. The detector
// assigns every book to at most one cluster and emits at most one cluster
// per (shape, folder), so the pair names one cluster; its membership is
// compared through the fingerprint.
func sbRowID(c dedup.SplitBookCandidate) string {
	sum := sha256.Sum256([]byte(c.Shape + "\x00" + c.ParentFolder))
	return "split:" + hex.EncodeToString(sum[:])[:20]
}

// Plan runs the detector once and decides every cluster with evaluate on a
// bounded pool: each evaluate makes a few reads per member book.
func (f *splitBooksFixer) Plan(ctx context.Context, _ json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	snap, err := f.loadSnapshot(ctx, func(loaded int) {
		if rep != nil {
			rep.SetCurrentItem(fmt.Sprintf("Split books: %d books read", loaded))
		}
	})
	if err != nil {
		return nil, err
	}
	rows := make([]repairs.Row, len(snap.cands))
	var done atomic.Int64
	// Each worker writes only rows[i] for its own i and reads the snapshot
	// without writing it. Clusters are book-disjoint (the detector assigns
	// a book to at most one), so no two workers read the same member.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(snap.cands)), func(ictx context.Context, i int) error {
		defer done.Add(1)
		row, err := f.evaluate(ictx, snap.store, snap.res, snap.cands[i])
		if err != nil {
			row = sbErrorRow(snap.cands[i], err)
		}
		rows[i] = row
		return nil
	}, registry.RunItemsOptions{
		Concurrency: titleRepairWorkers(),
		ErrMode:     registry.ErrModeCollect,
		// Label runs inside the workers: it reads only the atomic.
		Label: func(_, total int) string { return fmt.Sprintf("Split-book clusters %d/%d", done.Load(), total) },
	})
	if runErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if rep != nil {
		applicable := 0
		for _, r := range rows {
			if r.Applicable() {
				applicable++
			}
		}
		_ = rep.Log(slog.LevelInfo, fmt.Sprintf("%s: %d split-book clusters; %d rows applicable",
			splitBooksFixerID, len(rows), applicable))
	}
	return rows, nil
}

// Replan re-runs the detector (once per apply run, see BeginApply) and
// decides the planned cluster with the same evaluate. A cluster the detector
// no longer reports comes back as a resolved row (a different fingerprint:
// changed_since_plan).
func (f *splitBooksFixer) Replan(ctx context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	var st sbState
	if err := json.Unmarshal(planned.State, &st); err != nil {
		return repairs.Row{}, fmt.Errorf("%s: row %s: unreadable plan state: %w", splitBooksFixerID, planned.RowID, err)
	}
	snap, err := f.snapshotFor(ctx)
	if err != nil {
		return repairs.Row{}, err
	}
	found := snap.byRow[planned.RowID]
	if found == nil {
		return sbResolvedRow(planned), nil
	}
	row, err := f.evaluate(ctx, snap.store, snap.res, *found)
	if err != nil {
		return repairs.Row{}, err
	}
	if row.RowID != planned.RowID {
		return repairs.Row{}, fmt.Errorf("%s: cluster re-planned as %s, not %s", splitBooksFixerID, row.RowID, planned.RowID)
	}
	return row, nil
}

// sbApplySession is one apply run's state (repairs.ApplyScoped): the
// detector runs ONCE per run, on the first re-plan, instead of once per row
// (each run reads every book). That is safe against the run's own writes
// because rows are book-disjoint: merging one cluster retires only its own
// books, so it changes no other row's membership. Writers outside the run
// are caught by evaluate's fresh per-member reads and, under the merge lock,
// by Precheck.
type sbApplySession struct {
	once sync.Once
	snap *sbSnapshot
	err  error
}

type sbSessionKey struct{}

// BeginApply implements repairs.ApplyScoped.
func (f *splitBooksFixer) BeginApply(ctx context.Context, _ bool) (context.Context, func()) {
	return context.WithValue(ctx, sbSessionKey{}, &sbApplySession{}), func() {}
}

// snapshotFor returns the run's snapshot, or a fresh one outside a run.
func (f *splitBooksFixer) snapshotFor(ctx context.Context) (*sbSnapshot, error) {
	s, _ := ctx.Value(sbSessionKey{}).(*sbApplySession)
	if s == nil {
		return f.loadSnapshot(ctx, nil)
	}
	s.once.Do(func() { s.snap, s.err = f.loadSnapshot(ctx, nil) })
	return s.snap, s.err
}

// sbMember is one member book as evaluate read it.
type sbMember struct {
	book  *database.Book
	rows  []database.BookFile
	files []string
	n     int
	miss  int
}

// evaluate decides one cluster. It reads each member book, its book_file
// rows and its external ids, and the keep's field locks; it never writes. An
// error is returned only when ctx is done or the row's state cannot be
// encoded; a failed read is an error row (skipped).
func (f *splitBooksFixer) evaluate(ctx context.Context, rd sbReader, res *repairs.PathResolver, c dedup.SplitBookCandidate) (repairs.Row, error) {
	if err := ctx.Err(); err != nil {
		return repairs.Row{}, err
	}
	ids := append([]string(nil), c.BookIDs...)
	sort.Strings(ids)
	st, err := json.Marshal(sbState{Shape: c.Shape, Folder: c.ParentFolder, BookIDs: ids})
	if err != nil {
		return repairs.Row{}, fmt.Errorf("encode row state of cluster %s: %w", c.ParentFolder, err)
	}
	r := repairs.Row{
		RowID:   sbRowID(c),
		BookIDs: ids,
		Title:   c.SuggestedTitle,
		Author:  c.SuggestedAuthor,
		Class:   c.Shape,
		Risk:    repairs.RiskReview,
		State:   st,
	}
	keep := c.BookIDs[0]
	var (
		fp      strings.Builder
		gone    []string
		itunes  []string
		doubt   []string
		readErr []string
	)
	fmt.Fprintf(&fp, "%s|%s|keep=%s|title=%s", c.Shape, c.ParentFolder, keep, c.SuggestedTitle)
	members := make(map[string]*sbMember, len(ids))
	for _, id := range c.BookIDs {
		m, err := sbReadMember(rd, id)
		if err != nil {
			readErr = append(readErr, err.Error())
			fmt.Fprintf(&fp, "|%s:unreadable", id)
			continue
		}
		if m.book == nil || m.book.IsSoftDeleted() {
			gone = append(gone, id)
			fmt.Fprintf(&fp, "|%s:gone", id)
			continue
		}
		members[id] = m
		fmt.Fprintf(&fp, "|%s:%s:%s", id, m.book.FilePath, strings.Join(m.files, ","))
		if why, unsure, err := sbITunesWhy(rd, res, m); err != nil {
			readErr = append(readErr, err.Error())
		} else if unsure {
			doubt = append(doubt, id)
		} else if why != "" {
			itunes = append(itunes, fmt.Sprintf("%s (%s)", id, why))
		}
	}
	role := func(id string) string {
		if id == keep {
			return "keep"
		}
		return "fragment"
	}
	files := 0
	for _, id := range c.BookIDs {
		m := members[id]
		rm := repairs.RowMember{BookID: id, Role: role(id)}
		if m != nil {
			rm.Title, rm.Files, rm.MissingFiles = m.book.Title, m.n, m.miss
			files += m.n
		}
		r.Members = append(r.Members, rm)
	}
	r.Evidence = []string{
		fmt.Sprintf("%d books in %s (%s shape)", len(c.BookIDs), c.ParentFolder, c.Shape),
		"chapter numbers " + c.SequentialPattern,
		fmt.Sprintf("suggested keep %s (the lowest id)", keep),
	}
	keepTitle := ""
	if m := members[keep]; m != nil {
		keepTitle = m.book.Title
	}
	r.Current = map[string]string{
		"books": fmt.Sprintf("%d books, %d file rows, in %s", len(c.BookIDs), files, c.ParentFolder),
		"keep":  fmt.Sprintf("%q (%s)", keepTitle, keep),
	}
	proposedTitle := c.SuggestedTitle
	if keepTitle != "" {
		locks, lerr := database.LoadFieldLocks(rd, keep)
		switch {
		case lerr != nil:
			readErr = append(readErr, fmt.Sprintf("field locks of %s: %v", keep, lerr))
		case locks.Locked(database.FieldKeyTitle):
			proposedTitle = keepTitle + " (title locked, kept)"
			fp.WriteString("|title-locked")
		}
	}
	r.Proposed = map[string]string{
		"books": fmt.Sprintf("1 book (%s) holding all %d file rows; the other %d retired", keep, files, len(c.BookIDs)-1),
		"title": proposedTitle,
	}
	r.Reason = fmt.Sprintf("%d books in one folder are numbered chapters of one work (%s). Applying moves every "+
		"file row onto %s and retires the others.", len(c.BookIDs), c.SequentialPattern, keep)

	if len(itunes) == 0 && len(readErr) == 0 && len(doubt) == 0 && len(gone) == 0 {
		// The merge's own refusal, run on the same reads it will make at
		// apply (book path and every file path against the configured iTunes
		// roots), so the plan never offers a row the merge refuses.
		if gerr := merge.GuardITunesProtected(rd, ids); gerr != nil {
			if errors.Is(gerr, merge.ErrITunesProtected) {
				itunes = append(itunes, gerr.Error())
			} else {
				readErr = append(readErr, gerr.Error())
			}
		}
	}
	switch {
	case len(readErr) > 0:
		r.Skipped, r.SkipReason = sbSkipError, strings.Join(readErr, "; ")
		r.Reason = "A member could not be read: " + r.SkipReason
	case len(gone) > 0:
		r.Skipped, r.SkipReason = sbSkipGone, "member gone"
		r.Reason = fmt.Sprintf("Book(s) %s are gone or already retired since the detector listed them.", strings.Join(gone, ", "))
	case len(itunes) > 0:
		r.Skipped, r.SkipReason = repairs.SkipITunes, "iTunes"
		r.Reason = "The cluster has an iTunes book, which is never merged: " + strings.Join(itunes, "; ")
	case len(doubt) > 0:
		r.Skipped, r.SkipReason = repairs.SkipGuardUnreadable, "could not tell whether a file is under books/itunes/**"
		r.Reason = fmt.Sprintf("Could not tell whether book(s) %s are iTunes books (a path could not be resolved).", strings.Join(doubt, ", "))
	}
	if r.Applicable() {
		d := &sbDecision{rowID: r.RowID, keep: keep, title: c.SuggestedTitle,
			paths: map[string]string{}, files: map[string][]string{}}
		for _, id := range c.BookIDs {
			if id != keep {
				d.srcs = append(d.srcs, id)
			}
			d.paths[id] = members[id].book.FilePath
			d.files[id] = members[id].files
		}
		r.Detail = d
	}
	r.Fingerprint = sbFingerprint(r, fp.String())
	return r, nil
}

// sbReadMember reads one member book and its book_file rows.
func sbReadMember(rd sbReader, id string) (*sbMember, error) {
	b, err := rd.GetBookByID(id)
	if err != nil {
		return nil, fmt.Errorf("read book %s: %w", id, err)
	}
	if b == nil {
		return &sbMember{}, nil
	}
	rows, err := rd.GetBookFiles(id)
	if err != nil {
		return nil, fmt.Errorf("files of %s: %w", id, err)
	}
	m := &sbMember{book: b, rows: rows, n: len(rows)}
	for _, f := range rows {
		m.files = append(m.files, f.ID)
		if f.Missing {
			m.miss++
		}
	}
	sort.Strings(m.files)
	return m, nil
}

// sbITunesWhy is duplicate-copies' iTunes predicate (itunesCopyWhy) for one
// member: why it is an iTunes book ("" when it is not), or unsure when a
// path could not be settled.
func sbITunesWhy(rd sbReader, res *repairs.PathResolver, m *sbMember) (why string, unsure bool, err error) {
	id, rows := m.book.ID, m.rows
	exts, err := rd.GetExternalIDsForBook(id)
	if err != nil {
		return "", false, fmt.Errorf("external ids of %s: %w", id, err)
	}
	paths := []string{m.book.FilePath}
	ff := make([]fragFile, 0, len(rows))
	for _, r := range rows {
		paths = append(paths, r.FilePath)
		ff = append(ff, fragFile{ID: r.ID, ITunesPID: r.ITunesPersistentID, ITunesPath: r.ITunesPath})
	}
	pid := ""
	if m.book.ITunesPersistentID != nil {
		pid = *m.book.ITunesPersistentID
	}
	why, unsure = itunesCopyWhy(res, id, pid, paths, ff, exts)
	return why, unsure, nil
}

// sbFingerprint hashes the row's identity, its skip and every decision input
// (extra: shape, folder, keep, suggested title, every member's path and
// book_file ids, the keep's title lock).
func sbFingerprint(r repairs.Row, extra string) string {
	sum := sha256.Sum256([]byte(r.RowID + "\n" + r.Class + "\n" + r.Skipped + "\n" + extra))
	return hex.EncodeToString(sum[:])[:32]
}

// sbErrorRow is a cluster Plan could not decide.
func sbErrorRow(c dedup.SplitBookCandidate, err error) repairs.Row {
	r := repairs.Row{RowID: sbRowID(c), BookIDs: append([]string(nil), c.BookIDs...), Title: c.SuggestedTitle, Class: c.Shape,
		Skipped: sbSkipError, SkipReason: err.Error(), Reason: err.Error(), Risk: repairs.RiskReview}
	sort.Strings(r.BookIDs)
	r.Fingerprint = sbFingerprint(r, "error")
	return r
}

// sbResolvedRow is the re-plan of a cluster the detector no longer reports.
func sbResolvedRow(planned repairs.Row) repairs.Row {
	r := repairs.Row{RowID: planned.RowID, BookIDs: planned.BookIDs, Title: planned.Title, Class: planned.Class,
		Skipped: sbSkipResolved, SkipReason: "no longer a split-book cluster",
		Reason: "The detector no longer reports this cluster: its books were merged, moved or changed.",
		Risk:   planned.Risk, State: planned.State}
	r.Fingerprint = sbFingerprint(r, "resolved")
	return r
}

// sbRecord is the decision journaled on the keep before the merge.
type sbRecord struct {
	RowID   string   `json:"row_id"`
	Keep    string   `json:"keep"`
	Sources []string `json:"sources"`
	Title   string   `json:"title"`
}

// Apply merges one fresh row's cluster through the merge core (see APPLY
// PATH on splitBooksFixer).
func (f *splitBooksFixer) Apply(_ context.Context, w *repairs.Writer, fresh repairs.Row) error {
	d, ok := fresh.Detail.(*sbDecision)
	if !ok || d == nil {
		return fmt.Errorf("%s: row %s carries no decision", splitBooksFixerID, fresh.RowID)
	}
	ds, err := f.mergeStore()
	if err != nil {
		return err
	}
	rec, err := json.Marshal(sbRecord{RowID: d.rowID, Keep: d.keep, Sources: d.srcs, Title: d.title})
	if err != nil {
		return fmt.Errorf("%s: encode the decision of %s: %w", splitBooksFixerID, fresh.RowID, err)
	}
	var res *dedup.SplitBookMergeResult
	// write returns an error only when the core refused before its first
	// write (Precheck, the iTunes guard, an unreadable keep, a journal that
	// could not be written), which is exactly when JournalStep voids the
	// decision row.
	err = w.JournalStep(d.keep, repairs.UndoEntry{ChangeType: sbChangeType, Field: "row:" + d.rowID, New: string(rec)}, func() error {
		r, merr := dedup.MergeSplitBookClusterWithOptions(ds, d.keep, d.srcs, d.title, dedup.SplitMergeOptions{
			Precheck: func() error { return sbPrecheck(w, ds, d) },
		})
		if merr != nil {
			return merr
		}
		res = r
		return nil
	})
	if err != nil {
		if errors.Is(err, merge.ErrITunesProtected) {
			// The plan ran the same guard and found nothing, so a book's
			// files moved under the iTunes library since.
			return fmt.Errorf("%w: %w", repairs.ErrChangedSincePlan, err)
		}
		return err
	}
	if res.JournalID != "" {
		if jerr := w.Journal(d.keep, sbChangeType, "combine_journal", "", res.JournalID); jerr != nil {
			// The merge is made; only the pointer to its undo journal is
			// missing from the op journal. %v, not %w: the row reports
			// partially_applied (settled, visible) rather than a fence or
			// lease outcome that would re-run a finished merge.
			return fmt.Errorf("%w: cluster merged into %s, but its undo journal %s is not recorded in the apply's changes: %v",
				repairs.ErrPartiallyApplied, d.keep, res.JournalID, jerr)
		}
	}
	if len(res.Errors) == 0 && res.MergedSrcCount == len(d.srcs) {
		return nil
	}
	msg := fmt.Sprintf("%s: row %s: %d of %d books merged into %s, %d file rows moved (undo journal %q): %s",
		splitBooksFixerID, fresh.RowID, res.MergedSrcCount, len(d.srcs), d.keep, res.FilesMoved, res.JournalID,
		strings.Join(res.Errors, "; "))
	if res.MergedSrcCount > 0 || res.FilesMoved > 0 {
		return fmt.Errorf("%w: %s", repairs.ErrPartiallyApplied, msg)
	}
	return errors.New(msg)
}

// sbPrecheck runs under the merge lock, before the core reads anything for
// the merge. It beats the Writer (a run stopped, or a lease lost while the
// core waited for the lock, refuses with nothing written) and then requires
// every member to be where the row says: live, at the same path, with the
// same book_file rows. A change is ErrChangedSincePlan; the core wraps it
// with %w, so the engine reports changed_since_plan.
func sbPrecheck(w *repairs.Writer, rd sbReader, d *sbDecision) error {
	if err := w.Beat("split-book merge " + d.rowID); err != nil {
		return err
	}
	for _, id := range append([]string{d.keep}, d.srcs...) {
		m, err := sbReadMember(rd, id)
		if err != nil {
			return err
		}
		switch {
		case m.book == nil:
			return fmt.Errorf("%w: book %s is gone", repairs.ErrChangedSincePlan, id)
		case m.book.IsSoftDeleted():
			return fmt.Errorf("%w: book %s was retired", repairs.ErrChangedSincePlan, id)
		case filepath.Clean(m.book.FilePath) != filepath.Clean(d.paths[id]):
			return fmt.Errorf("%w: book %s moved from %q to %q", repairs.ErrChangedSincePlan, id, d.paths[id], m.book.FilePath)
		case strings.Join(m.files, ",") != strings.Join(d.files[id], ","):
			return fmt.Errorf("%w: book %s file rows changed", repairs.ErrChangedSincePlan, id)
		}
	}
	return nil
}
