// file: internal/plugins/maintenance/split_books_fixer_test.go
// version: 1.4.0
// guid: 946919fd-5279-41f2-bb7e-1331ea924e5e
// last-edited: 2026-10-20

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// sbCluster seeds n books in dir, one numbered chapter file each
// ("<stem> 01.mp3" ...), each dur seconds long, and returns their ids
// sorted (the detector's order: the first is the suggested keep).
func sbCluster(t *testing.T, f *fragFixture, dir, stem string, n, dur int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("%s %02d", stem, i)
		rel := filepath.Join(dir, name+".mp3")
		p := f.file(t, rel, 900+i)
		id := f.book(t, rel, name, p, nil)
		f.row(t, rel, id, p, name+".mp3", int64(900+i), dur, 0)
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// The fixer runs the merge core and re-lists folders on the server's store:
// database.Store must satisfy both surfaces mergeStore asserts.
var (
	_ dedup.Store            = database.Store(nil)
	_ database.BookDirLister = database.Store(nil)
)

func sbFixer(f *fragFixture) *splitBooksFixer { return newSplitBooksFixer(f.p) }

func sbPlan(t *testing.T, f *fragFixture, fx repairs.Fixer) *repairs.PlanResult {
	t.Helper()
	res, err := repairs.RunPlan(context.Background(), fx, nil, repairs.PlanDeps{Guard: f.s}, &fakeReporter{})
	require.NoError(t, err)
	return res
}

func sbWriter(f *fragFixture, opID string) *repairs.Writer {
	return repairs.NewWriter(f.s, f.s, splitBooksFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, opID)
}

func sbApply(t *testing.T, f *fragFixture, fx repairs.Fixer, res *repairs.PlanResult, opID string, rowIDs ...string) repairs.RowResult {
	t.Helper()
	out, err := repairs.RunApply(context.Background(), fx, res, "plan-1", rowIDs, false,
		repairs.ApplyDeps{Guard: f.s, Writer: sbWriter(f, opID), OpID: opID}, &fakeReporter{})
	require.NoError(t, err)
	require.Len(t, out.Rows, 1, "%+v", out.Rows)
	return out.Rows[0]
}

// sbRowWith returns the planned row whose books are exactly ids.
func sbRowWith(t *testing.T, res *repairs.PlanResult, ids []string) repairs.Row {
	t.Helper()
	for _, r := range res.Rows {
		got := append([]string(nil), r.BookIDs...)
		sort.Strings(got)
		if slices.Equal(got, ids) {
			return r
		}
	}
	t.Fatalf("no row with books %v", ids)
	return repairs.Row{}
}

// sbLiveJournal lists the op's journal rows that were not voided, as
// "type|field|book".
func sbLiveJournal(t *testing.T, f *fragFixture, opID string) []string {
	t.Helper()
	changes, err := f.s.GetOperationChanges(opID)
	require.NoError(t, err)
	var out []string
	for _, c := range changes {
		if c.RevertedAt == nil {
			out = append(out, c.ChangeType+"|"+strings.SplitN(c.FieldName, ":", 2)[0]+"|"+c.BookID)
		}
	}
	sort.Strings(out)
	return out
}

// sbFileOwners maps every book_file row of ids to the book that holds it.
func sbFileOwners(t *testing.T, f *fragFixture, ids []string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, id := range ids {
		rows, err := f.s.GetBookFiles(id)
		require.NoError(t, err)
		for _, r := range rows {
			out[r.ID] = id
		}
	}
	return out
}

func sbBook(t *testing.T, f *fragFixture, id string) *database.Book {
	t.Helper()
	b, err := f.s.GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, b)
	return b
}

// The fixer's rows are the clusters the old Split Books tab lists:
// ListSplitBookCandidates serves what dedup.split-book-scan saved
// (DetectSplitBookCandidates, then SplitBookStore.SaveAll), so the same
// fixture saved and listed that way must give the same clusters, keep and
// folder as the plan.
func TestSplitBooksFixer_PlanRowsEqualListSplitBookCandidates(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	sbCluster(t, f, "lib/Author A/Book One", "Book One", 3, 300)
	sbCluster(t, f, "lib/Author A/Book Two", "Book Two", 4, 300)
	sbCluster(t, f, "lib/Author B/Book Three", "Book Three", 5, 300)
	// Two numbered books are no cluster.
	sbCluster(t, f, "lib/Author B/Book Four", "Book Four", 2, 300)

	cands, err := dedup.DetectSplitBookCandidates(context.Background(), f.s, nil)
	require.NoError(t, err)
	cs := dedup.NewSplitBookStore(f.s.DB())
	require.NoError(t, cs.SaveAll(cands))
	listed, err := cs.List()
	require.NoError(t, err)
	require.Len(t, listed, 3)

	res := sbPlan(t, f, sbFixer(f))
	require.Len(t, res.Rows, len(listed), "one row per listed candidate")
	for _, c := range listed {
		ids := append([]string(nil), c.BookIDs...)
		sort.Strings(ids)
		r := sbRowWith(t, res, ids)
		assert.True(t, r.Applicable(), "%s: %s", r.RowID, r.SkipReason)
		assert.Equal(t, c.SuggestedTitle, r.Title)
		assert.Equal(t, c.Shape, r.Class)
		require.Len(t, r.Members, len(c.BookIDs))
		for _, m := range r.Members {
			want := "fragment"
			if m.BookID == c.BookIDs[0] {
				want = "keep"
			}
			assert.Equal(t, want, m.Role, "member %s", m.BookID)
			assert.Equal(t, 1, m.Files)
		}
		assert.Contains(t, strings.Join(r.Evidence, "\n"), c.ParentFolder)
	}
}

