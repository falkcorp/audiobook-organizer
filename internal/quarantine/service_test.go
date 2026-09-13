// file: internal/quarantine/service_test.go
// version: 1.2.0
// guid: 7c2e9d41-5a3b-4f86-b0e7-1d9a6c3f8e52
// last-edited: 2026-09-12

package quarantine

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	"github.com/stretchr/testify/require"
)

// Real store, real temp dir: the bug is the gap between the disk and the
// book_file rows, and neither half can be faked without faking the bug away.
func newTestService(t *testing.T) (*QuarantineService, *database.PebbleStore, string) {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	root := t.TempDir()
	qs := NewQuarantineService(store, &config.Config{RootDir: root}, plugin.NewEventBus())
	return qs, store, root
}

// svcWith builds a service over a wrapped store (failure injection).
func svcWith(s Store, root string) *QuarantineService {
	return NewQuarantineService(s, &config.Config{RootDir: root}, plugin.NewEventBus())
}

func writeAudio(t *testing.T, p, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
}

// addRows creates one book_file row per path. A row whose file exists records
// its real size and filehash digest, as the scanner would; every row carries
// a Title so a test can prove a repoint kept the full record.
func addRows(t *testing.T, store *database.PebbleStore, bookID string, files []string) {
	t.Helper()
	for i, f := range files {
		row := &database.BookFile{
			BookID:      bookID,
			FilePath:    f,
			TrackNumber: i + 1,
			Title:       "t-" + filepath.Base(f),
			Format:      "mp3",
		}
		if info, err := os.Stat(f); err == nil {
			row.FileSize = info.Size()
			h, err := filehash.BookFileHash(f)
			require.NoError(t, err)
			row.FileHash = h
		}
		require.NoError(t, store.CreateBookFile(row))
	}
}

func seedBook(t *testing.T, store *database.PebbleStore, bookPath string, files []string) *database.Book {
	t.Helper()
	book, err := store.CreateBook(&database.Book{Title: "Book", FilePath: bookPath, Format: "mp3"})
	require.NoError(t, err)
	addRows(t, store, book.ID, files)
	return book
}

// qdir is a book's quarantine folder: the book ID is part of the name.
func qdir(root, bookID string) string {
	return filepath.Join(root, ".failed", "Unknown Author", "Book ["+bookID+"]")
}

func under(dir string, names ...string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, filepath.Join(dir, n))
	}
	return out
}

var three = []string{"01.mp3", "02.mp3", "03.mp3"}

func srcThree(root string) []string { return under(filepath.Join(root, "Author", "Book"), three...) }

func rowPaths(t *testing.T, store *database.PebbleStore, bookID string) map[string]database.BookFile {
	t.Helper()
	rows, err := store.GetBookFiles(bookID)
	require.NoError(t, err)
	out := map[string]database.BookFile{}
	for _, r := range rows {
		out[r.FilePath] = r
	}
	return out
}

func requireRowsAt(t *testing.T, store *database.PebbleStore, bookID string, paths ...string) {
	t.Helper()
	rows := rowPaths(t, store, bookID)
	for _, p := range paths {
		_, ok := rows[p]
		require.True(t, ok, "no row at %s (rows: %v)", p, keys(rows))
	}
}

func requireOnDisk(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		_, err := os.Lstat(p)
		require.NoError(t, err, "%s must exist on disk", p)
	}
}

func requireGone(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		_, err := os.Lstat(p)
		require.True(t, os.IsNotExist(err), "%s must no longer exist", p)
	}
}

func requireBody(t *testing.T, p, want string) {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, want, string(b), "%s must be left untouched", p)
}

func requireQuarantined(t *testing.T, store *database.PebbleStore, bookID string, want bool) *database.Book {
	t.Helper()
	got, err := store.GetBookByID(bookID)
	require.NoError(t, err)
	require.Equal(t, want, got.QuarantinedAt != nil, "QuarantinedAt set = %v, want %v", got.QuarantinedAt != nil, want)
	return got
}

func countChanges(t *testing.T, store *database.PebbleStore, bookID, changeType string) int {
	t.Helper()
	hist, err := store.GetBookPathHistory(bookID)
	require.NoError(t, err)
	n := 0
	for _, h := range hist {
		if h.ChangeType == changeType {
			n++
		}
	}
	return n
}

