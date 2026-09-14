// file: internal/organizer/rename_tags_test.go
// version: 1.2.0
// guid: 5f7b3d19-2a6e-4c84-9d05-1e8a4c6b2f73
// last-edited: 2026-09-13

package organizer

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/stretchr/testify/require"
)

func newTagTestStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// A multi-file book: tags go to each book_file (never to the folder path) and
// each tag_write row names the file and carries the value it held before.
func TestWriteTagsRecordingOld_PerFileWithPreWriteValues(t *testing.T) {
	store := newTagTestStore(t)
	dir := filepath.Join(t.TempDir(), "Book")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	book, err := store.CreateBook(&database.Book{Title: "Book", FilePath: dir, Format: "mp3"})
	require.NoError(t, err)
	for _, n := range []string{"01.mp3", "02.mp3"} {
		p := filepath.Join(dir, n)
		require.NoError(t, os.WriteFile(p, []byte(n), 0o644))
		require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: p, Format: "mp3"}))
	}

	var mu sync.Mutex
	var wrote []string
	rs := NewRenameService(store)
	rs.ReadCurrentTags = func(p string) (map[string]string, error) {
		return map[string]string{"title": "Orig " + filepath.Base(p), "artist": ""}, nil
	}
	rs.WriteTags = func(p string, _ map[string]any) error {
		mu.Lock()
		defer mu.Unlock()
		wrote = append(wrote, p)
		return nil
	}

	n, lost := rs.writeTagsRecordingOld(book.ID, "op-tags", dir, dir, map[string]any{"title": "Book", "artist": "Someone"})
	require.Zero(t, lost)
	require.Equal(t, 4, n)
	sort.Strings(wrote)
	require.Equal(t, []string{filepath.Join(dir, "01.mp3"), filepath.Join(dir, "02.mp3")}, wrote)

	files, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	idToBase := map[string]string{}
	for _, f := range files {
		idToBase[f.ID] = filepath.Base(f.FilePath)
	}
	changes, err := store.GetOperationChanges("op-tags")
	require.NoError(t, err)
	require.Len(t, changes, 4)
	titles := 0
	for _, c := range changes {
		tag, fileID, ok := undo.TagWriteFromField(c.FieldName)
		require.True(t, ok, "row %q names no book_file", c.FieldName)
		base, known := idToBase[fileID]
		require.True(t, known)
		switch tag {
		case "title":
			titles++
			require.Equal(t, "Orig "+base, c.OldValue, "the pre-write title must be recorded")
			require.Equal(t, "", undo.NotRestorableLabel(c))
		case "artist":
			// The file had no artist: recorded as absent (not ""), so the
			// revert can remove it again.
			require.Equal(t, undo.TagAbsentValue, c.OldValue)
			require.Equal(t, "", undo.NotRestorableLabel(c))
		}
	}
	require.Equal(t, 2, titles)
}

// A file whose current tags cannot be read is not written: the write could
// not be undone.
func TestWriteTagsRecordingOld_UnreadableFileIsNotWritten(t *testing.T) {
	store := newTagTestStore(t)
	p := filepath.Join(t.TempDir(), "a.m4b")
	require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
	book, err := store.CreateBook(&database.Book{Title: "A", FilePath: p, Format: "m4b"})
	require.NoError(t, err)
	rs := NewRenameService(store)
	rs.ReadCurrentTags = func(string) (map[string]string, error) { return nil, errors.New("unreadable") }
	wrote := 0
	rs.WriteTags = func(string, map[string]any) error { wrote++; return nil }

	written, _ := rs.writeTagsRecordingOld(book.ID, "op-x", p, p, map[string]any{"title": "A"})
	require.Equal(t, 0, written)
	require.Equal(t, 0, wrote)
	changes, err := store.GetOperationChanges("op-x")
	require.NoError(t, err)
	require.Empty(t, changes)
}

// A multi-file book copied out of a protected source: the book_file rows still
// carry the source folder, and the tags go to the same files under the copy.
// Matching only rows under the copy found none, so no tag was written.
func TestWriteTagsRecordingOld_ProtectedSourceCopyMultiFile(t *testing.T) {
	store := newTagTestStore(t)
	root := t.TempDir()
	src := filepath.Join(root, "itunes", "Book")
	dst := filepath.Join(root, "library", "Author", "Book")
	for _, d := range []string{src, dst} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	book, err := store.CreateBook(&database.Book{Title: "Book", FilePath: dst, Format: "mp3"})
	require.NoError(t, err)
	for _, n := range []string{"01.mp3", "02.mp3"} {
		require.NoError(t, os.WriteFile(filepath.Join(src, n), []byte(n), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dst, n), []byte(n), 0o644))
		require.NoError(t, store.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: filepath.Join(src, n), Format: "mp3"}))
	}

	var wrote []string
	rs := NewRenameService(store)
	rs.ReadCurrentTags = func(string) (map[string]string, error) { return map[string]string{"title": "Orig"}, nil }
	rs.WriteTags = func(p string, _ map[string]any) error { wrote = append(wrote, p); return nil }

	written, _ := rs.writeTagsRecordingOld(book.ID, "op-copy", src, dst, map[string]any{"title": "Book"})
	require.Equal(t, 2, written)
	sort.Strings(wrote)
	require.Equal(t, []string{filepath.Join(dst, "01.mp3"), filepath.Join(dst, "02.mp3")}, wrote,
		"tags go to the copy, never to the protected source")

	changes, err := store.GetOperationChanges("op-copy")
	require.NoError(t, err)
	require.Len(t, changes, 2)
	for _, c := range changes {
		_, fileID, ok := undo.TagWriteFromField(c.FieldName)
		require.True(t, ok)
		require.NotEmpty(t, fileID)
	}
}
