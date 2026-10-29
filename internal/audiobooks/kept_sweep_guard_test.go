// file: internal/audiobooks/kept_sweep_guard_test.go
// version: 1.0.0
// guid: 4e8b1a37-9c52-4d06-b7f3-2a6d0c9e5b18
// last-edited: 2026-10-29

package audiobooks

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
)

// The kept-file sweep through the REAL purge guard (the book purge's own per-path
// decision): a kept file under the iTunes library, an import path or a .failed
// folder is never removed, while a kept file inside the library that nothing
// names any more is.
func TestSweepKeptFiles_RealGuardNeverTouchesITunesAndRemovesAnUnownedLibraryFile(t *testing.T) {
	f := newVersionGuardFixtureBookTrashed(t) // a live own book would serve every file outside the slot, before the guard is asked
	slot := versions.VersionSlotDir(f.bookDir, f.ver.ID)
	free := filepath.Join(slot, "part1.m4b")
	itunes := filepath.Join(f.base, "lib", "iTunes Media", "Audiobooks", "Hush", "hush.m4b")
	failed := filepath.Join(f.base, "lib", "Gotham", ".failed", "knightfall.m4b")
	for _, p := range []string{free, itunes, failed} {
		writeFile(t, p)
	}
	cur := f.reread(t)
	cur.Status = database.BookVersionStatusInactivePurged
	cur.PurgeKept = []database.PurgeFileNote{
		{Path: free, Reason: "book_file x of book y still names it"},
		{Path: itunes, Reason: "kept once"},
		{Path: failed, Reason: "kept once"},
	}
	require.NoError(t, f.store.UpdateBookVersion(cur))

	res, err := versions.SweepKeptFiles(f.store, f.svc.VersionPurgeGuard(), nil)
	require.NoError(t, err)

	assert.Equal(t, 1, res.Removed, "%+v", res)
	assert.Equal(t, 2, res.StillKept, "%+v", res)
	assert.Equal(t, 0, res.KeptOnError, "%+v", res)
	assert.False(t, fileExists(free), "the unowned library file is removed")
	assert.True(t, fileExists(itunes), "an iTunes file is never deleted")
	assert.True(t, fileExists(failed), "a .failed file is never deleted")
	got := f.reread(t)
	assert.Len(t, got.PurgeKept, 2, "only the removed file's note is dropped: %+v", got.PurgeKept)
	_, ok := keptReason(got, free)
	assert.False(t, ok)

	// A second run changes nothing.
	res, err = versions.SweepKeptFiles(f.store, f.svc.VersionPurgeGuard(), nil)
	require.NoError(t, err)
	assert.Equal(t, 0, res.Removed)
	assert.Equal(t, 2, res.StillKept)
}
