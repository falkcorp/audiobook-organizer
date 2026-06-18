// file: internal/database/pebble_acoustid_stats_test.go
// version: 1.2.0
// guid: e5f6a7b8-c9d0-1234-efab-234567890123
// last-edited: 2026-06-17

package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetAcoustIDStats_Empty(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	ps := store.(*PebbleStore)

	stats, err := ps.GetAcoustIDStats()
	require.NoError(t, err)
	require.NotNil(t, stats)
	assert.Equal(t, 0, stats.TotalFiles)
	assert.Equal(t, 0, stats.WithFingerprint)
	assert.Empty(t, stats.ByLibrary)
}

func TestGetAcoustIDStats_Mixed(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	importPath := "/lib/audiobooks"
	src := "audible"
	asin1, asin2 := "B001", "B002"

	books := []Book{
		{Title: "Book With Files", MetadataSource: &src, ASIN: &asin1, SourceImportPath: &importPath},
		{Title: "Book No FP", MetadataSource: &src, ASIN: &asin2, SourceImportPath: &importPath},
	}
	for i := range books {
		created, err := store.CreateBook(&books[i])
		require.NoError(t, err)
		books[i].ID = created.ID
	}

	// File with fingerprint (T020: use AcoustIDFingerprint, not seg fields).
	f1 := &BookFile{BookID: books[0].ID, FilePath: "/lib/audiobooks/book1.m4b", AcoustIDFingerprint: []byte{1, 2, 3, 4}}
	require.NoError(t, store.CreateBookFile(f1))

	// File without fingerprint
	f2 := &BookFile{BookID: books[1].ID, FilePath: "/lib/audiobooks/book2.m4b"}
	require.NoError(t, store.CreateBookFile(f2))

	ps := store.(*PebbleStore)
	stats, err := ps.GetAcoustIDStats()
	require.NoError(t, err)
	assert.Equal(t, 2, stats.TotalFiles)
	assert.Equal(t, 1, stats.WithFingerprint, "only one file has a fingerprint")
	assert.Len(t, stats.ByLibrary, 1, "both files belong to same library root")
	assert.Equal(t, "/lib/audiobooks", stats.ByLibrary[0].LibraryRoot)
	assert.Equal(t, 2, stats.ByLibrary[0].TotalFiles)
	assert.Equal(t, 1, stats.ByLibrary[0].WithFingerprint)
}

// TestGetAcoustIDStats_StaleMemDBDoesNotBreakLibraryGrouping is the deterministic
// regression for FLAKY-DB-TESTS-2026-06-17. The flake's root cause: GetAcoustIDStats
// scanned files pebble-direct (correct) but grouped them by library using GetAllBooks,
// which reads memdb when published. NewPebbleStore publishes memdb asynchronously, and
// memdb write-through is a no-op until that happens — so under CI -race load the warmup
// could publish an empty/stale memdb, leaving GetAllBooks blind to books that exist in
// pebble. Files then collapsed into the "(unknown)" library bucket and the LibraryRoot
// assertion failed intermittently.
//
// This test forces the exact bad state deterministically: it drains the warmup, then
// overwrites memPtr with a fresh EMPTY MemStore *after* the books are written. With the
// pre-fix code (book grouping via memdb) ByLibrary collapses to "(unknown)"; with the
// fix (book grouping pebble-direct) the grouping is correct regardless of memdb state.
func TestGetAcoustIDStats_StaleMemDBDoesNotBreakLibraryGrouping(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	ps := store.(*PebbleStore)

	importPath := "/lib/audiobooks"
	src := "audible"
	asin1, asin2 := "B001", "B002"
	books := []Book{
		{Title: "Book With Files", MetadataSource: &src, ASIN: &asin1, SourceImportPath: &importPath},
		{Title: "Book No FP", MetadataSource: &src, ASIN: &asin2, SourceImportPath: &importPath},
	}
	for i := range books {
		created, err := store.CreateBook(&books[i])
		require.NoError(t, err)
		books[i].ID = created.ID
	}
	require.NoError(t, store.CreateBookFile(&BookFile{BookID: books[0].ID, FilePath: "/lib/audiobooks/book1.m4b", AcoustIDFingerprint: []byte{1, 2, 3, 4}}))
	require.NoError(t, store.CreateBookFile(&BookFile{BookID: books[1].ID, FilePath: "/lib/audiobooks/book2.m4b"}))

	// Wait for the async warmup to finish so it cannot overwrite the empty memdb we
	// are about to inject, then publish a fresh empty memdb to simulate the warmup
	// race window (memdb published with a pre-write snapshot).
	<-ps.warmupDone
	emptyMem, err := NewMemStore()
	require.NoError(t, err)
	ps.memPtr.Store(emptyMem)

	stats, err := ps.GetAcoustIDStats()
	require.NoError(t, err)
	assert.Equal(t, 2, stats.TotalFiles)
	require.Len(t, stats.ByLibrary, 1, "files must group by their book's import path, not collapse to (unknown)")
	assert.Equal(t, "/lib/audiobooks", stats.ByLibrary[0].LibraryRoot)
	assert.Equal(t, 2, stats.ByLibrary[0].TotalFiles)
	assert.Equal(t, 1, stats.ByLibrary[0].WithFingerprint)
}

func TestGetAcoustIDStats_AllSegmentsChecked(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	importPath := "/lib"
	src := "audible"
	asin := "B003"
	book := Book{Title: "Seg Test", MetadataSource: &src, ASIN: &asin, SourceImportPath: &importPath}
	created, err := store.CreateBook(&book)
	require.NoError(t, err)

	// File with only a whole-file fingerprint (T020: seg fields are no longer stored).
	f := &BookFile{BookID: created.ID, FilePath: "/lib/seg6.m4b", AcoustIDFingerprint: []byte{0xAB, 0xCD, 0xEF, 0x01}}
	require.NoError(t, store.CreateBookFile(f))

	ps := store.(*PebbleStore)
	stats, err := ps.GetAcoustIDStats()
	require.NoError(t, err)
	assert.Equal(t, 1, stats.WithFingerprint, "AcoustIDFingerprint should count as having a fingerprint")
}