// A cluster with an iTunes book is a skipped row with the reason "iTunes":
// one with a book iTunes persistent id (which only the fixer's own predicate
// sees), and one whose files are under books/itunes/** (which the
// framework's plan guard sees too; it keeps the fixer's skip). The other
// cluster stays applicable.
func TestSplitBooksFixer_ITunesClusterIsSkipped(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	plain := sbCluster(t, f, "lib/Author A/Book One", "Book One", 3, 300)
	byID := sbCluster(t, f, "lib/Author A/Book Two", "Book Two", 3, 300)
	pid := "ABCDEF0123456789"
	_, err := f.s.ModifyBook(byID[1], func(b *database.Book) error { b.ITunesPersistentID = &pid; return nil })
	require.NoError(t, err)
	byPath := sbCluster(t, f, "books/itunes/Author C/Book Three", "Book Three", 3, 300)

	res := sbPlan(t, f, sbFixer(f))
	for _, tunes := range [][]string{byID, byPath} {
		r := sbRowWith(t, res, tunes)
		assert.False(t, r.Applicable())
		assert.Equal(t, repairs.SkipITunes, r.Skipped)
		assert.Equal(t, "iTunes", r.SkipReason)
		assert.Len(t, r.Members, 3, "a skipped row still lists its books")
	}
	assert.Contains(t, sbRowWith(t, res, byID).Reason, byID[1])
	assert.True(t, sbRowWith(t, res, plain).Applicable())
}

// Replan decides a cluster with the same evaluate as Plan: unchanged, the
// re-planned row is the planned one; a member retired since turns it into a
// row with another fingerprint.
func TestSplitBooksFixer_ReplanUsesTheSameEvaluate(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	ids := sbCluster(t, f, "lib/Author A/Book One", "Book One", 3, 300)
	sbCluster(t, f, "lib/Author A/Book Two", "Book Two", 3, 300)
	fx := sbFixer(f)
	res := sbPlan(t, f, fx)
	require.Len(t, res.Rows, 2)
	for _, planned := range res.Rows {
		fresh, err := fx.Replan(context.Background(), nil, planned, nil)
		require.NoError(t, err)
		assert.Equal(t, planned.Fingerprint, fresh.Fingerprint, planned.RowID)
		assert.Equal(t, planned.BookIDs, fresh.BookIDs)
		assert.Equal(t, planned.Members, fresh.Members)
		assert.Equal(t, planned.Current, fresh.Current)
		assert.Equal(t, planned.Proposed, fresh.Proposed)
		assert.Equal(t, planned.Evidence, fresh.Evidence)
		assert.NotNil(t, fresh.Detail)
	}

	planned := sbRowWith(t, res, ids)
	_, err := f.s.ModifyBook(ids[2], func(b *database.Book) error { v := true; b.MarkedForDeletion = &v; return nil })
	require.NoError(t, err)
	fresh, err := fx.Replan(context.Background(), nil, planned, nil)
	require.NoError(t, err)
	assert.NotEqual(t, planned.Fingerprint, fresh.Fingerprint)
	assert.False(t, fresh.Applicable())
}

