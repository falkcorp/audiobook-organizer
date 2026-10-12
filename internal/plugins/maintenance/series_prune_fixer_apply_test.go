// file: internal/plugins/maintenance/series_prune_fixer_apply_test.go
// version: 1.0.0
// guid: 0f6b2d84-93e1-4c57-a8d2-5e17b4c9a3f0
// last-edited: 2026-10-11

package maintenance

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// The series prune fixer's apply path, against writers outside the run: the
// series write-set, the per-series reference re-count before each delete,
// journal-first, the existence and hold re-checks, the book checks before
// and under each write lock, the per-run snapshot and the per-run cache drop.

// spStoreHook is the library store with hooks on what the fixer reads and
// deletes through deps.OpsStore().
type spStoreHook struct {
	*database.PebbleStore
	beforeDelete func(id int)
	counts       atomic.Int32
	// refCountErr fails the per-series reference re-count.
	refCountErr error
}

func (h *spStoreHook) GetSeriesBookRefCount(id int) (int, error) {
	if h.refCountErr != nil {
		return 0, h.refCountErr
	}
	return h.PebbleStore.GetSeriesBookRefCount(id)
}

func (h *spStoreHook) DeleteSeries(id int) error {
	if h.beforeDelete != nil {
		h.beforeDelete(id)
	}
	return h.PebbleStore.DeleteSeries(id)
}

func (h *spStoreHook) GetAllSeriesBookCounts() (map[int]int, error) {
	h.counts.Add(1)
	return h.PebbleStore.GetAllSeriesBookCounts()
}

// spModifyHook is the Writer's book store; before runs ahead of each
// ModifyBook, and its error fails that write unmade.
type spModifyHook struct {
	*database.PebbleStore
	before func(id string) error
}

func (h *spModifyHook) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if h.before != nil {
		if err := h.before(id); err != nil {
			return nil, err
		}
	}
	return h.PebbleStore.ModifyBook(id, fn)
}

// spBeforeApply runs before() between the engine's re-plan and the fixer's
// Apply: a write by another op landing in that window.
type spBeforeApply struct {
	*seriesPruneFixer
	before func()
}

func (f spBeforeApply) Apply(ctx context.Context, w *repairs.Writer, fresh repairs.Row) error {
	f.before()
	return f.seriesPruneFixer.Apply(ctx, w, fresh)
}

func spWriter(l *spLibrary, books repairs.BookModifier, opID string) *repairs.Writer {
	return repairs.NewWriter(books, l.st, seriesPruneFixerID, "bulk_update", "repairs-").WithJournal(l.st, l.st, opID)
}

func spRun(t *testing.T, l *spLibrary, fx repairs.Fixer, res *repairs.PlanResult, w *repairs.Writer, sd repairs.StandDown,
	opID string, rowIDs ...string) (*repairs.ApplyResult, error) {
	t.Helper()
	return repairs.RunApply(context.Background(), fx, res, "plan-1", rowIDs, false,
		repairs.ApplyDeps{Guard: l.st, Writer: w, OpID: opID, StandDown: sd}, &fakeReporter{})
}

func spOutcome(t *testing.T, out *repairs.ApplyResult) repairs.RowResult {
	t.Helper()
	require.NotNil(t, out)
	require.Len(t, out.Rows, 1, "%+v", out.Rows)
	return out.Rows[0]
}

// spLiveRows are the op's journal rows that were not voided.
func spLiveRows(t *testing.T, l *spLibrary, opID string) []string {
	t.Helper()
	changes, err := l.st.GetOperationChanges(opID)
	require.NoError(t, err)
	var out []string
	for _, c := range changes {
		if c.RevertedAt == nil {
			out = append(out, c.ChangeType+"|"+c.BookID)
		}
	}
	return out
}

func TestSeriesPruneFixer_ApplyDeclaresSeriesWriteSet(t *testing.T) {
	assert.True(t, slices.Contains((&Plugin{}).repairsApplyDef().Writes, sdk.ResSeries),
		"repairs.apply must declare the series write-set: its series prune fixer deletes series rows")
}

