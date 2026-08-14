// file: internal/database/aggregate_count_conformance_test.go
// version: 1.0.0
// guid: 6f2a83d5-14c7-4e91-a06b-5d38e7b2f94c
// last-edited: 2026-08-14

package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aggregateCountConformanceFixture builds a library where the live and trashed
// halves are deliberately DIFFERENT sizes, so a count that includes the trash
// cannot coincidentally match a count that excludes it.
//
// Helper name is task-unique on purpose: several suites in this package define
// fixture builders and a generic name collides on rebase.
type aggregateCountConformanceFixture struct {
	seriesID int

	liveBooks    int
	trashedBooks int

	// Every book is created under this path prefix so CountBooksByPathPrefix
	// sees the same live/trashed split as the series counters.
	pathPrefix string
}

func buildAggregateCountConformanceFixture(t *testing.T, store Store) aggregateCountConformanceFixture {
	t.Helper()

	fx := aggregateCountConformanceFixture{pathPrefix: "/lib/aggcount/"}

	series, err := store.CreateSeries("Aggregate Count Series", nil)
	require.NoError(t, err)
	fx.seriesID = series.ID

	yes := true

	mk := func(title string, trash bool) {
		b := &Book{
			Title:            title,
			FilePath:         fx.pathPrefix + title,
			SeriesID:         &fx.seriesID,
			IsPrimaryVersion: &yes,
		}
		created, cErr := store.CreateBook(b)
		require.NoError(t, cErr)

		// One file per book, so the *FileCounts variants have something to
		// count and inherit the same live/trashed split.
		require.NoError(t, store.CreateBookFile(&BookFile{
			BookID:   created.ID,
			FilePath: fx.pathPrefix + title + "/01.m4b",
		}))

		if trash {
			// Soft-delete through the real update path so the memdb re-index
			// runs, matching how a real deletion reaches the store.
			created.MarkedForDeletion = &yes
			_, uErr := store.UpdateBook(created.ID, created)
			require.NoError(t, uErr)
		}
	}

	for _, title := range []string{"Live One", "Live Two", "Live Three", "Live Four", "Live Five"} {
		mk(title, false)
		fx.liveBooks++
	}
	for _, title := range []string{"Trashed One", "Trashed Two"} {
		mk(title, true)
		fx.trashedBooks++
	}

	return fx
}

// TestAggregateCounts_MemDBAndPebbleAgree is the conformance gate for the
// per-series and per-path aggregate counters.
//
// These are the counts that feed the series list and the import-path screens.
// Their two implementations disagreed about the trash: the memdb walks excluded
// soft-deleted books, the Pebble scans counted them. So for the ~132 s it takes
// memdb to warm up after a restart, a series reported more books than it has and
// an import path reported more books than it holds.
//
// This is the same defect class as #2392 (soft-deleted rows leaking into
// GetAllBooksCore) but leaking from the OTHER store, which is the argument for
// testing conformance rather than auditing one implementation: whichever path a
// reviewer reads, the bug is in the one they did not.
//
// The fixture uses 5 live and 2 trashed books on purpose. Equal halves would let
// an off-by-one-direction implementation pass by coincidence.
func TestAggregateCounts_MemDBAndPebbleAgree(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	fx := buildAggregateCountConformanceFixture(t, store)

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")
	p.WaitForWarmup()
	require.True(t, p.IsMemReady(),
		"memdb must be published or the UseMemDB=true arm silently runs the Pebble path")

	// Non-vacuity guards: without trashed rows every assertion below passes
	// against an implementation that ignores the trash entirely.
	require.NotZero(t, fx.trashedBooks, "fixture must contain soft-deleted books")
	require.NotEqual(t, fx.liveBooks, fx.trashedBooks,
		"live and trashed counts must differ or a wrong answer can coincide with the right one")

	type snapshot struct {
		seriesBooks int
		seriesFiles int
		pathBooks   int
	}
	got := map[bool]snapshot{}

	for _, useMemDB := range []bool{true, false} {
		p.UseMemDB = useMemDB
		label := "PebbleScanPath"
		if useMemDB {
			label = "MemDBPath"
		}

		t.Run(label, func(t *testing.T) {
			bookCounts, err := store.GetAllSeriesBookCounts()
			require.NoError(t, err)
			fileCounts, err := store.GetAllSeriesFileCounts()
			require.NoError(t, err)
			pathCount, err := store.CountBooksByPathPrefix(fx.pathPrefix)
			require.NoError(t, err)

			// assert, not require: these three are independent counters and a
			// reader needs to see WHICH of them drifted. require aborts the
			// subtest at the first failure, which hides the others — and the
			// path-prefix counter in particular has its own dispatch bug, so
			// masking it behind the series counter is how it stayed invisible.
			assert.Equal(t, fx.liveBooks, bookCounts[fx.seriesID],
				"series book count included soft-deleted books — the series list would "+
					"report more books than the series has")
			assert.Equal(t, fx.liveBooks, fileCounts[fx.seriesID],
				"series file count included files belonging to soft-deleted books")
			assert.Equal(t, fx.liveBooks, pathCount,
				"import-path book count included soft-deleted books")

			got[useMemDB] = snapshot{
				seriesBooks: bookCounts[fx.seriesID],
				seriesFiles: fileCounts[fx.seriesID],
				pathBooks:   pathCount,
			}
		})
	}
	p.UseMemDB = true

	require.Equal(t, got[true], got[false],
		"memdb and Pebble implementations of the aggregate counters disagree")
}

// A note on the selector, because it is what makes the loop above mean
// anything.
//
// CountBooksByPathPrefix dispatched on memdb PUBLICATION alone
// (`p.mem() != nil`) rather than on UseMemDB, so its Pebble branch was
// unreachable whenever memdb was up. That is worse than a dormant fallback: it
// silently reduced the UseMemDB=false arm above to a second run of the memdb
// code, and the pathBooks assertion passed while proving nothing.
// ListBooksByITunesPID had the same defect, fixed 2026-08-14.
//
// A standalone "does it respect the flag?" test cannot detect this once both
// implementations are correct, because by then they agree by construction — it
// would pass whichever branch ran, which is the same trap one level up. What
// actually demonstrated the fix was the sequence: with the selector repaired
// and the soft-delete filter still missing, the pathBooks assertion above began
// failing (7 vs 5) where it had previously passed. That transition is the
// evidence the Pebble branch is now genuinely reached.
