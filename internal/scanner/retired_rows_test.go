// file: internal/scanner/retired_rows_test.go
// version: 1.6.0
// guid: 2f6a9c31-8d47-4b05-a1e3-7c9d50b2f184
// last-edited: 2026-10-26

package scanner

import (
	"errors"
	"fmt"
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

	holder, err := store.CreateBook(&database.Book{Title: "Year One", FilePath: filepath.Join(root, "holder")})
	require.NoError(t, err)
	shell, err := store.CreateBook(&database.Book{Title: "The Long Halloween", FilePath: filepath.Join(root, "shell")})
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
	owner, err := f.store.CreateBook(&database.Book{Title: "Knightfall", FilePath: shared})
	require.NoError(t, err)
	require.NoError(t, f.store.CreateBookFile(&database.BookFile{BookID: owner.ID, FilePath: shared, TrackNumber: 1}))

	v, err := checkFileOwnership(&Book{FilePath: shared})
	require.NoError(t, err)
	require.False(t, v.skip, "reason: %s", v.reason)
}

// The holder is itself the live owner of a path where it also carries a retired
// version's row: its own rescan proceeds, the retired row is not a second claim
// by the same book and does not turn the rescan into a skip.
func TestCheckFileOwnership_HolderOwnRescanIgnoresItsRetiredRowAtSamePath(t *testing.T) {
	f := newRetiredScanFixture(t)
	shared := f.write("holder/01.mp3", "shared")
	f.retiredRow(shared, "h-shared")
	require.NoError(t, f.store.CreateBookFile(&database.BookFile{BookID: f.holder.ID, FilePath: shared, TrackNumber: 1}))

	v, err := checkFileOwnership(&Book{FilePath: shared})
	require.NoError(t, err)
	require.False(t, v.skip, "reason: %s", v.reason)
}

// A retired folder plus one new file is skipped with the retired reason and
// nothing is appended to the holder: the new file beside a retired version is
// left for review, not grown into the holder.
func TestCheckFileOwnership_RetiredFolderPlusNewFileIsSkippedNotAppended(t *testing.T) {
	f := newRetiredScanFixture(t)
	old1 := f.write("shell/01.mp3", "r1")
	old2 := f.write("shell/02.mp3", "r2")
	fresh := f.write("shell/03.mp3", "brand-new")
	f.retiredRow(old1, "h-r1")
	f.retiredRow(old2, "h-r2")

	v, err := checkFileOwnership(&Book{FilePath: old1, SegmentFiles: []string{old1, old2, fresh}})
	require.NoError(t, err)
	require.True(t, v.skip)
	require.Empty(t, v.appendTo)
	require.Empty(t, v.sameBook)
	require.Equal(t, []string{f.holder.ID}, v.owners)
	require.Contains(t, v.reason, "2 of the scanned files are retired versions")
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

	serr := saveBookToDatabase(t.Context(), &Book{Title: "Zero Year", FilePath: copyPath, FileHash: hash})
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

// A new import is never refused while memdb is warming up (UseMemDB off makes
// the complete per-hash read refuse, the way an unpublished memdb does) when no
// retired version exists: there is nothing retired to find.
func TestLookupScanExisting_NoRetiredVersionsImportsWhileMemdbNotServing(t *testing.T) {
	f := newRetiredScanFixture(t)
	require.NoError(t, f.store.DeleteBookVersion(f.version.ID), "the fixture's version holds no rows")
	f.store.UseMemDB = false
	p := f.write("incoming/01.mp3", "fresh-bytes")
	hash, err := ComputeFileHash(p)
	require.NoError(t, err)
	_, werr := f.store.BookFilesWithHash(hash)
	require.ErrorIs(t, werr, database.ErrBookFilesWithHashUnavailable, "memdb is not serving")

	e, lerr := lookupScanExisting(&Book{FilePath: p}, fileOwnershipVerdict{}, &hash)
	require.NoError(t, lerr, "no retired version exists, so warmup alone must not block the import")
	require.Nil(t, e.retired)
	require.Nil(t, e.row())
}

// Once a retired version holds rows, the check does not fail open while memdb
// is not serving: it reads Pebble, still finds the retired content, and still
// lets a fresh file and a live copy through.
func TestLookupScanExisting_RetiredContentFoundWhileMemdbNotServing(t *testing.T) {
	f := newRetiredScanFixture(t)
	oldPath := f.write("shell/01.mp3", "same-bytes")
	hash, err := ComputeFileHash(oldPath)
	require.NoError(t, err)
	f.retiredRow(oldPath, hash)
	f.store.UseMemDB = false
	_, werr := f.store.BookFilesWithHash(hash)
	require.ErrorIs(t, werr, database.ErrBookFilesWithHashUnavailable, "memdb is not serving")

	e, lerr := lookupScanExisting(&Book{FilePath: f.write("incoming/01.mp3", "same-bytes")}, fileOwnershipVerdict{}, &hash)
	require.NoError(t, lerr)
	require.NotNil(t, e.retired, "retired content is still recognised while memdb warms up")
	require.Equal(t, f.holder.ID, e.retired.holder)

	fresh := f.write("incoming/02.mp3", "fresh-bytes")
	freshHash, err := ComputeFileHash(fresh)
	require.NoError(t, err)
	e, lerr = lookupScanExisting(&Book{FilePath: fresh}, fileOwnershipVerdict{}, &freshHash)
	require.NoError(t, lerr)
	require.Nil(t, e.retired)
}

// hashUnavailableStore fails the complete per-hash read AND the Pebble-only
// fallback, the way a store does that has retired versions and cannot read
// them.
type hashUnavailableStore struct{ *database.PebbleStore }

func (hashUnavailableStore) BookFilesWithHash(string) ([]database.BookFile, error) {
	return nil, database.ErrBookFilesWithHashUnavailable
}

func (hashUnavailableStore) RetiredBookFilesWithHash(string) ([]database.BookFile, bool, error) {
	return nil, false, database.ErrBookFilesWithHashUnavailable
}

// When the retired-content check cannot be answered at all, the import is
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

// retiredShellAt makes a trashed single-file shell whose own FilePath is the
// file, retires its one row into the holder under a version of its own, and
// returns the shell, the retired row and the file's hash.
func (f *retiredScanFixture) retiredShellAt(rel, body string) (*database.Book, *database.BookFile, string) {
	f.t.Helper()
	path := f.write(rel, body)
	hash, err := ComputeFileHash(path)
	require.NoError(f.t, err)
	shell, err := f.store.CreateBook(&database.Book{Title: "Arkham Asylum", FilePath: path, MarkedForDeletion: new(true)})
	require.NoError(f.t, err)
	v, err := f.store.CreateBookVersion(&database.BookVersion{
		BookID: f.holder.ID, Status: database.BookVersionStatusRetired, Format: "mp3", Source: "retired_book",
		RetiredFromBookID: shell.ID, RetireOpID: "op-shell", RetiredAt: new(time.Now()),
	})
	require.NoError(f.t, err)
	row := &database.BookFile{BookID: shell.ID, FilePath: path, FileHash: hash, Format: "mp3", TrackNumber: 1}
	require.NoError(f.t, f.store.CreateBookFile(row))
	require.NoError(f.t, f.store.RetireBookFiles(
		[]database.BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: shell.ID}}, f.holder.ID, v.ID))
	return shell, row, hash
}