// record writes a history entry the way a pass does just before a move -- to
// reproduce the state an interrupted pass leaves behind.
func record(t *testing.T, store *database.PebbleStore, bookID, changeType, from, to string) {
	t.Helper()
	require.NoError(t, store.RecordPathChange(&database.BookPathChange{
		BookID: bookID, OldPath: from, NewPath: to, ChangeType: changeType,
	}))
	time.Sleep(time.Millisecond) // distinct timestamps
}

func keys(m map[string]database.BookFile) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// failingUpdateStore fails the book-row write that follows the file moves.
type failingUpdateStore struct{ *database.PebbleStore }

func (failingUpdateStore) UpdateBook(string, *database.Book) (*database.Book, error) {
	return nil, errors.New("injected UpdateBook failure")
}

// failingHistoryStore fails every history write of one change type.
type failingHistoryStore struct {
	*database.PebbleStore
	failType string
}

func (f failingHistoryStore) RecordPathChange(c *database.BookPathChange) error {
	if c.ChangeType == f.failType {
		return errors.New("injected history failure")
	}
	return f.PebbleStore.RecordPathChange(c)
}

// ---------------------------------------------------------------------------
// The original bug and the round trip.
// ---------------------------------------------------------------------------

// Quarantine moved the audio and left every book_file row pointing at the old
// path. Every row must now name the new path, and that path must exist.
func TestQuarantineBook_RepointsEveryFileRow(t *testing.T) {
	qs, store, root := newTestService(t)
	src := srcThree(root)
	for _, p := range src {
		writeAudio(t, p, filepath.Base(p))
	}
	book := seedBook(t, store, src[0], src)
	dst := under(qdir(root, book.ID), three...)

	require.NoError(t, qs.QuarantineBook(book.ID, "taglib failed"))

	rows := rowPaths(t, store, book.ID)
	require.Len(t, rows, 3, "no row may be deleted")
	for i := range src {
		r, ok := rows[dst[i]]
		require.True(t, ok, "row for %s was not repointed to %s (rows: %v)", src[i], dst[i], keys(rows))
		require.Equal(t, "t-"+filepath.Base(dst[i]), r.Title, "repoint must keep the full record")
		require.NotEmpty(t, r.FileHash, "repoint must keep the full record")
	}
	requireOnDisk(t, dst...)
	requireGone(t, src...)

	got := requireQuarantined(t, store, book.ID, true)
	require.Equal(t, dst[0], got.FilePath)
	require.Equal(t, 1, countChanges(t, store, book.ID, changeQuarantine))
	require.Equal(t, 3, countChanges(t, store, book.ID, changeQuarantineFile))
}

// Quarantine then unquarantine: every file and row goes back where it started.
func TestUnquarantineBook_RoundTripRepointsRowsBack(t *testing.T) {
	qs, store, root := newTestService(t)
	src := srcThree(root)
	for _, p := range src {
		writeAudio(t, p, filepath.Base(p))
	}
	book := seedBook(t, store, src[0], src)
	dst := under(qdir(root, book.ID), three...)

	require.NoError(t, qs.QuarantineBook(book.ID, "taglib failed"))
	require.NoError(t, qs.UnquarantineBook(book.ID))

	require.Len(t, rowPaths(t, store, book.ID), 3)
	requireRowsAt(t, store, book.ID, src...)
	requireOnDisk(t, src...)
	requireGone(t, dst...)
	got := requireQuarantined(t, store, book.ID, false)
	require.Equal(t, src[0], got.FilePath, "the book must return to ITS path, not one of its files'")
}

