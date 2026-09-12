// file: internal/database/book_scan_fault_test.go
// version: 1.0.0
// guid: 5d1f7c2e-8a4b-4e3f-9c6d-2b7a1e0f4d93
// last-edited: 2026-09-12

package database

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests pin rule 3 of book_row_iter.go: a Pebble scan that stops on a
// read error must fail, never hand back a short result that reads as complete.
//
// A Pebble iterator that hits an error mid-range reports Valid() == false,
// exactly as it does at the end of the range. setRowScanFault reproduces that
// on a real store: after N rows the next move reports "not valid" and the error
// is visible only through the iterator's error check. Until 2026-09-12, 34 of
// the 44 book-row scans (and every raw book_file: scan) never made that check,
// so each case below returned a partial list or count with a nil error.

var errInjectedScanFault = errors.New("injected mid-scan read error")

// TestForEachRow_FaultEndsScanWithError pins the helper itself: the scan stops
// after exactly the armed number of rows and returns the injected error, and
// errStopScan still ends a scan cleanly.
func TestForEachRow_FaultEndsScanWithError(t *testing.T) {
	p := setupTestPebbleStore(t)
	p.WaitForWarmup()
	fx := buildBookRowRangeFixture(t, p, false)
	all := len(fx.live) + len(fx.trashed)

	count := func() (int, error) {
		n := 0
		err := forEachBookRow(p.db, func(string, []byte) error { n++; return nil })
		return n, err
	}
	n, err := count()
	require.NoError(t, err)
	require.Equal(t, all, n, "baseline: every book row is visited")

	clear := setRowScanFault(p.db, 3, errInjectedScanFault)
	n, err = count()
	clear()
	require.ErrorIs(t, err, errInjectedScanFault, "a truncated scan must fail")
	require.Equal(t, 3, n, "the fault must land partway through the scan, not before it")

	clear = setRowScanFault(p.db, 1, errInjectedScanFault)
	visited := 0
	err = forEachKeyInRange(p.db, []byte("book_file:"), []byte("book_file;"), func(_, _ []byte) error {
		visited++
		return nil
	})
	clear()
	require.ErrorIs(t, err, errInjectedScanFault)
	require.Equal(t, 1, visited)

	seen := 0
	require.NoError(t, forEachBookRow(p.db, func(string, []byte) error {
		seen++
		return errStopScan
	}), "errStopScan ends the scan without an error")
	require.Equal(t, 1, seen)

	sentinel := errors.New("visit failed")
	require.ErrorIs(t, forEachBookRow(p.db, func(string, []byte) error { return sentinel }), sentinel,
		"a visit error comes back unchanged")
}

