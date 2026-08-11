// file: internal/database/pebble_store_versiongroup_backfill_test.go
// version: 1.0.0
// guid: 4b8e1d07-9a3c-4f52-8e61-7d0c2a9f4b13
// last-edited: 2026-08-10

package database

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

// countVGIndexRows returns how many book:versiongroup:* rows exist.
func countVGIndexRows(t *testing.T, s *PebbleStore) int {
	t.Helper()
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:versiongroup:"),
		UpperBound: []byte("book:versiongroup;"),
	})
	if err != nil {
		t.Fatalf("NewIter: %v", err)
	}
	defer iter.Close()
	n := 0
	for iter.First(); iter.Valid(); iter.Next() {
		n++
	}
	return n
}

// seedGroupedBooks creates n books sharing one version group and returns their
// IDs. Index rows are deleted afterwards so the backfill has work to do —
// CreateBook already writes them.
func seedGroupedBooks(t *testing.T, s *PebbleStore, gid string, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		b := &Book{
			Title:          fmt.Sprintf("Backfill Book %d", i),
			VersionGroupID: &gid,
		}
		created, err := s.CreateBook(b)
		if err != nil {
			t.Fatalf("CreateBook %d: %v", i, err)
		}
		ids = append(ids, created.ID)
	}
	for _, id := range ids {
		key := []byte(fmt.Sprintf("book:versiongroup:%s:%s", gid, id))
		if err := s.db.Delete(key, pebble.Sync); err != nil {
			t.Fatalf("Delete index row: %v", err)
		}
	}
	if got := countVGIndexRows(t, s); got != 0 {
		t.Fatalf("precondition: expected 0 index rows after wipe, got %d", got)
	}
	return ids
}

// The sentinel must gate re-runs, and — because the sentinel is what makes the
// run cheap — a second call must not rewrite anything. The bug this guards is
// the inverse too: if the sentinel read ever misreports, a six-figure library
// rebuilds its whole index on every boot.
func TestBackfillVersionGroupIndex_SecondRunIsANoOp(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()
	s.WaitForWarmup()

	gid := "vg-backfill-idempotent"
	ids := seedGroupedBooks(t, s, gid, 5)

	if err := s.BackfillVersionGroupIndex(); err != nil {
		t.Fatalf("first backfill: %v", err)
	}
	after := countVGIndexRows(t, s)
	if after != len(ids) {
		t.Fatalf("first backfill wrote %d index rows, want %d", after, len(ids))
	}

	// Wipe one row, then re-run. The sentinel is set, so the run must skip and
	// leave the damage in place — proving the second call really is gated and
	// is not quietly redoing the full scan.
	damaged := []byte(fmt.Sprintf("book:versiongroup:%s:%s", gid, ids[0]))
	if err := s.db.Delete(damaged, pebble.Sync); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.BackfillVersionGroupIndex(); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if got := countVGIndexRows(t, s); got != len(ids)-1 {
		t.Fatalf("second backfill was NOT a no-op: %d rows, want %d", got, len(ids)-1)
	}
}

// Re-running the work itself (sentinel cleared) must converge on the same
// state, not accumulate or drop rows. This is the property that makes an
// interrupted run safe to resume.
func TestBackfillVersionGroupIndex_RerunConvergesToSameState(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()
	s.WaitForWarmup()

	gid := "vg-backfill-converge"
	ids := seedGroupedBooks(t, s, gid, 7)

	if err := s.BackfillVersionGroupIndex(); err != nil {
		t.Fatalf("first backfill: %v", err)
	}
	first := countVGIndexRows(t, s)

	// Clear the sentinel and run the real work a second time.
	if err := s.db.Delete([]byte(versionGroupBackfillKey), pebble.Sync); err != nil {
		t.Fatalf("clear sentinel: %v", err)
	}
	if err := s.BackfillVersionGroupIndex(); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	second := countVGIndexRows(t, s)

	if first != len(ids) || second != first {
		t.Fatalf("not convergent: first=%d second=%d want both %d", first, second, len(ids))
	}
}

