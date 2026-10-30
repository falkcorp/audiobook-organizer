// file: internal/audiobooks/protected_path_symlink_test.go
// version: 1.1.0
// guid: 0d6b9f41-8a27-4c53-b1e8-73f5a2c90d14
// last-edited: 2026-10-29

package audiobooks

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
)

// resetImportPathCacheForHelper empties the helper's process-wide import-path
// cache now and when the test ends, so one test's list never reaches another.
func resetImportPathCacheForHelper(t *testing.T) {
	t.Helper()
	reset := func() {
		importPathCacheForHelperMu.Lock()
		importPathCacheForHelper = nil
		importPathCacheForHelperAt = time.Time{}
		importPathCacheForHelperMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// setITunesPaths sets the two configured iTunes library files for one test.
func setITunesPaths(t *testing.T, read, itl string) {
	t.Helper()
	prevRead, prevITL := config.AppConfig.ITunes.LibraryReadPath, config.AppConfig.ITunes.LibraryITLPath
	t.Cleanup(func() {
		config.AppConfig.ITunes.LibraryReadPath, config.AppConfig.ITunes.LibraryITLPath = prevRead, prevITL
	})
	config.AppConfig.ITunes.LibraryReadPath, config.AppConfig.ITunes.LibraryITLPath = read, itl
}

type failingImportPathLister struct{}

func (failingImportPathLister) GetAllImportPaths() ([]database.ImportPath, error) {
	return nil, errors.New("store is closed")
}

// A file reached through a symlink into the iTunes library folder is the iTunes
// file: protected whichever configured library file names the folder.
func TestProtectedPathChecked_SymlinkIntoTheITunesFolder(t *testing.T) {
	for name, set := range map[string]func(dir string){
		"library read path": func(dir string) { setITunesPaths(t, filepath.Join(dir, "Library.xml"), "") },
		"library itl path":  func(dir string) { setITunesPaths(t, "", filepath.Join(dir, "Library.itl")) },
	} {
		t.Run(name, func(t *testing.T) {
			resetImportPathCacheForHelper(t)
			base := t.TempDir()
			itunes := filepath.Join(base, "lib", "Gotham Music")
			set(itunes)
			real := filepath.Join(itunes, "Hush", "hush.m4b")
			writeFile(t, real)
			require.NoError(t, os.MkdirAll(filepath.Join(base, "lib", "Gotham"), 0o755))
			dirLink := filepath.Join(base, "lib", "Gotham", "link")
			require.NoError(t, os.Symlink(filepath.Join(itunes, "Hush"), dirLink))
			fileLink := filepath.Join(base, "lib", "Gotham", "hush-link.m4b")
			require.NoError(t, os.Symlink(real, fileLink))
			dangling := filepath.Join(base, "lib", "Gotham", "gone-link.m4b")
			require.NoError(t, os.Symlink(filepath.Join(itunes, "Hush", "gone.m4b"), dangling))

			for what, p := range map[string]string{
				"plain spelling":             real,
				"symlinked parent directory": filepath.Join(dirLink, "hush.m4b"),
				"symlinked file":             fileLink,
				"dangling link":              dangling,
				"missing file under a link":  filepath.Join(dirLink, "new", "x.m4b"),
			} {
				got, err := protectedPathChecked(nil, p)
				require.NoError(t, err, what)
				assert.True(t, got, what)
				assert.True(t, isProtectedPath(nil, p), what)
			}
		})
	}
}

// The configured iTunes folder can itself be a symlink, and the file spelled by
// its real path.
func TestProtectedPathChecked_ITunesFolderConfiguredThroughASymlink(t *testing.T) {
	resetImportPathCacheForHelper(t)
	base := t.TempDir()
	real := filepath.Join(base, "lib", "Gotham Music", "Hush", "hush.m4b")
	writeFile(t, real)
	link := filepath.Join(base, "music")
	require.NoError(t, os.Symlink(filepath.Join(base, "lib", "Gotham Music"), link))
	setITunesPaths(t, filepath.Join(link, "Library.xml"), "")

	got, err := protectedPathChecked(nil, real)
	require.NoError(t, err)
	assert.True(t, got)
}

func TestProtectedPathChecked_SymlinkIntoAnImportPath(t *testing.T) {
	resetImportPathCacheForHelper(t)
	setITunesPaths(t, "", "")
	base := t.TempDir()
	imports := filepath.Join(base, "downloads")
	real := filepath.Join(imports, "Batman Hush", "hush.m4b")
	writeFile(t, real)
	require.NoError(t, os.MkdirAll(filepath.Join(base, "lib", "Gotham"), 0o755))
	link := filepath.Join(base, "lib", "Gotham", "link")
	require.NoError(t, os.Symlink(filepath.Join(imports, "Batman Hush"), link))
	store := &fakeImportPathLister{paths: []database.ImportPath{{Path: imports}}}

	got, err := protectedPathChecked(store, filepath.Join(link, "hush.m4b"))
	require.NoError(t, err)
	assert.True(t, got)
}

// No false positives: a path that is neither under a protected folder nor
// linked into one is unprotected, with symlinks and an iTunes folder around.
func TestProtectedPathChecked_UnprotectedPathStaysUnprotected(t *testing.T) {
	resetImportPathCacheForHelper(t)
	base := t.TempDir()
	setITunesPaths(t, filepath.Join(base, "lib", "Gotham Music", "Library.xml"), "")
	writeFile(t, filepath.Join(base, "lib", "Gotham Music", "Hush", "hush.m4b"))
	free := filepath.Join(base, "lib", "Arkham", "Joker", "killing-joke.m4b")
	writeFile(t, free)
	link := filepath.Join(base, "lib", "Arkham", "link")
	require.NoError(t, os.Symlink(filepath.Join(base, "lib", "Arkham", "Joker"), link))
	store := &fakeImportPathLister{paths: []database.ImportPath{{Path: filepath.Join(base, "downloads")}}}

	for name, p := range map[string]string{
		"plain":        free,
		"through link": filepath.Join(link, "killing-joke.m4b"),
		"missing":      filepath.Join(base, "lib", "Arkham", "Joker", "missing.m4b"),
	} {
		got, err := protectedPathChecked(store, p)
		require.NoError(t, err, name)
		assert.False(t, got, name)
	}
}

// A path whose symlinks cannot be resolved (a link cycle) is not passed as
// unprotected: the boolean answer is "protected", and the checked answer says
// why, so a caller that deletes can keep the file and retry instead.
func TestProtectedPathChecked_ResolutionErrorFailsClosed(t *testing.T) {
	resetImportPathCacheForHelper(t)
	setITunesPaths(t, "", "")
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, "lib"), 0o755))
	a := filepath.Join(base, "lib", "a")
	b := filepath.Join(base, "lib", "b")
	require.NoError(t, os.Symlink(b, a))
	require.NoError(t, os.Symlink(a, b))
	p := filepath.Join(a, "x.m4b")

	got, err := protectedPathChecked(nil, p)
	assert.True(t, got)
	require.ErrorIs(t, err, errProtectedPathUnresolved)
	assert.ErrorIs(t, err, syscall.ELOOP, "a link cycle stays distinguishable from a filesystem that failed to answer")
	assert.True(t, isProtectedPath(nil, p), "tag writes and previews treat it as protected")
}

