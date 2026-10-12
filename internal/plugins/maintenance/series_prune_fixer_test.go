// file: internal/plugins/maintenance/series_prune_fixer_test.go
// version: 1.0.0
// guid: 30beca88-5314-4b9d-92de-0c91dc9f4151
// last-edited: 2026-10-11

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// spCacheDeps counts the cache invalidations the fixer asks for.
type spCacheDeps struct {
	fakeDeps
	series, dedupCache atomic.Int32
}

func (d *spCacheDeps) InvalidateSeriesCache() { d.series.Add(1) }
func (d *spCacheDeps) InvalidateDedupCache()  { d.dedupCache.Add(1) }

// spLibrary is a synthetic library covering every row kind and skip.
type spLibrary struct {
	st *database.PebbleStore
	// duplicate "Series One" (Author A): s1 kept, s2 merged away (b04 is a
	// non-primary version).
	s1, s2 int
	// duplicate "Series Two" (Author B): s4 holds only a trashed book.
	s3, s4 int
	// "Series Three": s5 held and empty, s6 its unheld twin with a book.
	s5, s6 int
	// orphans: s7 empty, s8 only a trashed book, s9 only a non-primary book.
	s7, s8, s9 int
	// duplicate "Series Seven" (Author B), both empty.
	s10, s11 int
}

func newSPLibrary(t *testing.T) *spLibrary {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	l := &spLibrary{st: st}
	author := func(name string) *int {
		a, err := st.CreateAuthor(name)
		require.NoError(t, err)
		return &a.ID
	}
	// CreateSeries returns the existing row for an indexed name, so a
	// duplicate is created under another name and renamed onto the first,
	// the way production got them (UpdateSeriesName keeps the collision).
	series := func(name string, authorID *int) int {
		s, err := st.CreateSeries("tmp "+name, authorID)
		require.NoError(t, err)
		require.NoError(t, st.UpdateSeriesName(s.ID, name))
		return s.ID
	}
	a, b := author("Author A"), author("Author B")
	l.s1 = series("Series One", a)
	l.s2 = series("series one", a)
	l.s3 = series("Series Two", b)
	l.s4 = series("Series Two ", b)
	l.s5 = series("Series Three", nil)
	l.s6 = series("series three", nil)
	l.s7 = series("Series Four", nil)
	l.s8 = series("Series Five", nil)
	l.s9 = series("Series Eight", nil)
	l.s10 = series("Series Seven", b)
	l.s11 = series("SERIES SEVEN", b)
	l.book(t, "b01", l.s1, false, false)
	l.book(t, "b02", l.s1, false, false)
	l.book(t, "b03", l.s2, false, false)
	l.book(t, "b04", l.s2, false, true)
	l.book(t, "b05", l.s3, false, false)
	l.book(t, "b06", l.s4, true, false)
	l.book(t, "b07", l.s6, false, false)
	l.book(t, "b08", l.s8, true, false)
	l.book(t, "b09", l.s9, false, true)
	require.NoError(t, st.HoldSeries(l.s5, "maintenance.author-named-series"))
	return l
}

func (l *spLibrary) book(t *testing.T, id string, seriesID int, trashed, nonPrimary bool) {
	t.Helper()
	sid := seriesID
	b := &database.Book{ID: id, Title: "Book " + id, SeriesID: &sid, FilePath: "/srv/library/" + id + ".m4b", Format: "m4b"}
	if trashed {
		v := true
		b.MarkedForDeletion = &v
	}
	if nonPrimary {
		v := false
		b.IsPrimaryVersion = &v
	}
	_, err := l.st.CreateBook(b)
	require.NoError(t, err)
}

func (l *spLibrary) seriesOf(t *testing.T, bookID string) int {
	t.Helper()
	b, err := l.st.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, b)
	if b.SeriesID == nil {
		return 0
	}
	return *b.SeriesID
}

func (l *spLibrary) exists(t *testing.T, id int) bool {
	t.Helper()
	s, err := l.st.GetSeriesByID(id)
	require.NoError(t, err)
	return s != nil
}

func spPlan(t *testing.T, l *spLibrary, f *seriesPruneFixer) *repairs.PlanResult {
	t.Helper()
	res, err := repairs.RunPlan(context.Background(), f, nil, repairs.PlanDeps{Guard: l.st}, &fakeReporter{})
	require.NoError(t, err)
	return res
}

// spRowFor returns the planned row that names series id (as kept, merged or
// emptied).
func spRowFor(t *testing.T, res *repairs.PlanResult, id int) repairs.Row {
	t.Helper()
	for _, r := range res.Rows {
		if r.RowID == "series-empty-"+itoa(id) {
			return r
		}
		var st spState
		require.NoError(t, json.Unmarshal(r.State, &st))
		for _, m := range st.MemberIDs {
			if m == id {
				return r
			}
		}
	}
	t.Fatalf("no row names series %d", id)
	return repairs.Row{}
}