// Applying one row merges exactly that cluster into its suggested keep:
// every file row moves onto the keep, the others are retired, the keep
// takes the suggested title, and the op journal records the decision and
// the merge's undo journal. The other cluster is untouched.
func TestSplitBooksFixer_ApplyMergesOneClusterIntoTheKeep(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	ids := sbCluster(t, f, "lib/Author A/Book One", "Book One", 3, 300)
	other := sbCluster(t, f, "lib/Author A/Book Two", "Book Two", 3, 300)
	fx := sbFixer(f)
	res := sbPlan(t, f, fx)
	row := sbRowWith(t, res, ids)
	require.True(t, row.Applicable(), row.SkipReason)
	before := sbFileOwners(t, f, ids)
	require.Len(t, before, 3)

	out := sbApply(t, f, fx, res, "op-apply", row.RowID)
	require.Equal(t, repairs.OutcomeApplied, out.Outcome, out.Error)

	keep := sbBook(t, f, ids[0])
	assert.False(t, keep.IsSoftDeleted())
	assert.Equal(t, "Book One", keep.Title, "the keep takes the suggested title")
	for _, src := range ids[1:] {
		assert.True(t, sbBook(t, f, src).IsSoftDeleted(), "source %s is retired", src)
	}
	for fileID, owner := range sbFileOwners(t, f, ids) {
		assert.Equal(t, ids[0], owner, "file row %s is on the keep", fileID)
	}
	for _, id := range other {
		assert.False(t, sbBook(t, f, id).IsSoftDeleted(), "the other cluster is untouched")
	}
	assert.Equal(t, []string{
		undo.ChangeTypeMergeCombineJournal + "|" + undo.MergeCombineJournalField + "|" + ids[0],
		undo.ChangeTypeRepairPlanRecord + "|row|" + ids[0],
	}, sbLiveJournal(t, f, "op-apply"))

	// Applying the same plan again finds the cluster gone.
	again := sbApply(t, f, fx, res, "op-again", row.RowID)
	assert.Equal(t, repairs.OutcomeChangedSincePlan, again.Outcome, again.Error)
}

// A merge repoints book_file rows; it never deletes one. Every row the
// cluster had before the apply exists after it, with the same id.
func TestSplitBooksFixer_NoBookFileRowIsDeleted(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	ids := sbCluster(t, f, "lib/Author A/Book One", "Book One", 4, 300)
	// A second file on one source, and a missing row on another.
	extra := f.file(t, "lib/Author A/Book One/Book One 02b.mp3", 999)
	f.row(t, "extra", ids[1], extra, "Book One 02b.mp3", 999, 60, 0)
	f.row(t, "missing", ids[2], f.path("lib/Author A/Book One/gone.mp3"), "gone.mp3", 10, 10, 0)
	_, err := f.s.ModifyBookFile(ids[2], f.rowIDs["missing"], func(bf *database.BookFile) error { bf.Missing = true; return nil })
	require.NoError(t, err)

	fx := sbFixer(f)
	res := sbPlan(t, f, fx)
	row := sbRowWith(t, res, ids)
	require.True(t, row.Applicable(), row.SkipReason)
	before := sbFileOwners(t, f, ids)
	require.Len(t, before, 6)

	out := sbApply(t, f, fx, res, "op-rows", row.RowID)
	require.Equal(t, repairs.OutcomeApplied, out.Outcome, out.Error)
	after := sbFileOwners(t, f, ids)
	assert.Len(t, after, len(before), "no book_file row is deleted")
	for fileID := range before {
		assert.Equal(t, ids[0], after[fileID], "row %s survives, on the keep", fileID)
	}
}

// sbBeforeApply runs before() between the engine's re-plan and the fixer's
// Apply: another writer landing in that window.
type sbBeforeApply struct {
	*splitBooksFixer
	before func()
}

func (b sbBeforeApply) Apply(ctx context.Context, w *repairs.Writer, fresh repairs.Row) error {
	b.before()
	return b.splitBooksFixer.Apply(ctx, w, fresh)
}

