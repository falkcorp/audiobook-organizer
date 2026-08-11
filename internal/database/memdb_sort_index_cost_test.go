// file: internal/database/memdb_sort_index_cost_test.go
// version: 1.0.1
// guid: 6d0b3e57-41ac-4928-b3f6-8e17c2a95d10
// last-edited: 2026-08-11
//
// Measures what the sorted secondary indexes cost, because "tens of MB per
// field" was an estimate in the design doc and estimates are not measurements.
//
// Adding nine indexes to the books table is a production-affecting change, so
// the insert cost and the resident cost get measured on the same shape of
// data before it ships, not after someone notices a slower boot.
//
// Run:
//   go test ./internal/database/ -run TestSortIndexCost -v -count=1
//
// Compare against a tree without the indexes by running the identical file
// there — the numbers only mean something as a delta.
//
// ⚠️ MEASURED RESULT, 2026-08-09 (100,000 books, same fixture both sides):
//
//	                    without      with 9 indexes    delta
//	  heap per book     2,645 B      6,395 B           +142%  (+3,750 B)
//	  insert 100K       335 ms       935 ms            2.8x slower
//
// The reason is that go-memdb is an IMMUTABLE radix tree: every insert
// path-copies nodes from root to leaf, so the per-index cost is dominated by
// node allocation rather than key length — short keys do not make it cheap.
//
// This figure is a LOWER bound: the fixture leaves Author and Series unset,
// so two of the six physical indexes are storing the 1-byte "missing" key
// for nearly every row. A library with populated author/series data pays
// more.
//
// 🚨 EXTRAPOLATION CORRECTED 2026-08-11 — READ BEFORE QUOTING A TOTAL.
//
// The per-book delta above is a real measurement and is unchanged. What was
// wrong was the population it got multiplied by. This header used to read:
//
//	at 366,916 books  925.6 MB  2,237.8 MB  +1,312 MB
//	... ~146 MB per sort key ... enabling all nine would push memdb past 2.5 GB
//
// 366,916 was never a book count. It was the number of Pebble KEYS under the
// `book:` prefix, which is shared with roughly seven secondary-index families
// (book:path:, book:hash:, book:versiongroup:, book:work:, …) — about 7.5
// keys per actual row. See memdb_warmup.go and
// TestWarmupCounts_CountRowsNotPebbleKeys.
//
// The best current row count is ~48,900, from the organizer's own full paging
// enumeration on 2026-08-11 ("Fetched 48896 total books from database"),
// consistent with system status readings of 46,221 and 54,734.
//
// Re-extrapolated at 48,900 books:
//
//	                    without      with 9 indexes    delta
//	  books table       ~123 MB      ~298 MB           ~+175 MB
//
// So all nine sort keys cost roughly **175 MB, not 1.3 GB** — about 19 MB per
// key rather than 146 MB. The earlier figure overstated the cost by ~7.5x.
//
// This CHANGES THE DECISION it was gathered for, so it is flagged rather than
// quietly edited: "+1.3 GB on a box already at 1.25 GB" reads as prohibitive,
// while "+175 MB" does not. The owner's call either way — but on this number.
//
// Two things NOT claimed: the ~1.25 GB resident memdb figure is a real
// observation and is not contradicted here (memdb holds every table, and
// book_files alone is several hundred thousand rows — the books table is not
// the bulk of it); and 48,900 is the best available count, not a verified one.
// The definitive number comes from the row/key-separated warmup counter once
// it is deployed.

package database

import (
	"fmt"
	"runtime"
	"testing"
	"time"
)

// costBookCount is deliberately ABOVE the production book count (~48,900) so
// the per-book figure is measured on a tree at least as deep as the real one;
// the per-book cost is what extrapolates.
const costBookCount = 100_000

// prodBookCount is the best available production row count — the organizer's
// own full paging enumeration on 2026-08-11. NOT the 366,916 this file used
// to extrapolate to: that was Pebble keys under the `book:` prefix, ~7.5 per
// row. See the header for the full correction.
const prodBookCount = 48_900

func TestSortIndexCost(t *testing.T) {
	enableAllSortIndexes(t)
	if testing.Short() {
		t.Skip("cost measurement: not a correctness test, skipped in -short")
	}

	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	books := make([]Book, costBookCount)
	for i := range books {
		d := i % 100000
		fs := int64(i) * 1024
		br := 64 + (i % 256)
		yr := 1900 + (i % 130)
		created := base.Add(time.Duration(i) * time.Minute)
		updated := base.Add(time.Duration(i*2) * time.Minute)
		narrator := fmt.Sprintf("Narrator %06d", i%5000)
		books[i] = Book{
			ID:                   fmt.Sprintf("book-%08d", i),
			Title:                fmt.Sprintf("Title %08d", i),
			Narrator:             &narrator,
			Duration:             &d,
			FileSize:             &fs,
			Bitrate:              &br,
			AudiobookReleaseYear: &yr,
			CreatedAt:            &created,
			UpdatedAt:            &updated,
		}
	}

	m, err := NewMemStore()
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}

	// Settle the heap before the baseline reading so the fixture itself is
	// not counted as index cost.
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	start := time.Now()
	seedMemStore(t, m, books, nil, nil, nil)
	insertTook := time.Since(start)

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	// Both KeepAlives are load-bearing, and each was found by the delta
	// coming out wrong:
	//
	//   - without KeepAlive(books) the delta is NEGATIVE (~-100MB): `books`
	//     is dead once seedMemStore returns, so the GC between the two
	//     readings frees the fixture that was live during the first one.
	//   - without KeepAlive(m) the delta is ~ZERO: `m` is dead too, so the
	//     entire memdb tree — the thing being measured — is collected before
	//     the second reading.
	//
	// Go's GC collects from the last USE, not the end of scope, so a
	// measurement bracketing an object's last use measures nothing.
	runtime.KeepAlive(books)
	runtime.KeepAlive(m)

	heapDelta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	perBook := float64(heapDelta) / float64(costBookCount)

	t.Logf("books inserted:      %d", costBookCount)
	t.Logf("insert wall time:    %s (%.1f books/sec)",
		insertTook.Round(time.Millisecond),
		float64(costBookCount)/insertTook.Seconds())
	t.Logf("heap delta:          %.1f MB", float64(heapDelta)/(1024*1024))
	t.Logf("heap per book:       %.0f bytes", perBook)
	t.Logf("extrapolated to prod (%d books): %.1f MB, insert %.1fs",
		prodBookCount,
		perBook*prodBookCount/(1024*1024),
		insertTook.Seconds()*prodBookCount/float64(costBookCount))

	// Sanity, not a threshold: a wildly wrong number means the measurement
	// is broken rather than that the indexes are bad.
	if heapDelta <= 0 {
		t.Fatalf("heap delta %d is not positive — measurement is broken", heapDelta)
	}
}