// liveRowsAt is every non-retired book_file row at path.
func (f *retiredScanFixture) liveRowsAt(path string) []database.BookFile {
	f.t.Helper()
	rows, err := f.store.BookFilesAtPath(path)
	require.NoError(f.t, err)
	var live []database.BookFile
	for _, r := range rows {
		if !r.Retired {
			live = append(live, r)
		}
	}
	return live
}

// A forced rescan of a retired shell's OWN path (the shell row sits at the
// file) must not take the "update of the book at this path" shortcut: the
// file's only claim is a retired row, so the scan is skipped with the retired
// reason, saveBookToDatabase refuses it, and no live row appears on the file.
func TestRetiredShellOwnPath_RescanIsSkippedAndMintsNoSecondRow(t *testing.T) {
	f := newRetiredScanFixture(t)
	shell, row, hash := f.retiredShellAt("shell-own/B.mp3", "shell-own-bytes")
	book := &Book{Title: "Arkham Asylum", FilePath: row.FilePath, FileHash: hash}

	v, err := checkFileOwnership(book)
	require.NoError(t, err)
	require.True(t, v.skip, "a retired shell's own path is not an update of that book")
	require.Equal(t, []string{f.holder.ID}, v.owners)
	require.Contains(t, v.reason, "retired version of book "+f.holder.ID)

	serr := saveBookToDatabase(t.Context(), book)
	require.ErrorIs(t, serr, errFileOwnedByOtherBook)

	// The backstop behind the checks: even if a caller reaches the row-minting
	// step for the shell (an older caller, a new scan route), the shell gets no
	// live row on the retired file.
	createSingleFileBookFile(book, defaultLog)
	require.Empty(t, f.liveRowsAt(row.FilePath), "no live row may appear beside the retired one")
	shellRows, err := f.store.GetBookFiles(shell.ID)
	require.NoError(t, err)
	require.Empty(t, shellRows)
	all, err := f.store.BookFilesAtPath(row.FilePath)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.True(t, all[0].Retired)
}