// Membership that changed after the re-plan is caught under the merge lock
// (Precheck): the row is changed_since_plan, nothing is merged, and the
// journaled decision is voided. Membership that changed before the re-plan
// changes the fingerprint.
func TestSplitBooksFixer_ApplyRefusesChangedMembership(t *testing.T) {
	t.Parallel()
	t.Run("after the re-plan", func(t *testing.T) {
		t.Parallel()
		f := newFragFixture(t)
		ids := sbCluster(t, f, "lib/Author A/Book One", "Book One", 3, 300)
		base := sbFixer(f)
		res := sbPlan(t, f, base)
		row := sbRowWith(t, res, ids)
		fx := sbBeforeApply{splitBooksFixer: base, before: func() {
			_, err := f.s.ModifyBook(ids[2], func(b *database.Book) error { v := true; b.MarkedForDeletion = &v; return nil })
			require.NoError(t, err)
		}}
		out := sbApply(t, f, fx, res, "op-late", row.RowID)
		assert.Equal(t, repairs.OutcomeChangedSincePlan, out.Outcome, out.Error)
		assert.Contains(t, out.Error, ids[2])
		assert.False(t, sbBook(t, f, ids[1]).IsSoftDeleted(), "nothing is merged")
		assert.Len(t, sbFileOwners(t, f, ids[:1]), 1, "the keep gained no file")
		assert.Empty(t, sbLiveJournal(t, f, "op-late"), "the decision row of the refused merge is voided")
	})
	t.Run("before the re-plan", func(t *testing.T) {
		t.Parallel()
		f := newFragFixture(t)
		ids := sbCluster(t, f, "lib/Author A/Book One", "Book One", 3, 300)
		fx := sbFixer(f)
		res := sbPlan(t, f, fx)
		row := sbRowWith(t, res, ids)
		// A fourth chapter joins the folder: the detector's cluster is no
		// longer the planned one.
		p := f.file(t, "lib/Author A/Book One/Book One 04.mp3", 904)
		id := f.book(t, "four", "Book One 04", p, nil)
		f.row(t, "four", id, p, "Book One 04.mp3", 904, 300, 0)
		out := sbApply(t, f, fx, res, "op-early", row.RowID)
		assert.Equal(t, repairs.OutcomeChangedSincePlan, out.Outcome, out.Error)
		for _, src := range ids[1:] {
			assert.False(t, sbBook(t, f, src).IsSoftDeleted())
		}
		assert.Empty(t, sbLiveJournal(t, f, "op-early"))
	})
}

// sbApplyHeld is sbApply with the fixture's scripted scan stand-down, so the
// Writer's beats renew (and can lose) a lease. It returns RunApply's error
// instead of requiring success.
func sbApplyHeld(f *fragFixture, fx repairs.Fixer, res *repairs.PlanResult, opID string, rowIDs ...string) (*repairs.ApplyResult, error) {
	return repairs.RunApply(context.Background(), fx, res, "plan-1", rowIDs, false,
		repairs.ApplyDeps{Guard: f.s, Writer: sbWriter(f, opID), OpID: opID, StandDown: f.scan(), Wait: f.p.standDownWait},
		&fakeReporter{})
}

// The stand-down lease is lost while the row waits for the process-wide
// merge lock (an outside merge holds it). The merge core writes through the
// store, not the Writer, so a beat between the wait and its first mutation
// is what stops it: the row is aborted, no source is retired, no file row
// moves, and the decision row journaled before the wait is voided. Three
// beats stand there now (LockWaiting's after taking the lock, Precheck's,
// the pointer row's journal); this test went red when Precheck's was the
// only one and it was removed.
//
// Not parallel: it holds the process-wide merge lock.
func TestSplitBooksFixer_LeaseLostWhileWaitingForTheMergeLock(t *testing.T) {
	f := newFragFixture(t)
	ids := sbCluster(t, f, "lib/Author A/Book One", "Book One", 3, 300)
	fx := sbFixer(f)
	res := sbPlan(t, f, fx)
	row := sbRowWith(t, res, ids)
	require.True(t, row.Applicable(), row.SkipReason)

	type out struct {
		res *repairs.ApplyResult
		err error
	}
	done := make(chan out, 1)
	// parked closes on the first renewal after the decision row is journaled:
	// LockWaiting's beat before it blocks, the last thing the apply does
	// before it waits on the lock this test holds. (noWait stops LockWaiting's
	// ticker, so no other beat can follow until the lock is released.) lost
	// refuses every renewal from then on: armed only once parked is seen, so
	// the first refusal is the beat after the lock was taken.
	var parkedOnce sync.Once
	parked := make(chan struct{})
	var lost atomic.Bool
	scan := f.scan()
	scan.mu.Lock()
	scan.failWhen = lost.Load
	scan.mu.Unlock()
	scan.afterRenew = func(int) {
		if changes, err := f.s.GetOperationChanges("op-held"); err == nil && len(changes) > 0 {
			parkedOnce.Do(func() { close(parked) })
		}
	}
	merge.LockMergeRMW()
	locked := true
	defer func() {
		if locked {
			merge.UnlockMergeRMW()
		}
	}()
	go func() {
		r, err := sbApplyHeld(f, fx, res, "op-held", row.RowID)
		done <- out{r, err}
	}()
	select {
	case <-parked:
	case <-time.After(time.Minute):
		t.Fatal("the apply never reached the merge lock wait")
	}
	lost.Store(true)
	merge.UnlockMergeRMW()
	locked = false

	o := <-done
	require.ErrorIs(t, o.err, repairs.ErrStandDownLost)
	require.NotNil(t, o.res)
	require.Len(t, o.res.Rows, 1)
	assert.Equal(t, repairs.OutcomeAborted, o.res.Rows[0].Outcome, o.res.Rows[0].Error)
	assert.Contains(t, o.res.Rows[0].Error, "after waiting for the merge lock not written",
		"the stop is LockWaiting's beat after it took the lock")
	for _, src := range ids[1:] {
		assert.False(t, sbBook(t, f, src).IsSoftDeleted(), "source %s is not retired", src)
	}
	for _, id := range ids {
		assert.Len(t, sbFileOwners(t, f, []string{id}), 1, "book %s keeps its file row", id)
	}
	assert.Empty(t, sbLiveJournal(t, f, "op-held"), "the decision row of the stopped merge is voided")
}

