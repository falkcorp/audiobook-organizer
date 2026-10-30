// file: internal/audiobooks/tombstone_sweep_guard_test.go
// version: 1.1.0
// guid: 7a3c9e15-d604-4b82-a1f7-5e08c2b9d371
// last-edited: 2026-10-30

package audiobooks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/sweep"
)

// The guard is the book purge's own decision (planPurgePathIn + otherBookRowAt)
// plus the stamp comparison, run against a real store: these tests are the ones
// that show the tombstone sweeper refuses what the purge refuses. The sweep
// package's own tests use a fake guard and only show that it honours answers.
var _ sweep.FileGuard = TombstoneSweepGuard{}

func statStamp(t *testing.T, path string) *database.TombstoneFileStamp {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	s := stampOf(identityOf(info), info.IsDir())
	return &s
}

// Every refusal the brief names, as the real guard answers it. Check is a
// decision only: it never unlinks, so each row also asserts the file is intact.
func TestTombstoneSweepGuard_Check(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	lib := config.AppConfig.RootDir
	imports := filepath.Join(base, "downloads")
	_, err := store.CreateImportPath(imports, "downloads")
	require.NoError(t, err)

	file := func(rel string) string {
		p := filepath.Join(base, rel)
		writeFile(t, p)
		return p
	}

	clean := file("lib/Gotham/year-one.m4b")
	outside := file("elsewhere/hush.m4b")
	protectedImport := file("downloads/Batman Hush/hush.m4b")
	protectedFailed := file("lib/Gotham/.failed/knightfall.m4b")
	liveNamed := file("lib/Gotham/long-halloween.m4b")
	_, err = store.CreateBook(&database.Book{ID: "long-halloween", Title: "Batman: The Long Halloween", FilePath: liveNamed})
	require.NoError(t, err)
	trashedNamed := file("lib/Gotham/dark-victory.m4b")
	softDeleted(t, store, "dark-victory", trashedNamed)
	rowNamed := file("lib/Gotham/zero-year.m4b")
	require.NoError(t, store.CreateBookFile(&database.BookFile{ID: "zero-year-f", BookID: "long-halloween", FilePath: rowNamed, Format: "m4b"}))
	changed := file("lib/Gotham/court-of-owls.m4b")
	changedStamp := statStamp(t, changed)
	require.NoError(t, os.WriteFile(changed, []byte("a different, longer file"), 0o644))
	stampedAndLeftAlone := file("lib/Gotham/rebirth.m4b")
	notEmptyDir := filepath.Join(lib, "Gotham", "Arkham")
	writeFile(t, filepath.Join(notEmptyDir, "inmate.m4b"))
	gone := filepath.Join(lib, "Gotham", "gone.m4b")

	// Retired: a row of a trashed shell, repointed to a live book.
	retiredPath := file("lib/shell/01.mp3")
	holder, err := store.CreateBook(&database.Book{Title: "Year Two", FilePath: filepath.Join(lib, "holder")})
	require.NoError(t, err)
	shell, err := store.CreateBook(&database.Book{Title: "The Man Who Falls", FilePath: filepath.Dir(retiredPath)})
	require.NoError(t, err)
	now := time.Now()
	ver, err := store.CreateBookVersion(&database.BookVersion{
		BookID: holder.ID, Status: database.BookVersionStatusRetired, Format: "mp3", Source: "retired_book",
		RetiredFromBookID: shell.ID, RetireOpID: "op-1", RetiredAt: &now,
	})
	require.NoError(t, err)
	row := &database.BookFile{BookID: shell.ID, FilePath: retiredPath}
	require.NoError(t, store.CreateBookFile(row))
	require.NoError(t, store.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: shell.ID}}, holder.ID, ver.ID))

	cases := []struct {
		name       string
		path       string
		stamp      *database.TombstoneFileStamp
		wantRemove bool
		wantErr    string // substring; empty = no error
	}{
		{"clean file with its own stamp is removable", clean, statStamp(t, clean), true, ""},
		{"path outside the library root", outside, statStamp(t, outside), false, "outside the library root"},
		{"import path is protected", protectedImport, statStamp(t, protectedImport), false, "never deleted"},
		{".failed folder is protected", protectedFailed, statStamp(t, protectedFailed), false, "never deleted"},
		{"live book still names the path", liveNamed, statStamp(t, liveNamed), false, "long-halloween"},
		{"trashed book still names the path", trashedNamed, statStamp(t, trashedNamed), false, "dark-victory"},
		{"book_file row still names the path", rowNamed, statStamp(t, rowNamed), false, "zero-year-f"},
		{"retired book_file row still names the path", retiredPath, statStamp(t, retiredPath), false, row.ID},
		{"stamp of a different file (replaced since)", changed, changedStamp, false, "not the file the purge decided"},
		{"stamp of a different kind (dir stamp, file on disk)", stampedAndLeftAlone, func() *database.TombstoneFileStamp {
			s := *statStamp(t, stampedAndLeftAlone)
			s.IsDir = true
			return &s
		}(), false, "not the file the purge decided"},
		{"no stamp: predates stamps or purge kept files", clean, nil, false, "no stamp"},
		{"directory that still holds files", notEmptyDir, statStamp(t, notEmptyDir), false, "still holds files"},
		{"nothing at the path: only the tombstone goes", gone, nil, false, ""},
	}
	guard := svc.TombstoneSweepGuard()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			remove, err := guard.Check(tc.path, tc.stamp)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.wantRemove, remove)
			if tc.path != gone {
				assert.True(t, fileExists(tc.path), "Check decides; it never unlinks")
			}
		})
	}
}

