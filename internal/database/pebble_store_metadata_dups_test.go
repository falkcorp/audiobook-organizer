// file: internal/database/pebble_store_metadata_dups_test.go
// version: 1.0.0
// guid: 3c9d6e21-7a4b-4f18-9c02-metadatadups01
// last-edited: 2026-07-11

package database

import (
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// metadataDupFixtureIDs names the book IDs created by buildMetadataDupFixture,
// keyed by mnemonic, so subtests can assert on specific books without
// re-deriving IDs from titles.
type metadataDupFixtureIDs struct {
	dupA, dupB   string   // (a): near-identical title+author -> grouped at 0.80
	diffA, diffB string   // (b): same author+token, clearly different titles -> not grouped
	subA, subB   string   // (c): same author+token, sub-threshold title pair -> not grouped
	oversized    []string // (d): cap+1 same-key books -> bucket skipped
	emptyTitle   string   // (e): empty (whitespace) title -> never bucketed
}

// buildMetadataDupFixture populates store with the TASK-02 acceptance fixture
// covering cases (a)-(e). Each case uses its own author so buckets never
// collide across cases: the ONLY group the fixture should ever yield is the
// case-(a) pair.
func buildMetadataDupFixture(t *testing.T, store Store) metadataDupFixtureIDs {
	t.Helper()

	fileSeq := 0
	newAuthor := func(name string) *int {
		a, err := store.CreateAuthor(name)
		require.NoError(t, err)
		return &a.ID
	}
	create := func(title string, authorID *int) string {
		fileSeq++
		book := &Book{Title: title, AuthorID: authorID, FilePath: fmt.Sprintf("/library/f%d.mp3", fileSeq)}
		created, err := store.CreateBook(book)
		require.NoError(t, err)
		return created.ID
	}

	var ids metadataDupFixtureIDs

	// (a) near-identical title (case/whitespace differ -> identical when
	// normalized), same author -> one group of 2 at threshold 0.80.
	authA := newAuthor("Author One")
	ids.dupA = create("The Great Adventure", authA)
	ids.dupB = create("  the great adventure  ", authA)

	// (b) same author + same title token ("ocean"), clearly different titles
	// -> land in the same bucket but do NOT group.
	authB := newAuthor("Author Two")
	ids.diffA = create("Ocean Deep", authB)
	ids.diffB = create("Ocean Storm", authB)

	// (c) same author + same title token ("silent"), sub-threshold pair ->
	// bucketed together but scores below 0.80, so not grouped.
	authC := newAuthor("Author Three")
	ids.subA = create("Silent Night", authC)
	ids.subB = create("Silent Dawn", authC)

	// (d) oversized bucket: cap+1 books, identical author+title (they would
	// all group if compared) -> the bucket is skipped with a slog.Warn and
	// contributes zero groups, while the run still returns the (a) group.
	authD := newAuthor("Author Four")
	ids.oversized = make([]string, 0, metadataFuzzyBucketCap+1)
	for i := 0; i < metadataFuzzyBucketCap+1; i++ {
		ids.oversized = append(ids.oversized, create("Collected Works", authD))
	}

	// (e) empty (whitespace-only) title -> never bucketed, never panics.
	authE := newAuthor("Author Five")
	ids.emptyTitle = create("   ", authE)

	return ids
}

// metaGroupIDs returns the sorted book IDs of a single group, for
// order-independent comparison.
func metaGroupIDs(group []BookCore) []string {
	out := make([]string, len(group))
	for i, b := range group {
		out[i] = b.ID
	}
	sort.Strings(out)
	return out
}

// assertMetadataDupGroups asserts groups contains exactly the case-(a) pair
// and none of the deliberately-not-grouped fixture books (b/c/d/e).
func assertMetadataDupGroups(t *testing.T, groups [][]BookCore, ids metadataDupFixtureIDs) {
	t.Helper()
	require.Len(t, groups, 1, "expected exactly one metadata-duplicate group (the case-(a) pair)")

	want := []string{ids.dupA, ids.dupB}
	sort.Strings(want)
	require.Equal(t, want, metaGroupIDs(groups[0]))

	excluded := map[string]bool{
		ids.diffA:      true,
		ids.diffB:      true,
		ids.subA:       true,
		ids.subB:       true,
		ids.emptyTitle: true,
	}
	for _, id := range ids.oversized {
		excluded[id] = true
	}
	for _, g := range groups {
		for _, b := range g {
			require.False(t, excluded[b.ID], "book %s should not appear in any metadata-duplicate group", b.ID)
		}
	}
}

// TestGetDuplicateBooksByMetadataCore runs the shared TASK-02 fixture through
// BOTH the memdb-delegation path (UseMemDB=true, the production default) and
// the Pebble scan-fallback path (UseMemDB=false), asserting identical groups
// on both (case f: Pebble-vs-MemStore parity), plus the anti-over-suppression
// / termination guarantee for the oversized bucket (case d).
func TestGetDuplicateBooksByMetadataCore(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	ids := buildMetadataDupFixture(t, store)

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")
	p.WaitForWarmup()

	const threshold = 0.80

	t.Run("MemDBPath", func(t *testing.T) {
		p.UseMemDB = true
		groups, err := store.GetDuplicateBooksByMetadataCore(threshold)
		require.NoError(t, err)
		assertMetadataDupGroups(t, groups, ids)
	})

	t.Run("ScanFallbackPath", func(t *testing.T) {
		p.UseMemDB = false
		groups, err := store.GetDuplicateBooksByMetadataCore(threshold)
		require.NoError(t, err)
		assertMetadataDupGroups(t, groups, ids)
	})

	// (d) + (f): the oversized bucket is skipped (never processed pairwise, so
	// the run terminates) and its books never appear in a group, while the
	// valid case-(a) group is still returned — on BOTH backends.
	t.Run("OversizedBucketSkippedOthersStillGrouped", func(t *testing.T) {
		want := []string{ids.dupA, ids.dupB}
		sort.Strings(want)
		for _, useMemDB := range []bool{true, false} {
			p.UseMemDB = useMemDB
			groups, err := store.GetDuplicateBooksByMetadataCore(threshold)
			require.NoError(t, err)
			require.Len(t, groups, 1)
			require.Equal(t, want, metaGroupIDs(groups[0]))
			for _, id := range ids.oversized {
				require.NotContains(t, metaGroupIDs(groups[0]), id)
			}
		}
	})
}
