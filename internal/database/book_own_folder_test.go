// file: internal/database/book_own_folder_test.go
// version: 1.0.0
// guid: f45fb918-35b5-4c10-871a-1cd0b11c8805
// last-edited: 2026-09-25

package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ownFolderIDs(files []BookFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.ID)
	}
	return out
}

func TestSplitOwnFolderFiles(t *testing.T) {
	rows := []BookFile{
		{ID: "own", FilePath: "/lib/A/Book/01.m4b"},
		{ID: "cd2", FilePath: "/lib/A/Book/CD2/02.m4b"},
		{ID: "sibling", FilePath: "/lib/A/Book 2/01.m4b"},
		{ID: "itunes", FilePath: "/srv/books/itunes/A/Book/01.m4b"},
	}

	t.Run("directory path: own folder recursive, siblings stray", func(t *testing.T) {
		s := SplitOwnFolderFiles(&Book{FilePath: "/lib/A/Book"}, rows)
		assert.Equal(t, "/lib/A/Book", s.Dir)
		assert.Equal(t, []string{"own", "cd2"}, ownFolderIDs(s.Own))
		assert.Equal(t, []string{"sibling", "itunes"}, ownFolderIDs(s.Stray))
		assert.True(t, s.Active())
		assert.Equal(t, []string{"own", "cd2"}, ownFolderIDs(OwnFolderFiles(&Book{FilePath: "/lib/A/Book"}, rows)))
	})

	t.Run("file path: its directory is the own folder", func(t *testing.T) {
		s := SplitOwnFolderFiles(&Book{FilePath: "/lib/A/Book/01.m4b"}, rows)
		assert.Equal(t, "/lib/A/Book", s.Dir)
		assert.Equal(t, []string{"own", "cd2"}, ownFolderIDs(s.Own))
	})

	t.Run("a directory with no row under it does not climb to the author folder", func(t *testing.T) {
		s := SplitOwnFolderFiles(&Book{FilePath: "/lib/A/Gone"}, rows)
		assert.Equal(t, "/lib/A/Gone", s.Dir)
		assert.Empty(t, s.Own)
		assert.False(t, s.Active())
		assert.Len(t, s.Counted(rows), len(rows), "no own rows: every row still counts")
	})

	t.Run("own rows all missing: stale FilePath, keep every row", func(t *testing.T) {
		stale := []BookFile{
			{ID: "old", FilePath: "/lib/A/Book/01.m4b", Missing: true},
			{ID: "real", FilePath: "/lib/A/Book [x]/01.m4b"},
		}
		s := SplitOwnFolderFiles(&Book{FilePath: "/lib/A/Book"}, stale)
		assert.False(t, s.Active())
		assert.Equal(t, []string{"old", "real"}, ownFolderIDs(s.Counted(stale)))
	})

	t.Run("no stray rows: unchanged", func(t *testing.T) {
		s := SplitOwnFolderFiles(&Book{FilePath: "/lib/A/Book"}, rows[:2])
		assert.False(t, s.Active())
	})

	t.Run("nil book, empty or root path: no own folder", func(t *testing.T) {
		for _, b := range []*Book{nil, {}, {FilePath: "/"}, {FilePath: "/x.m4b"}} {
			s := SplitOwnFolderFiles(b, rows)
			assert.Empty(t, s.Dir)
			assert.Len(t, s.Counted(rows), len(rows))
		}
	})
}

// RecomputeBookAggregates stores the own-folder sum for a book whose rows
// span several folders, and the all-rows sum otherwise.
func TestRecomputeBookAggregates_CountsOnlyOwnFolderRows(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	created, err := store.CreateBook(&Book{Title: "own-folder", FilePath: "/lib/A/Own"})
	require.NoError(t, err)
	id := created.ID
	addFile(t, store, id, "/lib/A/Own/01.m4b", 600, 10)
	addFile(t, store, id, "/lib/A/Own/02.m4b", 400, 20)
	addFile(t, store, id, "/srv/books/itunes/A/Own/01.m4b", 1000, 30)
	addFile(t, store, id, "/srv/old/Own/ch01.mp3", 2000, 40)

	require.NoError(t, store.RecomputeBookAggregates(id))
	book, err := store.GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, book.Duration)
	assert.Equal(t, 1000, *book.Duration, "only the own-folder rows 600+400 count")
	assert.Equal(t, int64(30), *book.FileSize, "file size follows the same rows")

	// Without an own-folder row the filter is off and every row counts.
	other, err := store.CreateBook(&Book{Title: "no-own", FilePath: "/lib/A/Elsewhere"})
	require.NoError(t, err)
	addFile(t, store, other.ID, "/srv/old/X/01.m4b", 100, 1)
	addFile(t, store, other.ID, "/srv/old/Y/01.m4b", 200, 1)
	require.NoError(t, store.RecomputeBookAggregates(other.ID))
	ob, err := store.GetBookByID(other.ID)
	require.NoError(t, err)
	assert.Equal(t, 300, *ob.Duration)
}
