// file: internal/database/book_files_with_hash_test.go
// version: 1.1.0
// guid: 5d27b9e0-8c14-4f6a-a3d1-0e9f72b6c845
// last-edited: 2026-10-01

package database

import (
	"errors"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// hashOracle is the brute-force answer BookFilesWithHash must equal: every
// row of every known book, read Pebble-direct (GetBookFiles), whose file_hash
// or original_file_hash is h.
func hashOracle(t *testing.T, s *PebbleStore, books []string, h string) []string {
	t.Helper()
	var out []string
	for _, id := range books {
		rows, err := s.GetBookFiles(id)
		require.NoError(t, err)
		for _, r := range rows {
			if r.FileHash == h || r.OriginalFileHash == h {
				out = append(out, r.ID)
			}
		}
	}
	sort.Strings(out)
	return out
}

func hashLookup(t *testing.T, s *PebbleStore, h string) []string {
	t.Helper()
	rows, err := s.BookFilesWithHash(h)
	require.NoError(t, err)
	var out []string
	for _, r := range rows {
		out = append(out, r.ID)
	}
	sort.Strings(out)
	return out
}

// TestBookFilesWithHash_MatchesBruteForceAcrossWrites: after every kind of
// write that sets, changes or removes a hash, the lookup equals the
// brute-force oracle for every hash ever used. A write path that skips the
// memdb write-through shows up here as a missing row.
func TestBookFilesWithHash_MatchesBruteForceAcrossWrites(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()

	var books []string
	hashes := []string{"h1", "h2", "h3", "h4", "o1", "o2"}
	check := func(step string) {
		t.Helper()
		for _, h := range hashes {
			require.Equal(t, hashOracle(t, s, books, h), hashLookup(t, s, h), "%s: hash %s", step, h)
		}
	}
	newBook := func(title string) string {
		b, err := s.CreateBook(&Book{Title: title, FilePath: "/lib/" + title})
		require.NoError(t, err)
		books = append(books, b.ID)
		return b.ID
	}
	newRow := func(book, path, h, orig string) *BookFile {
		f := &BookFile{BookID: book, FilePath: path, FileHash: h, OriginalFileHash: orig, Duration: 600}
		require.NoError(t, s.CreateBookFile(f))
		return f
	}
	a, b, c := newBook("A"), newBook("B"), newBook("C")
	// Three books share h1: the single-owner index keeps only the last.
	newRow(a, "/lib/A/1.mp3", "h1", "")
	fb := newRow(b, "/lib/B/1.mp3", "h1", "o1")
	fc := newRow(c, "/lib/C/1.mp3", "h1", "")
	newRow(a, "/lib/A/2.mp3", "h2", "")
	check("create")
	require.Len(t, hashLookup(t, s, "h1"), 3)

	require.NoError(t, s.SetBookFileHash(fc.ID, "h3"))
	check("SetBookFileHash")
	require.NoError(t, s.UpdateBookFileHashes(fb.ID, "o2", "post", "h4"))
	check("UpdateBookFileHashes")

	row, err := s.GetBookFileByID(c, fc.ID)
	require.NoError(t, err)
	row.FileHash = "h2"
	require.NoError(t, s.UpdateBookFile(row.ID, row))
	check("UpdateBookFile")

	require.NoError(t, s.DeleteBookFile(fc.ID))
	check("DeleteBookFile")

	// A retired book's rows are still listed: the caller decides liveness.
	_, err = s.ModifyBook(a, func(bk *Book) error {
		yes := true
		bk.MarkedForDeletion = &yes
		return nil
	})
	require.NoError(t, err)
	check("soft-delete")
	require.Contains(t, hashLookup(t, s, "h1"), func() string {
		rows, _ := s.GetBookFiles(a)
		return rows[0].ID
	}())
}

// TestBookFilesWithHash_SharedOriginalHashAndStaleIndex: two books sharing
// an original hash are both found (the single-owner orig index holds only
// the last), and a stale single-owner index entry naming a row that no longer
// carries the hash is dropped by the Pebble verification.
func TestBookFilesWithHash_SharedOriginalHashAndStaleIndex(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()
	a, err := s.CreateBook(&Book{Title: "A", FilePath: "/lib/A"})
	require.NoError(t, err)
	b, err := s.CreateBook(&Book{Title: "B", FilePath: "/lib/B"})
	require.NoError(t, err)
	fa := &BookFile{BookID: a.ID, FilePath: "/lib/A/1.mp3", FileHash: "fa", OriginalFileHash: "orig"}
	require.NoError(t, s.CreateBookFile(fa))
	fb := &BookFile{BookID: b.ID, FilePath: "/lib/B/1.mp3", FileHash: "fb", OriginalFileHash: "orig"}
	require.NoError(t, s.CreateBookFile(fb))
	require.ElementsMatch(t, []string{fa.ID, fb.ID}, hashLookup(t, s, "orig"))

	require.NoError(t, s.db.Set([]byte("book_file_hash:ghost"), []byte(a.ID+":"+fa.ID), nil))
	require.Empty(t, hashLookup(t, s, "ghost"), "a stale index entry is verified away")
}

// TestBookFilesWithHash_FailsClosedWithoutMemdb: with memdb not serving the
// lookup refuses rather than answering from the single-owner index alone.
func TestBookFilesWithHash_FailsClosedWithoutMemdb(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()
	b, err := s.CreateBook(&Book{Title: "A", FilePath: "/lib/A"})
	require.NoError(t, err)
	require.NoError(t, s.CreateBookFile(&BookFile{BookID: b.ID, FilePath: "/lib/A/1.mp3", FileHash: "h1"}))
	s.UseMemDB = false
	_, err = s.BookFilesWithHash("h1")
	require.True(t, errors.Is(err, ErrBookFilesWithHashUnavailable), "got %v", err)
}

// TestClaimBookFilePathKey_PointsTheKeyOnlyWhileTheRowIsThere: the key names
// the claimed row; a row since moved is not claimed.
func TestClaimBookFilePathKey_PointsTheKeyOnlyWhileTheRowIsThere(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()
	a, err := s.CreateBook(&Book{Title: "A", FilePath: "/lib/A"})
	require.NoError(t, err)
	b, err := s.CreateBook(&Book{Title: "B", FilePath: "/lib/B"})
	require.NoError(t, err)
	fa := &BookFile{BookID: a.ID, FilePath: "/lib/x.mp3"}
	require.NoError(t, s.CreateBookFile(fa))
	fb := &BookFile{BookID: b.ID, FilePath: "/lib/x.mp3"}
	require.NoError(t, s.CreateBookFile(fb))
	got, err := s.GetBookFileByPath("/lib/x.mp3")
	require.NoError(t, err)
	require.Equal(t, fb.ID, got.ID, "last writer owns the key")
	before, err := s.GetBookFileByID(a.ID, fa.ID)
	require.NoError(t, err)

	ok, err := s.ClaimBookFilePathKey(a.ID, fa.ID, "/lib/x.mp3")
	require.NoError(t, err)
	require.True(t, ok)
	got, err = s.GetBookFileByPath("/lib/x.mp3")
	require.NoError(t, err)
	require.Equal(t, fa.ID, got.ID)
	after, err := s.GetBookFileByID(a.ID, fa.ID)
	require.NoError(t, err)
	require.Equal(t, before, after, "the claimed row itself is not written")

	ok, err = s.ClaimBookFilePathKey(b.ID, fb.ID, "/lib/elsewhere.mp3")
	require.NoError(t, err)
	require.False(t, ok, "a row not at the path is never claimed")
	got, err = s.GetBookFileByPath("/lib/x.mp3")
	require.NoError(t, err)
	require.Equal(t, fa.ID, got.ID)
}
