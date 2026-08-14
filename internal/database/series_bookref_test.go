// file: internal/database/series_bookref_test.go
// version: 1.0.0
// guid: 8f2c14ba-6d97-4e35-b0a1-72e5c9d38a04
// last-edited: 2026-08-14

package database

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests exist because a DISPLAY counter was used as an EXISTENCE test.
// GetAllSeriesBookCounts skips trashed and non-primary books — correct for a
// badge, catastrophic for "is it safe to delete this row". On production
// 2026-08-14 that had already stranded 13,322 live books on 6,893 series IDs
// that no longer resolved. Every assertion below is about the difference
// between the two questions.

func boolp(b bool) *bool { return &b }
func intp(i int) *int    { return &i }

// seedRefStore writes books through the normal path, then applies raw flag
// updates. Books are created with CreateBook so the memdb and Pebble both see
// them exactly as production does.
func seedRefStore(t *testing.T, dir string) *PebbleStore {
	t.Helper()
	store, err := NewPebbleStore(dir)
	require.NoError(t, err)
	store.WaitForWarmup()
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func mkBook(t *testing.T, s *PebbleStore, title string, seriesID int, primary, trashed bool) *Book {
	t.Helper()
	b, err := s.CreateBook(&Book{
		Title:             title,
		FilePath:          "/ref/" + title,
		SeriesID:          intp(seriesID),
		IsPrimaryVersion:  boolp(primary),
		MarkedForDeletion: boolp(trashed),
	})
	require.NoError(t, err)
	return b
}

// TestSeriesBookRefCounts_CountsTrashedAndNonPrimary is the core assertion: the
// unfiltered counter must see books the display counter deliberately hides.
// Without this, a series holding only trash reads as "referenced by nothing".
func TestSeriesBookRefCounts_CountsTrashedAndNonPrimary(t *testing.T) {
	store := seedRefStore(t, t.TempDir())

	const onlyTrashed = 900   // every book in the trash
	const onlyNonPrim = 901   // every book a secondary version
	const healthy = 902       // an ordinary series
	mkBook(t, store, "trashed-a", onlyTrashed, true, true)
	mkBook(t, store, "trashed-b", onlyTrashed, true, true)
	mkBook(t, store, "secondary", onlyNonPrim, false, false)
	mkBook(t, store, "normal", healthy, true, false)

	refs, err := store.GetAllSeriesBookRefCounts()
	require.NoError(t, err)

	require.Equal(t, 2, refs[onlyTrashed],
		"a series whose books are all trashed is still REFERENCED — deleting it strands them")
	require.Equal(t, 1, refs[onlyNonPrim],
		"a non-primary version still holds the series_id")
	require.Equal(t, 1, refs[healthy])

	// And prove the OLD instrument disagrees — otherwise this test would pass
	// even if the fix were a no-op.
	display, err := store.GetAllSeriesBookCounts()
	require.NoError(t, err)
	require.Zero(t, display[onlyTrashed],
		"precondition: the display counter must report 0 here, which is exactly why it must not drive deletion")
	require.Zero(t, display[onlyNonPrim], "precondition: display counter hides non-primary")
}

// TestSeriesBookRefCounts_UnreferencedSeriesAreAbsent — the safe-to-delete
// signal is absence from the map, so absence must actually mean "nothing points
// here", not "no book passed a filter".
func TestSeriesBookRefCounts_UnreferencedSeriesAreAbsent(t *testing.T) {
	store := seedRefStore(t, t.TempDir())
	mkBook(t, store, "somewhere", 910, true, false)

	refs, err := store.GetAllSeriesBookRefCounts()
	require.NoError(t, err)
	require.Equal(t, 1, refs[910])
	_, present := refs[911]
	require.False(t, present, "a series nothing references must be absent from the map")
}

// TestSeriesBookRefCounts_MemDBAndPebbleAgree is the conformance test. Two
// implementations answer this question — memdb (the prod default) and the
// Pebble scan — and per-path hardcoded expectations cannot catch drift between
// them, because whoever writes a path also writes its expectation. One fixture,
// both implementations, assert EQUAL.
func TestSeriesBookRefCounts_MemDBAndPebbleAgree(t *testing.T) {
	store := seedRefStore(t, t.TempDir())

	// A deliberately mixed population: trashed, non-primary, both, neither,
	// nil-flags, and books with no series at all.
	for i := 0; i < 40; i++ {
		sid := 920 + (i % 7)
		mkBook(t, store, fmt.Sprintf("mix-%02d", i), sid, i%2 == 0, i%3 == 0)
	}
	// nil flags — the pointer-nil branch each filter treats differently.
	_, err := store.CreateBook(&Book{Title: "nilflags", FilePath: "/ref/nilflags", SeriesID: intp(927)})
	require.NoError(t, err)
	// no series at all — must not appear under any key.
	_, err = store.CreateBook(&Book{Title: "noseries", FilePath: "/ref/noseries"})
	require.NoError(t, err)

	fromMem, err := store.mem().GetAllSeriesBookRefCounts()
	require.NoError(t, err)
	fromPebble, err := store.getAllSeriesBookRefCountsPebble()
	require.NoError(t, err)

	require.Equal(t, fromPebble, fromMem,
		"memdb and Pebble must agree on unfiltered series references; drift here means "+
			"the answer depends on warmup state, and deletion decisions ride on it")
	require.NotEmpty(t, fromMem, "fixture must actually produce references, or this asserts nothing")
	require.Equal(t, 41, sumCounts(fromMem), "every seeded book with a series must be counted exactly once")
}

func sumCounts(m map[int]int) int {
	t := 0
	for _, v := range m {
		t += v
	}
	return t
}

// TestAsSeriesBookRefStore_ResolvesPebbleStore guards the capability lookup.
// prod wraps the store in the Bleve indexedStore decorator, and a bare type
// assertion against a wrapped store is indistinguishable from an unsupported
// backend — which is how several ops silently no-opped in production.
func TestAsSeriesBookRefStore_ResolvesPebbleStore(t *testing.T) {
	store := seedRefStore(t, t.TempDir())
	require.NotNil(t, AsSeriesBookRefStore(store))
	require.Nil(t, AsSeriesBookRefStore(nil))
	require.Nil(t, AsSeriesBookRefStore(struct{}{}))
}