// An orphan another op points a book at after the re-plan is not deleted:
// the delete re-counts its one series first. Its journal row is voided.
func TestSeriesPruneFixer_OrphanDeleteRecountsItsSeries(t *testing.T) {
	l := newSPLibrary(t)
	deps := &spCacheDeps{fakeDeps: fakeDeps{store: l.st}}
	base := newSeriesPruneFixer(&Plugin{deps: deps})
	res := spPlan(t, l, base)
	fx := spBeforeApply{seriesPruneFixer: base, before: func() { l.book(t, "b20", l.s7, true, false) }}

	out, err := spRun(t, l, fx, res, spWriter(l, l.st, "op-recount"), nil, "op-recount", spRowFor(t, res, l.s7).RowID)
	require.NoError(t, err)
	row := spOutcome(t, out)
	assert.Equal(t, repairs.OutcomeChangedSincePlan, row.Outcome, row.Error)
	assert.Contains(t, row.Error, "now name series")
	assert.True(t, l.exists(t, l.s7), "a series a book names is never deleted")
	assert.Equal(t, l.s7, l.seriesOf(t, "b20"))
	assert.Empty(t, spLiveRows(t, l, "op-recount"), "the journal row of the refused delete is voided")
	assert.Zero(t, deps.series.Load(), "nothing written, nothing invalidated")
}

// The same re-count on a merge's delete: the books move, the copy another op
// pointed a trashed book at stays, and the row is partially applied.
func TestSeriesPruneFixer_MergeDeleteRecountsItsSeries(t *testing.T) {
	l := newSPLibrary(t)
	deps := &spCacheDeps{fakeDeps: fakeDeps{store: l.st}}
	base := newSeriesPruneFixer(&Plugin{deps: deps})
	res := spPlan(t, l, base)
	fx := spBeforeApply{seriesPruneFixer: base, before: func() { l.book(t, "b20", l.s2, true, false) }}

	out, err := spRun(t, l, fx, res, spWriter(l, l.st, "op-merge-recount"), nil, "op-merge-recount", spRowFor(t, res, l.s2).RowID)
	require.NoError(t, err)
	row := spOutcome(t, out)
	assert.Equal(t, repairs.OutcomePartial, row.Outcome, row.Error)
	assert.Equal(t, l.s1, l.seriesOf(t, "b03"))
	assert.Equal(t, l.s1, l.seriesOf(t, "b04"))
	assert.True(t, l.exists(t, l.s2), "the trashed book still names the copy")
	assert.ElementsMatch(t, []string{"series_merge|b03", "series_merge|b04"}, spLiveRows(t, l, "op-merge-recount"))
	assert.Equal(t, int32(1), deps.series.Load(), "books moved: the cache is dropped once, when the run ends")
}

// JOURNAL FIRST: each row is in the op journal when its write runs.
func TestSeriesPruneFixer_JournalsEachRowBeforeItsWrite(t *testing.T) {
	l := newSPLibrary(t)
	hook := &spStoreHook{PebbleStore: l.st}
	f := newSeriesPruneFixer(&Plugin{deps: fakeDeps{store: hook}})
	res := spPlan(t, l, f)
	const op = "op-first"
	var seen []string
	hook.beforeDelete = func(id int) {
		seen = append(seen, "delete "+itoa(id)+" after "+strings.Join(spLiveRows(t, l, op), ","))
	}
	books := &spModifyHook{PebbleStore: l.st, before: func(id string) error {
		seen = append(seen, "modify "+id+" after "+strings.Join(spLiveRows(t, l, op), ","))
		return nil
	}}

	out, err := spRun(t, l, f, res, spWriter(l, books, op), nil, op, spRowFor(t, res, l.s2).RowID)
	require.NoError(t, err)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	require.Len(t, seen, 3, "%v", seen)
	for _, s := range seen[:2] {
		id := strings.Fields(s)[1]
		assert.Contains(t, s, "series_merge|"+id, "the book's row is journaled before its Modify: %s", s)
	}
	assert.Contains(t, seen[2], "series_delete|", "the delete's row is journaled before the delete: %s", seen[2])
}