// sbAfterFirstReplan runs after() once, right after the first row of an
// apply run is re-planned: the run's detector snapshot is taken by then.
type sbAfterFirstReplan struct {
	*splitBooksFixer
	once  sync.Once
	after func(first repairs.Row)
}

func (b *sbAfterFirstReplan) Replan(ctx context.Context, p json.RawMessage, planned repairs.Row, rep registry.Reporter) (repairs.Row, error) {
	row, err := b.splitBooksFixer.Replan(ctx, p, planned, rep)
	b.once.Do(func() { b.after(planned) })
	return row, err
}

// A book lands in a later row's folder after the run's detector snapshot (the
// first re-plan). The later row's re-plan reads that stale snapshot and its
// planned members are unchanged, so its fingerprint matches; Precheck re-lists
// the folder under the merge lock and refuses the row, so the new book is not
// left behind by a merge of the others. The first row applies.
func TestSplitBooksFixer_BookAddedToALaterRowsFolderIsRefused(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	one := sbCluster(t, f, "lib/Author A/Book One", "Book One", 3, 300)
	two := sbCluster(t, f, "lib/Author A/Book Two", "Book Two", 3, 300)
	base := sbFixer(f)
	res := sbPlan(t, f, base)
	rowOne, rowTwo := sbRowWith(t, res, one), sbRowWith(t, res, two)

	var added string
	var laterIDs []string
	fx := &sbAfterFirstReplan{splitBooksFixer: base, after: func(first repairs.Row) {
		dir, stem := "lib/Author A/Book Two", "Book Two"
		laterIDs = two
		if slices.Equal(first.BookIDs, two) {
			dir, stem, laterIDs = "lib/Author A/Book One", "Book One", one
		}
		rel := filepath.Join(dir, stem+" 04.mp3")
		p := f.file(t, rel, 904)
		added = f.book(t, "added", stem+" 04", p, nil)
		f.row(t, "added", added, p, stem+" 04.mp3", 904, 300, 0)
	}}
	out, err := repairs.RunApply(context.Background(), fx, res, "plan-1", []string{rowOne.RowID, rowTwo.RowID}, false,
		repairs.ApplyDeps{Guard: f.s, Writer: sbWriter(f, "op-two"), OpID: "op-two", Concurrency: 1}, &fakeReporter{})
	require.NoError(t, err)
	require.Len(t, out.Rows, 2)
	outcomes := map[string]repairs.RowResult{}
	for _, r := range out.Rows {
		outcomes[r.RowID] = r
	}
	laterRow, firstRow := rowTwo, rowOne
	if slices.Equal(laterIDs, one) {
		laterRow, firstRow = rowOne, rowTwo
	}
	assert.Equal(t, repairs.OutcomeApplied, outcomes[firstRow.RowID].Outcome, outcomes[firstRow.RowID].Error)
	later := outcomes[laterRow.RowID]
	assert.Equal(t, repairs.OutcomeChangedSincePlan, later.Outcome, later.Error)
	assert.Contains(t, later.Error, added)
	for _, src := range laterIDs[1:] {
		assert.False(t, sbBook(t, f, src).IsSoftDeleted(), "the refused row merges nothing")
	}
	assert.False(t, sbBook(t, f, added).IsSoftDeleted())
	assert.Len(t, sbFileOwners(t, f, []string{added}), 1, "the new book keeps its file")
}

// A member that became an iTunes book after the re-plan (here a book iTunes
// persistent id, which the merge's own path guard does not see) is caught by
// the plan's iTunes predicate, re-run under the merge lock: the row is
// changed_since_plan and nothing is merged or journaled.
func TestSplitBooksFixer_ITunesBookSinceTheReplanIsRefused(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	ids := sbCluster(t, f, "lib/Author A/Book One", "Book One", 3, 300)
	base := sbFixer(f)
	res := sbPlan(t, f, base)
	row := sbRowWith(t, res, ids)
	fx := sbBeforeApply{splitBooksFixer: base, before: func() {
		pid := "ABCDEF0123456789"
		_, err := f.s.ModifyBook(ids[1], func(b *database.Book) error { b.ITunesPersistentID = &pid; return nil })
		require.NoError(t, err)
	}}
	out := sbApply(t, f, fx, res, "op-tunes", row.RowID)
	assert.Equal(t, repairs.OutcomeChangedSincePlan, out.Outcome, out.Error)
	assert.Contains(t, out.Error, "iTunes")
	for _, src := range ids[1:] {
		assert.False(t, sbBook(t, f, src).IsSoftDeleted(), "nothing is merged")
	}
	assert.Empty(t, sbLiveJournal(t, f, "op-tunes"))
}