func spApply(t *testing.T, l *spLibrary, f *seriesPruneFixer, res *repairs.PlanResult, opID string, rowIDs ...string) *repairs.ApplyResult {
	t.Helper()
	w := repairs.NewWriter(l.st, l.st, f.ID(), "bulk_update", "repairs-").WithJournal(l.st, l.st, opID)
	out, err := repairs.RunApply(context.Background(), f, res, "plan-1", rowIDs, false,
		repairs.ApplyDeps{Guard: l.st, Writer: w, OpID: opID}, &fakeReporter{})
	require.NoError(t, err)
	return out
}

func TestSeriesPruneFixer_Registered(t *testing.T) {
	p := &Plugin{deps: fakeDeps{}}
	fx, ok := p.Repairs().Get(seriesPruneFixerID)
	require.True(t, ok, "the fixer is registered")
	assert.Equal(t, "Prune series", fx.Title())
	assert.NotContains(t, fx.Description(), "undo", "a deleted series cannot be brought back; the lane must not promise an undo")
}

// plan has duplicate rows and orphan rows.
func TestSeriesPruneFixer_PlanHasDuplicateAndOrphanRows(t *testing.T) {
	l := newSPLibrary(t)
	f := newSeriesPruneFixer(&Plugin{deps: fakeDeps{store: l.st}})
	res := spPlan(t, l, f)

	byClass := map[string]int{}
	for _, r := range res.Rows {
		byClass[r.Class]++
	}
	assert.Equal(t, map[string]int{spClassDuplicate: 3, spClassOrphan: 4}, byClass)

	one := spRowFor(t, res, l.s2)
	assert.Empty(t, one.Skipped, one.SkipReason)
	assert.Equal(t, `Merge 1 duplicate series into "Series One"`, one.Title)
	assert.Equal(t, "Author A", one.Author)
	assert.ElementsMatch(t, []string{"b01", "b02", "b03", "b04"}, one.BookIDs)
	roles := map[string]string{}
	for _, m := range one.Members {
		roles[m.BookID] = m.Role
	}
	assert.Equal(t, map[string]string{"b01": "canonical", "b02": "canonical", "b03": "merge", "b04": "merge"}, roles)
	assert.Contains(t, one.Current["kept"], "#"+itoa(l.s1))
	assert.Contains(t, one.Current["merged away"], "#"+itoa(l.s2))

	empty := spRowFor(t, res, l.s7)
	assert.Empty(t, empty.Skipped, empty.SkipReason)
	assert.Equal(t, `Delete empty series "Series Four"`, empty.Title)
	assert.Empty(t, empty.BookIDs, "an empty-series row changes no book")

	both := spRowFor(t, res, l.s11)
	assert.Empty(t, both.Skipped, "two empty twins merge: nothing references the copy")
	for _, r := range res.Rows {
		assert.NotEqual(t, "series-empty-"+itoa(l.s10), r.RowID, "a duplicate-group member is never also an orphan row")
		assert.NotEqual(t, "series-empty-"+itoa(l.s11), r.RowID, "a duplicate-group member is never also an orphan row")
	}
}

// plan uses the execute grouping rule (normalised name, held rows skipped).
func TestSeriesPruneFixer_PlanUsesTheExecuteGroupingRule(t *testing.T) {
	l := newSPLibrary(t)
	f := newSeriesPruneFixer(&Plugin{deps: fakeDeps{store: l.st}})
	res := spPlan(t, l, f)

	var st spState
	require.NoError(t, json.Unmarshal(spRowFor(t, res, l.s4).State, &st))
	assert.ElementsMatch(t, []int{l.s3, l.s4}, st.MemberIDs, "a trailing space is normalised away")

	for _, r := range res.Rows {
		var s spState
		require.NoError(t, json.Unmarshal(r.State, &s))
		assert.NotContains(t, s.MemberIDs, l.s5, "a held series is never in a duplicate group")
		assert.NotContains(t, s.MemberIDs, l.s6, "its unheld twin has no group left to be in")
	}
	held := spRowFor(t, res, l.s5)
	assert.Equal(t, spSkipHeld, held.Skipped)
	assert.Equal(t, "held for undo", held.SkipReason)
}

