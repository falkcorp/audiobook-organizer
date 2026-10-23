// file: internal/scanner/retired_rows_test.go
// version: 1.0.0
// guid: 2f6a9c31-8d47-4b05-a1e3-7c9d50b2f184
// last-edited: 2026-10-23

package scanner

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// retiredScanFixture is a pebble-backed scanner with a live holder book and a
// trashed shell whose files can be retired into the holder as a version.
type retiredScanFixture struct {
	t       *testing.T
	store   *database.PebbleStore
	root    string
	holder  *database.Book
	shell   *database.Book
	version *database.BookVersion
}

func newRetiredScanFixture(t *testing.T) *retiredScanFixture {
	t.Helper()
	store, cleanup := setupPebbleStore(t)
	t.Cleanup(cleanup)
	store.WaitForWarmup()
	useScannerStore(t, store)

	prev := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prev })
	root := t.TempDir()
	config.AppConfig.RootDir = root
	config.AppConfig.SupportedExtensions = []string{".mp3"}

	holder, err := store.CreateBook(&database.Book{Title: "Holder", FilePath: filepath.Join(root, "holder")})
	require.NoError(t, err)
	shell, err := store.CreateBook(&database.Book{Title: "Shell", FilePath: filepath.Join(root, "shell")})
	require.NoError(t, err)
	v, err := store.CreateBookVersion(&database.BookVersion{
		BookID: holder.ID, Status: database.BookVersionStatusRetired, Format: "mp3", Source: "retired_book",
		RetiredFromBookID: shell.ID, RetireOpID: "op-1", RetiredAt: new(time.Now()),
	})
	require.NoError(t, err)
	return &retiredScanFixture{t: t, store: store, root: root, holder: holder, shell: shell, version: v}
}

func (f *retiredScanFixture) write(rel, body string) string {
	f.t.Helper()
	p := filepath.Join(f.root, rel)
	require.NoError(f.t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(f.t, os.WriteFile(p, []byte(body), 0o644))
	return p
}

// retiredRow creates a shell row and retires it into the holder.
func (f *retiredScanFixture) retiredRow(path, hash string) *database.BookFile {
	f.t.Helper()
	row := &database.BookFile{BookID: f.shell.ID, FilePath: path, FileHash: hash, Format: "mp3"}
	require.NoError(f.t, f.store.CreateBookFile(row))
	require.NoError(f.t, f.store.RetireBookFiles(
		[]database.BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: f.shell.ID}}, f.holder.ID, f.version.ID))
	return row
}

// A scanned file whose only claim is a retired row is skipped with the
// retired-version reason, and never read as the holder's own file, even when
// every live row of the holder is Missing (the case that used to return
// sameBook=holder and repoint the holder at the retired folder).
func TestCheckFileOwnership_RetiredOnlyFileIsSkipped(t *testing.T) {
	f := newRetiredScanFixture(t)
	retiredPath := f.write("shell/01.mp3", "retired-bytes")
	require.NoError(t, f.store.CreateBookFile(&database.BookFile{
		BookID: f.holder.ID, FilePath: filepath.Join(f.root, "holder", "gone.mp3"), Missing: true}))
	f.retiredRow(retiredPath, "h-retired")

	v, err := checkFileOwnership(&Book{FilePath: retiredPath})
	require.NoError(t, err)
	require.True(t, v.skip)
	require.Empty(t, v.sameBook)
	require.Empty(t, v.appendTo)
	require.Equal(t, []string{f.holder.ID}, v.owners)
	require.Contains(t, v.reason, "retired version of book "+f.holder.ID)
	require.Contains(t, v.reason, "version "+f.version.ID)
}

// One retired-only file among live-owned ones skips the whole scanned book,
// and nothing is appended to the holder.
func TestCheckFileOwnership_OneRetiredOnlyFileSkipsTheGroup(t *testing.T) {
	f := newRetiredScanFixture(t)
	live := f.write("holder/01.mp3", "live")
	retiredPath := f.write("shell/02.mp3", "retired")
	require.NoError(t, f.store.CreateBookFile(&database.BookFile{BookID: f.holder.ID, FilePath: live, TrackNumber: 1}))
	f.retiredRow(retiredPath, "h-retired")

	v, err := checkFileOwnership(&Book{FilePath: live, SegmentFiles: []string{live, retiredPath}})
	require.NoError(t, err)
	require.True(t, v.skip)
	require.Empty(t, v.appendTo, "a file beside a retired version is never appended to the holder")
	require.Contains(t, v.reason, "retired version of book")
}

// A file that a live row also claims ignores its retired row: the live owner's
// rescan is unaffected, so the holder and the path's real owner are not two
// owners.
func TestCheckFileOwnership_LiveClaimIgnoresRetiredRowAtSamePath(t *testing.T) {
	f := newRetiredScanFixture(t)
	shared := f.write("owner/01.mp3", "shared")
	f.retiredRow(shared, "h-shared")
	owner, err := f.store.CreateBook(&database.Book{Title: "Owner", FilePath: shared})
	require.NoError(t, err)
	require.NoError(t, f.store.CreateBookFile(&database.BookFile{BookID: owner.ID, FilePath: shared, TrackNumber: 1}))

	v, err := checkFileOwnership(&Book{FilePath: shared})
	require.NoError(t, err)
	require.False(t, v.skip, "reason: %s", v.reason)
}

