// file: internal/database/library_generation_order_test.go
// version: 1.0.0
// guid: 8b4f1d63-2a7e-4c95-b0d8-6e3a9f2c1b47
// last-edited: 2026-10-01

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLibraryGeneration_BumpFollowsMemDBWriteThrough: the library generation
// moves only AFTER a book write reaches memdb. Were it bumped first, a reader
// that saw the new generation could read the old memdb row and cache it under
// the new generation, stale until the next write (folder-books' title index,
// the audiobook list cache, the ABS attribute index all key on it).
func TestLibraryGeneration_BumpFollowsMemDBWriteThrough(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	p, ok := store.(*PebbleStore)
	require.True(t, ok)
	p.WaitForWarmup()
	require.True(t, p.IsMemReady(), "memdb must be live or the write-through is not exercised")

	var atLastApply uint64
	applied := 0
	memSyncApplyHook = func(string) {
		applied++
		atLastApply = p.libGen.Value()
	}
	t.Cleanup(func() { memSyncApplyHook = nil })

	check := func(name string, write func()) {
		t.Helper()
		before := p.libGen.Value()
		applied = 0
		write()
		require.Positive(t, applied, "%s: no memdb write-through ran", name)
		require.Equal(t, before, atLastApply, "%s: the generation moved before the memdb write-through", name)
		require.Greater(t, p.libGen.Value(), before, "%s: the generation did not move", name)
	}

	var id string
	check("create", func() {
		b, err := p.CreateBook(&Book{Title: "Before", FilePath: "/lib/a.m4b"})
		require.NoError(t, err)
		id = b.ID
	})
	titleInMem := func() string {
		cores, err := p.GetAllBooksCore(0, 0)
		require.NoError(t, err)
		for _, c := range cores {
			if c.ID == id {
				return c.Title
			}
		}
		return ""
	}
	require.Equal(t, "Before", titleInMem())
	check("update", func() {
		_, err := p.ModifyBook(id, func(b *Book) error { b.Title = "After"; return nil })
		require.NoError(t, err)
	})
	require.Equal(t, "After", titleInMem(), "a reader at the new generation sees the write")
	check("delete", func() {
		require.NoError(t, p.DeleteBook(id))
	})
	require.Empty(t, titleInMem())
}