// A lease lost right before the delete stops it: nothing deleted, nothing
// journaled for it. Renewals: row start, before Apply, then the journal
// step of the delete (the third) is refused.
func TestSeriesPruneFixer_LostLeaseDeletesNothingMore(t *testing.T) {
	l := newSPLibrary(t)
	deps := &spCacheDeps{fakeDeps: fakeDeps{store: l.st}}
	f := newSeriesPruneFixer(&Plugin{deps: deps})
	res := spPlan(t, l, f)

	out, err := spRun(t, l, f, res, spWriter(l, l.st, "op-lease"), &scriptedScan{renewsLeft: 2}, "op-lease", spRowFor(t, res, l.s7).RowID)
	require.ErrorIs(t, err, repairs.ErrStandDownLost)
	row := spOutcome(t, out)
	assert.Equal(t, repairs.OutcomeAborted, row.Outcome, row.Error)
	assert.True(t, l.exists(t, l.s7), "a lease-lost run deletes nothing more")
	assert.Empty(t, spLiveRows(t, l, "op-lease"))
	assert.Zero(t, deps.series.Load())

	// A merge cut off at its delete keeps the books it moved, and still
	// drops the cache once (the run's end, not a settled row, does it).
	// Renewals: row start, before Apply, journal+Modify for each of the two
	// books; the seventh (the delete's journal step) is refused.
	res = spPlan(t, l, f)
	out, err = spRun(t, l, f, res, spWriter(l, l.st, "op-lease-2"), &scriptedScan{renewsLeft: 6}, "op-lease-2", spRowFor(t, res, l.s2).RowID)
	require.ErrorIs(t, err, repairs.ErrStandDownLost)
	row = spOutcome(t, out)
	assert.Equal(t, repairs.OutcomeAborted, row.Outcome, row.Error)
	assert.Contains(t, row.Error, "partially applied")
	assert.Equal(t, l.s1, l.seriesOf(t, "b03"))
	assert.Equal(t, l.s1, l.seriesOf(t, "b04"))
	assert.True(t, l.exists(t, l.s2))
	assert.ElementsMatch(t, []string{"series_merge|b03", "series_merge|b04"}, spLiveRows(t, l, "op-lease-2"))
	assert.Equal(t, int32(1), deps.series.Load(), "an aborted run that moved books still drops the cache")

	// The cut-off merge is not resumed: its re-plan sees the moved books.
	out, err = spRun(t, l, f, res, spWriter(l, l.st, "op-lease-2"), nil, "op-lease-2", spRowFor(t, res, l.s2).RowID)
	require.NoError(t, err)
	assert.Equal(t, repairs.OutcomeChangedSincePlan, spOutcome(t, out).Outcome)
	assert.True(t, l.exists(t, l.s2))
}

// The kept series removed between the re-plan and the write: no book is
// pointed at a series that no longer exists.
func TestSeriesPruneFixer_MergeRefusesAMissingKeep(t *testing.T) {
	l := newSPLibrary(t)
	f := newSeriesPruneFixer(&Plugin{deps: fakeDeps{store: l.st}})
	res := spPlan(t, l, f)
	fresh, err := f.Replan(context.Background(), nil, spRowFor(t, res, l.s2), nil)
	require.NoError(t, err)
	require.NoError(t, l.st.DeleteSeries(l.s1))

	err = f.Apply(context.Background(), spWriter(l, l.st, "op-keep"), fresh)
	require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
	assert.NotErrorIs(t, err, repairs.ErrPartiallyApplied)
	assert.Equal(t, l.s2, l.seriesOf(t, "b03"))
	assert.Equal(t, l.s2, l.seriesOf(t, "b04"))
	assert.True(t, l.exists(t, l.s2))
	assert.Empty(t, spLiveRows(t, l, "op-keep"))
}

// A merged-away series held for an undo since the re-plan is not merged:
// its books are not moved.
func TestSeriesPruneFixer_MergeRefusesANewlyHeldCopy(t *testing.T) {
	l := newSPLibrary(t)
	f := newSeriesPruneFixer(&Plugin{deps: fakeDeps{store: l.st}})
	res := spPlan(t, l, f)
	fresh, err := f.Replan(context.Background(), nil, spRowFor(t, res, l.s2), nil)
	require.NoError(t, err)
	require.NoError(t, l.st.HoldSeries(l.s2, "maintenance.author-named-series"))

	err = f.Apply(context.Background(), spWriter(l, l.st, "op-held"), fresh)
	require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
	assert.NotErrorIs(t, err, repairs.ErrPartiallyApplied, "refused before the first book moved")
	assert.Equal(t, l.s2, l.seriesOf(t, "b03"))
	assert.Equal(t, l.s2, l.seriesOf(t, "b04"))
	assert.Empty(t, spLiveRows(t, l, "op-held"))
}

