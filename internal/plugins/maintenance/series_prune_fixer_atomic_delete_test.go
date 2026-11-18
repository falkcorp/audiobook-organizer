// file: internal/plugins/maintenance/series_prune_fixer_atomic_delete_test.go
// version: 1.0.0
// guid: 7b3e1a90-4c5d-4f68-9e02-d81a6c37f5b4
// last-edited: 2026-11-18

package maintenance

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// The series-prune fixer's deletes are the store's check-and-delete
// (DeleteSeriesIfUnreferenced). Before, each delete counted the series'
// references and then called the plain DeleteSeries, and a book another writer
// linked into the series between the two was stranded on a row that no longer
// existed. These tests put that write exactly between the fixer's last look and
// its delete, on every delete path the fixer can reach.

// spIntervene wraps the library's store and runs fn once, immediately before the
// fixer's delete reaches the store, whichever delete it calls: the plain
// DeleteSeries (the old path) or DeleteSeriesIfUnreferenced (the new one). That
// is the moment between "nothing references it" and "delete it".
type spIntervene struct {
	database.Store
	once sync.Once
	fn   func()
}

func (d *spIntervene) Unwrap() database.Store { return d.Store }

func (d *spIntervene) fire() { d.once.Do(d.fn) }

func (d *spIntervene) DeleteSeries(id int) error {
	d.fire()
	return d.Store.DeleteSeries(id)
}

func (d *spIntervene) DeleteSeriesIfUnreferenced(id int, expectName string, expectAuthorID *int) (bool, error) {
	d.fire()
	inner, ok := database.AsCapability[database.SeriesUnreferencedDeleter](d.Store)
	if !ok {
		return false, errors.New("fixture: inner store has no DeleteSeriesIfUnreferenced")
	}
	return inner.DeleteSeriesIfUnreferenced(id, expectName, expectAuthorID)
}

// spBare hides every optional capability of the store under it: it neither
// implements them nor unwraps, so AsCapability stops at it.
type spBare struct{ database.Store }

// spNotReady has the delete but answers the readiness probe with a refusal, as a
// store whose memdb is not serving or is missing rows does.
type spNotReady struct{ database.Store }

func (d *spNotReady) Unwrap() database.Store { return d.Store }

func (d *spNotReady) SeriesDeleteReady() error { return errors.New("fixture: memdb is not serving") }

// requireNoDanglingSeriesLinks fails when any of the books names a series that
// does not resolve: the stranding the check-and-delete exists to prevent.
func requireNoDanglingSeriesLinks(t *testing.T, l *spLibrary, bookIDs ...string) {
	t.Helper()
	for _, id := range bookIDs {
		b, err := l.st.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, b, "book %s", id)
		if b.SeriesID == nil {
			continue
		}
		s, err := l.st.GetSeriesByID(*b.SeriesID)
		require.NoError(t, err)
		require.NotNil(t, s, "book %s names series %d, which does not exist", id, *b.SeriesID)
	}
}

// A book linked into an empty series after the re-plan keeps the series: the
// delete refuses, the row settles changed_since_plan, and the book is not left
// naming a deleted series.
func TestSeriesPruneFixer_OrphanDeleteRefusesABookLinkedAfterTheLastCount(t *testing.T) {
	l := newSPLibrary(t)
	wrap := &spIntervene{Store: l.st}
	wrap.fn = func() { l.book(t, "b-late", l.s7, false, false) }
	f := newSeriesPruneFixer(&Plugin{deps: fakeDeps{store: wrap}})
	res := spPlan(t, l, f)
	row := spRowFor(t, res, l.s7)

	out := spApply(t, l, f, res, "op-orphan-window", row.RowID)

	assert.Zero(t, out.Applied, "%v", out.ByOutcome)
	assert.Equal(t, 1, out.ChangedSincePlan, "%v", out.ByOutcome)
	assert.True(t, l.exists(t, l.s7), "a series a book names must not be deleted")
	assert.Equal(t, l.s7, l.seriesOf(t, "b-late"))
	requireNoDanglingSeriesLinks(t, l, "b-late")
	assert.Empty(t, spLiveRows(t, l, "op-orphan-window"), "a refused delete leaves no live journal row")
}

