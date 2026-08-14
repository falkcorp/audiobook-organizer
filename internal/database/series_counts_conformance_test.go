// file: internal/database/series_counts_conformance_test.go
// version: 1.0.0
// guid: 7c1f4a9e-2b83-4d51-9f6a-0e5c8d3b7a12
// last-edited: 2026-08-14

package database

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// seriesCountsFixture builds two series whose live/trashed split makes the
// soft-delete filter falsifiable: if an implementation forgets to skip trashed
// books, seriesA's book count reads 4 instead of 2 and its file count reads 4
// instead of 2. seriesB has no trash at all, so a mutation that skips *every*
// book (rather than just the deleted ones) also fails.
type seriesCountsFixture struct {
	seriesA         int
	seriesB         int
	liveBookIDs     []string
	trashedBookIDs  []string
	wantBookCountsA int
	wantFileCountsA int
	wantBookCountsB int
	wantFileCountsB int
}

func buildSeriesCountsFixture(t *testing.T, store Store) seriesCountsFixture {
	t.Helper()

	yes := true
	var fx seriesCountsFixture

	sa, err := store.CreateSeries("Conformance Series A", nil)
	require.NoError(t, err)
	sb, err := store.CreateSeries("Conformance Series B", nil)
	require.NoError(t, err)
	fx.seriesA, fx.seriesB = sa.ID, sb.ID

	// mk creates a book in a series with `files` audio files attached.
	mk := func(title string, seriesID int, files int) *Book {
		sid := seriesID
		created, err := store.CreateBook(&Book{
			Title:    title,
			FilePath: "/lib/" + title,
			SeriesID: &sid,
		})
		require.NoError(t, err)
		for i := 0; i < files; i++ {
			require.NoError(t, store.CreateBookFile(&BookFile{
				BookID:   created.ID,
				FilePath: fmt.Sprintf("/lib/%s/part%d.m4b", title, i),
				Format:   "m4b",
			}))
		}
		return created
	}

	for _, title := range []string{"Series A Live One", "Series A Live Two"} {
		fx.liveBookIDs = append(fx.liveBookIDs, mk(title, fx.seriesA, 1).ID)
	}

	// Soft-delete through UpdateBook so the memdb re-index runs — a row born
	// deleted would skip the live->trashed transition these methods must survive.
	for _, title := range []string{"Series A Trashed One", "Series A Trashed Two"} {
		created := mk(title, fx.seriesA, 1)
		created.MarkedForDeletion = &yes
		_, err := store.UpdateBook(created.ID, created)
		require.NoError(t, err)
		fx.trashedBookIDs = append(fx.trashedBookIDs, created.ID)
	}

	fx.liveBookIDs = append(fx.liveBookIDs, mk("Series B Live One", fx.seriesB, 2).ID)

	fx.wantBookCountsA, fx.wantFileCountsA = 2, 2
	fx.wantBookCountsB, fx.wantFileCountsB = 1, 2
	return fx
}

// TestSeriesCounts_ExcludeTrashOnBothPaths is the conformance gate for the two
// series aggregate counters.
//
// These drifted: MemStore.GetAllSeriesBookCounts / GetAllSeriesFileCounts have
// always skipped soft-deleted books, while the Pebble scans counted them. memdb
// is the production default, so the visible effect was confined to cold start
// (before warmup publishes) and to any deployment running with UseMemDB=false --
// a series' book and file counts would jump by the size of its trash and then
// settle, with no error anywhere to explain the change.
//
// Both methods already gated on `p.UseMemDB && p.mem() != nil`, so unlike
// ListBooksByITunesPID the flag flip below genuinely selected two paths before
// this test existed; the IsMemReady guard keeps it that way.
func TestSeriesCounts_ExcludeTrashOnBothPaths(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	fx := buildSeriesCountsFixture(t, store)

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")
	p.WaitForWarmup()
	require.True(t, p.IsMemReady(),
		"memdb must be published or the UseMemDB=true arm silently runs the Pebble path")

	// Non-vacuity: with no trashed rows the exclusion assertion is unfalsifiable,
	// and with no live rows an implementation returning nothing would pass.
	require.NotEmpty(t, fx.trashedBookIDs, "fixture must contain soft-deleted books in a series")
	require.NotEmpty(t, fx.liveBookIDs, "fixture must contain live books in a series")

	originalUseMemDB := p.UseMemDB
	defer func() { p.UseMemDB = originalUseMemDB }()

	bookCounts := map[bool]map[int]int{}
	fileCounts := map[bool]map[int]int{}

	for _, useMemDB := range []bool{true, false} {
		p.UseMemDB = useMemDB
		label := "PebbleScanPath"
		if useMemDB {
			label = "MemDBPath"
		}

		t.Run(label, func(t *testing.T) {
			bc, err := p.GetAllSeriesBookCounts()
			require.NoError(t, err)
			fc, err := p.GetAllSeriesFileCounts()
			require.NoError(t, err)

			require.Equal(t, fx.wantBookCountsA, bc[fx.seriesA],
				"series A book count must exclude its %d trashed books", len(fx.trashedBookIDs))
			require.Equal(t, fx.wantFileCountsA, fc[fx.seriesA],
				"series A file count must exclude files of trashed books")
			require.Equal(t, fx.wantBookCountsB, bc[fx.seriesB],
				"series B has no trash; its count must be unaffected")
			require.Equal(t, fx.wantFileCountsB, fc[fx.seriesB],
				"series B file count must be unaffected")

			bookCounts[useMemDB] = bc
			fileCounts[useMemDB] = fc
		})
	}

	// The conformance assertion proper: whatever the numbers are, both
	// implementations must produce the SAME ones for the series under test.
	require.Equal(t, bookCounts[true][fx.seriesA], bookCounts[false][fx.seriesA],
		"memdb and Pebble disagree on series A book count")
	require.Equal(t, fileCounts[true][fx.seriesA], fileCounts[false][fx.seriesA],
		"memdb and Pebble disagree on series A file count")
	require.Equal(t, bookCounts[true][fx.seriesB], bookCounts[false][fx.seriesB],
		"memdb and Pebble disagree on series B book count")
	require.Equal(t, fileCounts[true][fx.seriesB], fileCounts[false][fx.seriesB],
		"memdb and Pebble disagree on series B file count")
}