// a series with a trashed or non-primary-version book is skipped as still
// referenced.
func TestSeriesPruneFixer_StillReferencedIsSkipped(t *testing.T) {
	l := newSPLibrary(t)
	f := newSeriesPruneFixer(&Plugin{deps: fakeDeps{store: l.st}})
	res := spPlan(t, l, f)
	for name, id := range map[string]int{"trashed-only orphan": l.s8, "non-primary-only orphan": l.s9, "merge-away with a trashed book": l.s4} {
		r := spRowFor(t, res, id)
		assert.Equal(t, spSkipReferenced, r.Skipped, name)
		assert.Equal(t, "still referenced", r.SkipReason, name)
		assert.False(t, r.Applicable(), name)
	}
}

// replan uses the same evaluate as plan.
func TestSeriesPruneFixer_ReplanMatchesPlan(t *testing.T) {
	l := newSPLibrary(t)
	f := newSeriesPruneFixer(&Plugin{deps: fakeDeps{store: l.st}})
	res := spPlan(t, l, f)
	require.Len(t, res.Rows, 7)
	for _, planned := range res.Rows {
		t.Run(planned.RowID, func(t *testing.T) {
			fresh, err := f.Replan(context.Background(), nil, planned, nil)
			require.NoError(t, err)
			assert.Equal(t, planned.Fingerprint, fresh.Fingerprint)
			assert.Equal(t, planned.Skipped, fresh.Skipped)
			assert.Equal(t, planned.Title, fresh.Title)
			assert.Equal(t, planned.Class, fresh.Class)
			assert.ElementsMatch(t, planned.BookIDs, fresh.BookIDs)
			assert.Equal(t, planned.Members, fresh.Members)
			assert.Equal(t, planned.Applicable(), fresh.Detail != nil, "an applicable row carries its decision")
		})
	}
}

// apply merge repoints every book then deletes the emptied series.
func TestSeriesPruneFixer_ApplyMerge(t *testing.T) {
	l := newSPLibrary(t)
	deps := &spCacheDeps{fakeDeps: fakeDeps{store: l.st}}
	f := newSeriesPruneFixer(&Plugin{deps: deps})
	res := spPlan(t, l, f)
	row := spRowFor(t, res, l.s2)

	out := spApply(t, l, f, res, "op-merge", row.RowID)
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)

	for _, id := range []string{"b01", "b02", "b03", "b04"} {
		assert.Equal(t, l.s1, l.seriesOf(t, id), id)
	}
	assert.False(t, l.exists(t, l.s2), "the emptied copy is removed")
	assert.True(t, l.exists(t, l.s1))
	assert.Equal(t, l.s3, l.seriesOf(t, "b05"), "another group is untouched")
	assert.True(t, l.exists(t, l.s7), "an orphan not selected is untouched")

	changes, err := l.st.GetOperationChanges("op-merge")
	require.NoError(t, err)
	kinds := map[string]int{}
	for _, c := range changes {
		kinds[c.ChangeType+"/"+c.FieldName]++
		assert.Equal(t, seriesPruneFixerID, c.Source)
	}
	assert.Equal(t, map[string]int{"series_merge/series_id": 2, "series_delete/series": 1}, kinds)
	assert.Positive(t, deps.series.Load(), "the cached series list is dropped")
	assert.Positive(t, deps.dedupCache.Load())
}

// apply orphan deletes only the series row.
func TestSeriesPruneFixer_ApplyOrphan(t *testing.T) {
	l := newSPLibrary(t)
	deps := &spCacheDeps{fakeDeps: fakeDeps{store: l.st}}
	f := newSeriesPruneFixer(&Plugin{deps: deps})
	res := spPlan(t, l, f)
	before := map[string]int{}
	for _, id := range []string{"b01", "b02", "b03", "b04", "b05", "b06", "b07", "b08", "b09"} {
		before[id] = l.seriesOf(t, id)
	}

	out := spApply(t, l, f, res, "op-orphan", spRowFor(t, res, l.s7).RowID)
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)

	assert.False(t, l.exists(t, l.s7))
	for id, sid := range before {
		assert.Equal(t, sid, l.seriesOf(t, id), "book %s", id)
	}
	all, err := l.st.GetAllSeries()
	require.NoError(t, err)
	assert.Len(t, all, 10, "exactly one series row removed")
	changes, err := l.st.GetOperationChanges("op-orphan")
	require.NoError(t, err)
	require.Len(t, changes, 1)
	assert.Equal(t, "series_delete", changes[0].ChangeType)
	assert.Equal(t, "orphan_series", changes[0].FieldName)
	assert.Equal(t, int32(1), deps.series.Load())
}