// A second book moved by another op AFTER the pre-write check (inside the
// write) keeps where that op put it: the check under the book's write lock
// refuses it. The first book's move stands; the row is partial.
func TestSeriesPruneFixer_UnderLockCheckKeepsAMovedSecondBook(t *testing.T) {
	l := newSPLibrary(t)
	deps := &spCacheDeps{fakeDeps: fakeDeps{store: l.st}}
	f := newSeriesPruneFixer(&Plugin{deps: deps})
	res := spPlan(t, l, f)
	var moved atomic.Bool
	books := &spModifyHook{PebbleStore: l.st, before: func(id string) error {
		if id == "b04" && !moved.Swap(true) {
			_, err := l.st.ModifyBook("b04", func(b *database.Book) error { s := l.s3; b.SeriesID = &s; return nil })
			return err
		}
		return nil
	}}

	out, err := spRun(t, l, f, res, spWriter(l, books, "op-lock"), nil, "op-lock", spRowFor(t, res, l.s2).RowID)
	require.NoError(t, err)
	row := spOutcome(t, out)
	assert.Equal(t, repairs.OutcomePartial, row.Outcome, row.Error)
	assert.Equal(t, l.s3, l.seriesOf(t, "b04"), "another op's move stands")
	assert.Equal(t, l.s1, l.seriesOf(t, "b03"))
	assert.True(t, l.exists(t, l.s2), "a refused repoint keeps the copy")
	assert.ElementsMatch(t, []string{"series_merge|b03"}, spLiveRows(t, l, "op-lock"), "b04's row is voided")
}

// A book moved before the write refuses the row whole: the pre-write check
// stops it before the first book (the one still in place) moves.
func TestSeriesPruneFixer_PreWriteCheckRefusesTheRowWhole(t *testing.T) {
	l := newSPLibrary(t)
	f := newSeriesPruneFixer(&Plugin{deps: fakeDeps{store: l.st}})
	res := spPlan(t, l, f)
	fresh, err := f.Replan(context.Background(), nil, spRowFor(t, res, l.s2), nil)
	require.NoError(t, err)
	for _, id := range []string{"b03", "b04"} {
		t.Run("moved "+id, func(t *testing.T) {
			other := map[string]string{"b03": "b04", "b04": "b03"}[id]
			_, err := l.st.ModifyBook(id, func(b *database.Book) error { s := l.s3; b.SeriesID = &s; return nil })
			require.NoError(t, err)
			t.Cleanup(func() {
				_, _ = l.st.ModifyBook(id, func(b *database.Book) error { s := l.s2; b.SeriesID = &s; return nil })
			})
			err = f.Apply(context.Background(), spWriter(l, l.st, "op-pre-"+id), fresh)
			require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
			assert.NotErrorIs(t, err, repairs.ErrPartiallyApplied)
			assert.Equal(t, l.s2, l.seriesOf(t, other), "nothing written")
			assert.Empty(t, spLiveRows(t, l, "op-pre-"+id))
		})
	}
}

// A repoint that fails after another one landed reaches the engine as
// partially applied, not failed.
func TestSeriesPruneFixer_PartialMergeIsReportedPartial(t *testing.T) {
	l := newSPLibrary(t)
	f := newSeriesPruneFixer(&Plugin{deps: fakeDeps{store: l.st}})
	res := spPlan(t, l, f)
	books := &spModifyHook{PebbleStore: l.st, before: func(id string) error {
		if id == "b04" {
			return errors.New("disk full")
		}
		return nil
	}}
	out, err := spRun(t, l, f, res, spWriter(l, books, "op-partial"), nil, "op-partial", spRowFor(t, res, l.s2).RowID)
	require.NoError(t, err)
	row := spOutcome(t, out)
	assert.Equal(t, repairs.OutcomePartial, row.Outcome, row.Error)
	assert.Contains(t, row.Error, "disk full")
	assert.Equal(t, l.s1, l.seriesOf(t, "b03"))
	assert.Equal(t, l.s2, l.seriesOf(t, "b04"))
	assert.True(t, l.exists(t, l.s2))
}

// One apply run reads the library-wide counts once, not once per row, and
// drops the caches once.
func TestSeriesPruneFixer_ApplyRunReadsTheLibraryOnce(t *testing.T) {
	l := newSPLibrary(t)
	hook := &spStoreHook{PebbleStore: l.st}
	deps := &spCacheDeps{fakeDeps: fakeDeps{store: hook}}
	f := newSeriesPruneFixer(&Plugin{deps: deps})
	res := spPlan(t, l, f)
	hook.counts.Store(0)
	ids := []string{spRowFor(t, res, l.s2).RowID, spRowFor(t, res, l.s7).RowID, spRowFor(t, res, l.s11).RowID}

	out, err := spRun(t, l, f, res, spWriter(l, l.st, "op-once"), nil, "op-once", ids...)
	require.NoError(t, err)
	require.Equal(t, 3, out.Applied, "%+v", out.Rows)
	assert.Equal(t, int32(1), hook.counts.Load(), "three rows, one library-wide read")
	assert.Equal(t, int32(1), deps.series.Load(), "three writing rows, one cache drop")
	assert.Equal(t, int32(1), deps.dedupCache.Load())

	// A dry run writes nothing and drops nothing.
	res = spPlan(t, l, f)
	_, err = repairs.RunApply(context.Background(), f, res, "plan-2", []string{spRowFor(t, res, l.s10).RowID}, true,
		repairs.ApplyDeps{Guard: l.st, Writer: spWriter(l, l.st, "op-dry"), OpID: "op-dry"}, &fakeReporter{})
	require.NoError(t, err)
	assert.Equal(t, int32(1), deps.series.Load())
}