// Scanned content that is held only by a retired row (the file moved or
// re-downloaded, so its path matches nothing) is known retired content: the
// save is skipped, nothing is created, and the holder is not adopted.
func TestSaveBookToDatabase_RetiredContentIsNotReimported(t *testing.T) {
	f := newRetiredScanFixture(t)
	oldPath := f.write("shell/01.mp3", "same-bytes")
	hash, err := ComputeFileHash(oldPath)
	require.NoError(t, err)
	f.retiredRow(oldPath, hash)
	copyPath := f.write("incoming/01.mp3", "same-bytes")

	before, err := f.store.GetAllBooksCore(0, 0)
	require.NoError(t, err)

	e, lerr := lookupScanExisting(&Book{FilePath: copyPath}, fileOwnershipVerdict{}, &hash)
	require.NoError(t, lerr)
	require.NotNil(t, e.retired)
	require.Equal(t, f.holder.ID, e.retired.holder)
	require.Nil(t, e.row(), "the holder must never be offered as the existing row")

	serr := saveBookToDatabase(t.Context(), &Book{Title: "Copy", FilePath: copyPath, FileHash: hash})
	require.ErrorIs(t, serr, errFileOwnedByOtherBook)
	var se *ownershipSkipError
	require.True(t, errors.As(serr, &se))
	require.Equal(t, []string{f.holder.ID}, se.Owners)
	require.Contains(t, se.Reason, "retired version")

	after, err := f.store.GetAllBooksCore(0, 0)
	require.NoError(t, err)
	require.Len(t, after, len(before), "no book may be created for retired content")
}

// A hash a live row also holds is not "known retired content": the live
// lookups own that case.
func TestLookupScanExisting_LiveRowAlsoHoldingHashIsNotARetiredHit(t *testing.T) {
	f := newRetiredScanFixture(t)
	p := f.write("shell/01.mp3", "dup-bytes")
	hash, err := ComputeFileHash(p)
	require.NoError(t, err)
	f.retiredRow(p, hash)
	live := f.write("holder/01.mp3", "dup-bytes")
	require.NoError(t, f.store.CreateBookFile(&database.BookFile{BookID: f.holder.ID, FilePath: live, FileHash: hash, TrackNumber: 1}))

	e, lerr := lookupScanExisting(&Book{FilePath: f.write("incoming/01.mp3", "dup-bytes")}, fileOwnershipVerdict{}, &hash)
	require.NoError(t, lerr)
	require.Nil(t, e.retired)
}

// A segment list is a retired hit at the same 80% bar as the live vote.
func TestLookupScanExisting_RetiredSegmentVoteNeedsTheBar(t *testing.T) {
	f := newRetiredScanFixture(t)
	var segs []string
	for i, body := range []string{"s1", "s2", "s3"} {
		old := f.write(filepath.Join("shell", body+".mp3"), body)
		h, err := ComputeFileHash(old)
		require.NoError(t, err)
		// Retire only two of the three: 2 of 3 is below ceil(0.8*3)=3.
		if i < 2 {
			f.retiredRow(old, h)
		}
		segs = append(segs, f.write(filepath.Join("incoming", body+".mp3"), body))
	}
	first, err := ComputeFileHash(segs[0])
	require.NoError(t, err)

	e, lerr := lookupScanExisting(&Book{FilePath: segs[0], SegmentFiles: segs}, fileOwnershipVerdict{}, &first)
	require.NoError(t, lerr)
	require.Nil(t, e.retired, "2 of 3 segments is below the bar")

	// Retire the third too: 3 of 3 clears it.
	old3 := filepath.Join(f.root, "shell", "s3.mp3")
	h3, err := ComputeFileHash(old3)
	require.NoError(t, err)
	f.retiredRow(old3, h3)
	e, lerr = lookupScanExisting(&Book{FilePath: segs[0], SegmentFiles: segs}, fileOwnershipVerdict{}, &first)
	require.NoError(t, lerr)
	require.NotNil(t, e.retired)
	require.Equal(t, 3, e.retired.matched)
	require.Equal(t, 3, e.retired.total)
}

// hashUnavailableStore fails the complete per-hash read, the way a store does
// while memdb is not serving.
type hashUnavailableStore struct{ *database.PebbleStore }

func (hashUnavailableStore) BookFilesWithHash(string) ([]database.BookFile, error) {
	return nil, database.ErrBookFilesWithHashUnavailable
}

// When the retired-content check cannot be answered completely, the import is
// skipped with an error (the file imports on a later scan), not treated as new.
func TestLookupScanExisting_RetiredCheckUnavailableFailsClosed(t *testing.T) {
	f := newRetiredScanFixture(t)
	useScannerStore(t, hashUnavailableStore{f.store})
	p := f.write("incoming/01.mp3", "fresh-bytes")
	hash, err := ComputeFileHash(p)
	require.NoError(t, err)

	_, lerr := lookupScanExisting(&Book{FilePath: p}, fileOwnershipVerdict{}, &hash)
	require.ErrorIs(t, lerr, database.ErrBookFilesWithHashUnavailable)
}
