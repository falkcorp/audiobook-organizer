// file: internal/maintenance/jobs/cleanup_series_pebble_test.go
// version: 1.0.0
// guid: 9a4f1c6e-2b83-4d57-8e10-6c3d7a5b9f42
// last-edited: 2026-11-18

package jobs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// The run-level tests elsewhere use fakes whose DeleteSeriesIfUnreferenced
// re-implements the store's contract. These run cleanup-series against a real
// *database.PebbleStore, so two things no fake can show are pinned: the name and
// author compare between the memdb row GetAllSeries served and the Pebble row the
// delete reads, and the capability resolving off the real store.

// csRealStore is a real PebbleStore (so a maintenance.JobStore, and so carrying
// both capabilities cleanup-series resolves) with the "other writer" hook: it
// runs once, right after the bulk membership read, the last read the job takes
// before it writes.
type csRealStore struct {
	*database.PebbleStore
	afterMembership func()
}

func (s *csRealStore) GetBooksBySeriesIDsAllVersions(ids []int) (map[int][]database.BookCore, error) {
	out, err := s.PebbleStore.GetBooksBySeriesIDsAllVersions(ids)
	if err == nil && s.afterMembership != nil {
		hook := s.afterMembership
		s.afterMembership = nil
		hook()
	}
	return out, err
}

func newCsRealStore(t *testing.T) *csRealStore {
	t.Helper()
	ps, err := database.NewPebbleStoreInMemory("db")
	require.NoError(t, err)
	ps.WaitForWarmup()
	t.Cleanup(func() { _ = ps.Close() })
	require.True(t, ps.IsMemReady(), "the fixture needs the memdb serving, as production does")
	return &csRealStore{PebbleStore: ps}
}

func (s *csRealStore) mkBook(t *testing.T, title string, seriesID int) *database.Book {
	t.Helper()
	sid, primary, trashed := seriesID, true, false
	b, err := s.CreateBook(&database.Book{
		Title:             title,
		FilePath:          "/library/" + title,
		SeriesID:          &sid,
		IsPrimaryVersion:  &primary,
		MarkedForDeletion: &trashed,
	})
	require.NoError(t, err)
	return b
}

// hushFixture: "Batman: Hush" holds one book (phase 1 collapses it); "batman
// hush" holds four, so the two normalize to one name and share a duplicate group.
func (s *csRealStore) hushFixture(t *testing.T) (single, keeper *database.Series, singleBook *database.Book) {
	t.Helper()
	single, err := s.CreateSeries("Batman: Hush", nil)
	require.NoError(t, err)
	keeper, err = s.CreateSeries("batman hush", nil)
	require.NoError(t, err)
	singleBook = s.mkBook(t, "Hush One", single.ID)
	for _, title := range []string{"Hush Two", "Hush Three", "Hush Four", "Hush Five"} {
		s.mkBook(t, title, keeper.ID)
	}
	return single, keeper, singleBook
}

func (s *csRealStore) requireNoDangling(t *testing.T) {
	t.Helper()
	all, err := s.GetAllSeries()
	require.NoError(t, err)
	live := map[int]bool{}
	for _, ser := range all {
		live[ser.ID] = true
	}
	refs, err := database.SeriesRefCounts(s)
	require.NoError(t, err)
	for id, n := range refs {
		require.True(t, live[id], "%d book rows name series %d, which has no row", n, id)
	}
}

func TestCleanupSeriesRun_RealStore_DeletesAnEmptiedSeriesThroughTheConditionalDelete(t *testing.T) {
	// Control: the compare between the memdb row and the Pebble row passes for an
	// untouched series, and the real delete removes it.
	s := newCsRealStore(t)
	single, keeper, singleBook := s.hushFixture(t)
	rep := &csPhase1Reporter{}

	require.NoError(t, (&cleanupSeriesJob{}).Run(context.Background(), s, rep, false))

	gone, err := s.GetSeriesByID(single.ID)
	require.NoError(t, err)
	require.Nil(t, gone, "an emptied, unreferenced series must be deleted")
	kept, err := s.GetSeriesByID(keeper.ID)
	require.NoError(t, err)
	require.NotNil(t, kept)
	got, err := s.GetBookByID(singleBook.ID)
	require.NoError(t, err)
	require.Nil(t, got.SeriesID, "phase 1 unlinks the book before it deletes the row")
	s.requireNoDangling(t)
	require.False(t, csLogContains(rep, "live references"), "nothing was refused: %v", rep.logs)
}