// A control for the shortcut: a book that is NOT a shell still takes it, so an
// ordinary single-file rescan is unchanged.
func TestRetiredShellOwnPath_OrdinaryBookAtPathStillProceeds(t *testing.T) {
	f := newRetiredScanFixture(t)
	p := f.write("ordinary/B.mp3", "ordinary-bytes")
	b, err := f.store.CreateBook(&database.Book{Title: "The Killing Joke", FilePath: p})
	require.NoError(t, err)
	require.NoError(t, f.store.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: p, TrackNumber: 1}))

	v, err := checkFileOwnership(&Book{FilePath: p})
	require.NoError(t, err)
	require.False(t, v.skip, "reason: %s", v.reason)
}

// A file tagged with the shell's organizer ID but sitting at another path is
// the shell's retired content: lookupScanExisting names the shell by ID, and
// must still report the retired hit instead of accepting the shell as the row
// (the relink would repoint the shell at the file and mint a live row on it).
func TestRetiredShellOrganizerID_TaggedCopyOfRetiredContentIsRetiredHit(t *testing.T) {
	f := newRetiredScanFixture(t)
	shell, _, hash := f.retiredShellAt("shell-tag/B.mp3", "tagged-bytes")
	moved := f.write("moved/B.mp3", "tagged-bytes")

	e, lerr := lookupScanExisting(&Book{FilePath: moved, BookOrganizerID: shell.ID}, fileOwnershipVerdict{}, &hash)
	require.NoError(t, lerr)
	require.NotNil(t, e.byOrgID, "the shell is still named by its tag")
	require.NotNil(t, e.retired, "but the content is a retired version's")
	require.Equal(t, f.holder.ID, e.retired.holder)

	serr := saveBookToDatabase(t.Context(), &Book{Title: "A Death in the Family", FilePath: moved, FileHash: hash, BookOrganizerID: shell.ID})
	require.ErrorIs(t, serr, errFileOwnedByOtherBook)
	got, err := f.store.GetBookByID(shell.ID)
	require.NoError(t, err)
	require.NotEqual(t, moved, got.FilePath, "the shell must not be relinked onto the retired content")
	require.Empty(t, f.liveRowsAt(moved))
}

// retiredMultiShellAt is a trashed multi-file directory book whose files are
// all retired into the holder: one shell row at dir, one retired row per body
// at dir/NN.mp3. It returns the shell and the files' paths.
func (f *retiredScanFixture) retiredMultiShellAt(dir string, bodies ...string) (*database.Book, []string) {
	f.t.Helper()
	paths := make([]string, len(bodies))
	rows := make([]*database.BookFile, len(bodies))
	ids := make([]string, len(bodies))
	shell, err := f.store.CreateBook(&database.Book{Title: "Hush", FilePath: filepath.Join(f.root, dir), MarkedForDeletion: new(true)})
	require.NoError(f.t, err)
	for i, body := range bodies {
		paths[i] = f.write(filepath.Join(dir, fmt.Sprintf("%02d.mp3", i+1)), body)
		h, herr := ComputeFileHash(paths[i])
		require.NoError(f.t, herr)
		rows[i] = &database.BookFile{BookID: shell.ID, FilePath: paths[i], FileHash: h, Format: "mp3", TrackNumber: i + 1}
		require.NoError(f.t, f.store.CreateBookFile(rows[i]))
		ids[i] = rows[i].ID
	}
	v, err := f.store.CreateBookVersion(&database.BookVersion{
		BookID: f.holder.ID, Status: database.BookVersionStatusRetired, Format: "mp3", Source: "retired_book",
		RetiredFromBookID: shell.ID, RetireOpID: "op-multi", RetiredAt: new(time.Now()),
	})
	require.NoError(f.t, err)
	require.NoError(f.t, f.store.RetireBookFiles(
		[]database.BookFileMove{{FileIDs: ids, SourceBookID: shell.ID}}, f.holder.ID, v.ID))
	return shell, paths
}

