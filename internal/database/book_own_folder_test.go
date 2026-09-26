// file: internal/database/book_own_folder_test.go
// version: 2.1.0
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

func TestIsBookFileCopy(t *testing.T) {
	base := BookFile{FilePath: "/lib/A/Book/01.mp3", OriginalFilename: "01 Book.mp3",
		FileHash: "h1", FileSize: 1000, Duration: 600}
	cases := []struct {
		name string
		b    BookFile
		want bool
	}{
		{"same non-empty hash", BookFile{FilePath: "/x/other.mp3", FileHash: "h1", FileSize: 1000, Duration: 600}, true},
		{"same hash, size and duration unknown", BookFile{FilePath: "/x/other.mp3", FileHash: "h1"}, true},
		// Legacy 1 MB-prefix hashes collide across distinct tracks.
		{"same legacy hash, different size", BookFile{FilePath: "/x/other.mp3", FileHash: "h1", FileSize: 5, Duration: 600}, false},
		// Durations from different measurement sources drift; they never
		// veto a hash match with agreeing sizes.
		{"same hash and size, durations 2 s apart", BookFile{FilePath: "/x/other.mp3", FileHash: "h1", FileSize: 1000, Duration: 602}, true},
		{"empty hashes never match", BookFile{FilePath: "/x/other.mp3", FileSize: 1000, Duration: 600}, false},
		{"same base name, size, duration", BookFile{FilePath: "/x/01.mp3", FileHash: "h2", FileSize: 1000, Duration: 600}, true},
		{"original filename matches base name", BookFile{FilePath: "/x/01 Book.mp3", FileHash: "h2", FileSize: 1000, Duration: 600}, true},
		{"duration 1 s apart", BookFile{FilePath: "/x/01.mp3", FileHash: "h2", FileSize: 1000, Duration: 601}, true},
		{"duration 1 s apart the other way", BookFile{FilePath: "/x/01.mp3", FileHash: "h2", FileSize: 1000, Duration: 599}, true},
		{"duration 2 s apart", BookFile{FilePath: "/x/01.mp3", FileHash: "h2", FileSize: 1000, Duration: 602}, false},
		{"same name, different size", BookFile{FilePath: "/x/01.mp3", FileHash: "h2", FileSize: 1001, Duration: 600}, false},
		{"same size and duration, different name", BookFile{FilePath: "/x/02.mp3", FileHash: "h2", FileSize: 1000, Duration: 600}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, IsBookFileCopy(c.b, base))
			assert.Equal(t, c.want, IsBookFileCopy(base, c.b), "symmetric")
		})
	}
	t.Run("unknown size is no evidence", func(t *testing.T) {
		a := BookFile{FilePath: "/a/01.mp3"}
		b := BookFile{FilePath: "/b/01.mp3"}
		assert.False(t, IsBookFileCopy(a, b))
	})
}

func TestSplitOwnFolderFiles(t *testing.T) {
	rows := []BookFile{
		{ID: "own", FilePath: "/lib/A/Book/01.m4b", FileHash: "h1", FileSize: 100, Duration: 60},
		{ID: "cd2", FilePath: "/lib/A/Book/CD2/02.m4b", FileHash: "h2", FileSize: 200, Duration: 70},
		{ID: "itunes-copy", FilePath: "/srv/books/itunes/A/Book/01.m4b", FileHash: "h1", FileSize: 100, Duration: 60},
		{ID: "moved", FilePath: "/lib/A/Loser/03.m4b", FileHash: "h3", FileSize: 300, Duration: 80},
	}

	t.Run("directory path: own folder recursive, only copies excluded", func(t *testing.T) {
		b := &Book{FilePath: "/lib/A/Book"}
		s := SplitOwnFolderFiles(b, rows)
		assert.Equal(t, "/lib/A/Book", s.Dir)
		assert.Equal(t, []string{"own", "cd2"}, ownFolderIDs(s.Own))
		assert.Equal(t, []string{"itunes-copy"}, ownFolderIDs(s.Copies))
		assert.Equal(t, []string{"moved"}, ownFolderIDs(s.Others))
		assert.True(t, s.Spans())
		assert.Equal(t, []string{"own", "cd2", "moved"}, ownFolderIDs(OwnFolderFiles(b, rows)))
	})

	t.Run("file path: its directory is the own folder", func(t *testing.T) {
		s := SplitOwnFolderFiles(&Book{FilePath: "/lib/A/Book/01.m4b"}, rows)
		assert.Equal(t, "/lib/A/Book", s.Dir)
		assert.Equal(t, []string{"own", "cd2"}, ownFolderIDs(s.Own))
	})

	t.Run("no own-folder row: every row counts", func(t *testing.T) {
		b := &Book{FilePath: "/lib/A/Gone"}
		s := SplitOwnFolderFiles(b, rows)
		assert.Equal(t, "/lib/A/Gone", s.Dir, "a directory with no row under it does not climb to the author folder")
		assert.Empty(t, s.Own)
		assert.Empty(t, s.Copies)
		assert.False(t, s.HasOwnBasis())
		assert.Equal(t, ownFolderIDs(rows), ownFolderIDs(OwnFolderFiles(b, rows)))
	})

	t.Run("own rows all missing: nothing is a copy", func(t *testing.T) {
		stale := []BookFile{
			{ID: "old", FilePath: "/lib/A/Book/01.m4b", FileHash: "h1", Missing: true},
			{ID: "real", FilePath: "/lib/A/Book [x]/01.m4b", FileHash: "h1"},
		}
		s := SplitOwnFolderFiles(&Book{FilePath: "/lib/A/Book"}, stale)
		assert.False(t, s.HasOwnBasis())
		assert.Empty(t, s.Copies, "a present row is never dropped in favour of a missing twin")
		assert.Equal(t, []string{"old", "real"}, ownFolderIDs(s.Counted(stale)))
	})

	t.Run("nil book, empty or root path: no own folder", func(t *testing.T) {
		for _, b := range []*Book{nil, {}, {FilePath: "/"}, {FilePath: "/x.m4b"}} {
			s := SplitOwnFolderFiles(b, rows)
			assert.Empty(t, s.Dir)
			assert.Len(t, s.Counted(rows), len(rows))
		}
	})
}

