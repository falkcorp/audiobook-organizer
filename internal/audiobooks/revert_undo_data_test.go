// file: internal/audiobooks/revert_undo_data_test.go
// version: 1.3.1
// guid: 9a4c2e71-5d3b-4f80-b1e6-7c0d8f2a5b39
// last-edited: 2026-09-14

package audiobooks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/stretchr/testify/require"
)

// A tag_write row recorded with no pre-write value must never be "restored":
// writing "" back deletes the tag the organize wrote. Before 2026-09-13 every
// tag_write row had OldValue "" and the revert counted it Restored.
func TestRevertTagWrite_EmptyOldValueIsNotRestored(t *testing.T) {
	stub := &ledgerStub{
		book: &database.Book{ID: "b1", Title: "T", FilePath: filepath.Join(t.TempDir(), "gone.m4b")},
		changes: []*database.OperationChange{
			{ID: "c1", OperationID: "op", BookID: "b1", ChangeType: "tag_write", FieldName: "title", OldValue: "", NewValue: "Organized Title"},
		},
	}
	res, err := NewRevertService(stub).RevertOperation("op")
	var nre *NotRestorableError
	require.True(t, errors.As(err, &nre), "a tag_write row with no pre-write value must be not-restorable, got res=%+v err=%v", res, err)
	require.Empty(t, stub.marked, "no row may be marked reverted")
}

func newRevertPebble(t *testing.T) *database.PebbleStore {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// A multi-file book whose folder organize moved: the revert moves the folder
// back AND points every book_file row back under it. It used to update only
// Book.file_path, leaving every file row at a path that no longer existed.
func TestRevertFileMove_RepointsEveryBookFile(t *testing.T) {
	store := newRevertPebble(t)
	root := t.TempDir()
	oldDir := filepath.Join(root, "import", "Book")
	newDir := filepath.Join(root, "library", "Author", "Book")
	require.NoError(t, os.MkdirAll(newDir, 0o755))
	for _, n := range []string{"01.mp3", "02.mp3"} {
		require.NoError(t, os.WriteFile(filepath.Join(newDir, n), []byte(n), 0o644))
	}
	book, err := store.CreateBook(&database.Book{Title: "Book", FilePath: newDir, Format: "mp3"})
	require.NoError(t, err)
	for _, n := range []string{"01.mp3", "02.mp3"} {
		require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: filepath.Join(newDir, n), Format: "mp3"}))
	}
	require.NoError(t, store.CreateOperationChange(&database.OperationChange{
		OperationID: "op-move", BookID: book.ID, ChangeType: "file_move", FieldName: "file_path", OldValue: oldDir, NewValue: newDir,
	}))

	res, err := NewRevertService(store).RevertOperation("op-move")
	require.NoError(t, err, "result %+v", res)

	got, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, oldDir, got.FilePath)
	files, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Len(t, files, 2)
	for _, f := range files {
		require.Equal(t, oldDir, filepath.Dir(f.FilePath), "book_file %s still points at %s", f.ID, f.FilePath)
		_, statErr := os.Stat(f.FilePath)
		require.NoError(t, statErr, "file row points at a path with no file")
	}
}

// The book's path is compare-and-set: when something repointed the book
// since the organize, the revert leaves the file and the book where they are.
func TestRevertFileMove_RefusesWhenBookPathChangedSince(t *testing.T) {
	store := newRevertPebble(t)
	root := t.TempDir()
	oldPath := filepath.Join(root, "import", "a.m4b")
	newPath := filepath.Join(root, "library", "a.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(newPath), 0o755))
	require.NoError(t, os.WriteFile(newPath, []byte("x"), 0o644))
	elsewhere := filepath.Join(root, "elsewhere.m4b")
	book, err := store.CreateBook(&database.Book{Title: "A", FilePath: elsewhere, Format: "m4b"})
	require.NoError(t, err)
	require.NoError(t, store.CreateOperationChange(&database.OperationChange{
		OperationID: "op-drift", BookID: book.ID, ChangeType: "file_move", FieldName: "file_path", OldValue: oldPath, NewValue: newPath,
	}))

	res, err := NewRevertService(store).RevertOperation("op-drift")
	require.Error(t, err)
	require.NotNil(t, res)
	require.Equal(t, 1, res.Failed)
	require.Equal(t, 1, res.ChangedSince)

	_, statErr := os.Stat(newPath)
	require.NoError(t, statErr, "the file must stay where the organize left it")
	_, statErr = os.Stat(oldPath)
	require.True(t, os.IsNotExist(statErr), "nothing may be moved to the old path")
	got, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, elsewhere, got.FilePath)
}