// A copy of a retired MULTI-file version, still tagged with the shell's
// organizer ID and sitting at another folder, is retired content too. A segment
// list is judged by the segments' hashes, which only the segment hashing writes;
// the shell branch used to skip it, so the copy read as no match, the shell was
// relinked onto the copy and live rows were minted under it.
func TestRetiredShellOrganizerID_MultiFileTaggedCopyOfRetiredContentIsRetiredHit(t *testing.T) {
	f := newRetiredScanFixture(t)
	shell, _ := f.retiredMultiShellAt("shellm", "seg-one-bytes", "seg-two-bytes")
	m1 := f.write("movedm/01.mp3", "seg-one-bytes")
	m2 := f.write("movedm/02.mp3", "seg-two-bytes")
	dir := filepath.Dir(m1)
	h1, err := ComputeFileHash(m1)
	require.NoError(t, err)

	e, lerr := lookupScanExisting(&Book{FilePath: dir, SegmentFiles: []string{m1, m2}, BookOrganizerID: shell.ID}, fileOwnershipVerdict{}, &h1)
	require.NoError(t, lerr)
	require.NotNil(t, e.byOrgID, "the shell is still named by its tag")
	require.NotNil(t, e.retired, "but the segments are a retired version's")
	require.Equal(t, f.holder.ID, e.retired.holder)
	require.Equal(t, 2, e.retired.matched)
	require.Equal(t, 2, e.retired.total)

	serr := saveBookToDatabase(t.Context(), &Book{Title: "Hush", FilePath: dir, SegmentFiles: []string{m1, m2}, FileHash: h1, BookOrganizerID: shell.ID})
	require.ErrorIs(t, serr, errFileOwnedByOtherBook)
	got, err := f.store.GetBookByID(shell.ID)
	require.NoError(t, err)
	require.NotEqual(t, dir, got.FilePath, "the shell must not be relinked onto the retired content")
	require.Empty(t, f.liveRowsAt(m1))
	require.Empty(t, f.liveRowsAt(m2))
}

// The multi-file twin of TestRetiredShellOrganizerID_TaggedFileWithOtherContentIsNotAHit:
// hashing the segments for the shell branch flags nothing when no segment is a
// retired version's, and leaves the hashes for the row minting to reuse.
func TestRetiredShellOrganizerID_MultiFileTaggedFilesWithOtherContentAreNotAHit(t *testing.T) {
	f := newRetiredScanFixture(t)
	shell, _ := f.retiredMultiShellAt("shellm2", "seg-one-bytes", "seg-two-bytes")
	o1 := f.write("movedm2/01.mp3", "other-one-bytes")
	o2 := f.write("movedm2/02.mp3", "other-two-bytes")
	h1, err := ComputeFileHash(o1)
	require.NoError(t, err)

	book := &Book{FilePath: filepath.Dir(o1), SegmentFiles: []string{o1, o2}, BookOrganizerID: shell.ID}
	e, lerr := lookupScanExisting(book, fileOwnershipVerdict{}, &h1)
	require.NoError(t, lerr)
	require.Nil(t, e.retired)
	require.NotNil(t, e.byOrgID)
	require.Len(t, book.SegmentHashes, 2, "the segment hashes are kept for the row minting")
}

// Content that cannot be hashed is not content that does not match. A segment
// of a tagged copy that cannot be read leaves the segment list short of hashes;
// with one of two missing the readable segment is a 1/2 match, under the 80%
// bar, which read as "not a retired version" and relinked the shell onto the
// copy with live rows of its own. The scan fails closed instead: nothing is
// relinked and no live row is minted, and the files import once readable.
func TestRetiredShellOrganizerID_MultiFileUnreadableSegmentFailsClosed(t *testing.T) {
	f := newRetiredScanFixture(t)
	shell, _ := f.retiredMultiShellAt("shellm3", "seg-one-bytes", "seg-two-bytes")
	m1 := f.write("movedm3/01.mp3", "seg-one-bytes")
	m2 := filepath.Join(f.root, "movedm3", "02.mp3") // listed, but gone before it could be hashed
	dir := filepath.Dir(m1)
	h1, err := ComputeFileHash(m1)
	require.NoError(t, err)

	_, lerr := lookupScanExisting(&Book{FilePath: dir, SegmentFiles: []string{m1, m2}, BookOrganizerID: shell.ID}, fileOwnershipVerdict{}, &h1)
	require.Error(t, lerr)
	require.Contains(t, lerr.Error(), "retired-version check undeterminable")
	require.Contains(t, lerr.Error(), m2, "the unreadable segment is named")

	serr := saveBookToDatabase(t.Context(), &Book{Title: "Hush", FilePath: dir, SegmentFiles: []string{m1, m2}, FileHash: h1, BookOrganizerID: shell.ID})
	require.Error(t, serr)
	require.Contains(t, serr.Error(), "retired-version check undeterminable")
	got, err := f.store.GetBookByID(shell.ID)
	require.NoError(t, err)
	require.NotEqual(t, dir, got.FilePath, "the shell must not be relinked onto content that could not be judged")
	require.Empty(t, f.liveRowsAt(m1))
	require.Empty(t, f.liveRowsAt(m2))
}

