// file: internal/database/series_bookref_test.go
// version: 1.2.0
// guid: 8f2c14ba-6d97-4e35-b0a1-72e5c9d38a04
// last-edited: 2026-08-24

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

//go:fix inline
func boolp(b bool) *bool { return new(b) }

//go:fix inline
func intp(i int) *int { return new(i) }

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
		SeriesID:          new(seriesID),
		IsPrimaryVersion:  new(primary),
		MarkedForDeletion: new(trashed),
	})
	require.NoError(t, err)
	return b
}

// TestSeriesBookRefCounts_CountsTrashedAndNonPrimary is the core assertion: the
// unfiltered counter must see books the display counter deliberately hides.
// Without this, a series holding only trash reads as "referenced by nothing".
func TestSeriesBookRefCounts_CountsTrashedAndNonPrimary(t *testing.T) {
	store := seedRefStore(t, t.TempDir())

	const onlyTrashed = 900 // every book in the trash
	const onlyNonPrim = 901 // every book a secondary version
	const healthy = 902     // an ordinary series
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
	for i := range 40 {
		sid := 920 + (i % 7)
		mkBook(t, store, fmt.Sprintf("mix-%02d", i), sid, i%2 == 0, i%3 == 0)
	}
	// nil flags — the pointer-nil branch each filter treats differently.
	_, err := store.CreateBook(&Book{Title: "nilflags", FilePath: "/ref/nilflags", SeriesID: new(927)})
	require.NoError(t, err)
	// no series at all — must not appear under any key.
	_, err = store.CreateBook(&Book{Title: "noseries", FilePath: "/ref/noseries"})
	require.NoError(t, err)

	// A caller-supplied letter-leading ID. Every other book here gets a minted
	// ULID, which is why this conformance test could not see the bounds bug
	// fixed on 2026-08-24: the two implementations agreed because the fixture
	// only ever produced keys that both of them happened to admit. A
	// conformance test is bounded by the diversity of its fixture, not by the
	// number of implementations it compares.
	_, err = store.CreateBook(&Book{
		ID: "ZZREF00000000000000000000", Title: "letterled",
		FilePath: "/ref/letterled", SeriesID: new(928),
	})
	require.NoError(t, err)

	fromMem, err := store.mem().GetAllSeriesBookRefCounts()
	require.NoError(t, err)
	fromPebble, err := store.getAllSeriesBookRefCountsPebble()
	require.NoError(t, err)

	require.Equal(t, fromPebble, fromMem,
		"memdb and Pebble must agree on unfiltered series references; drift here means "+
			"the answer depends on warmup state, and deletion decisions ride on it")
	require.NotEmpty(t, fromMem, "fixture must actually produce references, or this asserts nothing")
	require.Equal(t, 42, sumCounts(fromMem), "every seeded book with a series must be counted exactly once")
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

	// THE CASE THIS TEST EXISTS FOR, and the one it was missing.
	//
	// The three assertions above all pass against a plain
	// `s.(SeriesBookRefStore)` type assertion: a bare *PebbleStore satisfies
	// the interface directly, and nil/struct{}{} fail either way. So none of
	// them can tell AsCapability apart from the bare assertion this lookup
	// exists to avoid -- verified by mutation, which printed `ok` with the
	// assertion swapped in.
	//
	// Production never holds a bare *PebbleStore: it is wrapped in the Bleve
	// indexedStore decorator, which embeds the Store INTERFACE and so does not
	// promote SeriesBookRefStore. Against a decorator the bare assertion finds
	// nothing and the guard silently no-ops -- exactly where it matters most.
	require.NotNil(t, AsSeriesBookRefStore(&decoratorStore{Store: store}),
		"capability lookup must see THROUGH the Bleve decorator; prod never holds a bare *PebbleStore")

	// A decorator that has not opted in via Unwrap MUST NOT resolve: reaching
	// around it would bypass whatever behaviour it was added to provide.
	require.Nil(t, AsSeriesBookRefStore(&decoratorNoUnwrap{Store: store}),
		"a decorator without Unwrap must not be reached around")
}

// TestSeriesBookRefCounts_CountsALetterLeadingBookID isolates the iterator
// BOUNDS from the agreement question above, so a future fixture change cannot
// quietly retire the coverage.
//
// getAllSeriesBookRefCountsPebble scanned ["book:0", "book:;") -- a byte range
// admitting only '0'-'9' and ':' as the first character after the prefix.
// CreateBook mints a ULID only when book.ID == "", so a caller-supplied
// letter-leading ID is constructible, sorts above the upper bound, and was
// invisible to the scan.
//
// The stakes are higher HERE than in the merge getter that shipped the same fix
// hours earlier. This counter is the guard three delete sites consult before
// removing a series row (series_dedup.go:486, cleanup_series.go:120 and :307);
// they were classified "genuinely covered" precisely because they gate on it.
// A counter that undercounts reports "referenced by nothing" -- the permissive
// answer -- and the delete proceeds, stranding the book it could not see.
func TestSeriesBookRefCounts_CountsALetterLeadingBookID(t *testing.T) {
	store := seedRefStore(t, t.TempDir())

	const lonely = 940 // referenced by exactly one book, so the count is unambiguous
	b, err := store.CreateBook(&Book{
		ID: "ZZBOUNDS0000000000000000", Title: "letter-leading",
		FilePath: "/ref/letter-leading", SeriesID: intp(lonely),
		IsPrimaryVersion: new(true),
	})
	require.NoError(t, err)
	require.Equal(t, "ZZBOUNDS0000000000000000", b.ID,
		"fixture check: CreateBook must not have re-minted the ID, or this tests nothing")

	// The Pebble arm directly -- the fall-through target, and the only arm the
	// bounds apply to. Going through GetAllSeriesBookRefCounts would be served
	// by the memdb and prove nothing about the scan.
	fromPebble, err := store.getAllSeriesBookRefCountsPebble()
	require.NoError(t, err)
	require.Equal(t, 1, fromPebble[lonely],
		"a referenced series the ref counter cannot see reads as safe-to-delete, "+
			"and the delete strands the very book the scan missed")
}