// A directory-shaped book moves in one rename; every row under it follows by
// prefix, and the round trip restores them.
func TestQuarantineBook_DirectoryBookRepointsRows(t *testing.T) {
	qs, store, root := newTestService(t)
	dir := filepath.Join(root, "Author", "Book")
	names := []string{"01.mp3", filepath.Join("disc2", "01.mp3"), "03.mp3"}
	src := under(dir, names...)
	for _, p := range src {
		writeAudio(t, p, p)
	}
	writeAudio(t, filepath.Join(dir, "cover.jpg"), "img")
	book := seedBook(t, store, dir, src)
	qBookDir := filepath.Join(qdir(root, book.ID), "Book")
	dst := under(qBookDir, names...)

	require.NoError(t, qs.QuarantineBook(book.ID, "manual"))
	requireRowsAt(t, store, book.ID, dst...)
	requireOnDisk(t, dst...)
	requireOnDisk(t, filepath.Join(qBookDir, "cover.jpg"))

	require.NoError(t, qs.UnquarantineBook(book.ID))
	requireRowsAt(t, store, book.ID, src...)
	requireOnDisk(t, src...)
}

// ---------------------------------------------------------------------------
// Partial passes, rollback, resume (round-1 review P1-P5).
// ---------------------------------------------------------------------------

// One file's destination is taken. The two moved files are repointed, the
// stuck one keeps its old path, the error names it, and the book is not
// marked quarantined. Clearing the obstacle and retrying finishes the job.
func TestQuarantineBook_PartialFailureRowsMatchDisk(t *testing.T) {
	qs, store, root := newTestService(t)
	src := srcThree(root)
	for _, p := range src {
		writeAudio(t, p, filepath.Base(p))
	}
	book := seedBook(t, store, src[0], src)
	dst := under(qdir(root, book.ID), three...)
	writeAudio(t, dst[1], "someone else's file") // blocks 02.mp3

	err := qs.QuarantineBook(book.ID, "taglib failed")
	require.Error(t, err)
	require.Contains(t, err.Error(), "02.mp3")

	require.Len(t, rowPaths(t, store, book.ID), 3, "no row may be deleted")
	requireRowsAt(t, store, book.ID, dst[0], src[1], dst[2])
	requireOnDisk(t, dst[0], src[1], dst[2])
	requireBody(t, dst[1], "someone else's file")
	got := requireQuarantined(t, store, book.ID, false)
	require.Equal(t, dst[0], got.FilePath, "the book's own file moved, so its row follows the disk")

	require.NoError(t, os.Remove(dst[1]))
	require.NoError(t, qs.QuarantineBook(book.ID, "taglib failed"))
	requireRowsAt(t, store, book.ID, dst...)
	requireOnDisk(t, dst...)
	requireQuarantined(t, store, book.ID, true)
}

// When the book row cannot be written the whole pass is rolled back: files
// return and every row names its original path again. The history entries
// the pass wrote first stay, and must not mislead a later cycle.
func TestQuarantineBook_BookUpdateFailureRollsBackRows(t *testing.T) {
	good, store, root := newTestService(t)
	src := srcThree(root)
	for _, p := range src {
		writeAudio(t, p, "x"+filepath.Base(p))
	}
	book := seedBook(t, store, src[0], src)
	dst := under(qdir(root, book.ID), three...)

	require.Error(t, svcWith(failingUpdateStore{store}, root).QuarantineBook(book.ID, "taglib failed"))
	requireRowsAt(t, store, book.ID, src...)
	requireOnDisk(t, src...)
	requireGone(t, dst...)

	require.NoError(t, good.QuarantineBook(book.ID, "taglib failed"))
	requireRowsAt(t, store, book.ID, dst...)
	require.NoError(t, good.UnquarantineBook(book.ID))
	requireRowsAt(t, store, book.ID, src...)
	requireOnDisk(t, src...)
}

// P1: a directory book whose 02.mp3 was missing BEFORE quarantine. The missing
// row cannot move and must not block the quarantine.
func TestQuarantineBook_DirectoryBookWithPreMissingRowCompletes(t *testing.T) {
	qs, store, root := newTestService(t)
	dir := filepath.Join(root, "Author", "Book")
	src := under(dir, three...)
	writeAudio(t, src[0], "a")
	writeAudio(t, src[2], "c") // 02.mp3 is already missing
	book := seedBook(t, store, dir, src)
	dst := under(filepath.Join(qdir(root, book.ID), "Book"), three...)

	require.NoError(t, qs.QuarantineBook(book.ID, "manual"))
	requireQuarantined(t, store, book.ID, true)
	requireRowsAt(t, store, book.ID, dst[0], src[1], dst[2])
	requireOnDisk(t, dst[0], dst[2])

	require.NoError(t, qs.UnquarantineBook(book.ID))
	requireQuarantined(t, store, book.ID, false)
	requireRowsAt(t, store, book.ID, src...)
	requireOnDisk(t, src[0], src[2])
}

