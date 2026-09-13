// file: internal/database/memdb_metadata_hash_test.go
// version: 1.0.0
// guid: 7c3e1b95-0d4a-4f28-8e61-b2a94f6d0c18
// last-edited: 2026-09-13

package database

import (
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// seedMetadataHashFixture writes books through the real store so the memdb
// index is maintained the way production maintains it: 3 live books sharing
// hash "h-dup", 1 soft-deleted and 1 merged book with the same hash (both must
// be excluded), 1 book with another hash, and 1 with none.
func seedMetadataHashFixture(t testing.TB, store Store) (liveIDs []string) {
	t.Helper()
	yes := true
	mk := func(title, hash string) *Book {
		b := &Book{Title: title, FilePath: "/lib/" + title}
		if hash != "" {
			h := hash
			b.MetadataSourceHash = &h
		}
		created, err := store.CreateBook(b)
		require.NoError(t, err)
		return created
	}
	for _, title := range []string{"Live A", "Live B", "Live C"} {
		liveIDs = append(liveIDs, mk(title, "h-dup").ID)
	}
	trashed := mk("Trashed", "h-dup")
	trashed.MarkedForDeletion = &yes
	_, err := store.UpdateBook(trashed.ID, trashed)
	require.NoError(t, err)

	merged := mk("Merged", "h-dup")
	into := liveIDs[0]
	merged.MergedIntoBookID = &into
	_, err = store.UpdateBook(merged.ID, merged)
	require.NoError(t, err)

	mk("Other", "h-other")
	mk("NoHash", "")
	sort.Strings(liveIDs)
	return liveIDs
}

func sortedIDs(books []Book) []string {
	ids := make([]string, 0, len(books))
	for _, b := range books {
		ids = append(ids, b.ID)
	}
	sort.Strings(ids)
	return ids
}

// TestGetBooksByMetadataSourceHash_MemDBAndPebbleAgree pins the memdb fast
// path to the Pebble scan it replaces on the hot path: same live, unmerged
// rows, soft-deleted and merged rows excluded by both.
func TestGetBooksByMetadataSourceHash_MemDBAndPebbleAgree(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	live := seedMetadataHashFixture(t, store)

	p, ok := store.(*PebbleStore)
	require.True(t, ok)
	p.WaitForWarmup()
	require.True(t, p.IsMemReady(), "memdb must be published or the memdb arm runs the Pebble path")

	got := map[bool][]string{}
	for _, useMemDB := range []bool{true, false} {
		p.UseMemDB = useMemDB
		books, err := p.GetBooksByMetadataSourceHash("h-dup")
		require.NoError(t, err)
		got[useMemDB] = sortedIDs(books)
		require.Equal(t, live, got[useMemDB], "useMemDB=%v returned the wrong rows", useMemDB)

		none, err := p.GetBooksByMetadataSourceHash("h-absent")
		require.NoError(t, err)
		require.Empty(t, none, "useMemDB=%v: unknown hash matched rows", useMemDB)
	}
	p.UseMemDB = true
	require.Equal(t, got[true], got[false])

	// Called directly, the memdb method must answer from the index (not error
	// into the fallback), or the conformance above proves nothing about it.
	direct, err := p.mem().GetBooksByMetadataSourceHash("h-dup")
	require.NoError(t, err)
	require.Equal(t, live, sortedIDs(direct))
}

// BenchmarkGetBooksByMetadataSourceHash compares the Pebble full scan with the
// memdb index on a library of b.N-independent size (2,000 books).
func BenchmarkGetBooksByMetadataSourceHash(b *testing.B) {
	store, cleanup := setupPebbleTestDB(&testing.T{})
	defer cleanup()
	for i := range 2000 {
		h := fmt.Sprintf("h-%d", i%1000)
		_, err := store.CreateBook(&Book{Title: fmt.Sprintf("Book %d", i), FilePath: fmt.Sprintf("/lib/%d", i), MetadataSourceHash: &h})
		if err != nil {
			b.Fatal(err)
		}
	}
	p := store.(*PebbleStore)
	p.WaitForWarmup()
	if !p.IsMemReady() {
		b.Fatal("memdb not ready")
	}
	for _, useMemDB := range []bool{false, true} {
		name := "PebbleScan"
		if useMemDB {
			name = "MemDBIndex"
		}
		b.Run(name, func(b *testing.B) {
			p.UseMemDB = useMemDB
			for b.Loop() {
				if _, err := p.GetBooksByMetadataSourceHash("h-7"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	p.UseMemDB = true
}
