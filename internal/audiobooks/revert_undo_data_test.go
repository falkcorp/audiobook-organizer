// file: internal/audiobooks/revert_undo_data_test.go
// version: 1.0.0
// guid: 9a4c2e71-5d3b-4f80-b1e6-7c0d8f2a5b39
// last-edited: 2026-09-13

package audiobooks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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