// Reverting the apply operation undoes the merge through the combine journal
// its pointer row names: the sources are live again with their own file
// rows, and the keep has its old title back. The ledger decision is marked
// with the rest.
func TestSplitBooksFixer_RevertingTheApplyUndoesTheMerge(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	ids := sbCluster(t, f, "lib/Author A/Book One", "Book One", 3, 300)
	fx := sbFixer(f)
	res := sbPlan(t, f, fx)
	row := sbRowWith(t, res, ids)
	before := sbFileOwners(t, f, ids)
	keepTitle := sbBook(t, f, ids[0]).Title
	out := sbApply(t, f, fx, res, "op-undo", row.RowID)
	require.Equal(t, repairs.OutcomeApplied, out.Outcome, out.Error)
	require.True(t, sbBook(t, f, ids[1]).IsSoftDeleted())

	rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-undo")
	require.NoError(t, err)
	assert.Equal(t, 1, rr.Restored)
	for _, src := range ids[1:] {
		assert.False(t, sbBook(t, f, src).IsSoftDeleted(), "source %s is live again", src)
	}
	assert.Equal(t, before, sbFileOwners(t, f, ids), "every file row is back on its own book")
	assert.Equal(t, keepTitle, sbBook(t, f, ids[0]).Title)
	assert.Empty(t, sbLiveJournal(t, f, "op-undo"), "every row of the operation is marked reverted")
}

// setSplitCandidates wires the fixture's SplitBookCandidates.
func (f *fragFixture) setSplitCandidates(t *testing.T, cs SplitBookCandidateRows) {
	t.Helper()
	sd, ok := f.p.deps.(scanDeps)
	require.True(t, ok, "fixture deps are %T, not scanDeps", f.p.deps)
	sd.fakeDeps.splitCands = cs
	f.p.deps = sd
}

// A complete merge deletes the Split Books tab's candidate row with the same
// books (as the bulk merge op does); the other cluster's row stays.
func TestSplitBooksFixer_CompleteMergeDropsItsCandidateRow(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	one := sbCluster(t, f, "lib/Author A/Book One", "Book One", 3, 300)
	two := sbCluster(t, f, "lib/Author A/Book Two", "Book Two", 3, 300)
	cands, err := dedup.DetectSplitBookCandidates(context.Background(), f.s, nil)
	require.NoError(t, err)
	cs := dedup.NewSplitBookStore(f.s.DB())
	require.NoError(t, cs.SaveAll(cands))
	f.setSplitCandidates(t, cs)

	fx := sbFixer(f)
	res := sbPlan(t, f, fx)
	out := sbApply(t, f, fx, res, "op-cands", sbRowWith(t, res, one).RowID)
	require.Equal(t, repairs.OutcomeApplied, out.Outcome, out.Error)

	listed, err := cs.List()
	require.NoError(t, err)
	require.Len(t, listed, 1, "the merged cluster's row is gone")
	got := append([]string(nil), listed[0].BookIDs...)
	sort.Strings(got)
	assert.Equal(t, two, got, "the other cluster's row stays")
}

