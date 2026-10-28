// file: internal/audiobooks/purge_version_guard_test.go
// version: 1.0.1
// guid: 5e9a2d71-4c06-4f83-b1d8-0a7c3e6f9b24
// last-edited: 2026-10-27

package audiobooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
)

// versionGuardFixture is a book in the library with a trashed version whose
// purge is due, and a purge run through the real guard (the book purge's own
// per-path decision), not a stand-in.
type versionGuardFixture struct {
	svc     *AudiobookService
	store   *database.PebbleStore
	base    string
	bookDir string
	ver     *database.BookVersion
}

func newVersionGuardFixture(t *testing.T) *versionGuardFixture {
	t.Helper()
	svc, store, base := setupPurgeBoundary(t)
	bookDir := filepath.Join(base, "lib", "Gotham", "Year One")
	writeFile(t, filepath.Join(bookDir, "year-one.m4b"))
	book, err := store.CreateBook(&database.Book{ID: "year-one", Title: "Batman: Year One", FilePath: filepath.Join(bookDir, "year-one.m4b")})
	require.NoError(t, err)
	stamp := time.Now().Add(-40 * 24 * time.Hour)
	ver, err := store.CreateBookVersion(&database.BookVersion{
		BookID: book.ID, Status: database.BookVersionStatusTrash, Format: "m4b", Source: "imported", TrashedAt: &stamp,
	})
	require.NoError(t, err)
	return &versionGuardFixture{svc: svc, store: store, base: base, bookDir: bookDir, ver: ver}
}

// ownRow gives the version a row naming path.
func (f *versionGuardFixture) ownRow(t *testing.T, path string, track int) {
	t.Helper()
	require.NoError(t, f.store.CreateBookFile(&database.BookFile{BookID: f.ver.BookID, FilePath: path, VersionID: f.ver.ID, TrackNumber: track, Format: "m4b"}))
}

func (f *versionGuardFixture) purge(t *testing.T) error {
	t.Helper()
	v, err := f.store.GetBookVersion(f.ver.ID)
	require.NoError(t, err)
	return versions.PurgeVersion(f.store, v, f.svc.VersionPurgeGuard())
}

func (f *versionGuardFixture) reread(t *testing.T) *database.BookVersion {
	t.Helper()
	v, err := f.store.GetBookVersion(f.ver.ID)
	require.NoError(t, err)
	return v
}

func keptReason(v *database.BookVersion, path string) (string, bool) {
	for _, n := range v.PurgeKept {
		if n.Path == path {
			return n.Reason, true
		}
	}
	return "", false
}

// Control: a version's file inside the library is removed, and so is the slot
// directory it leaves empty.
func TestVersionPurgeGuard_RemovesAFileInsideTheLibraryAndItsEmptySlot(t *testing.T) {
	f := newVersionGuardFixture(t)
	slotFile := filepath.Join(versions.VersionSlotDir(f.bookDir, f.ver.ID), "part1.m4b")
	writeFile(t, slotFile)
	f.ownRow(t, slotFile, 1)

	require.NoError(t, f.purge(t))
	assert.False(t, fileExists(slotFile))
	_, err := os.Stat(versions.VersionSlotDir(f.bookDir, f.ver.ID))
	assert.True(t, os.IsNotExist(err), "the empty slot directory is removed with its last file")
	assert.Equal(t, database.BookVersionStatusInactivePurged, f.reread(t).Status)
}

// The iTunes library is hands-off by the owner's rule, and an import path and a
// .failed folder are protected the same way: a version row naming a file there
// does not get it deleted.
func TestVersionPurgeGuard_NeverRemovesAFileUnderITunesOrAnImportPathOrFailed(t *testing.T) {
	f := newVersionGuardFixture(t)
	itunes := filepath.Join(f.base, "lib", "iTunes Media", "Audiobooks", "Hush", "hush.m4b")
	failed := filepath.Join(f.base, "lib", "Gotham", ".failed", "knightfall.m4b")
	writeFile(t, itunes)
	writeFile(t, failed)
	f.ownRow(t, itunes, 1)
	f.ownRow(t, failed, 2)

	require.NoError(t, f.purge(t))
	assert.True(t, fileExists(itunes), "an iTunes file is never deleted")
	assert.True(t, fileExists(failed), "a .failed file is never deleted")
	got := f.reread(t)
	assert.Equal(t, database.BookVersionStatusInactivePurged, got.Status)
	for _, p := range []string{itunes, failed} {
		reason, ok := keptReason(got, p)
		assert.True(t, ok, "purge_kept = %+v does not name %s", got.PurgeKept, p)
		assert.Contains(t, reason, "never deletes")
	}
}