// The sweep wired to the real guard, end to end, on a real store: the clean
// tombstone's file goes with its tombstone; every other tombstone keeps both and
// is counted as a refusal.
func TestSweepTombstones_WithTheRealGuard(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	lib := config.AppConfig.RootDir
	// libraryRootReady needs a live book on disk to believe the library is mounted.
	liveBookWithFile(t, store, "witness", filepath.Join(lib, "Gotham", "witness.m4b"))

	clean := filepath.Join(lib, "Gotham", "year-one.m4b")
	writeFile(t, clean)
	unstamped := filepath.Join(lib, "Gotham", "hush.m4b") // a plain tombstone: a purge without delete_files
	writeFile(t, unstamped)
	outside := filepath.Join(base, "elsewhere", "knightfall.m4b")
	writeFile(t, outside)
	named := filepath.Join(lib, "Gotham", "named.m4b")
	writeFile(t, named)
	softDeleted(t, store, "dark-victory", named)

	require.NoError(t, store.CreateStampedBookTombstone(&database.Book{ID: "t-clean", FilePath: clean}, *statStamp(t, clean)))
	require.NoError(t, store.CreateBookTombstone(&database.Book{ID: "t-unstamped", FilePath: unstamped}))
	require.NoError(t, store.CreateStampedBookTombstone(&database.Book{ID: "t-outside", FilePath: outside}, *statStamp(t, outside)))
	require.NoError(t, store.CreateStampedBookTombstone(&database.Book{ID: "t-named", FilePath: named}, *statStamp(t, named)))

	res, err := sweep.SweepTombstones(store, svc.TombstoneSweepGuard())
	require.NoError(t, err)

	assert.Equal(t, 1, res.TombstonesCleaned, "result = %+v", res)
	assert.Equal(t, 3, res.RefusedFiles, "result = %+v", res)
	assert.Len(t, res.Refusals, 3)
	assert.Empty(t, res.Errors)
	assert.False(t, fileExists(clean), "the clean, stamped file is removed")
	for _, p := range []string{unstamped, outside, named} {
		assert.True(t, fileExists(p), "%s must be kept", p)
	}
	left, err := store.ListBookTombstones(0)
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, b := range left {
		ids[b.ID] = true
	}
	assert.Equal(t, map[string]bool{"t-unstamped": true, "t-outside": true, "t-named": true}, ids, "refused tombstones are kept")
}

// A file that was replaced after the purge decided on it is not removed.
func TestSweepTombstones_ReplacedFileIsKept(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	lib := config.AppConfig.RootDir
	liveBookWithFile(t, store, "witness", filepath.Join(lib, "Gotham", "witness.m4b"))
	p := filepath.Join(lib, "Gotham", "no-mans-land.m4b")
	writeFile(t, p)
	require.NoError(t, store.CreateStampedBookTombstone(&database.Book{ID: "t1", FilePath: p}, *statStamp(t, p)))
	// A re-download puts different audio at the same organised path.
	require.NoError(t, os.WriteFile(p, []byte("a different and longer recording"), 0o644))

	res, err := sweep.SweepTombstones(store, svc.TombstoneSweepGuard())
	require.NoError(t, err)
	assert.Equal(t, 1, res.RefusedFiles)
	assert.Equal(t, 0, res.TombstonesCleaned)
	assert.True(t, fileExists(p))
}