// The chunked commit path must write EVERY row, not just the last partial
// chunk. Before chunking existed, one unbounded batch buffered the whole
// rebuild; the risk introduced by chunking is dropping rows at a boundary, so
// exercise several boundaries with an exact multiple and a remainder.
func TestBackfillVersionGroupIndex_ChunkedCommitWritesEveryRow(t *testing.T) {
	for _, tc := range []struct{ books, chunk int }{
		{books: 9, chunk: 2},  // remainder
		{books: 8, chunk: 2},  // exact multiple — final flush has nothing to do
		{books: 5, chunk: 1},  // commit every row
		{books: 4, chunk: 99}, // never reaches a chunk boundary
	} {
		t.Run(fmt.Sprintf("books=%d_chunk=%d", tc.books, tc.chunk), func(t *testing.T) {
			orig := versionGroupBackfillChunk
			versionGroupBackfillChunk = tc.chunk
			t.Cleanup(func() { versionGroupBackfillChunk = orig })

			s, err := NewPebbleStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewPebbleStore: %v", err)
			}
			defer s.Close()
			s.WaitForWarmup()

			gid := "vg-backfill-chunk"
			ids := seedGroupedBooks(t, s, gid, tc.books)

			if err := s.BackfillVersionGroupIndex(); err != nil {
				t.Fatalf("backfill: %v", err)
			}
			if got := countVGIndexRows(t, s); got != len(ids) {
				t.Fatalf("chunked backfill wrote %d rows, want %d", got, len(ids))
			}
			// Every specific ID must be present — a count alone would pass if
			// a boundary row were replaced by a duplicate of another.
			for _, id := range ids {
				key := []byte(fmt.Sprintf("book:versiongroup:%s:%s", gid, id))
				val, closer, err := s.db.Get(key)
				if err != nil {
					t.Fatalf("missing index row for %s: %v", id, err)
				}
				if string(val) != id {
					t.Fatalf("index row for %s holds %q", id, string(val))
				}
				closer.Close()
			}
		})
	}
}

// The scan must consider only primary `book:<id>` rows. The previous filter was
// a blacklist of index prefixes — it listed ":organizedhash:" twice and would
// have started unmarshalling any prefix added later and forgotten. The rule is
// now structural: book IDs are ULIDs and carry no colons, so a primary row has
// exactly one.
func TestBackfillVersionGroupIndex_IgnoresSecondaryIndexRows(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()
	s.WaitForWarmup()

	gid := "vg-backfill-prefixes"
	ids := seedGroupedBooks(t, s, gid, 3)

	// A secondary-index row that (a) no blacklist in the old code mentioned and
	// (b) falls INSIDE the iterator's [book:0, book:;) bounds, so the in-loop
	// filter is genuinely what has to reject it. Keys such as "book:path:..."
	// start with 'p' (0x70) and are already excluded by the upper bound, which
	// is why the old substring blacklist was mostly unreachable — and why a
	// test using one of those would pass no matter how broken the filter was.
	// This key starts with '0' (0x30) and is therefore scanned.
	bogus := []byte(`{"id":"NOT-A-REAL-BOOK","version_group_id":"` + gid + `"}`)
	if err := s.db.Set([]byte("book:0futureindex:xyz"), bogus, pebble.Sync); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := s.BackfillVersionGroupIndex(); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if got := countVGIndexRows(t, s); got != len(ids) {
		t.Fatalf("backfill wrote %d rows, want %d — a secondary index row was scanned", got, len(ids))
	}
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:versiongroup:"),
		UpperBound: []byte("book:versiongroup;"),
	})
	if err != nil {
		t.Fatalf("NewIter: %v", err)
	}
	defer iter.Close()
	for iter.First(); iter.Valid(); iter.Next() {
		if strings.Contains(string(iter.Key()), "NOT-A-REAL-BOOK") {
			t.Fatalf("backfill indexed a secondary-index row: %s", iter.Key())
		}
	}
}
