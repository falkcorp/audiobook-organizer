// file: internal/audiobooks/kept_sweep_guard_test.go
// version: 1.1.2
// guid: 4e8b1a37-9c52-4d06-b7f3-2a6d0c9e5b18
// last-edited: 2026-10-29

package audiobooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
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

	res, err := versions.SweepKeptFiles(f.store, f.svc.VersionPurgeGuard(), versions.KeptSweepOptions{Apply: true})
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
	res, err = versions.SweepKeptFiles(f.store, f.svc.VersionPurgeGuard(), versions.KeptSweepOptions{Apply: true})
	require.NoError(t, err)
	assert.Equal(t, 0, res.Removed)
	assert.Equal(t, 2, res.StillKept)
}

// keptSweepOnPurged notes each path as kept on the fixture's version, which is
// made inactive_purged, runs the sweep through the real guard and returns the
// result.
func keptSweepOnPurged(t *testing.T, f *versionGuardFixture, paths ...string) versions.KeptSweepResult {
	t.Helper()
	cur := f.reread(t)
	cur.Status = database.BookVersionStatusInactivePurged
	for _, p := range paths {
		cur.PurgeKept = append(cur.PurgeKept, database.PurgeFileNote{Path: p, Reason: "kept once"})
	}
	require.NoError(t, f.store.UpdateBookVersion(cur))
	res, err := versions.SweepKeptFiles(f.store, f.svc.VersionPurgeGuard(), versions.KeptSweepOptions{Apply: true})
	require.NoError(t, err)
	return res
}

// The iTunes rules of the guard other than the "iTunes Media" folder name: the
// folder of the configured iTunes library file (read path) and the folder of its
// .itl file. A kept file under either survives the sweep whatever the folder is
// called.
func TestSweepKeptFiles_RealGuardProtectsTheConfiguredITunesFolders(t *testing.T) {
	for name, set := range map[string]func(path string){
		"library read path": func(p string) { config.AppConfig.ITunes.LibraryReadPath = p },
		"library itl path":  func(p string) { config.AppConfig.ITunes.LibraryITLPath = p },
	} {
		t.Run(name, func(t *testing.T) {
			f := newVersionGuardFixtureBookTrashed(t)
			prevRead, prevITL := config.AppConfig.ITunes.LibraryReadPath, config.AppConfig.ITunes.LibraryITLPath
			t.Cleanup(func() {
				config.AppConfig.ITunes.LibraryReadPath, config.AppConfig.ITunes.LibraryITLPath = prevRead, prevITL
			})
			config.AppConfig.ITunes.LibraryReadPath, config.AppConfig.ITunes.LibraryITLPath = "", ""
			dir := filepath.Join(f.base, "lib", "Gotham Music")
			set(filepath.Join(dir, "Library.xml"))
			inITunes := filepath.Join(dir, "Audiobooks", "Hush", "hush.m4b")
			free := filepath.Join(versions.VersionSlotDir(f.bookDir, f.ver.ID), "part1.m4b")
			writeFile(t, inITunes)
			writeFile(t, free)

			res := keptSweepOnPurged(t, f, inITunes, free)

			assert.True(t, fileExists(inITunes), "a file under the iTunes library folder is never deleted")
			assert.False(t, fileExists(free), "control: the unprotected kept file is removed")
			assert.Equal(t, 1, res.Removed, "%+v", res)
			assert.Equal(t, 1, res.StillKept, "%+v", res)
		})
	}
}

// A kept path spelled through a symlink into the iTunes folder. The guard the book
// purge and the version purge share (protectedPathChecked) also tests the
// symlink-resolved spelling since #4256, so the file under the iTunes folder stays.
func TestSweepKeptFiles_RealGuardSymlinkIntoITunesFolder(t *testing.T) {
	f := newVersionGuardFixtureBookTrashed(t)
	prevRead := config.AppConfig.ITunes.LibraryReadPath
	t.Cleanup(func() { config.AppConfig.ITunes.LibraryReadPath = prevRead })
	itunesDir := filepath.Join(f.base, "lib", "Gotham Music")
	config.AppConfig.ITunes.LibraryReadPath = filepath.Join(itunesDir, "Library.xml")
	real := filepath.Join(itunesDir, "Hush", "hush.m4b")
	writeFile(t, real)
	link := filepath.Join(f.base, "lib", "Gotham", "link")
	require.NoError(t, os.Symlink(filepath.Join(itunesDir, "Hush"), link))
	viaLink := filepath.Join(link, "hush.m4b")

	res := keptSweepOnPurged(t, f, viaLink)
	t.Logf("result %+v", res)
	assert.True(t, fileExists(real), "a file under the iTunes folder reached through a symlink is never deleted")
}
