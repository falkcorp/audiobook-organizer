// file: internal/quarantine/service_test.go
// version: 1.1.0
// guid: 7c2e9d41-5a3b-4f86-b0e7-1d9a6c3f8e52
// last-edited: 2026-09-12

package quarantine

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
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

func writeAudio(t *testing.T, p, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
}

// seedBook creates a book whose FilePath is bookPath and one book_file row per
// file. Each row carries a FileHash so a test can prove the repoint kept the
// full record (UpdateBookFile replaces the whole row).
func seedBook(t *testing.T, store *database.PebbleStore, bookPath string, files []string) *database.Book {
	t.Helper()
	book, err := store.CreateBook(&database.Book{Title: "Book", FilePath: bookPath, Format: "mp3"})
	require.NoError(t, err)
	for i, f := range files {
		require.NoError(t, store.CreateBookFile(&database.BookFile{
			BookID:      book.ID,
			FilePath:    f,
			TrackNumber: i + 1,
			FileHash:    "hash-" + filepath.Base(f),
			Format:      "mp3",
		}))
	}
	return book
}

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

func requireOnDisk(t *testing.T, p string) {
	t.Helper()
	_, err := os.Lstat(p)
	require.NoError(t, err, "%s must exist on disk", p)
}

func requireGone(t *testing.T, p string) {
	t.Helper()
	_, err := os.Lstat(p)
	require.True(t, os.IsNotExist(err), "%s must no longer exist", p)
}

func threeFiles(root string) (src, dst []string) {
	for _, n := range []string{"01.mp3", "02.mp3", "03.mp3"} {
		src = append(src, filepath.Join(root, "Author", "Book", n))
		dst = append(dst, filepath.Join(root, ".failed", "Unknown Author", "Book", n))
	}
	return src, dst
}

// The bug: quarantine moved the audio and left every book_file row pointing at
// the old path. Every row must now name the new path, and that path must exist.
func TestQuarantineBook_RepointsEveryFileRow(t *testing.T) {
	qs, store, root := newTestService(t)
	src, dst := threeFiles(root)
	for _, p := range src {
		writeAudio(t, p, filepath.Base(p))
	}
	book := seedBook(t, store, src[0], src)

	require.NoError(t, qs.QuarantineBook(book.ID, "taglib failed"))

	rows := rowPaths(t, store, book.ID)
	require.Len(t, rows, 3, "no row may be deleted")
	for i := range src {
		r, ok := rows[dst[i]]
		require.True(t, ok, "row for %s was not repointed to %s (rows: %v)", src[i], dst[i], keys(rows))
		require.Equal(t, "hash-"+filepath.Base(dst[i]), r.FileHash, "repoint must keep the full record")
		requireOnDisk(t, dst[i])
		requireGone(t, src[i])
	}

	got, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, dst[0], got.FilePath)
	require.NotNil(t, got.QuarantinedAt)

	// The per-file journal uses its own change type; the book's own entry is
	// still the only "quarantine" row, so unquarantine finds the book's path.
	hist, err := store.GetBookPathHistory(book.ID)
	require.NoError(t, err)
	var bookEntries, fileEntries int
	for _, h := range hist {
		switch h.ChangeType {
		case changeQuarantine:
			bookEntries++
			require.Equal(t, src[0], h.OldPath)
		case changeQuarantineFile:
			fileEntries++
		}
	}
	require.Equal(t, 1, bookEntries)
	require.Equal(t, 3, fileEntries)
}

// Quarantine then unquarantine: every file and row goes back where it started.
func TestUnquarantineBook_RoundTripRepointsRowsBack(t *testing.T) {
	qs, store, root := newTestService(t)
	src, dst := threeFiles(root)
	for _, p := range src {
		writeAudio(t, p, filepath.Base(p))
	}
	book := seedBook(t, store, src[0], src)

	require.NoError(t, qs.QuarantineBook(book.ID, "taglib failed"))
	require.NoError(t, qs.UnquarantineBook(book.ID))

	rows := rowPaths(t, store, book.ID)
	require.Len(t, rows, 3)
	for i := range src {
		_, ok := rows[src[i]]
		require.True(t, ok, "row for %s was not repointed back (rows: %v)", src[i], keys(rows))
		requireOnDisk(t, src[i])
		requireGone(t, dst[i])
	}
	got, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, src[0], got.FilePath, "the book must return to ITS path, not one of its files'")
	require.Nil(t, got.QuarantinedAt)
}