// Doctor Who / Big Finish / Torchwood series rows are skipped by name: an
// empty-series row has no book for the framework guard to read.
func TestSeriesPruneFixer_FranchiseSeriesAreOwnerManual(t *testing.T) {
	l := newSPLibrary(t)
	empty, err := l.st.CreateSeries("Doctor Who: The Monthly Adventures", nil)
	require.NoError(t, err)
	tw1, err := l.st.CreateSeries("tmp torchwood", nil)
	require.NoError(t, err)
	require.NoError(t, l.st.UpdateSeriesName(tw1.ID, "Torchwood: The Story Continues"))
	tw2, err := l.st.CreateSeries("tmp torchwood 2", nil)
	require.NoError(t, err)
	require.NoError(t, l.st.UpdateSeriesName(tw2.ID, "torchwood: the story continues"))
	f := newSeriesPruneFixer(&Plugin{deps: fakeDeps{store: l.st}})
	res := spPlan(t, l, f)

	for name, id := range map[string]int{"empty Doctor Who series": empty.ID, "empty Torchwood pair": tw2.ID} {
		r := spRowFor(t, res, id)
		assert.Equal(t, repairs.SkipOwnerManual, r.Skipped, name)
		assert.False(t, r.Applicable(), name)
		fresh, err := f.Replan(context.Background(), nil, r, nil)
		require.NoError(t, err)
		assert.Equal(t, r.Fingerprint, fresh.Fingerprint, "plan and re-plan agree: %s", name)
		assert.Nil(t, fresh.Detail, name)
	}
	assert.Empty(t, spRowFor(t, res, l.s7).Skipped, "an ordinary empty series is still offered")
}

// A re-count that cannot be read refuses the delete (fail closed): an
// unreadable count is never a zero.
func TestSeriesPruneFixer_UnreadableRecountDeletesNothing(t *testing.T) {
	l := newSPLibrary(t)
	hook := &spStoreHook{PebbleStore: l.st}
	f := newSeriesPruneFixer(&Plugin{deps: fakeDeps{store: hook}})
	res := spPlan(t, l, f)
	hook.refCountErr = errors.New("memdb gone")

	out, err := spRun(t, l, f, res, spWriter(l, l.st, "op-unread"), nil, "op-unread", spRowFor(t, res, l.s7).RowID)
	require.NoError(t, err)
	row := spOutcome(t, out)
	assert.Equal(t, repairs.OutcomeFailed, row.Outcome, row.Error)
	assert.Contains(t, row.Error, "memdb gone")
	assert.True(t, l.exists(t, l.s7))
	assert.Empty(t, spLiveRows(t, l, "op-unread"))
}

// The orphan core's own guards stand behind evaluate's: a decision naming a
// referenced or held series (evaluate never builds one) deletes nothing and
// is never reported applied.
func TestSeriesPruneFixer_OrphanCoreRefusalIsNeverApplied(t *testing.T) {
	l := newSPLibrary(t)
	deps := &spCacheDeps{fakeDeps: fakeDeps{store: l.st}}
	f := newSeriesPruneFixer(&Plugin{deps: deps})
	s7, err := l.st.GetSeriesByID(l.s7)
	require.NoError(t, err)
	held := *s7
	held.HeldBy = "maintenance.author-named-series"
	for name, d := range map[string]*spDecision{
		"referenced": {kind: dedup.SeriesPruneOrphan, orphan: *s7, orphanRf: 1},
		"held":       {kind: dedup.SeriesPruneOrphan, orphan: held},
	} {
		err := f.Apply(context.Background(), spWriter(l, l.st, "op-core-"+name), repairs.Row{RowID: "r", Detail: d})
		require.ErrorIs(t, err, repairs.ErrChangedSincePlan, name)
		assert.True(t, l.exists(t, l.s7), name)
		assert.Empty(t, spLiveRows(t, l, "op-core-"+name), name)
	}
	assert.Zero(t, deps.series.Load())
}