func TestCleanupSeriesRun_RealStore_KeepsASeriesABookWasLinkedIntoAfterTheMembershipRead(t *testing.T) {
	s := newCsRealStore(t)
	single, _, _ := s.hushFixture(t)
	var late *database.Book
	s.afterMembership = func() { late = s.mkBook(t, "Hush Late", single.ID) }
	rep := &csPhase1Reporter{}

	require.NoError(t, (&cleanupSeriesJob{}).Run(context.Background(), s, rep, false))

	row, err := s.GetSeriesByID(single.ID)
	require.NoError(t, err)
	require.NotNil(t, row, "the series a book was linked into after the read must stay")
	got, err := s.GetBookByID(late.ID)
	require.NoError(t, err)
	require.NotNil(t, got.SeriesID)
	require.Equal(t, single.ID, *got.SeriesID)
	s.requireNoDangling(t)
	// Counted once, under its real cause: not again by phase 2's reference guard.
	require.True(t, csLogContains(rep, "found live references in 1 single-book series and 0 merged-from series"), "%v", rep.logs)
	require.False(t, csLogContains(rep, "Reference guard kept"), "%v", rep.logs)
}

func TestCleanupSeriesRun_RealStore_KeepsASeriesRenamedAfterTheMembershipRead(t *testing.T) {
	// The name the job read came from the memdb; the delete compares it with the
	// Pebble row under the series lock. A rename in between must make them differ.
	s := newCsRealStore(t)
	single, _, _ := s.hushFixture(t)
	s.afterMembership = func() { require.NoError(t, s.UpdateSeriesName(single.ID, "Batman: Hush Returns")) }
	rep := &csPhase1Reporter{}

	require.NoError(t, (&cleanupSeriesJob{}).Run(context.Background(), s, rep, false))

	row, err := s.GetSeriesByID(single.ID)
	require.NoError(t, err)
	require.NotNil(t, row, "a series renamed since the read is somebody's edit: it stays")
	require.Equal(t, "Batman: Hush Returns", row.Name)
	require.True(t, csLogContains(rep, "kept 1 single-book series and 0 merged-from series whose row was renamed"), "%v", rep.logs)
}

func TestCleanupSeriesRun_RealStore_MergesADuplicateThroughTheConditionalDelete(t *testing.T) {
	s := newCsRealStore(t)
	dup, err := s.CreateSeries("Court of Owls", nil)
	require.NoError(t, err)
	keeper, err := s.CreateSeries("Court: of Owls", nil)
	require.NoError(t, err)
	// Sequence 2 keeps phase 1 away from the duplicate.
	two := 2
	moved := s.mkBook(t, "Court One", dup.ID)
	_, err = s.ModifyBook(moved.ID, func(b *database.Book) error { b.SeriesSequence = &two; return nil })
	require.NoError(t, err)
	s.mkBook(t, "Night One", keeper.ID)
	s.mkBook(t, "Night Two", keeper.ID)

	require.NoError(t, (&cleanupSeriesJob{}).Run(context.Background(), s, &csPhase1Reporter{}, false))

	row, err := s.GetSeriesByID(dup.ID)
	require.NoError(t, err)
	require.Nil(t, row, "the merged-from series is deleted once nothing references it")
	got, err := s.GetBookByID(moved.ID)
	require.NoError(t, err)
	require.NotNil(t, got.SeriesID)
	require.Equal(t, keeper.ID, *got.SeriesID)
	s.requireNoDangling(t)
}
