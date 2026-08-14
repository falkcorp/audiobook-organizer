// file: internal/database/series_getter_conformance_test.go
// version: 1.0.0
// guid: 9a4c72e1-3f68-4d20-8b95-6c07e3d1a4f8
// last-edited: 2026-08-14

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// seriesGetterConformanceFixture builds one series whose books exercise every
// axis GetBooksBySeriesIDCore filters or orders on.
//
// Helper name is task-unique on purpose: several suites in this package define
// fixture builders and a generic name collides on rebase.
type seriesGetterConformanceFixture struct {
	seriesID int

	// Live primary books, in the order the series listing should return them:
	// by SeriesSequence ascending, then title.
	wantOrderedIDs []string

	nonPrimaryBookID  string
	softDeletedBookID string
	otherSeriesBookID string
}

func buildSeriesGetterConformanceFixture(t *testing.T, store Store) seriesGetterConformanceFixture {
	t.Helper()

	var fx seriesGetterConformanceFixture

	series, err := store.CreateSeries("Conformance Series", nil)
	require.NoError(t, err)
	fx.seriesID = series.ID

	other, err := store.CreateSeries("Some Other Series", nil)
	require.NoError(t, err)

	yes := true
	no := false

	mk := func(title string, seriesID int, seq *int, primary *bool, trash bool) *Book {
		b := &Book{
			Title:            title,
			FilePath:         "/lib/series/" + title,
			SeriesID:         &seriesID,
			SeriesSequence:   seq,
			IsPrimaryVersion: primary,
		}
		created, cErr := store.CreateBook(b)
		require.NoError(t, cErr)
		if trash {
			created.MarkedForDeletion = &yes
			_, uErr := store.UpdateBook(created.ID, created)
			require.NoError(t, uErr)
		}
		return created
	}

	seq := func(n int) *int { return &n }

	// Created OUT of sequence order on purpose. If a path does not sort, the
	// ULID/key order it returns will not match wantOrderedIDs, and creating
	// them pre-sorted would hide exactly that.
	third := mk("Book Three", fx.seriesID, seq(3), &yes, false)
	first := mk("Book One", fx.seriesID, seq(1), &yes, false)
	second := mk("Book Two", fx.seriesID, seq(2), &yes, false)

	// Two books SHARING a sequence number, created with their titles in
	// reverse. This is what exercises the title tiebreaker, and it is not
	// decoration: the tiebreaker read a stale, unpermuted keys slice until
	// 2026-08-14, so with distinct sequence numbers alone the bug is invisible.
	// "Zulu" is created before "Alpha" so a comparator that is merely stable
	// (rather than correct) leaves them in the wrong order.
	tieZulu := mk("Book Four Zulu", fx.seriesID, seq(4), &yes, false)
	tieAlpha := mk("Book Four Alpha", fx.seriesID, seq(4), &yes, false)

	fx.wantOrderedIDs = []string{first.ID, second.ID, third.ID, tieAlpha.ID, tieZulu.ID}

	// Non-primary version of a book in the series: a duplicate of something
	// already listed, so the listing view must exclude it.
	fx.nonPrimaryBookID = mk("Book Two Alternate Rip", fx.seriesID, seq(2), &no, false).ID

	// In the series but in the trash.
	fx.softDeletedBookID = mk("Book Four Trashed", fx.seriesID, seq(4), &yes, true).ID

	// Control: an implementation that ignored seriesID entirely would pass
	// every "contains" assertion without this.
	fx.otherSeriesBookID = mk("Wrong Series Book", other.ID, seq(1), &yes, false).ID

	return fx
}

// TestGetBooksBySeriesIDCore_MemDBAndPebbleAgree is the conformance gate for the
// series listing getter.
//
// This is the same defect shape as the author getters (see
// author_getter_conformance_test.go), found by sweeping the other 25
// dual-dispatch store methods for it. GetBooksBySeriesIDCore has two backing
// implementations that disagreed on TWO axes at once:
//
//   - the memdb walk excluded non-primary versions; the Pebble scan kept them,
//     so a series listing served during the ~132 s warmup window showed every
//     alternate rip alongside the book it duplicates;
//   - the memdb walk sorted by SeriesSequence then title; the Pebble scan did
//     not sort at all, so the same series came back in ULID order.
//
// Sequence order is the whole point of a series listing, so the second half is
// not cosmetic either: "book 1, book 2, book 3" became whatever order the keys
// happened to be in.
//
// Asserting each path against its own hardcoded expectation would not have
// caught this. What catches it is holding the two implementations to the SAME
// answer on the SAME fixture.
func TestGetBooksBySeriesIDCore_MemDBAndPebbleAgree(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	fx := buildSeriesGetterConformanceFixture(t, store)

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")
	p.WaitForWarmup()
	require.True(t, p.IsMemReady(),
		"memdb must be published or the UseMemDB=true arm silently runs the Pebble path")

	// Non-vacuity guards: without each of these rows the corresponding
	// assertion below is unfalsifiable.
	require.NotEmpty(t, fx.nonPrimaryBookID, "fixture must contain a non-primary version")
	require.NotEmpty(t, fx.softDeletedBookID, "fixture must contain a soft-deleted book")
	require.Greater(t, len(fx.wantOrderedIDs), 1, "fixture must contain enough books to order")

	got := map[bool][]string{}

	for _, useMemDB := range []bool{true, false} {
		p.UseMemDB = useMemDB
		label := "PebbleScanPath"
		if useMemDB {
			label = "MemDBPath"
		}

		t.Run(label, func(t *testing.T) {
			books, err := store.GetBooksBySeriesIDCore(fx.seriesID)
			require.NoError(t, err)

			ids := make([]string, 0, len(books))
			for _, b := range books {
				ids = append(ids, b.ID)
			}

			require.NotContains(t, ids, fx.nonPrimaryBookID,
				"non-primary version leaked into the series listing — it duplicates a book "+
					"already in the list")
			require.NotContains(t, ids, fx.softDeletedBookID,
				"soft-deleted book leaked into the series listing")
			require.NotContains(t, ids, fx.otherSeriesBookID,
				"book from a different series was returned")

			// Order is asserted, not just membership: a series listing that
			// returns the right books in the wrong order is still broken.
			require.Equal(t, fx.wantOrderedIDs, ids,
				"series listing did not return the live primary books ordered by "+
					"SeriesSequence — the fixture is created out of sequence order on "+
					"purpose, so an unsorted path fails here")

			got[useMemDB] = ids
		})
	}
	p.UseMemDB = true

	require.Equal(t, got[true], got[false],
		"memdb and Pebble implementations of GetBooksBySeriesIDCore returned different "+
			"results — same defect shape as the author getters fixed alongside this")
}