// A directory-shaped book moves in one rename; every row under it follows by
// prefix, and the round trip restores them.
func TestQuarantineBook_DirectoryBookRepointsRows(t *testing.T) {
	qs, store, root := newTestService(t)
	dir := filepath.Join(root, "Author", "Book")
	var src, dst []string
	for _, n := range []string{"01.mp3", filepath.Join("disc2", "01.mp3"), "03.mp3"} {
		src = append(src, filepath.Join(dir, n))
		dst = append(dst, filepath.Join(root, ".failed", "Unknown Author", "Book", "Book", n))
	}
	for _, p := range src {
		writeAudio(t, p, "x")
	}
	writeAudio(t, filepath.Join(dir, "cover.jpg"), "img")
	book := seedBook(t, store, dir, src)

	require.NoError(t, qs.QuarantineBook(book.ID, "manual"))
	rows := rowPaths(t, store, book.ID)
	for i := range src {
		_, ok := rows[dst[i]]
		require.True(t, ok, "row not repointed to %s (rows: %v)", dst[i], keys(rows))
		requireOnDisk(t, dst[i])
	}
	requireOnDisk(t, filepath.Join(root, ".failed", "Unknown Author", "Book", "Book", "cover.jpg"))

	require.NoError(t, qs.UnquarantineBook(book.ID))
	rows = rowPaths(t, store, book.ID)
	for i := range src {
		_, ok := rows[src[i]]
		require.True(t, ok, "row not restored to %s (rows: %v)", src[i], keys(rows))
		requireOnDisk(t, src[i])
	}
}

// One file cannot move (its destination is already taken). Rows must match
// the disk: the two moved files are repointed, the stuck one keeps its old
// path, the error names it, and the book is not marked quarantined. Clearing
// the obstacle and retrying finishes the job.
func TestQuarantineBook_PartialFailureRowsMatchDisk(t *testing.T) {
	qs, store, root := newTestService(t)
	src, dst := threeFiles(root)
	for _, p := range src {
		writeAudio(t, p, filepath.Base(p))
	}
	writeAudio(t, dst[1], "someone else's file") // blocks 02.mp3
	book := seedBook(t, store, src[0], src)

	err := qs.QuarantineBook(book.ID, "taglib failed")
	require.Error(t, err)
	require.Contains(t, err.Error(), "02.mp3")

	rows := rowPaths(t, store, book.ID)
	require.Len(t, rows, 3, "no row may be deleted")
	for _, i := range []int{0, 2} {
		_, ok := rows[dst[i]]
		require.True(t, ok, "moved file %s must be repointed (rows: %v)", dst[i], keys(rows))
		requireOnDisk(t, dst[i])
	}
	_, ok := rows[src[1]]
	require.True(t, ok, "the file that did not move must keep its old path (rows: %v)", keys(rows))
	requireOnDisk(t, src[1])
	b, err := os.ReadFile(dst[1])
	require.NoError(t, err)
	require.Equal(t, "someone else's file", string(b), "an existing file must never be overwritten")

	got, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Nil(t, got.QuarantinedAt, "a partial move must not mark the book quarantined")
	require.Equal(t, dst[0], got.FilePath, "the book's own file moved, so its row follows the disk")

	require.NoError(t, os.Remove(dst[1]))
	require.NoError(t, qs.QuarantineBook(book.ID, "taglib failed"))
	rows = rowPaths(t, store, book.ID)
	for i := range dst {
		_, ok := rows[dst[i]]
		require.True(t, ok, "retry must finish: %s (rows: %v)", dst[i], keys(rows))
		requireOnDisk(t, dst[i])
	}
	got, err = store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, got.QuarantinedAt)
}

// failingUpdateStore fails the book-row write that follows the file moves.
type failingUpdateStore struct{ *database.PebbleStore }

func (failingUpdateStore) UpdateBook(string, *database.Book) (*database.Book, error) {
	return nil, errors.New("injected UpdateBook failure")
}