// The purge does not read an unresolvable path as a protected one it is done
// with (the book would go and the file stay with no record): it is a path that
// cannot be decided, and the file is kept. A link cycle is the permanent kind and
// is marked so (errPathUnresolvable): a pass purge does not hold the book in the
// trash for it, it purges the book and leaves the file where it is.
func TestPlanPurgePathIn_UnresolvableSymlinksKeepTheFile(t *testing.T) {
	resetImportPathCacheForHelper(t)
	setITunesPaths(t, "", "")
	svc, _, base := setupPurgeBoundary(t)
	dir := filepath.Join(base, "lib", "Gotham")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	require.NoError(t, os.Symlink(b, a))
	require.NoError(t, os.Symlink(a, b))

	v := svc.planPurgePathIn(filepath.Join(a, "x.m4b"), nil, nil)
	assert.False(t, v.protected, "%+v", v)
	assert.Empty(t, v.remove)
	require.Error(t, v.keptErr)
	assert.ErrorIs(t, v.keptErr, errPathLookupFailed)
	assert.ErrorIs(t, v.keptErr, errPathUnresolvable)
	// An unresolvable path does not hold the book in the trash: the purge goes on
	// and keeps the file (reported as "book purged, file kept"). The bounded
	// retries only apply to a journal record that already exists.
	assert.Nil(t, fileUndecided(&database.Book{ID: "year-one"}, v), "the book is purged and the file kept, not held")
}