// The single-file twin: a tagged copy whose hash could not be computed has
// nothing to judge, so the shell is not accepted as its row.
func TestRetiredShellOrganizerID_SingleFileWithoutHashFailsClosed(t *testing.T) {
	f := newRetiredScanFixture(t)
	shell, _, _ := f.retiredShellAt("shell-tag2/B.mp3", "tagged-bytes")
	moved := f.write("moved2/B.mp3", "tagged-bytes")
	empty := ""

	for name, fileHash := range map[string]*string{"nil": nil, "empty": &empty} {
		t.Run(name, func(t *testing.T) {
			_, lerr := lookupScanExisting(&Book{FilePath: moved, BookOrganizerID: shell.ID}, fileOwnershipVerdict{}, fileHash)
			require.Error(t, lerr)
			require.Contains(t, lerr.Error(), "retired-version check undeterminable")
		})
	}
	require.Empty(t, f.liveRowsAt(moved))
}

// A shell's own folder rescanned after its files changed is judged by the path
// claims of the retired rows, not by the content hashes: the scan is skipped
// and no live row appears on any file.
func TestRetiredShellOwnPath_MultiFileChangedBytesAreStillSkippedByPathClaim(t *testing.T) {
	f := newRetiredScanFixture(t)
	shell, paths := f.retiredMultiShellAt("shell-multi-changed", "before-one", "before-two")
	for i, p := range paths {
		require.NoError(t, os.WriteFile(p, []byte(fmt.Sprintf("after-the-file-changed-%d", i)), 0o644))
	}
	h1, err := ComputeFileHash(paths[0])
	require.NoError(t, err)

	serr := saveBookToDatabase(t.Context(), &Book{Title: "Hush", FilePath: shell.FilePath, SegmentFiles: paths, FileHash: h1})
	require.ErrorIs(t, serr, errFileOwnedByOtherBook)
	for _, p := range paths {
		require.Empty(t, f.liveRowsAt(p))
	}
}

// A tagged file whose bytes are NOT a retired version's is unaffected: the
// shell is still the existing row and nothing is flagged retired.
func TestRetiredShellOrganizerID_TaggedFileWithOtherContentIsNotAHit(t *testing.T) {
	f := newRetiredScanFixture(t)
	shell, _, _ := f.retiredShellAt("shell-tag2/B.mp3", "shell-bytes")
	other := f.write("moved2/B.mp3", "other-bytes")
	otherHash, err := ComputeFileHash(other)
	require.NoError(t, err)

	e, lerr := lookupScanExisting(&Book{FilePath: other, BookOrganizerID: shell.ID}, fileOwnershipVerdict{}, &otherHash)
	require.NoError(t, lerr)
	require.Nil(t, e.retired)
	require.NotNil(t, e.byOrgID)
}

// The shell's own path with changed bytes (a force_update rescan after the
// file changed) is judged by the path claim, not the content hash: the retired
// row still claims the path, so the scan is skipped.
func TestRetiredShellOwnPath_ChangedBytesAreStillSkippedByPathClaim(t *testing.T) {
	f := newRetiredScanFixture(t)
	_, row, _ := f.retiredShellAt("shell-changed/B.mp3", "before")
	require.NoError(t, os.WriteFile(row.FilePath, []byte("after-the-file-changed"), 0o644))
	newHash, err := ComputeFileHash(row.FilePath)
	require.NoError(t, err)

	serr := saveBookToDatabase(t.Context(), &Book{Title: "No Man's Land", FilePath: row.FilePath, FileHash: newHash})
	require.ErrorIs(t, serr, errFileOwnedByOtherBook)
	require.Empty(t, f.liveRowsAt(row.FilePath))
}

// The owner of a multi-file book's first segment is locked for the scan. For a
// segment only a retired version's row claims, that owner is the book the
// version is held under: the scan reads its retired rows to decide the file is
// not new, and a retire into it or a purge of it must not run underneath.
func TestResolveScanLockSet_RetiredFirstSegmentLocksTheHolder(t *testing.T) {
	f := newRetiredScanFixture(t)
	first := f.write("shell/01.mp3", "retired-bytes")
	second := f.write("shell/02.mp3", "more-bytes")
	f.retiredRow(first, "h-retired-lock")

	ids, _, err := resolveScanLockSet(&Book{FilePath: filepath.Join(f.root, "shell"), SegmentFiles: []string{first, second}})
	require.NoError(t, err)
	require.Contains(t, ids, f.holder.ID, "the holder of the retired version was not locked")
}
