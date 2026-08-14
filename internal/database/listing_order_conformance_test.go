// file: internal/database/listing_order_conformance_test.go
// version: 1.0.0
// guid: 9f4a2c68-3b71-4e05-8a9d-6c0e5b17d234
// last-edited: 2026-08-14

package database

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The listings below have two backing implementations that disagreed about
// ORDER: MemStore sorted, the PebbleStore scan did not sort at all.
//
// These tests assert the SEQUENCE, not the set. The pre-existing conformance
// test for ListSoftDeletedBooks used require.ElementsMatch and stayed green
// through the entire drift, because ElementsMatch is order-insensitive by
// construction — it can only ever prove the two paths return the same books,
// which was never in doubt.
//
// Each fixture is built so that insertion order, ID order and sorted order are
// all different, and with enough rows that the raw Pebble key order is visibly
// wrong: keys are strings, so "author:10" and "author:11" sort before
// "author:2". A fixture of three rows would let an unsorted implementation pass
// by coincidence.

// namesOutOfOrder returns 12 names whose alphabetical order differs from both
// their insertion order and their eventual "<kind>:<id>" key order.
func namesOutOfOrder(prefix string) []string {
	return []string{
		prefix + " Zulu", prefix + " alpha", prefix + " Mike", prefix + " bravo",
		prefix + " Yankee", prefix + " charlie", prefix + " November", prefix + " delta",
		prefix + " Xray", prefix + " echo", prefix + " Oscar", prefix + " foxtrot",
	}
}