// P5: the file-shaped twin of P1 -- one sibling missing before quarantine.
func TestQuarantineBook_FileBookWithMissingSiblingCompletes(t *testing.T) {
	qs, store, root := newTestService(t)
	src := srcThree(root)
	writeAudio(t, src[0], "a")
	writeAudio(t, src[2], "c")
	book := seedBook(t, store, src[0], src)
	dst := under(qdir(root, book.ID), three...)

	require.NoError(t, qs.QuarantineBook(book.ID, "taglib failed"))
	requireQuarantined(t, store, book.ID, true)
	requireRowsAt(t, store, book.ID, dst[0], src[1], dst[2])

	require.NoError(t, qs.UnquarantineBook(book.ID))
	requireQuarantined(t, store, book.ID, false)
	requireRowsAt(t, store, book.ID, src...)
	requireOnDisk(t, src[0], src[2])
}

// P2: a resumed pass keeps the subfolder layout and the original destination,
// even though the title changed in between.
func TestQuarantineBook_ResumeKeepsSubfolderLayoutAndDestination(t *testing.T) {
	qs, store, root := newTestService(t)
	bookDir := filepath.Join(root, "Author", "Book")
	names := []string{"01.mp3", filepath.Join("disc2", "01.mp3")}
	src := under(bookDir, names...)
	writeAudio(t, src[0], "disc1")
	writeAudio(t, src[1], "disc2")
	book := seedBook(t, store, src[0], src)
	dst := under(qdir(root, book.ID), names...)
	writeAudio(t, dst[1], "obstacle")

	require.Error(t, qs.QuarantineBook(book.ID, "taglib failed"))
	requireQuarantined(t, store, book.ID, false)

	require.NoError(t, os.Remove(dst[1]))
	b, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	b.Title = "Renamed Since"
	_, err = store.UpdateBook(book.ID, b)
	require.NoError(t, err)

	require.NoError(t, qs.QuarantineBook(book.ID, "taglib failed"))
	got := requireQuarantined(t, store, book.ID, true)
	require.Equal(t, dst[0], got.FilePath, "a resumed pass must not move the book again")
	requireRowsAt(t, store, book.ID, dst...)
	requireBody(t, dst[1], "disc2")
	require.Equal(t, 1, countChanges(t, store, book.ID, changeQuarantine),
		"one quarantine entry per cycle; a second one would point inside .failed")

	require.NoError(t, qs.UnquarantineBook(book.ID))
	requireRowsAt(t, store, book.ID, src...)
	requireOnDisk(t, src...)
}

// N1: the book's OWN file is the one blocked, so the book path never moved,
// and the title changes before the retry. The retry must use the folder the
// first pass journaled, not a new one derived from the new title, or the book
// ends up split across two .failed folders.
func TestQuarantineBook_ResumeUsesJournaledFolderWhenBookFileWasBlocked(t *testing.T) {
	qs, store, root := newTestService(t)
	src := srcThree(root)
	for _, p := range src {
		writeAudio(t, p, filepath.Base(p))
	}
	book := seedBook(t, store, src[0], src)
	dst := under(qdir(root, book.ID), three...)
	writeAudio(t, dst[0], "obstacle") // blocks the book's own file

	require.Error(t, qs.QuarantineBook(book.ID, "taglib failed"))
	requireRowsAt(t, store, book.ID, src[0], dst[1], dst[2])

	require.NoError(t, os.Remove(dst[0]))
	b, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	b.Title = "Renamed Since"
	_, err = store.UpdateBook(book.ID, b)
	require.NoError(t, err)

	require.NoError(t, qs.QuarantineBook(book.ID, "taglib failed"))
	got := requireQuarantined(t, store, book.ID, true)
	require.Equal(t, dst[0], got.FilePath)
	requireRowsAt(t, store, book.ID, dst...)
	requireOnDisk(t, dst...)
}