// TestBookScans_FailOnMidScanReadError runs every scan routed through the
// helpers against a real Pebble store, first clean (it must succeed, so the
// failure below is the fault's and not the fixture's) and then with a read
// fault armed partway through. Each must return the fault, not a short answer.
//
// Not covered here: the three scans that read through a snapshot
// (getAllAuthorBookRefBucketsPebble, getAllAuthorFileRefCountsPebble,
// GetAllNarratorRefs). The fault is keyed by reader and those open their own
// *pebble.Snapshot; they use the same helper, which the first test pins.
func TestBookScans_FailOnMidScanReadError(t *testing.T) {
	p := setupTestPebbleStore(t)
	p.WaitForWarmup()
	p.UseMemDB = false // every getter takes its Pebble branch
	fx := buildBookRowRangeFixture(t, p, false)

	// Rows for the families the book-row fixture leaves empty, two of each so
	// a fault after one row is partway through.
	for _, k := range []string{
		"book_file_errors_by_book:lower:/lib/range/a.mp3",
		"book_file_errors_by_book:Upper:/lib/range/b.mp3",
		"book:work:wk-fault:lower",
		"book:work:wk-fault:Upper",
	} {
		require.NoError(t, p.db.Set([]byte(k), []byte("x"), nil))
	}
	require.NoError(t, p.db.Set([]byte("work:w-fault-1"), []byte(`{"id":"w-fault-1","title":"one"}`), nil))
	require.NoError(t, p.db.Set([]byte("work:w-fault-2"), []byte(`{"id":"w-fault-2","title":"two"}`), nil))

	err1 := func(_ any, err error) error { return err }
	cases := []struct {
		name  string
		after int // rows delivered before the fault; 0 only for one-row scans
		run   func() error
	}{
		// book:<id> row scans (forEachBookRow / forEachBookRowAfter / forEachBareRow)
		{"CountSoftDeletedBooks", 1, func() error { return err1(p.CountSoftDeletedBooks(nil)) }},
		{"getAllBooksCoreFromPebble", 1, func() error { return err1(p.getAllBooksCoreFromPebble(0, 0)) }},
		{"GetAllBooksFullFrom", 1, func() error { return err1(p.GetAllBooksFullFrom("", 0)) }},
		{"ListBookIDs", 1, func() error { return err1(p.ListBookIDs()) }},
		{"walkFilteredBooksPebble", 1, func() error {
			return p.walkFilteredBooksPebble(BookSummaryFilter{}, func(*Book) bool { return true })
		}},
		{"GetBookByITunesPersistentID", 1, func() error { return err1(p.GetBookByITunesPersistentID("no-such-pid")) }},
		{"ListBooksByITunesPID", 1, func() error { return err1(p.ListBooksByITunesPID(0, 0)) }},
		{"GetDuplicateBooks", 1, func() error { return err1(p.GetDuplicateBooks()) }},
		{"GetBooksByTitleInDir", 1, func() error { return err1(p.GetBooksByTitleInDir("x", "/lib/range")) }},
		{"getBooksBySeriesIDFull", 1, func() error { return err1(p.getBooksBySeriesIDFull(fx.seriesID, false)) }},
		{"getBooksByAuthorIDFull", 1, func() error { return err1(p.getBooksByAuthorIDFull(1)) }},
		{"booksByAuthorIDForMutation", 1, func() error { return err1(p.booksByAuthorIDForMutation(1, true)) }},
		{"SearchBooks", 1, func() error { return err1(p.SearchBooks("no-such-title", 0, 0)) }},
		{"countPrimaryBooksScan", 1, func() error { return err1(p.countPrimaryBooksScan()) }},
		{"CountAllBooks", 1, func() error { return err1(p.CountAllBooks()) }},
		{"GetDistinctGenres", 1, func() error { return err1(p.GetDistinctGenres()) }},
		{"GetDistinctLanguages", 1, func() error { return err1(p.GetDistinctLanguages()) }},
		{"ListSoftDeletedBooks", 1, func() error { return err1(p.ListSoftDeletedBooks(0, 0, nil)) }},
		{"GetBooksByMetadataSourceHash", 1, func() error { return err1(p.GetBooksByMetadataSourceHash("no-such-hash")) }},
		{"GetDistinctPublishedYears", 1, func() error { return err1(p.GetDistinctPublishedYears()) }},
		{"computeQuickQueryCount", 1, func() error { return err1(p.computeQuickQueryCount("missing_covers")) }},
		{"GetAllBookIDsForQuickQuery", 1, func() error { return err1(p.GetAllBookIDsForQuickQuery("missing_covers")) }},
		{"CountFiles", 1, func() error { return err1(p.CountFiles()) }},
		{"computeLibraryStats", 1, func() error { return err1(p.computeLibraryStats()) }},
		{"GetITunesPurgePendingBooks", 1, func() error { return err1(p.GetITunesPurgePendingBooks()) }},
		{"GetITunesDirtyBooks", 1, func() error { return err1(p.GetITunesDirtyBooks()) }},
		{"getAllSeriesBookRefCountsPebble", 1, func() error { return err1(p.getAllSeriesBookRefCountsPebble()) }},
		{"GetAllWorks_Pebble", 1, func() error { return err1(p.GetAllWorks_Pebble()) }},
		{"GetAllWorkBookCounts", 1, func() error { return err1(p.GetAllWorkBookCounts()) }},
		{"CountBooksByPathPrefix", 1, func() error { return err1(p.CountBooksByPathPrefix("/lib")) }},
		{"getAllBooksPebbleScan", 1, func() error { return err1(p.getAllBooksPebbleScan()) }},
		{"BackfillVersionGroupIndex", 1, func() error {
			// Clear the sentinel so the backfill scans instead of returning early.
			require.NoError(t, p.db.Delete([]byte(versionGroupBackfillKey), nil))
			return p.BackfillVersionGroupIndex()
		}},
		{"GetQuarantinedBooks", 1, func() error { return err1(p.GetQuarantinedBooks(0, 0)) }},
		{"CountQuarantinedBooks", 1, func() error { return err1(p.CountQuarantinedBooks()) }},
		{"GetAllSeriesBookCounts_Pebble", 1, func() error { return err1(p.GetAllSeriesBookCounts_Pebble()) }},
		{"GetAllSeriesFileCounts", 1, func() error { return err1(p.GetAllSeriesFileCounts()) }},
		{"GetAllAuthorBookCounts", 1, func() error { return err1(p.GetAllAuthorBookCounts()) }},
		{"GetAllAuthorFileCounts_Pebble", 1, func() error { return err1(p.GetAllAuthorFileCounts_Pebble()) }},
		{"BackfillBookFileScanCache", 1, func() error { return err1(p.BackfillBookFileScanCache(true)) }},
		{"GetDirtyBookFolders", 1, func() error { return err1(p.GetDirtyBookFolders()) }},

		// raw-range scans in the book / book_file families (forEachKeyInRange)
		{"GetBookFiles", 1, func() error { return err1(p.GetBookFiles("lower")) }},
		{"getBookFilesForIDsPebbleScan", 1, func() error { return err1(p.getBookFilesForIDsPebbleScan([]string{"lower"})) }},
		{"getAllBookFilesPebbleScan", 1, func() error { return err1(p.getAllBookFilesPebbleScan()) }},
		{"scanForBookFileByID", 1, func() error { return err1(p.scanForBookFileByID("f3")) }},
		{"GetScanCacheMap", 1, func() error { return err1(p.GetScanCacheMap()) }},
		{"loadBookFilesForBookID", 1, func() error { return err1(p.loadBookFilesForBookID("lower")) }},
		{"ListBooksWithFileErrors", 1, func() error { return err1(p.ListBooksWithFileErrors()) }},
		{"GetBooksByVersionGroup", 1, func() error { return err1(p.GetBooksByVersionGroup("vg-range")) }},
		{"GetBooksByWorkID", 1, func() error { return err1(p.GetBooksByWorkID("wk-fault")) }},
		{"GetBookIDsByISBNASIN", 0, func() error { return err1(p.GetBookIDsByISBNASIN("", "", "B0RANGElower")) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, tc.run(), "baseline: the scan must succeed with no fault armed")

			clear := setRowScanFault(p.db, tc.after, errInjectedScanFault)
			defer clear()
			require.ErrorIs(t, tc.run(), errInjectedScanFault,
				"a scan truncated by a read error must return that error, not a short result")
		})
	}
}