func TestVersionPurgeGuard_NeverRemovesAFileUnderAnImportPath(t *testing.T) {
	f := newVersionGuardFixture(t)
	imports := filepath.Join(f.base, "downloads")
	inImport := filepath.Join(imports, "Batman Hush", "hush.m4b")
	writeFile(t, inImport)
	_, err := f.store.CreateImportPath(imports, "downloads")
	require.NoError(t, err)
	f.ownRow(t, inImport, 1)
	// The import-path list is cached for a while by the helper; the cache is
	// per process, so make sure this test reads it fresh.
	importPathCacheForHelper = nil

	require.NoError(t, f.purge(t))
	assert.True(t, fileExists(inImport))
	_, ok := keptReason(f.reread(t), inImport)
	assert.True(t, ok)
}

// A path outside the library root is not the purge's to delete.
func TestVersionPurgeGuard_NeverRemovesAFileOutsideTheLibraryRoot(t *testing.T) {
	f := newVersionGuardFixture(t)
	outside := filepath.Join(f.base, "elsewhere", "hush.m4b")
	writeFile(t, outside)
	f.ownRow(t, outside, 1)

	require.NoError(t, f.purge(t))
	assert.True(t, fileExists(outside))
	reason, ok := keptReason(f.reread(t), outside)
	require.True(t, ok, "purge_kept = %+v", f.reread(t).PurgeKept)
	assert.Contains(t, reason, "outside the library root")
}

// A library that is not mounted: its files all read ENOENT. The purge claims
// nothing, removes nothing and does not mark the version purged.
func TestVersionPurgeGuard_AnUnmountedLibraryDoesNotCountEveryFileAsRemoved(t *testing.T) {
	f := newVersionGuardFixture(t)
	slotFile := filepath.Join(versions.VersionSlotDir(f.bookDir, f.ver.ID), "part1.m4b")
	writeFile(t, slotFile)
	f.ownRow(t, slotFile, 1)
	liveBookWithFile(t, f.store, "witness", filepath.Join(config.AppConfig.RootDir, "Gotham", "witness.m4b"))
	remount := unmountLibrary(t)

	err := f.purge(t)
	require.ErrorIs(t, err, ErrLibraryRootNotReady)
	got := f.reread(t)
	assert.Equal(t, database.BookVersionStatusTrash, got.Status, "nothing was claimed")
	assert.Nil(t, got.PurgedDate)

	remount()
	require.NoError(t, f.purge(t), "once the library is back the purge goes ahead")
	assert.False(t, fileExists(slotFile))
}

// A file that is not there under a folder that is not there either is "cannot
// tell", not "already removed": nothing was unlinked, so the version is not even
// claimed (it stays restorable trash) and is not marked purged with the file
// unaccounted for.
func TestVersionPurgeGuard_AMissingFileUnderAMissingFolderIsNotRemoved(t *testing.T) {
	f := newVersionGuardFixture(t)
	// Two library files that are there, so the root check (a majority of the
	// sampled live files must be visible) passes and the question reaches the
	// per-path decision.
	liveBookWithFile(t, f.store, "witness-one", filepath.Join(config.AppConfig.RootDir, "Gotham", "witness-one.m4b"))
	liveBookWithFile(t, f.store, "witness-two", filepath.Join(config.AppConfig.RootDir, "Gotham", "witness-two.m4b"))
	ghost := filepath.Join(f.base, "lib", "Gotham", "No Such Folder", "ghost.m4b")
	f.ownRow(t, ghost, 1)

	err := f.purge(t)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "cannot tell whether"), "error = %v", err)
	assert.False(t, errors.Is(err, versions.ErrPurgeFailed), "it may pass; it is not terminal")
	got := f.reread(t)
	assert.Equal(t, database.BookVersionStatusTrash, got.Status, "nothing was unlinked, so no claim was taken")
	assert.Nil(t, got.PurgedDate)

	// With the folder there (and holding something), the file is simply gone.
	writeFile(t, filepath.Join(filepath.Dir(ghost), "neighbour.m4b"))
	require.NoError(t, f.purge(t))
	assert.Equal(t, database.BookVersionStatusInactivePurged, f.reread(t).Status)
}

// A row naming a directory that holds files: kept and reported, not stranded.
func TestVersionPurgeGuard_ADirectoryThatHoldsFilesIsKept(t *testing.T) {
	f := newVersionGuardFixture(t)
	dir := filepath.Join(f.base, "lib", "Gotham", "Knightfall")
	writeFile(t, filepath.Join(dir, "part1.m4b"))
	f.ownRow(t, dir, 1)

	require.NoError(t, f.purge(t))
	assert.True(t, fileExists(filepath.Join(dir, "part1.m4b")))
	reason, ok := keptReason(f.reread(t), dir)
	require.True(t, ok)
	assert.Contains(t, reason, "still holds files")
}