// P3: quarantine, unquarantine, organize elsewhere, quarantine, unquarantine.
// PebbleStore returns history newest-first; the old loop kept the LAST match,
// i.e. the oldest, and sent everything back to where it lived before the
// first quarantine.
func TestUnquarantineBook_SecondCycleRestoresLatestOrigin(t *testing.T) {
	qs, store, root := newTestService(t)
	src := srcThree(root)
	for _, p := range src {
		writeAudio(t, p, filepath.Base(p))
	}
	book := seedBook(t, store, src[0], src)
	require.NoError(t, qs.QuarantineBook(book.ID, "first"))
	require.NoError(t, qs.UnquarantineBook(book.ID))

	org := under(filepath.Join(root, "Author2", "Book"), three...)
	for i := range src {
		require.NoError(t, os.MkdirAll(filepath.Dir(org[i]), 0o755))
		require.NoError(t, os.Rename(src[i], org[i]))
	}
	rows, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	for i := range rows {
		r := rows[i]
		r.FilePath = filepath.Join(root, "Author2", "Book", filepath.Base(r.FilePath))
		require.NoError(t, store.UpdateBookFile(r.ID, &r))
	}
	b, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	b.FilePath = org[0]
	_, err = store.UpdateBook(book.ID, b)
	require.NoError(t, err)

	require.NoError(t, qs.QuarantineBook(book.ID, "second"))
	require.NoError(t, qs.UnquarantineBook(book.ID))

	got := requireQuarantined(t, store, book.ID, false)
	require.Equal(t, org[0], got.FilePath)
	requireRowsAt(t, store, book.ID, org...)
	requireOnDisk(t, org...)
	requireGone(t, src...)
}

// P4: the process died after the directory rename and before any row write.
// The journal-first entry records the move, so the retry adopts it.
func TestQuarantineBook_ResumesAfterCrashBetweenDirRenameAndDBWrite(t *testing.T) {
	qs, store, root := newTestService(t)
	dir := filepath.Join(root, "Author", "Book")
	src := under(dir, three...)
	for _, p := range src {
		writeAudio(t, p, filepath.Base(p))
	}
	book := seedBook(t, store, dir, src)
	qBookDir := filepath.Join(qdir(root, book.ID), "Book")
	dst := under(qBookDir, three...)
	record(t, store, book.ID, changeQuarantine, dir, qBookDir)
	require.NoError(t, os.MkdirAll(filepath.Dir(qBookDir), 0o755))
	require.NoError(t, os.Rename(dir, qBookDir)) // the "crash"

	require.NoError(t, qs.QuarantineBook(book.ID, "taglib failed"))
	got := requireQuarantined(t, store, book.ID, true)
	require.Equal(t, qBookDir, got.FilePath)
	requireRowsAt(t, store, book.ID, dst...)
	require.Equal(t, 1, countChanges(t, store, book.ID, changeQuarantine))

	require.NoError(t, qs.UnquarantineBook(book.ID))
	requireRowsAt(t, store, book.ID, src...)
	requireOnDisk(t, src...)
}

// P4, file-shaped: one file was renamed after its entry was written, and the
// process died before its row was. The retry adopts it and unquarantine
// returns it.
func TestQuarantineBook_ResumesAfterCrashBetweenFileRenameAndRowWrite(t *testing.T) {
	qs, store, root := newTestService(t)
	src := srcThree(root)
	for _, p := range src {
		writeAudio(t, p, filepath.Base(p))
	}
	book := seedBook(t, store, src[0], src)
	dst := under(qdir(root, book.ID), three...)
	record(t, store, book.ID, changeQuarantine, src[0], dst[0])
	record(t, store, book.ID, changeQuarantineFile, src[1], dst[1])
	require.NoError(t, os.MkdirAll(filepath.Dir(dst[1]), 0o755))
	require.NoError(t, os.Rename(src[1], dst[1])) // the "crash"

	require.NoError(t, qs.QuarantineBook(book.ID, "taglib failed"))
	requireQuarantined(t, store, book.ID, true)
	requireRowsAt(t, store, book.ID, dst...)

	require.NoError(t, qs.UnquarantineBook(book.ID))
	requireRowsAt(t, store, book.ID, src...)
	requireOnDisk(t, src...)
}

// ---------------------------------------------------------------------------
// "Already moved" must be CONFIRMED (round-2 review B1, B2, reverse).
// ---------------------------------------------------------------------------