// An unmounted library: the witness book's file is missing, so every file reads
// "gone". The sweep keeps every tombstone that names a file.
func TestSweepTombstones_UnmountedLibraryKeepsEverything(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	lib := config.AppConfig.RootDir
	witness := filepath.Join(lib, "Gotham", "witness.m4b")
	liveBookWithFile(t, store, "witness", witness)
	p := filepath.Join(lib, "Gotham", "the-cult.m4b")
	writeFile(t, p)
	require.NoError(t, store.CreateStampedBookTombstone(&database.Book{ID: "t1", FilePath: p}, *statStamp(t, p)))
	require.NoError(t, os.Remove(witness))
	require.NoError(t, os.RemoveAll(filepath.Join(lib, "Gotham")))
	writeFile(t, p) // the file is back, but the library cannot vouch for itself

	res, err := sweep.SweepTombstones(store, svc.TombstoneSweepGuard())
	require.NoError(t, err)
	assert.Equal(t, 1, res.RefusedFiles, "result = %+v", res)
	assert.True(t, fileExists(p))
}

// writeTombstone stamps only when the purge committed to removing the file.
func TestWriteTombstone_StampsOnlyACommittedRemoval(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	p := filepath.Join(config.AppConfig.RootDir, "Gotham", "gotham-central.m4b")
	writeFile(t, p)
	info, err := os.Stat(p)
	require.NoError(t, err)
	verdict := purgeFileVerdict{remove: p, id: identityOf(info)}
	book := func(id string) *database.Book { return &database.Book{ID: id, FilePath: p} }

	cases := []struct {
		name      string
		plan      *filePlan
		wantStamp bool
	}{
		{"journaled removal of this very path", &filePlan{path: p, v: verdict, journal: true}, true},
		{"purge without delete_files (no plan)", nil, false},
		{"plan the preview said stays (not journaled)", &filePlan{path: p, v: verdict, journal: false}, false},
		{"plan with nothing to remove", &filePlan{path: p, v: purgeFileVerdict{}, journal: true}, false},
		{"plan made for another path", &filePlan{path: p + ".other", v: verdict, journal: true}, false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := string(rune('a' + i))
			require.NoError(t, svc.writeTombstone(book(id), tc.plan))
			st, err := store.GetBookTombstoneStamp(id)
			require.NoError(t, err)
			if !tc.wantStamp {
				assert.Nil(t, st)
				return
			}
			require.NotNil(t, st)
			assert.Equal(t, stampOf(verdict.id, false), *st)
			tomb, err := store.GetBookTombstone(id)
			require.NoError(t, err)
			require.NotNil(t, tomb)
			assert.Equal(t, p, tomb.FilePath)
		})
	}
}

// deleteFailingStore is a real store whose DeleteBook fails, so a purge's row
// delete can be made to fail after its tombstone is written. Everything else,
// including the stamped-tombstone capability, is the real store's.
type deleteFailingStore struct{ *database.PebbleStore }

func (deleteFailingStore) DeleteBook(string) error {
	return errors.New("pebble: injected delete failure")
}

// A purge whose row delete fails leaves the book live. The stamped tombstone it
// wrote is withdrawn (rewritten plain, as the code before stamps left it), so the
// two records agree, and the sweep guard refuses the path (the live row names
// it) with or without the stamp.
func TestPurgeDeleteRow_FailedDeleteWithdrawsTheTombstoneStamp(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	svc := NewAudiobookService(deleteFailingStore{store})
	lib := config.AppConfig.RootDir
	p := filepath.Join(lib, "Gotham", "under-the-red-hood.m4b")
	writeFile(t, p)
	book, err := store.CreateBook(&database.Book{ID: "red-hood", Title: "Batman: Under the Red Hood", FilePath: p})
	require.NoError(t, err)
	info, err := os.Stat(p)
	require.NoError(t, err)
	plan := &filePlan{path: p, journal: true, v: purgeFileVerdict{remove: p, id: identityOf(info)}}

	err = svc.purgeDeleteRow(book, plan)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to delete DB record")

	tomb, err := store.GetBookTombstone(book.ID)
	require.NoError(t, err)
	require.NotNil(t, tomb, "the tombstone the old code left on this path is still there")
	stamp, err := store.GetBookTombstoneStamp(book.ID)
	require.NoError(t, err)
	assert.Nil(t, stamp, "the stamp was withdrawn: no removal is decided for a book that is still here")
	live, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, live, "the book row stays")

	// The guard refuses the path even when handed the stamp the purge once saw.
	remove, err := svc.TombstoneSweepGuard().Check(p, statStamp(t, p))
	assert.False(t, remove)
	require.Error(t, err)
	assert.Contains(t, err.Error(), book.ID)

	// And the sweep cleans the orphan tombstone without touching the file.
	res, err := sweep.SweepTombstones(store, svc.TombstoneSweepGuard())
	require.NoError(t, err)
	assert.Equal(t, 1, res.TombstonesCleaned)
	assert.Equal(t, 0, res.RefusedFiles)
	assert.True(t, fileExists(p))
}