// When the book row cannot be written the whole pass is rolled back: files
// return and every row names its original path again.
func TestQuarantineBook_BookUpdateFailureRollsBackRows(t *testing.T) {
	_, store, root := newTestService(t)
	qs := NewQuarantineService(failingUpdateStore{store}, &config.Config{RootDir: root}, plugin.NewEventBus())
	src, dst := threeFiles(root)
	for _, p := range src {
		writeAudio(t, p, "x")
	}
	book := seedBook(t, store, src[0], src)

	require.Error(t, qs.QuarantineBook(book.ID, "taglib failed"))
	rows := rowPaths(t, store, book.ID)
	for i := range src {
		_, ok := rows[src[i]]
		require.True(t, ok, "row must be restored to %s (rows: %v)", src[i], keys(rows))
		requireOnDisk(t, src[i])
		requireGone(t, dst[i])
	}
	require.Zero(t, countChanges(t, store, book.ID, changeQuarantineFile),
		"a rolled-back pass must leave no quarantine_file entry for unquarantine to follow")
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

func requireRowsAt(t *testing.T, store *database.PebbleStore, bookID string, paths ...string) {
	t.Helper()
	rows := rowPaths(t, store, bookID)
	for _, p := range paths {
		_, ok := rows[p]
		require.True(t, ok, "no row at %s (rows: %v)", p, keys(rows))
	}
}

func requireQuarantined(t *testing.T, store *database.PebbleStore, bookID string, want bool) *database.Book {
	t.Helper()
	got, err := store.GetBookByID(bookID)
	require.NoError(t, err)
	require.Equal(t, want, got.QuarantinedAt != nil, "QuarantinedAt set = %v, want %v", got.QuarantinedAt != nil, want)
	return got
}

// P1: a directory book whose 02.mp3 row was missing BEFORE quarantine. The
// missing row cannot move and must not block the quarantine; before the fix
// the book was left half-quarantined forever (QuarantinedAt nil, retry failed
// "outside the book directory", unquarantine a no-op).
func TestQuarantineBook_DirectoryBookWithPreMissingRowCompletes(t *testing.T) {
	qs, store, root := newTestService(t)
	dir := filepath.Join(root, "Author", "Book")
	qdir := filepath.Join(root, ".failed", "Unknown Author", "Book", "Book")
	var src, dst []string
	for _, n := range []string{"01.mp3", "02.mp3", "03.mp3"} {
		src = append(src, filepath.Join(dir, n))
		dst = append(dst, filepath.Join(qdir, n))
	}
	writeAudio(t, src[0], "a")
	writeAudio(t, src[2], "c") // 02.mp3 is already missing
	book := seedBook(t, store, dir, src)

	require.NoError(t, qs.QuarantineBook(book.ID, "manual"))
	requireQuarantined(t, store, book.ID, true)
	requireRowsAt(t, store, book.ID, dst[0], src[1], dst[2])
	requireOnDisk(t, dst[0])
	requireOnDisk(t, dst[2])

	require.NoError(t, qs.UnquarantineBook(book.ID))
	requireQuarantined(t, store, book.ID, false)
	requireRowsAt(t, store, book.ID, src...)
	requireOnDisk(t, src[0])
	requireOnDisk(t, src[2])
}

// P5: the file-shaped twin of P1 -- one sibling missing before quarantine.
func TestQuarantineBook_FileBookWithMissingSiblingCompletes(t *testing.T) {
	qs, store, root := newTestService(t)
	src, dst := threeFiles(root)
	writeAudio(t, src[0], "a")
	writeAudio(t, src[2], "c")
	book := seedBook(t, store, src[0], src)

	require.NoError(t, qs.QuarantineBook(book.ID, "taglib failed"))
	requireQuarantined(t, store, book.ID, true)
	requireRowsAt(t, store, book.ID, dst[0], src[1], dst[2])

	require.NoError(t, qs.UnquarantineBook(book.ID))
	requireQuarantined(t, store, book.ID, false)
	requireRowsAt(t, store, book.ID, src...)
	requireOnDisk(t, src[0])
	requireOnDisk(t, src[2])
}

// P2: a resumed pass keeps the subfolder layout and the original destination.
// Before the fix the retry derived srcBase from the book's CURRENT path (now
// under .failed), flattened disc2/01.mp3 onto the book's own 01.mp3 and failed
// forever; and a title edit between runs moved the book a second time.
func TestQuarantineBook_ResumeKeepsSubfolderLayoutAndDestination(t *testing.T) {
	qs, store, root := newTestService(t)
	bookDir := filepath.Join(root, "Author", "Book")
	qdir := filepath.Join(root, ".failed", "Unknown Author", "Book")
	src := []string{filepath.Join(bookDir, "01.mp3"), filepath.Join(bookDir, "disc2", "01.mp3")}
	dst := []string{filepath.Join(qdir, "01.mp3"), filepath.Join(qdir, "disc2", "01.mp3")}
	writeAudio(t, src[0], "disc1")
	writeAudio(t, src[1], "disc2")
	writeAudio(t, dst[1], "obstacle")
	book := seedBook(t, store, src[0], src)

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
	body, err := os.ReadFile(dst[1])
	require.NoError(t, err)
	require.Equal(t, "disc2", string(body))
	require.Equal(t, 1, countChanges(t, store, book.ID, changeQuarantine),
		"one quarantine entry per cycle; a second one would point inside .failed")

	require.NoError(t, qs.UnquarantineBook(book.ID))
	requireRowsAt(t, store, book.ID, src...)
	requireOnDisk(t, src[0])
	requireOnDisk(t, src[1])
}

// P3: quarantine, unquarantine, organize elsewhere, quarantine, unquarantine.
// PebbleStore returns history newest-first; the old loop kept the LAST match,
// i.e. the oldest, and sent everything back to where it lived before the
// first quarantine.
func TestUnquarantineBook_SecondCycleRestoresLatestOrigin(t *testing.T) {
	qs, store, root := newTestService(t)
	src, _ := threeFiles(root)
	for _, p := range src {
		writeAudio(t, p, filepath.Base(p))
	}
	book := seedBook(t, store, src[0], src)
	require.NoError(t, qs.QuarantineBook(book.ID, "first"))
	require.NoError(t, qs.UnquarantineBook(book.ID))

	// Organize the book to Author2/Book.
	var org []string
	for _, p := range src {
		np := filepath.Join(root, "Author2", "Book", filepath.Base(p))
		require.NoError(t, os.MkdirAll(filepath.Dir(np), 0o755))
		require.NoError(t, os.Rename(p, np))
		org = append(org, np)
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
	for i := range org {
		requireOnDisk(t, org[i])
		requireGone(t, src[i])
	}
}

// P4: the process died after the directory rename and before any DB write.
// The source is gone and the destination holds the book; a retry must treat
// that as already moved and repoint the rows, not fail on a missing source.
func TestQuarantineBook_ResumesAfterCrashBetweenDirRenameAndDBWrite(t *testing.T) {
	qs, store, root := newTestService(t)
	dir := filepath.Join(root, "Author", "Book")
	qBookDir := filepath.Join(root, ".failed", "Unknown Author", "Book", "Book")
	var src, dst []string
	for _, n := range []string{"01.mp3", "02.mp3", "03.mp3"} {
		src = append(src, filepath.Join(dir, n))
		dst = append(dst, filepath.Join(qBookDir, n))
		writeAudio(t, src[len(src)-1], n)
	}
	book := seedBook(t, store, dir, src)
	require.NoError(t, os.MkdirAll(filepath.Dir(qBookDir), 0o755))
	require.NoError(t, os.Rename(dir, qBookDir)) // the "crash"

	require.NoError(t, qs.QuarantineBook(book.ID, "taglib failed"))
	got := requireQuarantined(t, store, book.ID, true)
	require.Equal(t, qBookDir, got.FilePath)
	requireRowsAt(t, store, book.ID, dst...)
	require.Equal(t, 1, countChanges(t, store, book.ID, changeQuarantine))

	require.NoError(t, qs.UnquarantineBook(book.ID))
	requireRowsAt(t, store, book.ID, src...)
	for _, p := range src {
		requireOnDisk(t, p)
	}
}

// P4, file-shaped: one file was renamed and the process died before its row
// was written. The retry repoints (and journals) it so unquarantine returns it.
func TestQuarantineBook_ResumesAfterCrashBetweenFileRenameAndRowWrite(t *testing.T) {
	qs, store, root := newTestService(t)
	src, dst := threeFiles(root)
	for _, p := range src {
		writeAudio(t, p, filepath.Base(p))
	}
	book := seedBook(t, store, src[0], src)
	require.NoError(t, os.MkdirAll(filepath.Dir(dst[1]), 0o755))
	require.NoError(t, os.Rename(src[1], dst[1])) // the "crash"

	require.NoError(t, qs.QuarantineBook(book.ID, "taglib failed"))
	requireQuarantined(t, store, book.ID, true)
	requireRowsAt(t, store, book.ID, dst...)

	require.NoError(t, qs.UnquarantineBook(book.ID))
	requireRowsAt(t, store, book.ID, src...)
	for _, p := range src {
		requireOnDisk(t, p)
	}
}

func keys(m map[string]database.BookFile) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