// B1: 02.mp3 was missing before quarantine and an unrelated file sits at its
// destination. Source gone + destination present is NOT proof of a move: with
// no history entry the row stays put and the unrelated file is untouched.
func TestQuarantineBook_UnrelatedFileAtDestinationIsNotAdopted(t *testing.T) {
	qs, store, root := newTestService(t)
	src := srcThree(root)
	writeAudio(t, src[0], "a")
	writeAudio(t, src[2], "c")
	book := seedBook(t, store, src[0], src)
	dst := under(qdir(root, book.ID), three...)
	writeAudio(t, dst[1], "other-book")

	err := qs.QuarantineBook(book.ID, "taglib failed")
	require.Error(t, err)
	require.Contains(t, err.Error(), "taken by a different file")
	requireRowsAt(t, store, book.ID, dst[0], src[1], dst[2])
	requireBody(t, dst[1], "other-book")
	requireQuarantined(t, store, book.ID, false)
}

// B1, with a history entry: the entry alone is not enough when the file at
// the destination is not this row's file (size/hash differ).
func TestQuarantineBook_RecordedMoveOfADifferentFileIsNotAdopted(t *testing.T) {
	qs, store, root := newTestService(t)
	src := srcThree(root)
	for _, p := range src {
		writeAudio(t, p, filepath.Base(p))
	}
	book := seedBook(t, store, src[0], src)
	dst := under(qdir(root, book.ID), three...)
	record(t, store, book.ID, changeQuarantine, src[0], dst[0])
	record(t, store, book.ID, changeQuarantineFile, src[1], dst[1])
	require.NoError(t, os.Remove(src[1]))
	writeAudio(t, dst[1], "a different, longer file")

	require.Error(t, qs.QuarantineBook(book.ID, "taglib failed"))
	requireRowsAt(t, store, book.ID, dst[0], src[1], dst[2])
	requireBody(t, dst[1], "a different, longer file")
}

// B2: a directory book's folder is missing and a folder sits at its
// destination. With no record of this book moving there, quarantine fails as
// main did, touches nothing and journals nothing.
func TestQuarantineBook_MissingDirectoryDoesNotAdoptForeignFolder(t *testing.T) {
	qs, store, root := newTestService(t)
	dir := filepath.Join(root, "Author", "Book")
	src := under(dir, three...)
	book := seedBook(t, store, dir, src)
	qBookDir := filepath.Join(qdir(root, book.ID), "Book")
	for _, n := range three {
		writeAudio(t, filepath.Join(qBookDir, n), "foreign")
	}

	require.Error(t, qs.QuarantineBook(book.ID, "manual"))
	requireRowsAt(t, store, book.ID, src...)
	requireQuarantined(t, store, book.ID, false)
	requireBody(t, filepath.Join(qBookDir, "02.mp3"), "foreign")
	require.Zero(t, countChanges(t, store, book.ID, changeQuarantine))
}

// Reverse direction: a quarantined file vanished from .failed and an unrelated
// file now sits at its original path. Unquarantine must not adopt it, and the
// book stays quarantined rather than being cleared with a row left behind.
func TestUnquarantineBook_UnrelatedFileAtOriginalPathIsNotAdopted(t *testing.T) {
	qs, store, root := newTestService(t)
	src := srcThree(root)
	for _, p := range src {
		writeAudio(t, p, filepath.Base(p))
	}
	book := seedBook(t, store, src[0], src)
	dst := under(qdir(root, book.ID), three...)
	require.NoError(t, qs.QuarantineBook(book.ID, "taglib failed"))
	require.NoError(t, os.Remove(dst[1]))
	writeAudio(t, src[1], "someone new")

	require.Error(t, qs.UnquarantineBook(book.ID))
	requireRowsAt(t, store, book.ID, src[0], dst[1], src[2])
	requireBody(t, src[1], "someone new")
	requireQuarantined(t, store, book.ID, true)
}

// ---------------------------------------------------------------------------
// A history write that fails fails the pass (round-2 review C).
// ---------------------------------------------------------------------------