// The same for the merged-away copy of a duplicate group: its books moved, and
// a book linked into the copy after that keeps the copy alive.
func TestSeriesPruneFixer_MergeDeleteRefusesABookLinkedAfterTheMove(t *testing.T) {
	l := newSPLibrary(t)
	wrap := &spIntervene{Store: l.st}
	wrap.fn = func() { l.book(t, "b-late", l.s2, false, false) }
	f := newSeriesPruneFixer(&Plugin{deps: fakeDeps{store: wrap}})
	res := spPlan(t, l, f)
	row := spRowFor(t, res, l.s2)

	out := spApply(t, l, f, res, "op-merge-window", row.RowID)

	assert.Zero(t, out.Applied, "a row that could not remove the copy is not applied: %v", out.ByOutcome)
	assert.True(t, l.exists(t, l.s2), "a series a book names must not be deleted")
	assert.Equal(t, l.s2, l.seriesOf(t, "b-late"))
	requireNoDanglingSeriesLinks(t, l, "b01", "b02", "b03", "b04", "b-late")
}

// A series renamed after the plan is somebody's edit: the delete compares the
// row's name with the one the plan read and does not remove it.
func TestSeriesPruneFixer_DeleteRefusesASeriesRenamedAfterThePlan(t *testing.T) {
	l := newSPLibrary(t)
	wrap := &spIntervene{Store: l.st}
	wrap.fn = func() { require.NoError(t, l.st.UpdateSeriesName(l.s7, "The Long Halloween")) }
	f := newSeriesPruneFixer(&Plugin{deps: fakeDeps{store: wrap}})
	res := spPlan(t, l, f)

	out := spApply(t, l, f, res, "op-renamed", spRowFor(t, res, l.s7).RowID)

	assert.Zero(t, out.Applied, "%v", out.ByOutcome)
	assert.Equal(t, 1, out.ChangedSincePlan, "%v", out.ByOutcome)
	got, err := l.st.GetSeriesByID(l.s7)
	require.NoError(t, err)
	require.NotNil(t, got, "the renamed series must survive")
	assert.Equal(t, "The Long Halloween", got.Name)
}

// A store that cannot delete only-if-unreferenced is refused outright: no
// fallback to the plain DeleteSeries, nothing written, nothing journaled.
func TestSeriesPruneFixer_StoreWithoutTheConditionalDeleteRefusesBothRowKinds(t *testing.T) {
	l := newSPLibrary(t)
	// Planned over the real store (a plan needs the counts); applied over one
	// that has no conditional delete.
	res := spPlan(t, l, newSeriesPruneFixer(&Plugin{deps: fakeDeps{store: l.st}}))
	f := newSeriesPruneFixer(&Plugin{deps: fakeDeps{store: spBare{Store: l.st}}})
	before := map[string]int{}
	for _, id := range []string{"b01", "b02", "b03", "b04"} {
		before[id] = l.seriesOf(t, id)
	}

	out := spApply(t, l, f, res, "op-bare", spRowFor(t, res, l.s7).RowID, spRowFor(t, res, l.s2).RowID)

	assert.Zero(t, out.Applied, "%v", out.ByOutcome)
	assert.Equal(t, 2, out.Failed, "%v", out.ByOutcome)
	assert.True(t, l.exists(t, l.s7))
	assert.True(t, l.exists(t, l.s2))
	for id, sid := range before {
		assert.Equal(t, sid, l.seriesOf(t, id), "book %s must not move", id)
	}
	changes, err := l.st.GetOperationChanges("op-bare")
	require.NoError(t, err)
	assert.Empty(t, changes)
}

// A store that has the delete but cannot count right now is refused BEFORE a
// merge moves its first book, so the row is not left half applied.
func TestSeriesPruneFixer_StoreThatCannotCountMovesNoBook(t *testing.T) {
	l := newSPLibrary(t)
	f := newSeriesPruneFixer(&Plugin{deps: fakeDeps{store: &spNotReady{Store: l.st}}})
	res := spPlan(t, l, f)
	before := map[string]int{}
	for _, id := range []string{"b01", "b02", "b03", "b04"} {
		before[id] = l.seriesOf(t, id)
	}

	out := spApply(t, l, f, res, "op-notready", spRowFor(t, res, l.s2).RowID, spRowFor(t, res, l.s7).RowID)

	assert.Zero(t, out.Applied, "%v", out.ByOutcome)
	assert.Equal(t, 2, out.Failed, "%v", out.ByOutcome)
	assert.Zero(t, out.Partial, "nothing may be half applied: %v", out.ByOutcome)
	assert.True(t, l.exists(t, l.s2))
	assert.True(t, l.exists(t, l.s7))
	for id, sid := range before {
		assert.Equal(t, sid, l.seriesOf(t, id), "book %s must not move", id)
	}
	changes, err := l.st.GetOperationChanges("op-notready")
	require.NoError(t, err)
	assert.Empty(t, changes)
}