// recomputeDuration creates a book at bookPath with rows, recomputes its
// aggregates and returns the stored Duration and FileSize.
func recomputeDuration(t *testing.T, store Store, bookPath string, rows []BookFile) (int, int64) {
	t.Helper()
	b, err := store.CreateBook(&Book{Title: "copies", FilePath: bookPath})
	require.NoError(t, err)
	for i := range rows {
		f := rows[i]
		f.BookID = b.ID
		f.Format = "mp3"
		require.NoError(t, store.CreateBookFile(&f))
	}
	require.NoError(t, store.RecomputeBookAggregates(b.ID))
	got, err := store.GetBookByID(b.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Duration)
	require.NotNil(t, got.FileSize)
	return *got.Duration, *got.FileSize
}

// RecomputeBookAggregates drops an out-of-folder row only when it is a copy of
// a present own-folder row ("skip only copies", owner 2026-09-25).
func TestRecomputeBookAggregates_SkipsOnlyOutOfFolderCopies(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	t.Run("hash-equal copies in two folders count once", func(t *testing.T) {
		d, sz := recomputeDuration(t, store, "/lib/A/Hash", []BookFile{
			{FilePath: "/lib/A/Hash/01.mp3", FileHash: "hA", FileSize: 10, Duration: 600},
			{FilePath: "/lib/A/Hash/02.mp3", FileHash: "hB", FileSize: 20, Duration: 400},
			{FilePath: "/srv/books/itunes/A/Hash/Track 1.mp3", FileHash: "hA", FileSize: 10, Duration: 600},
			{FilePath: "/srv/old/Hash/second.mp3", FileHash: "hB", FileSize: 20, Duration: 400},
		})
		assert.Equal(t, 1000, d)
		assert.Equal(t, int64(30), sz)
	})

	t.Run("name + size + duration copy counts once", func(t *testing.T) {
		d, _ := recomputeDuration(t, store, "/lib/A/Name", []BookFile{
			{FilePath: "/lib/A/Name/01.mp3", FileHash: "n1", FileSize: 1000, Duration: 600},
			{FilePath: "/srv/old/Name/01.mp3", FileHash: "n2", FileSize: 1000, Duration: 601},
		})
		assert.Equal(t, 600, d, "the 601 s copy is within 1 s of the own row: counted once")
	})

	t.Run("same name, different size counts twice", func(t *testing.T) {
		d, _ := recomputeDuration(t, store, "/lib/A/Size", []BookFile{
			{FilePath: "/lib/A/Size/01.mp3", FileHash: "s1", FileSize: 1000, Duration: 600},
			{FilePath: "/srv/old/Size/01.mp3", FileHash: "s2", FileSize: 1001, Duration: 600},
		})
		assert.Equal(t, 1200, d)
	})

	t.Run("merged book: a moved non-copy row keeps its runtime", func(t *testing.T) {
		d, sz := recomputeDuration(t, store, "/lib/A/Winner", []BookFile{
			{FilePath: "/lib/A/Winner/t1.m4b", FileHash: "w1", FileSize: 500, Duration: 50},
			{FilePath: "/lib/A/Loser/t1.m4b", FileHash: "l1", FileSize: 1000, Duration: 100},
			{FilePath: "/lib/A/Loser/t2.m4b", FileHash: "l2", FileSize: 2000, Duration: 200},
		})
		assert.Equal(t, 350, d)
		assert.Equal(t, int64(3500), sz)
	})

	t.Run("no own-folder rows: every row counts", func(t *testing.T) {
		d, _ := recomputeDuration(t, store, "/lib/A/Elsewhere", []BookFile{
			{FilePath: "/srv/old/X/01.mp3", FileHash: "x1", FileSize: 1000, Duration: 100},
			{FilePath: "/srv/old/Y/01.mp3", FileHash: "x1", FileSize: 1000, Duration: 200},
		})
		assert.Equal(t, 300, d, "hash-equal rows, but none in the own folder: no basis to call either a copy")
	})
}
