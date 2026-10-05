// file: internal/database/metadata_cache_keep_on_identifier_test.go
// version: 1.0.0
// guid: 247c268d-d5c6-4e27-b079-6b3a43dcd85d
// last-edited: 2026-10-05

package database

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// These tests pin which book writes drop the book's cached metadata
// candidates. Until 2026-10-05 every ASIN/ISBN/series change dropped them, and
// metafetch.asin-backfill -- filling an EMPTY ASIN, often the cached
// candidate's own -- wiped the candidates of 1,078 production books. Only a
// change to what the search asks (title, or the author's name) drops the row
// now; identifiers are judged by the apply gate against the kept row.

func newKeepCacheStore(t *testing.T) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStoreInMemory("db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seedCachedCandidate stores a one-candidate cache row for bookID whose candidate
// carries asin.
func seedCachedCandidate(t *testing.T, s *PebbleStore, bookID, asin string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"title": "The Book", "author": "An Author", "asin": asin, "score": 0.9})
	require.NoError(t, err)
	require.NoError(t, s.PutMetadataCache(&MetadataCandidateCache{
		BookID: bookID, FetchedAt: time.Now(), Candidates: []json.RawMessage{raw}, SourceHash: "h",
	}))
}

func requireCandidates(t *testing.T, s *PebbleStore, bookID string, want int, msg string) {
	t.Helper()
	entry, err := s.GetMetadataCache(bookID)
	require.NoError(t, err)
	if want == 0 {
		require.Nil(t, entry, msg)
		return
	}
	require.NotNil(t, entry, msg)
	require.Len(t, entry.Candidates, want, msg)
}


// TestUpdateBook_FillingAnEmptyIdentifierKeepsCandidates: an ASIN, ISBN-13 or
// ISBN-10 written onto a book that had none keeps the cached candidates, on
// both write paths (UpdateBook and ModifyBook, which asin-backfill uses), and
// does not move the cache generation (no cache row changed).
func TestUpdateBook_FillingAnEmptyIdentifierKeepsCandidates(t *testing.T) {
	for name, fill := range map[string]func(*Book){
		"asin":   func(b *Book) { b.ASIN = strp("B00CANDID8") },
		"isbn13": func(b *Book) { b.ISBN13 = strp("9780000000002") },
		"isbn10": func(b *Book) { b.ISBN10 = strp("0000000001") },
		"all three": func(b *Book) {
			b.ASIN, b.ISBN13, b.ISBN10 = strp("B00CANDID8"), strp("9780000000002"), strp("0000000001")
		},
	} {
		t.Run(name+"/ModifyBook", func(t *testing.T) {
			s := newKeepCacheStore(t)
			b, err := s.CreateBook(&Book{Title: "The Book", FilePath: "/lib/a/" + name, Format: "m4b"})
			require.NoError(t, err)
			seedCachedCandidate(t, s, b.ID, "B00CANDID8")
			g := s.MetadataCacheGeneration()
			_, err = s.ModifyBook(b.ID, func(cur *Book) error { fill(cur); return nil })
			require.NoError(t, err)
			requireCandidates(t, s, b.ID, 1, "filling an empty identifier must keep the cached candidate")
			require.Equal(t, g, s.MetadataCacheGeneration(), "no cache row changed")
		})
		t.Run(name+"/UpdateBook", func(t *testing.T) {
			s := newKeepCacheStore(t)
			b, err := s.CreateBook(&Book{Title: "The Book", FilePath: "/lib/b/" + name, Format: "m4b"})
			require.NoError(t, err)
			seedCachedCandidate(t, s, b.ID, "B00CANDID8")
			fill(b)
			_, err = s.UpdateBook(b.ID, b)
			require.NoError(t, err)
			requireCandidates(t, s, b.ID, 1, "filling an empty identifier must keep the cached candidate")
		})
	}
}

// TestUpdateBook_ReplacedASINKeepsCandidatesForTheGate: a non-empty ASIN
// replaced by another keeps the row. The store cannot tell which candidate is
// right (candidates are opaque to it); the apply gate refuses a candidate
// whose ASIN differs from the book's (asin_conflict), so the old candidates
// are flagged, not destroyed. A series change keeps the row too: no search
// asks by series.
func TestUpdateBook_ReplacedASINKeepsCandidatesForTheGate(t *testing.T) {
	s := newKeepCacheStore(t)
	b, err := s.CreateBook(&Book{Title: "The Book", FilePath: "/lib/c/x.m4b", Format: "m4b", ASIN: strp("B00OLDASIN")})
	require.NoError(t, err)
	seedCachedCandidate(t, s, b.ID, "B00OLDASIN")
	_, err = s.ModifyBook(b.ID, func(cur *Book) error { cur.ASIN = strp("B00NEWASIN"); return nil })
	require.NoError(t, err)
	requireCandidates(t, s, b.ID, 1, "a replaced ASIN keeps the row; the apply gate judges it")

	ser, err := s.CreateSeries("A Series", nil)
	require.NoError(t, err)
	_, err = s.ModifyBook(b.ID, func(cur *Book) error { cur.SeriesID = &ser.ID; return nil })
	require.NoError(t, err)
	requireCandidates(t, s, b.ID, 1, "a series change keeps the row")
}

// TestUpdateBook_TitleOrAuthorNameChangeDropsCandidates: the candidates were
// fetched for the old title or author; a change to either drops them. An
// AuthorID change that keeps the author's name (a relink onto a same-named
// row) searches exactly as before and keeps them.
func TestUpdateBook_TitleOrAuthorNameChangeDropsCandidates(t *testing.T) {
	s := newKeepCacheStore(t)
	a1, err := s.CreateAuthor("Jane Writer")
	require.NoError(t, err)
	// A second row that ends up with the same name: what a relink onto a
	// duplicate author row leaves (CreateAuthor itself resolves by name).
	a2, err := s.CreateAuthor("Jane Writer Duplicate")
	require.NoError(t, err)
	require.NoError(t, s.UpdateAuthorName(a2.ID, "Jane Writer "))
	a3, err := s.CreateAuthor("Somebody Else")
	require.NoError(t, err)
	require.NotEqual(t, a1.ID, a2.ID)

	b, err := s.CreateBook(&Book{Title: "The Book", FilePath: "/lib/d/x.m4b", Format: "m4b", AuthorID: &a1.ID})
	require.NoError(t, err)
	seedCachedCandidate(t, s, b.ID, "B00CANDID8")

	_, err = s.ModifyBook(b.ID, func(cur *Book) error { cur.AuthorID = &a2.ID; return nil })
	require.NoError(t, err)
	requireCandidates(t, s, b.ID, 1, "a relink to a same-named author keeps the row")

	g := s.MetadataCacheGeneration()
	_, err = s.ModifyBook(b.ID, func(cur *Book) error { cur.AuthorID = &a3.ID; return nil })
	require.NoError(t, err)
	requireCandidates(t, s, b.ID, 0, "an author name change drops the row")
	require.Equal(t, g+1, s.MetadataCacheGeneration(), "the dropped row moves the generation")

	seedCachedCandidate(t, s, b.ID, "B00CANDID8")
	_, err = s.ModifyBook(b.ID, func(cur *Book) error { cur.AuthorID = nil; return nil })
	require.NoError(t, err)
	requireCandidates(t, s, b.ID, 0, "removing the author drops the row")

	seedCachedCandidate(t, s, b.ID, "B00CANDID8")
	_, err = s.ModifyBook(b.ID, func(cur *Book) error { cur.Title = "Another Book"; return nil })
	require.NoError(t, err)
	requireCandidates(t, s, b.ID, 0, "a title change drops the row")
}