// apply returns ErrChangedSincePlan when membership changed.
func TestSeriesPruneFixer_ChangedSincePlan(t *testing.T) {
	l := newSPLibrary(t)
	f := newSeriesPruneFixer(&Plugin{deps: fakeDeps{store: l.st}})
	res := spPlan(t, l, f)
	row := spRowFor(t, res, l.s2)

	// Through the engine: the re-plan's fingerprint differs.
	_, err := l.st.ModifyBook("b03", func(b *database.Book) error { b.SeriesID = nil; return nil })
	require.NoError(t, err)
	out := spApply(t, l, f, res, "op-csp", row.RowID)
	assert.Equal(t, 1, out.ChangedSincePlan, "%v", out.ByOutcome)
	assert.Zero(t, out.Applied)
	assert.True(t, l.exists(t, l.s2), "nothing written")
	assert.Equal(t, l.s2, l.seriesOf(t, "b04"), "nothing written")

	// Directly: a fresh row whose books move before the write is refused
	// whole, before anything is written.
	fresh, err := f.Replan(context.Background(), nil, row, nil)
	require.NoError(t, err)
	require.NotNil(t, fresh.Detail)
	_, err = l.st.ModifyBook("b04", func(b *database.Book) error { s := l.s3; b.SeriesID = &s; return nil })
	require.NoError(t, err)
	w := repairs.NewWriter(l.st, l.st, f.ID(), "bulk_update", "repairs-").WithJournal(l.st, l.st, "op-direct")
	err = f.Apply(context.Background(), w, fresh)
	require.True(t, errors.Is(err, repairs.ErrChangedSincePlan), "err = %v", err)
	assert.False(t, errors.Is(err, repairs.ErrPartiallyApplied), "err = %v", err)
	assert.True(t, l.exists(t, l.s2))
	changes, err := l.st.GetOperationChanges("op-direct")
	require.NoError(t, err)
	assert.Empty(t, changes)

	// A series removed between the re-plan and the write.
	orphan, err := f.Replan(context.Background(), nil, spRowFor(t, res, l.s7), nil)
	require.NoError(t, err)
	require.NoError(t, l.st.DeleteSeries(l.s7))
	err = f.Apply(context.Background(), w, orphan)
	require.True(t, errors.Is(err, repairs.ErrChangedSincePlan), "err = %v", err)
}

// apply is idempotent on a second run.
func TestSeriesPruneFixer_SecondApplyWritesNothing(t *testing.T) {
	l := newSPLibrary(t)
	f := newSeriesPruneFixer(&Plugin{deps: fakeDeps{store: l.st}})
	res := spPlan(t, l, f)
	ids := []string{spRowFor(t, res, l.s2).RowID, spRowFor(t, res, l.s7).RowID, spRowFor(t, res, l.s11).RowID}

	first := spApply(t, l, f, res, "op-1", ids...)
	require.Equal(t, 3, first.Applied, "%v %+v", first.ByOutcome, first.Rows)
	after, err := l.st.GetAllSeries()
	require.NoError(t, err)

	second := spApply(t, l, f, res, "op-2", ids...)
	assert.Zero(t, second.Applied, "%v", second.ByOutcome)
	assert.Equal(t, 3, second.ChangedSincePlan, "%v", second.ByOutcome)
	again, err := l.st.GetAllSeries()
	require.NoError(t, err)
	assert.Equal(t, len(after), len(again))
	changes, err := l.st.GetOperationChanges("op-2")
	require.NoError(t, err)
	assert.Empty(t, changes)

	// A new plan no longer lists the applied rows; the kept twin of the
	// empty pair is an empty series now, offered on its own.
	fresh := spPlan(t, l, f)
	for _, r := range fresh.Rows {
		assert.NotContains(t, ids, r.RowID)
	}
	assert.Empty(t, spRowFor(t, fresh, l.s10).Skipped)
}

// fingerprint changes when a book count changes.
func TestSeriesPruneFixer_FingerprintFollowsBookCounts(t *testing.T) {
	l := newSPLibrary(t)
	f := newSeriesPruneFixer(&Plugin{deps: fakeDeps{store: l.st}})
	res := spPlan(t, l, f)
	dup, empty := spRowFor(t, res, l.s2), spRowFor(t, res, l.s10)

	l.book(t, "b10", l.s1, false, false)
	fresh, err := f.Replan(context.Background(), nil, dup, nil)
	require.NoError(t, err)
	assert.NotEqual(t, dup.Fingerprint, fresh.Fingerprint, "a book added to the kept series")

	l.book(t, "b11", l.s11, true, false)
	fresh, err = f.Replan(context.Background(), nil, empty, nil)
	require.NoError(t, err)
	assert.NotEqual(t, empty.Fingerprint, fresh.Fingerprint, "a trashed book now names the copy")
	assert.Equal(t, spSkipReferenced, fresh.Skipped)
}