// An import-path list that cannot be read is still reported when nothing else
// protects the path, and a symlink into the iTunes folder wins over that error.
func TestProtectedPathChecked_ImportPathErrorStillReportedAndSymlinkStillWins(t *testing.T) {
	resetImportPathCacheForHelper(t)
	base := t.TempDir()
	setITunesPaths(t, filepath.Join(base, "lib", "Gotham Music", "Library.xml"), "")
	real := filepath.Join(base, "lib", "Gotham Music", "Hush", "hush.m4b")
	writeFile(t, real)
	free := filepath.Join(base, "lib", "Arkham", "killing-joke.m4b")
	writeFile(t, free)
	link := filepath.Join(base, "lib", "Arkham", "hush-link.m4b")
	require.NoError(t, os.Symlink(real, link))

	got, err := protectedPathChecked(failingImportPathLister{}, free)
	assert.False(t, got)
	assert.Error(t, err, "nothing else protects it, so the unreadable list is reported")

	got, err = protectedPathChecked(failingImportPathLister{}, link)
	assert.True(t, got)
	assert.NoError(t, err)
}

// A configured protected folder that cannot be resolved is the configuration's
// problem, not the path's. It must hold the book in the trash (a lookup that may
// pass), never be marked errPathUnresolvable: that class is not held, so one
// looping iTunes folder would let every due book go with its file left behind.
func TestPlanPurgePathIn_UnresolvableProtectedFolderHoldsTheBook(t *testing.T) {
	resetImportPathCacheForHelper(t)
	svc, _, base := setupPurgeBoundary(t)
	loopA := filepath.Join(base, "music-a")
	loopB := filepath.Join(base, "music-b")
	require.NoError(t, os.Symlink(loopB, loopA))
	require.NoError(t, os.Symlink(loopA, loopB))
	setITunesPaths(t, filepath.Join(loopA, "Library.xml"), "")
	x := filepath.Join(base, "lib", "Gotham", "year-one.m4b")
	writeFile(t, x)

	v := svc.planPurgePathIn(x, nil, nil)
	assert.False(t, v.protected, "%+v", v)
	assert.Empty(t, v.remove, "the file is not removed")
	require.Error(t, v.keptErr)
	assert.ErrorIs(t, v.keptErr, errPathLookupFailed)
	assert.NotErrorIs(t, v.keptErr, errPathUnresolvable, "a bad folder is not an unresolvable path")
	assert.Contains(t, v.keptErr.Error(), "music-a", "the message names the folder")
	assert.NotNil(t, fileUndecided(&database.Book{ID: "year-one"}, v), "the book is held in the trash")
	assert.True(t, fileExists(x))
}

// The decision the book purge and the version purge share: a file reached
// through a symlink into the iTunes folder is protected (kept, not an error).
func TestPlanPurgePathIn_SymlinkIntoITunesFolderIsProtected(t *testing.T) {
	resetImportPathCacheForHelper(t)
	svc, _, base := setupPurgeBoundary(t)
	itunes := filepath.Join(base, "lib", "Gotham Music")
	setITunesPaths(t, filepath.Join(itunes, "Library.xml"), "")
	real := filepath.Join(itunes, "Hush", "hush.m4b")
	writeFile(t, real)
	link := filepath.Join(base, "lib", "Gotham", "link")
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(itunes, "Hush"), link))

	v := svc.planPurgePathIn(filepath.Join(link, "hush.m4b"), nil, nil)
	assert.True(t, v.protected, "%+v", v)
	assert.Empty(t, v.remove)
	assert.True(t, fileExists(real))
}