// The lease is lost between two sources of one cluster: the merge stops
// before the next source (BeforeSource), the source already absorbed stays
// merged, the rest are untouched, and the row is aborted (not settled) with
// both journal rows standing, so reverting the operation undoes the part
// that was made.
func TestSplitBooksFixer_LeaseLostBetweenSourcesStopsTheMerge(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	ids := sbCluster(t, f, "lib/Author A/Book One", "Book One", 4, 300)
	fx := sbFixer(f)
	res := sbPlan(t, f, fx)
	scan := f.scan()
	// The lease is refused at the first beat after the first source was
	// retired: BeforeSource's beat for the second. Named by library state,
	// not by counting renewals, so a beat added to or dropped from the apply
	// elsewhere cannot move the stop to another place unnoticed.
	scan.mu.Lock()
	scan.failWhen = func() bool {
		b, err := f.s.GetBookByID(ids[1])
		return err == nil && b != nil && b.IsSoftDeleted()
	}
	scan.mu.Unlock()
	out, err := sbApplyHeld(f, fx, res, "op-stop", res.Rows[0].RowID)
	require.ErrorIs(t, err, repairs.ErrStandDownLost)
	require.Len(t, out.Rows, 1)
	r := out.Rows[0]
	assert.Equal(t, repairs.OutcomeAborted, r.Outcome, r.Error)
	assert.Contains(t, r.Error, "1 of 3 books merged")
	assert.Contains(t, r.Error, "split-book merge of "+ids[2]+" into "+ids[0]+" not written",
		"the stop is the beat before the second source")
	assert.True(t, sbBook(t, f, ids[1]).IsSoftDeleted(), "the source absorbed before the stop stays merged")
	for _, src := range ids[2:] {
		assert.False(t, sbBook(t, f, src).IsSoftDeleted(), "source %s is not attempted", src)
		assert.Len(t, sbFileOwners(t, f, []string{src}), 1, "source %s keeps its file", src)
	}
	assert.Equal(t, []string{
		undo.ChangeTypeMergeCombineJournal + "|" + undo.MergeCombineJournalField + "|" + ids[0],
		undo.ChangeTypeRepairPlanRecord + "|row|" + ids[0],
	}, sbLiveJournal(t, f, "op-stop"), "the decision and the pointer stand")

	scan.mu.Lock()
	scan.failWhen = nil
	scan.mu.Unlock()
	_, err = audiobooks.NewRevertService(f.s).RevertOperation("op-stop")
	require.NoError(t, err)
	assert.False(t, sbBook(t, f, ids[1]).IsSoftDeleted(), "the revert undoes the partial merge")
}

// TestSplitBooksFixer_D21OverlapWithFragmentNoParent is the measurement owner
// decision D21 asks for, on a synthetic library: of N split-book clusters,
// how many the fragment-consolidation fixer also lists as a "no parent" row
// with exactly the same books. It is a test, not a runtime feature; on prod
// the same comparison is made by running both trials in the Repairs lane and
// comparing the two book-id sets.
func TestSplitBooksFixer_D21OverlapWithFragmentNoParent(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	// Short chapters with an organized keep: the fragment fixer's own
	// no-parent shape.
	a := sbCluster(t, f, "lib/Author A/Book One", "Book One", 3, 300)
	f.organized(t, a[0])
	// The same shape with nothing organized.
	sbCluster(t, f, "lib/Author A/Book Two", "Book Two", 4, 300)
	// Hour-long parts: whole books to the fragment fixer.
	sbCluster(t, f, "lib/Author B/Book Three", "Book Three", 3, 3*3600)
	// One chapter per numbered subfolder (the grandparent shape).
	for i := 1; i <= 3; i++ {
		rel := fmt.Sprintf("lib/Author B/Book Four/%d/Book Four.mp3", i)
		p := f.file(t, rel, 950+i)
		id := f.book(t, rel, "Book Four", p, nil)
		f.row(t, rel, id, p, "Book Four.mp3", int64(950+i), 300, 0)
	}

	split := sbPlan(t, f, sbFixer(f))
	frag := f.plan(t, "plan-frag")
	noParent := map[string]repairs.Row{}
	for _, r := range frag.Rows {
		if r.Class != fragClassNoParent {
			continue
		}
		ids := append([]string(nil), r.BookIDs...)
		sort.Strings(ids)
		noParent[strings.Join(ids, ",")] = r
	}
	var both, bothApplicable []string
	for _, r := range split.Rows {
		ids := append([]string(nil), r.BookIDs...)
		sort.Strings(ids)
		if fr, ok := noParent[strings.Join(ids, ",")]; ok {
			t.Logf("D21: %q is also a fragment no-parent row (skipped=%q: %s)", r.Title, fr.Skipped, fr.SkipReason)
			both = append(both, r.Title)
			if fr.Applicable() {
				bothApplicable = append(bothApplicable, r.Title)
			}
		}
	}
	sort.Strings(both)
	sort.Strings(bothApplicable)
	t.Logf("D21: %d split-book clusters; %d also fragment no-parent rows (%v), %d of them applicable there (%v)",
		len(split.Rows), len(both), both, len(bothApplicable), bothApplicable)
	// Measured: the grandparent shape (one chapter per numbered subfolder)
	// is the split fixer's alone. The hour-long parts are a fragment
	// no-parent row too, skipped there by its duration gate; the split fixer
	// now holds them with the same gate (held_duration_gate, the same
	// repair_chapter_max_min limit), so the two fixers agree on which of the
	// overlapping rows are applicable.
	assert.Len(t, split.Rows, 4)
	assert.Equal(t, []string{"Book One", "Book Three", "Book Two"}, both)
	assert.Equal(t, []string{"Book One", "Book Two"}, bothApplicable)
	var splitApplicable []string
	for _, r := range split.Rows {
		if r.Applicable() {
			splitApplicable = append(splitApplicable, r.Title)
		}
	}
	sort.Strings(splitApplicable)
	assert.Equal(t, []string{"Book Four", "Book One", "Book Two"}, splitApplicable,
		"the split fixer applies the same rows the fragment fixer does, plus the grandparent shape that is its own")
	for _, r := range split.Rows {
		if r.Title == "Book Three" {
			assert.Equal(t, sbHeldDurationGate, r.Skipped, r.SkipReason)
		}
	}
}