// file_copy is listed as not restorable: the copy is not deleted and the book
// keeps pointing at it.
func TestRevertFileCopy_IsReportedNotUndone(t *testing.T) {
	store := newRevertPebble(t)
	root := t.TempDir()
	src := filepath.Join(root, "itunes", "a.m4b")
	dst := filepath.Join(root, "library", "a.m4b")
	for _, p := range []string{src, dst} {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
	}
	book, err := store.CreateBook(&database.Book{Title: "A", FilePath: dst, Format: "m4b"})
	require.NoError(t, err)
	require.NoError(t, store.CreateOperationChange(&database.OperationChange{
		OperationID: "op-copy", BookID: book.ID, ChangeType: "file_copy", FieldName: "file_path", OldValue: src, NewValue: dst,
	}))

	_, err = NewRevertService(store).RevertOperation("op-copy")
	var nre *NotRestorableError
	require.True(t, errors.As(err, &nre), "got %v", err)
	require.Equal(t, 1, nre.Types["file_copy"])
	_, statErr := os.Stat(dst)
	require.NoError(t, statErr)
	got, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, dst, got.FilePath)
}

// fakeTagFile is one file's tags. A tag written "" is removed, as TagLib does.
type fakeTagFile struct {
	tags   map[string]string
	locked map[string]bool
	// heldDuringWrite records whether the path lock was held at each write.
	heldDuringWrite []bool
}

func (f *fakeTagFile) wire(rs *RevertService, path string) {
	rs.ReadTags = func(string) (map[string]string, error) {
		out := map[string]string{"title": "", "artist": ""}
		for k, v := range f.tags {
			out[k] = v
		}
		return out, nil
	}
	rs.WriteTags = func(_ string, tags map[string]any) error {
		f.heldDuringWrite = append(f.heldDuringWrite, f.locked[path])
		for k, v := range tags {
			if s, _ := v.(string); s == "" {
				delete(f.tags, k)
			} else {
				f.tags[k] = s
			}
		}
		return nil
	}
	rs.LockPath = func(p string) func() {
		f.locked[p] = true
		return func() { f.locked[p] = false }
	}
}

// tagWriteBook makes a single-file book with one book_file and records the
// tag_write row the organizer writes for tag: pre-write oldValue, written
// newValue.
func tagWriteBook(t *testing.T, store *database.PebbleStore, op, tag, oldValue, newValue string) (string, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.m4b")
	require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
	book, err := store.CreateBook(&database.Book{Title: "A", FilePath: p, Format: "m4b"})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: p, Format: "m4b"}))
	files, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.NoError(t, store.CreateOperationChange(&database.OperationChange{
		OperationID: op, BookID: book.ID, ChangeType: undo.ChangeTypeTagWrite,
		FieldName: undo.TagWriteField(tag, files[0].ID), OldValue: oldValue, NewValue: newValue,
	}))
	return book.ID, p
}

// Organize wrote the title, a later write-back changed it again, then the
// organize is reverted: the write-back's value stays. The revert used to write
// the pre-organize value regardless and erase the later edit.
func TestRevertTagWrite_KeepsALaterWrite(t *testing.T) {
	store := newRevertPebble(t)
	_, p := tagWriteBook(t, store, "op-later", "title", "Orig", "Organized")
	file := &fakeTagFile{tags: map[string]string{"title": "Organized"}, locked: map[string]bool{}}
	file.tags["title"] = "Written Back Later" // the later write-back

	rs := NewRevertService(store)
	file.wire(rs, p)
	res, err := rs.RevertOperation("op-later")
	require.Error(t, err)
	require.NotNil(t, res)
	require.Equal(t, 1, res.Failed)
	require.Equal(t, 1, res.ChangedSince)
	require.Equal(t, "Written Back Later", file.tags["title"], "the later value must survive the revert")
	require.Empty(t, file.heldDuringWrite, "nothing may be written")
}

// With no later write, the revert puts the pre-organize value back, under the
// file's path lock.
func TestRevertTagWrite_RestoresUnderPathLock(t *testing.T) {
	store := newRevertPebble(t)
	_, p := tagWriteBook(t, store, "op-restore", "title", "Orig", "Organized")
	file := &fakeTagFile{tags: map[string]string{"title": "Organized"}, locked: map[string]bool{}}

	rs := NewRevertService(store)
	file.wire(rs, p)
	res, err := rs.RevertOperation("op-restore")
	require.NoError(t, err, "result %+v", res)
	require.Equal(t, "Orig", file.tags["title"])
	require.Equal(t, []bool{true}, file.heldDuringWrite, "the tag write must run under the path lock")
	require.False(t, file.locked[p], "the lock is released")
}

// A tag the file did not carry before the organize is removed again. It used
// to be recorded as "", which cannot be told apart from "unknown".
func TestRevertTagWrite_AbsentBeforeIsRemoved(t *testing.T) {
	store := newRevertPebble(t)
	_, p := tagWriteBook(t, store, "op-absent", "artist", undo.TagAbsentValue, "Someone")
	file := &fakeTagFile{tags: map[string]string{"artist": "Someone"}, locked: map[string]bool{}}

	rs := NewRevertService(store)
	file.wire(rs, p)
	res, err := rs.RevertOperation("op-absent")
	require.NoError(t, err, "result %+v", res)
	_, still := file.tags["artist"]
	require.False(t, still, "the tag the organize added must be removed")
}

