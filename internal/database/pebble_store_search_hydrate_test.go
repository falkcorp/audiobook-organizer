// file: internal/database/pebble_store_search_hydrate_test.go
// version: 1.0.0
// guid: 9b2e4c7a-1d38-4f65-8a0c-3e5d7b1f9a24
// last-edited: 2026-09-25

package database

import (
	"fmt"
	"sort"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

func seedSearchHydrateStore(t *testing.T) (*PebbleStore, []string) {
	t.Helper()
	store := seedAuthorRefStore(t, t.TempDir())
	a, err := store.CreateAuthor("Quill Writer")
	require.NoError(t, err)
	var ids []string
	titles := []string{"Quill", "Quill and Ink", "The Quill Road", "Inkwell", "quilled", "Other Book", "Ink Quill"}
	for i, title := range titles {
		b := &Book{Title: title, FilePath: fmt.Sprintf("/hydrate/%d", i), IsPrimaryVersion: new(true)}
		if i == 3 {
			b.AuthorID = &a.ID // matches "quill" only through the author name
		}
		created, err := store.CreateBook(b)
		require.NoError(t, err)
		ids = append(ids, created.ID)
	}
	return store, ids
}

// Review finding 9/19/28: one undecodable row costs that row only. The rest of
// the batch, including rows after it, is still returned, and the bad ID is
// reported. GetBooksByIDs stops at the first bad row.
func TestGetBooksForSearch_SkipsBadRowOnly(t *testing.T) {
	store, ids := seedSearchHydrateStore(t)
	require.NoError(t, store.DB().Set([]byte("book:"+ids[2]), []byte("{not json"), pebble.Sync))

	books, bad, err := store.GetBooksForSearch(ids, false)
	require.NoError(t, err)
	require.Equal(t, []string{ids[2]}, bad)
	require.Len(t, books, len(ids)-1)
	require.Equal(t, ids[len(ids)-1], books[len(books)-1].ID, "rows after the bad one must survive")

	truncated, gErr := store.GetBooksByIDs(ids)
	require.Error(t, gErr)
	require.Len(t, truncated, 2, "the contrast: GetBooksByIDs truncates at the bad row")
}

// Review finding 2/25: ranks for a handful of IDs come from point lookups and
// agree exactly with the order and membership of the full filtered scan, on
// both the memdb and the Pebble path.
func TestSearchBookRanksFiltered_AgreesWithScan(t *testing.T) {
	store, ids := seedSearchHydrateStore(t)
	f := BookSummaryFilter{}
	for _, useMem := range []bool{true, false} {
		store.UseMemDB = useMem
		scan, err := store.SearchBookIDsFiltered("quill", 0, 0, f)
		require.NoError(t, err)
		ranks, err := store.SearchBookRanksFiltered("quill", ids, f)
		require.NoError(t, err)
		got := make([]string, 0, len(ranks))
		for id := range ranks {
			got = append(got, id)
		}
		sort.Slice(got, func(i, j int) bool { return ranks[got[i]].Less(ranks[got[j]]) })
		require.Equal(t, scan, got, "useMemDB=%v", useMem)
		require.Contains(t, got, ids[3], "author-name match must rank (useMemDB=%v)", useMem)
		require.NotContains(t, got, ids[5], "useMemDB=%v", useMem)
	}
	store.UseMemDB = true
}

// Review finding 4a: the pre-warmup fallback keeps IDs only and returns the
// same list as the memdb scan.
func TestSearchBookIDsDisk_MatchesMemdb(t *testing.T) {
	store, _ := seedSearchHydrateStore(t)
	f := BookSummaryFilter{}
	mem, err := store.mem().SearchBookIDsFiltered("quill", 0, 0, f)
	require.NoError(t, err)
	disk, err := store.searchBookIDsDisk("quill", 0, 0, &f)
	require.NoError(t, err)
	require.Equal(t, mem, disk)
	require.NotEmpty(t, disk)
}

// SearchQueryKey is exactly the substring search's fold, untrimmed.
func TestSearchQueryKey(t *testing.T) {
	require.Equal(t, SearchQueryKey("Foo_Bar  baz"), SearchQueryKey("foo bar baz"))
	require.NotEqual(t, SearchQueryKey("rock "), SearchQueryKey("rock"))
	require.NotEqual(t, SearchQueryKey("_foo"), SearchQueryKey("foo"))
}