func sortedLower(in []string) []string {
	out := append([]string(nil), in...)
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

// runBothPaths executes fn against the memdb path and the Pebble path and
// returns the two results keyed by UseMemDB.
func runBothPaths[T any](t *testing.T, p *PebbleStore, fn func() (T, error)) map[bool]T {
	t.Helper()
	original := p.UseMemDB
	t.Cleanup(func() { p.UseMemDB = original })

	got := map[bool]T{}
	for _, useMemDB := range []bool{true, false} {
		p.UseMemDB = useMemDB
		v, err := fn()
		require.NoError(t, err)
		got[useMemDB] = v
	}
	return got
}

func newOrderConformanceStore(t *testing.T) (Store, *PebbleStore) {
	t.Helper()
	store, cleanup := setupPebbleTestDB(t)
	t.Cleanup(cleanup)

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")
	p.WaitForWarmup()
	require.True(t, p.IsMemReady(),
		"memdb must be published or the UseMemDB=true arm silently runs the Pebble path")
	return store, p
}

func TestGetAllAuthors_OrderMatchesOnBothPaths(t *testing.T) {
	store, p := newOrderConformanceStore(t)

	names := namesOutOfOrder("Author")
	for _, n := range names {
		_, err := store.CreateAuthor(n)
		require.NoError(t, err)
	}
	require.Greater(t, len(names), 10,
		"fixture must exceed 10 rows or the author:10-before-author:2 key ordering never appears")

	got := runBothPaths(t, p, func() ([]Author, error) { return p.GetAllAuthors() })

	want := sortedLower(names)
	for _, useMemDB := range []bool{true, false} {
		actual := make([]string, 0, len(got[useMemDB]))
		for _, a := range got[useMemDB] {
			actual = append(actual, a.Name)
		}
		require.Equal(t, want, actual,
			"authors must come back name-sorted (useMemDB=%v)", useMemDB)
	}
}

func TestGetAllSeries_OrderMatchesOnBothPaths(t *testing.T) {
	store, p := newOrderConformanceStore(t)

	names := namesOutOfOrder("Series")
	for _, n := range names {
		_, err := store.CreateSeries(n, nil)
		require.NoError(t, err)
	}

	got := runBothPaths(t, p, func() ([]Series, error) { return p.GetAllSeries() })

	want := sortedLower(names)
	for _, useMemDB := range []bool{true, false} {
		actual := make([]string, 0, len(got[useMemDB]))
		for _, s := range got[useMemDB] {
			actual = append(actual, s.Name)
		}
		require.Equal(t, want, actual,
			"series must come back name-sorted (useMemDB=%v)", useMemDB)
	}
}

func TestGetAllImportPaths_OrderMatchesOnBothPaths(t *testing.T) {
	store, p := newOrderConformanceStore(t)

	names := namesOutOfOrder("Folder")
	for i, n := range names {
		_, err := store.CreateImportPath(fmt.Sprintf("/import/p%02d", i), n)
		require.NoError(t, err)
	}

	got := runBothPaths(t, p, func() ([]ImportPath, error) { return p.GetAllImportPaths() })

	want := sortedLower(names)
	for _, useMemDB := range []bool{true, false} {
		actual := make([]string, 0, len(got[useMemDB]))
		for _, ip := range got[useMemDB] {
			actual = append(actual, ip.Name)
		}
		require.Equal(t, want, actual,
			"import paths must come back name-sorted (useMemDB=%v)", useMemDB)
	}
}

func TestListSoftDeletedBooks_OrderMatchesOnBothPaths(t *testing.T) {
	store, p := newOrderConformanceStore(t)

	// Delete timestamps deliberately NOT in creation order, so "most recently
	// deleted first" cannot be satisfied by insertion or ID order.
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	offsets := []int{3, 11, 1, 7, 5, 12, 2, 9, 4, 10, 6, 8}
	yes := true

	type want struct {
		id string
		at time.Time
	}
	var expected []want

	for i, off := range offsets {
		created, err := store.CreateBook(&Book{
			Title:    fmt.Sprintf("Trashed %02d", i),
			FilePath: fmt.Sprintf("/lib/trash/%02d.m4b", i),
		})
		require.NoError(t, err)

		at := base.Add(time.Duration(off) * time.Hour)
		created.MarkedForDeletion = &yes
		created.MarkedForDeletionAt = &at
		_, err = store.UpdateBook(created.ID, created)
		require.NoError(t, err)

		expected = append(expected, want{id: created.ID, at: at})
	}
	require.Greater(t, len(expected), 10, "fixture must exceed 10 rows")

	// Most recently deleted first.
	sort.SliceStable(expected, func(i, j int) bool { return expected[i].at.After(expected[j].at) })
	wantIDs := make([]string, 0, len(expected))
	for _, e := range expected {
		wantIDs = append(wantIDs, e.id)
	}

	got := runBothPaths(t, p, func() ([]Book, error) { return p.ListSoftDeletedBooks(0, 0, nil) })
	for _, useMemDB := range []bool{true, false} {
		actual := make([]string, 0, len(got[useMemDB]))
		for _, b := range got[useMemDB] {
			actual = append(actual, b.ID)
		}
		require.Equal(t, wantIDs, actual,
			"trash must be ordered most-recently-deleted first (useMemDB=%v)", useMemDB)
	}

	// The paged form is where unsorted iteration did real damage: the Pebble
	// path applied limit/offset DURING iteration, so it could not sort at all.
	// Walking the pages must reconstruct the same total order as one big call.
	const pageSize = 5
	for _, useMemDB := range []bool{true, false} {
		p.UseMemDB = useMemDB
		var paged []string
		for offset := 0; offset < len(wantIDs); offset += pageSize {
			page, err := p.ListSoftDeletedBooks(pageSize, offset, nil)
			require.NoError(t, err)
			for _, b := range page {
				paged = append(paged, b.ID)
			}
		}
		require.Equal(t, wantIDs, paged,
			"paging must partition the same ordered set (useMemDB=%v)", useMemDB)
	}
}

func TestGetAllAuthorAliases_OrderMatchesOnBothPaths(t *testing.T) {
	store, p := newOrderConformanceStore(t)

	// Two authors, aliases added in an order that is neither grouped by author
	// nor alphabetical within an author.
	a1, err := store.CreateAuthor("Alias Owner One")
	require.NoError(t, err)
	a2, err := store.CreateAuthor("Alias Owner Two")
	require.NoError(t, err)

	type seed struct {
		authorID int
		alias    string
	}
	seeds := []seed{
		{a2.ID, "zeta"}, {a1.ID, "Yankee"}, {a2.ID, "alpha"}, {a1.ID, "bravo"},
		{a2.ID, "Mike"}, {a1.ID, "charlie"}, {a2.ID, "delta"}, {a1.ID, "Oscar"},
		{a1.ID, "echo"}, {a2.ID, "foxtrot"}, {a1.ID, "November"}, {a2.ID, "golf"},
	}
	for _, s := range seeds {
		_, aErr := store.CreateAuthorAlias(s.authorID, s.alias, "manual")
		require.NoError(t, aErr)
	}

	want := append([]seed(nil), seeds...)
	sort.SliceStable(want, func(i, j int) bool {
		if want[i].authorID != want[j].authorID {
			return want[i].authorID < want[j].authorID
		}
		return strings.ToLower(want[i].alias) < strings.ToLower(want[j].alias)
	})
	wantPairs := make([]string, 0, len(want))
	for _, w := range want {
		wantPairs = append(wantPairs, fmt.Sprintf("%d/%s", w.authorID, w.alias))
	}

	got := runBothPaths(t, p, func() ([]AuthorAlias, error) { return p.GetAllAuthorAliases() })
	for _, useMemDB := range []bool{true, false} {
		actual := make([]string, 0, len(got[useMemDB]))
		for _, a := range got[useMemDB] {
			actual = append(actual, fmt.Sprintf("%d/%s", a.AuthorID, a.AliasName))
		}
		require.Equal(t, wantPairs, actual,
			"aliases must be grouped by author then sorted by name (useMemDB=%v)", useMemDB)
	}
}