// A file move holds the path lock on both paths across the rename and the
// book write.
func TestRevertFileMove_TakesPathLocks(t *testing.T) {
	store := newRevertPebble(t)
	root := t.TempDir()
	oldPath := filepath.Join(root, "import", "a.m4b")
	newPath := filepath.Join(root, "library", "a.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(newPath), 0o755))
	require.NoError(t, os.WriteFile(newPath, []byte("x"), 0o644))
	book, err := store.CreateBook(&database.Book{Title: "A", FilePath: newPath, Format: "m4b"})
	require.NoError(t, err)
	require.NoError(t, store.CreateOperationChange(&database.OperationChange{
		OperationID: "op-lock", BookID: book.ID, ChangeType: "file_move", FieldName: "file_path", OldValue: oldPath, NewValue: newPath,
	}))

	var locked []string
	rs := NewRevertService(store)
	rs.LockPath = func(p string) func() { locked = append(locked, p); return func() {} }
	res, err := rs.RevertOperation("op-lock")
	require.NoError(t, err, "result %+v", res)
	require.ElementsMatch(t, []string{oldPath, newPath}, locked)
	got, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, oldPath, got.FilePath)
}

// An iTunes-linked book_file row's iTunes path follows the file back; a row
// with none stays without one. The repoint used to leave the organized
// location in ITunesPath.
func TestRevertFileMove_RecomputesITunesPath(t *testing.T) {
	store := newRevertPebble(t)
	root := t.TempDir()
	oldDir := filepath.Join(root, "import", "Book")
	newDir := filepath.Join(root, "library", "Book")
	require.NoError(t, os.MkdirAll(newDir, 0o755))
	for _, n := range []string{"01.mp3", "02.mp3"} {
		require.NoError(t, os.WriteFile(filepath.Join(newDir, n), []byte(n), 0o644))
	}
	book, err := store.CreateBook(&database.Book{Title: "Book", FilePath: newDir, Format: "mp3"})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: filepath.Join(newDir, "01.mp3"), Format: "mp3", ITunesPath: "itunes:" + filepath.Join(newDir, "01.mp3")}))
	require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: filepath.Join(newDir, "02.mp3"), Format: "mp3"}))
	require.NoError(t, store.CreateOperationChange(&database.OperationChange{
		OperationID: "op-itunes", BookID: book.ID, ChangeType: "file_move", FieldName: "file_path", OldValue: oldDir, NewValue: newDir,
	}))

	rs := NewRevertService(store)
	rs.ComputeITunesPath = func(p string) string { return "itunes:" + p }
	res, err := rs.RevertOperation("op-itunes")
	require.NoError(t, err, "result %+v", res)

	files, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Len(t, files, 2)
	for _, f := range files {
		switch filepath.Base(f.FilePath) {
		case "01.mp3":
			require.Equal(t, "itunes:"+filepath.Join(oldDir, "01.mp3"), f.ITunesPath)
		case "02.mp3":
			require.Empty(t, f.ITunesPath, "a row with no iTunes link must not gain one")
		}
	}
}

// A file that already holds the pre-organize value is not written and the row
// succeeds: it must not be refused as changed since.
func TestRevertTagWrite_AlreadyReadsPreOrganizeValue(t *testing.T) {
	store := newRevertPebble(t)
	_, p := tagWriteBook(t, store, "op-alias", "title", "Orig", "Organized")
	file := &fakeTagFile{tags: map[string]string{"title": "Orig"}, locked: map[string]bool{}}

	rs := NewRevertService(store)
	file.wire(rs, p)
	res, err := rs.RevertOperation("op-alias")
	require.NoError(t, err, "result %+v", res)
	require.Equal(t, 0, res.ChangedSince)
	require.Equal(t, "Orig", file.tags["title"])
	require.Empty(t, file.heldDuringWrite, "the file already holds the pre-organize value; nothing is written")
}

// A narrator row recorded before per-tag undo values holds one plain value,
// from an organize that wrote NARRATOR only. Writing it back now would also
// overwrite PERFORMER, so the row is refused with a message and nothing is
// written.
func TestRevertTagWrite_LegacyNarratorRowIsRefused(t *testing.T) {
	store := newRevertPebble(t)
	_, p := tagWriteBook(t, store, "op-legacy-narr", "narrator", "Old Narrator", "New Narrator")
	file := &fakeTagFile{tags: map[string]string{"narrator": "New Narrator"}, locked: map[string]bool{}}

	rs := NewRevertService(store)
	file.wire(rs, p)
	res, err := rs.RevertOperation("op-legacy-narr")
	require.Error(t, err)
	require.Equal(t, 1, res.Failed)
	require.Equal(t, "New Narrator", file.tags["narrator"], "nothing is written")
}
