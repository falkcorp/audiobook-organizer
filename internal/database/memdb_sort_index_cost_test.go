// file: internal/database/memdb_sort_index_cost_test.go
// version: 1.0.0
// guid: 6d0b3e57-41ac-4928-b3f6-8e17c2a95d10
// last-edited: 2026-08-09
//
// Measures what the sorted secondary indexes cost, because "tens of MB per
// field" was an estimate in the design doc and estimates are not measurements.
//
// Prod is 366,916 books with memdb warmup at 107.9s and ~1.25GB resident.
// Adding nine indexes to that structure is a production-affecting change, so
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
//	  heap per book     2,645 B      6,395 B           +142%
//	  at 366,916 books  925.6 MB     2,237.8 MB        +1,312 MB
//	  insert 100K       335 ms       935 ms            2.8x slower
//
// That is ~146 MB per sort key. The design doc estimated "tens of MB per
// sort field" and was optimistic by roughly an order of magnitude.
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
// Consequence: memdb is already ~1.25 GB resident with a 107.9s warmup.
// Enabling all nine would push it past ~2.5 GB. That is a decision to take
// with the number in hand, not on the estimate.

package database

import (
	"fmt"
	"runtime"
	"testing"
	"time"
)

// costBookCount is deliberately below prod's 366,916 so the test stays
// usable in CI; the per-book cost is what extrapolates.
const costBookCount = 100_000

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
	t.Logf("extrapolated to prod (366,916 books): %.1f MB, insert %.1fs",
		perBook*366916/(1024*1024),
		insertTook.Seconds()*366916/float64(costBookCount))

	// Sanity, not a threshold: a wildly wrong number means the measurement
	// is broken rather than that the indexes are bad.
	if heapDelta <= 0 {
		t.Fatalf("heap delta %d is not positive — measurement is broken", heapDelta)
	}
}