// sbSeedNoFiles seeds n books in dir, one numbered file each on disk but NO
// book_file rows (the detector groups by the stored book path, which can be
// stale), and returns their ids sorted.
func sbSeedNoFiles(t *testing.T, f *fragFixture, dir, stem string, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("%s %02d", stem, i)
		rel := filepath.Join(dir, name+".mp3")
		p := f.file(t, rel, 800+i)
		ids = append(ids, f.book(t, rel, name, p, nil))
	}
	sort.Strings(ids)
	return ids
}

// A cluster whose books have no book_file rows is listed but never
// applicable: skipped_no_files.
func TestSplitBooksFixer_ZeroFileClusterIsSkipped(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	ghost := sbSeedNoFiles(t, f, "lib/Author A/Ghost Book", "Ghost Book", 3)
	ok := sbCluster(t, f, "lib/Author A/Book One", "Book One", 3, 300)

	res := sbPlan(t, f, sbFixer(f))
	r := sbRowWith(t, res, ghost)
	assert.False(t, r.Applicable())
	assert.Equal(t, sbSkipNoFiles, r.Skipped)
	assert.Contains(t, r.SkipReason, "3 of 3 books have no file rows")
	assert.Len(t, r.Members, 3, "a skipped row still lists its books")
	assert.True(t, sbRowWith(t, res, ok).Applicable())
}

// A file at or over repair_chapter_max_min is a whole book: held_duration_gate
// (the fragment fixer's gate and limit).
func TestSplitBooksFixer_LongFilesAreHeldByDurationGate(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	long := sbCluster(t, f, "lib/Author A/Book Long", "Book Long", 3, 3*3600)
	res := sbPlan(t, f, sbFixer(f))
	r := sbRowWith(t, res, long)
	assert.False(t, r.Applicable())
	assert.Equal(t, sbHeldDurationGate, r.Skipped, r.SkipReason)
	assert.Contains(t, r.SkipReason, "3 of 3 file rows run 120 min or longer")
	assert.Contains(t, strings.Join(r.Evidence, "\n"), "3 at or over 120 min")
}

// A file with no recorded duration cannot be told from a whole book: an
// unknown duration is not "short" (fragment fixer rule).
func TestSplitBooksFixer_UnknownDurationIsHeld(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	unk := sbCluster(t, f, "lib/Author A/Book Unknown", "Book Unknown", 3, 0)
	res := sbPlan(t, f, sbFixer(f))
	r := sbRowWith(t, res, unk)
	assert.False(t, r.Applicable())
	assert.Equal(t, sbHeldDurationUnknown, r.Skipped, r.SkipReason)
	assert.Contains(t, r.SkipReason, "3 of 3 file rows have no recorded duration")
}

// A series folder of separate books is held with the detector's vetoes in its
// evidence, and a re-plan finds the row (it is not "resolved").
func TestSplitBooksFixer_DistinctWorksAreHeld(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	series := sbCluster(t, f, "lib/Author A/Embergate Series", "Embergate", 3, 300)
	other := sbCluster(t, f, "lib/Author A/Book One", "Book One", 3, 300)
	fx := sbFixer(f)
	res := sbPlan(t, f, fx)
	r := sbRowWith(t, res, series)
	assert.False(t, r.Applicable())
	assert.Equal(t, sbHeldDistinctWorks, r.Skipped, r.SkipReason)
	assert.Contains(t, r.SkipReason, "V4")
	assert.Contains(t, strings.Join(r.Evidence, "\n"), "VETO V4")
	assert.True(t, sbRowWith(t, res, other).Applicable())

	again, err := fx.Replan(context.Background(), nil, r, &fakeReporter{})
	require.NoError(t, err)
	assert.Equal(t, sbHeldDistinctWorks, again.Skipped, "the held row is re-planned as held, not resolved")
	assert.Equal(t, r.Fingerprint, again.Fingerprint)

	// The fail-closed detector (the scan op, the bulk merge, the CLI) never
	// lists the vetoed cluster.
	cands, err := dedup.DetectSplitBookCandidates(context.Background(), f.s, nil)
	require.NoError(t, err)
	for _, c := range cands {
		assert.NotEqual(t, series, append([]string(nil), c.BookIDs...))
	}
	assert.Len(t, cands, 1)
}