// End to end: a trashed version whose row names a file through a symlink into
// the iTunes folder. The purge leaves the iTunes file alone and notes it kept.
func TestVersionPurgeGuard_NeverRemovesAFileReachedThroughASymlinkIntoITunes(t *testing.T) {
	resetImportPathCacheForHelper(t)
	f := newVersionGuardFixtureBookTrashed(t)
	itunes := filepath.Join(f.base, "lib", "Gotham Music")
	setITunesPaths(t, filepath.Join(itunes, "Library.xml"), "")
	real := filepath.Join(itunes, "Hush", "hush.m4b")
	writeFile(t, real)
	link := filepath.Join(f.base, "lib", "Gotham", "link")
	require.NoError(t, os.Symlink(filepath.Join(itunes, "Hush"), link))
	viaLink := filepath.Join(link, "hush.m4b")
	f.ownRow(t, viaLink, 1)

	require.NoError(t, f.purge(t))
	assert.True(t, fileExists(real), "a file under the iTunes folder reached through a symlink is never deleted")
	reason, ok := keptReason(f.reread(t), viaLink)
	require.True(t, ok, "purge_kept = %+v", f.reread(t).PurgeKept)
	assert.Contains(t, reason, "never deletes")
}

// repointedITunesFolder sets up the iTunes folder as a symlink that, until the
// test re-points it, leads somewhere that holds no file of interest, and warms
// the shared protected-folder cache with that answer. It returns the file inside
// the real iTunes folder and a func that re-points the link at that folder, as an
// operator fixing a mount would, inside the cache window.
func repointedITunesFolder(t *testing.T, base string) (hush string, repoint func()) {
	t.Helper()
	resetImportPathCacheForHelper(t)
	itunes := filepath.Join(base, "lib", "Gotham Music")
	hush = filepath.Join(itunes, "Hush", "hush.m4b")
	writeFile(t, hush)
	elsewhere := filepath.Join(base, "lib", "Other Music")
	require.NoError(t, os.MkdirAll(elsewhere, 0o755))
	link := filepath.Join(base, "music")
	require.NoError(t, os.Symlink(elsewhere, link))
	setITunesPaths(t, filepath.Join(link, "Library.xml"), "")

	require.False(t, isProtectedPath(nil, hush), "the link leads away from the file, and the cache now holds that")
	return hush, func() {
		require.NoError(t, os.Remove(link))
		require.NoError(t, os.Symlink(itunes, link))
	}
}

// The purge protects at least as much as a fresh look: an iTunes folder whose
// link is re-pointed at the folder holding the file, inside the cache window,
// is already protecting it. The cached answer (tag writes, previews) is the
// stale one and may stay so; a deletion must not use it.
func TestPlanPurgePathIn_ALinkRepointedInsideTheCacheWindowStillProtects(t *testing.T) {
	svc, _, base := setupPurgeBoundary(t)
	hush, repoint := repointedITunesFolder(t, base)
	repoint()

	v := svc.planPurgePathIn(hush, nil, nil)
	assert.True(t, v.protected, "a fresh look protects it: %+v", v)
	assert.Empty(t, v.remove, "and it is not scheduled for removal")
}

// The version purge decides its paths with the same function, so it keeps the
// file too.
func TestVersionPurgeDecider_ALinkRepointedInsideTheCacheWindowStillKeeps(t *testing.T) {
	svc, _, base := setupPurgeBoundary(t)
	hush, repoint := repointedITunesFolder(t, base)
	repoint()

	decision, _, err := svc.VersionPurgeGuard().NewDecider().Decide(hush)
	require.NoError(t, err)
	assert.Equal(t, versions.PurgeKeep, decision)
}