// Per-file entries cannot be written: nothing moves, the pass fails, and a
// later healthy pass finishes and round-trips.
func TestQuarantineBook_FileHistoryWriteFailureMovesNothing(t *testing.T) {
	good, store, root := newTestService(t)
	src := srcThree(root)
	for _, p := range src {
		writeAudio(t, p, filepath.Base(p))
	}
	book := seedBook(t, store, src[0], src)

	bad := svcWith(failingHistoryStore{store, changeQuarantineFile}, root)
	require.Error(t, bad.QuarantineBook(book.ID, "taglib failed"))
	requireRowsAt(t, store, book.ID, src...)
	requireOnDisk(t, src...)
	requireQuarantined(t, store, book.ID, false)

	require.NoError(t, good.QuarantineBook(book.ID, "taglib failed"))
	require.NoError(t, good.UnquarantineBook(book.ID))
	requireRowsAt(t, store, book.ID, src...)
	requireOnDisk(t, src...)
}

// The book-level entry cannot be written: the pass fails before anything
// moves, so the book can never sit under .failed without the record a resume
// needs (the old dead end: "under .failed but no quarantine history").
func TestQuarantineBook_BookHistoryWriteFailureMovesNothing(t *testing.T) {
	good, store, root := newTestService(t)
	src := srcThree(root)
	for _, p := range src {
		writeAudio(t, p, filepath.Base(p))
	}
	book := seedBook(t, store, src[0], src)

	bad := svcWith(failingHistoryStore{store, changeQuarantine}, root)
	require.Error(t, bad.QuarantineBook(book.ID, "taglib failed"))
	requireRowsAt(t, store, book.ID, src...)
	requireOnDisk(t, src...)
	got := requireQuarantined(t, store, book.ID, false)
	require.Equal(t, src[0], got.FilePath)

	require.NoError(t, good.QuarantineBook(book.ID, "taglib failed"))
	requireQuarantined(t, store, book.ID, true)
}

// Unquarantine cannot journal its per-file moves: it fails, moves nothing,
// and the book stays quarantined -- no row stranded under .failed with
// QuarantinedAt cleared.
func TestUnquarantineBook_FileHistoryWriteFailureStrandsNothing(t *testing.T) {
	good, store, root := newTestService(t)
	src := srcThree(root)
	for _, p := range src {
		writeAudio(t, p, filepath.Base(p))
	}
	book := seedBook(t, store, src[0], src)
	dst := under(qdir(root, book.ID), three...)
	require.NoError(t, good.QuarantineBook(book.ID, "taglib failed"))

	bad := svcWith(failingHistoryStore{store, changeUnquarantineFile}, root)
	require.Error(t, bad.UnquarantineBook(book.ID))
	requireQuarantined(t, store, book.ID, true)
	requireRowsAt(t, store, book.ID, dst...)
	requireOnDisk(t, dst...)

	require.NoError(t, good.UnquarantineBook(book.ID))
	requireRowsAt(t, store, book.ID, src...)
}

// Fallback: rows under the book's quarantine folder with NO per-file entry
// (a pre-journal quarantine whose rows a repair repointed) go back by their
// place under that folder, subfolders included.
func TestUnquarantineBook_FallbackMapsUnjournaledRowsByPosition(t *testing.T) {
	qs, store, root := newTestService(t)
	orig := filepath.Join(root, "Author", "Book")
	names := []string{"01.mp3", filepath.Join("disc2", "02.mp3")}
	src := under(orig, names...)
	book, err := store.CreateBook(&database.Book{Title: "Book", FilePath: src[0], Format: "mp3"})
	require.NoError(t, err)
	q := under(qdir(root, book.ID), names...)
	for _, p := range q {
		writeAudio(t, p, filepath.Base(p))
	}
	addRows(t, store, book.ID, q)
	record(t, store, book.ID, changeQuarantine, src[0], q[0])
	b, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	now := time.Now()
	b.FilePath, b.QuarantinedAt = q[0], &now
	_, err = store.UpdateBook(book.ID, b)
	require.NoError(t, err)

	require.NoError(t, qs.UnquarantineBook(book.ID))
	requireRowsAt(t, store, book.ID, src...)
	requireOnDisk(t, src...)
	requireGone(t, q...)
	requireQuarantined(t, store, book.ID, false)
}
