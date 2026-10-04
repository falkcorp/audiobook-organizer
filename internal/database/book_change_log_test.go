// file: internal/database/book_change_log_test.go
// version: 2.1.1
// guid: 0b1d4a52-6f0e-4c1a-9d37-5e8a2c7b1f63
// last-edited: 2026-10-03

package database

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/cache"
)

// TestIDChangeLog_ListsWhatChangedOrRefuses: each book once, in write
// order; a bump with no record, or a start older than the log holds, is not
// listable.
func TestIDChangeLog_ListsWhatChangedOrRefuses(t *testing.T) {
	var g cache.Generation
	var l idChangeLog
	l.bump(&g, "a")
	l.bump(&g, "b")
	l.bump(&g, "a")
	ids, upTo, ok := l.since(&g, 0)
	require.True(t, ok)
	require.Equal(t, []string{"a", "b"}, ids)
	require.Equal(t, uint64(3), upTo)
	ids, upTo, ok = l.since(&g, 3)
	require.True(t, ok)
	require.Empty(t, ids)
	require.Equal(t, uint64(3), upTo)

	g.Bump() // a write the log did not see
	l.bump(&g, "c")
	_, _, ok = l.since(&g, 3)
	require.False(t, ok, "a gap means a book changed that the log cannot name")
	ids, _, ok = l.since(&g, 4)
	require.True(t, ok)
	require.Equal(t, []string{"c"}, ids)

	for i := 0; i < idChangeLogCap; i++ {
		l.bump(&g, "x")
	}
	_, _, ok = l.since(&g, 4)
	require.False(t, ok, "older than the log holds")
	ids, _, ok = l.since(&g, g.Value()-1)
	require.True(t, ok)
	require.Equal(t, []string{"x"}, ids)
}

// TestIDChangeLog_MidLogGapIsNotListable: a bare Bump between two
// recorded bumps leaves a generation no record names, so since() must refuse
// from before it, and list from after it.
func TestIDChangeLog_MidLogGapIsNotListable(t *testing.T) {
	var g cache.Generation
	var l idChangeLog
	l.bump(&g, "a") // 1
	g.Bump()        // 2: a write the log did not see
	l.bump(&g, "b") // 3
	_, _, ok := l.since(&g, 0)
	require.False(t, ok, "generation 2 has no record")
	ids, upTo, ok := l.since(&g, 2)
	require.True(t, ok)
	require.Equal(t, []string{"b"}, ids)
	require.Equal(t, uint64(3), upTo)
}

// TestIDChangeLog_RecordsMustCoverExactlyTheRange pins since()'s own
// contract: it is ok only when the records exactly cover (from, cur]. A
// record count that happens to equal cur-from is not proof; the records must
// be contiguous from from+1. No production caller can reach this state (the
// reader holds l.mu and every record is <= the generation it reads), so this
// drives since() with a generation behind the log: records 1, 3, 4 against
// cur 3 is three records for a span of three, with generation 2 missing.
func TestIDChangeLog_RecordsMustCoverExactlyTheRange(t *testing.T) {
	var g, behind cache.Generation
	var l idChangeLog
	l.bump(&g, "a") // 1
	g.Bump()        // 2: no record
	l.bump(&g, "b") // 3
	l.bump(&g, "c") // 4
	behind.Bump()
	behind.Bump()
	behind.Bump()
	_, _, ok := l.since(&behind, 0)
	require.False(t, ok, "generation 2 has no record")
}

// TestPebbleStore_BooksChangedSince: create, update and delete are logged
// against their book.
func TestPebbleStore_BooksChangedSince(t *testing.T) {
	s, err := NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()
	from := s.LibraryGeneration().Value()
	a, err := s.CreateBook(&Book{Title: "A", FilePath: "/lib/A"})
	require.NoError(t, err)
	b, err := s.CreateBook(&Book{Title: "B", FilePath: "/lib/B"})
	require.NoError(t, err)
	_, err = s.ModifyBook(a.ID, func(x *Book) error { x.Title = "A2"; return nil })
	require.NoError(t, err)
	require.NoError(t, s.DeleteBook(b.ID))
	ids, upTo, ok := BooksChangedSinceOf(s, from)
	require.True(t, ok)
	require.Equal(t, []string{a.ID, b.ID}, ids)
	require.Equal(t, s.LibraryGeneration().Value(), upTo)
}

// TestIDChangeLog_BumpAllForgetsEverything: a write that touched every id
// (Reset) cannot be listed; a reader at any earlier generation is told to
// rebuild, a reader at the new one is up to date, and the log works again
// after it.
func TestIDChangeLog_BumpAllForgetsEverything(t *testing.T) {
	var g cache.Generation
	var l idChangeLog
	l.bump(&g, "a") // 1
	l.bump(&g, "b") // 2
	l.bumpAll(&g)   // 3
	_, upTo, ok := l.since(&g, 0)
	require.False(t, ok)
	require.Equal(t, uint64(3), upTo)
	_, _, ok = l.since(&g, 2)
	require.False(t, ok, "the generation just before the wipe is behind it")
	ids, upTo, ok := l.since(&g, 3)
	require.True(t, ok)
	require.Empty(t, ids)
	require.Equal(t, uint64(3), upTo)
	l.bump(&g, "c") // 4
	ids, _, ok = l.since(&g, 3)
	require.True(t, ok)
	require.Equal(t, []string{"c"}, ids)
	_, _, ok = l.since(&g, 1)
	require.False(t, ok)
}

// TestPebbleStore_MetadataCacheChangedSince: every writer of the
// "metadata_cache:" keyspace moves the generation by one and is logged
// against its book -- Put, Delete, a raw key write (the id is the key's
// suffix), a raw batch delete, UpdateBook's identity-change delete and
// DeleteBook -- and Reset makes every earlier generation unlistable.
func TestPebbleStore_MetadataCacheChangedSince(t *testing.T) {
	s, err := NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()

	book, err := s.CreateBook(&Book{Title: "A", FilePath: "/lib/A"})
	require.NoError(t, err)
	other, err := s.CreateBook(&Book{Title: "B", FilePath: "/lib/B"})
	require.NoError(t, err)
	from := s.MetadataCacheGeneration()

	require.NoError(t, s.PutMetadataCache(&MetadataCandidateCache{BookID: "x"}))
	require.NoError(t, s.DeleteMetadataCache("y"))
	require.NoError(t, s.SetRaw("metadata_cache:raw1", []byte(`{"book_id":"raw1"}`)))
	require.NoError(t, s.SetRaw("not_cache:raw1", []byte("ignored")))
	require.NoError(t, s.DeleteRaw("metadata_cache:raw2"))
	require.NoError(t, s.DeleteRawBatch([]string{"metadata_cache:raw3", "other:raw3", "metadata_cache:raw4"}))
	ids, upTo, ok := MetadataCacheChangedSinceOf(s, from)
	require.True(t, ok)
	require.Equal(t, []string{"x", "y", "raw1", "raw2", "raw3", "raw4"}, ids)
	require.Equal(t, from+6, upTo, "one bump per cache key written, none for other keys")
	require.Equal(t, s.MetadataCacheGeneration(), upTo)

	// UpdateBook's identity-change delete and DeleteBook, only when the row
	// existed to be deleted.
	require.NoError(t, s.PutMetadataCache(&MetadataCandidateCache{BookID: book.ID}))
	require.NoError(t, s.PutMetadataCache(&MetadataCandidateCache{BookID: other.ID}))
	from = s.MetadataCacheGeneration()
	asin := "B000000001"
	_, err = s.ModifyBook(book.ID, func(b *Book) error { b.ASIN = &asin; return nil })
	require.NoError(t, err)
	require.NoError(t, s.DeleteBook(other.ID))
	ids, _, ok = MetadataCacheChangedSinceOf(s, from)
	require.True(t, ok)
	require.Equal(t, []string{book.ID, other.ID}, ids)
	desc := "no identity change"
	_, err = s.ModifyBook(book.ID, func(b *Book) error { b.Description = &desc; return nil })
	require.NoError(t, err)
	_, upTo2, ok := MetadataCacheChangedSinceOf(s, from)
	require.True(t, ok)
	require.Equal(t, from+2, upTo2, "a write that deletes no cache row does not move it")

	// Reset wipes the keyspace: nothing before it is listable.
	before := s.MetadataCacheGeneration()
	require.NoError(t, s.Reset())
	_, upTo, ok = MetadataCacheChangedSinceOf(s, before)
	require.False(t, ok)
	require.Equal(t, before+1, upTo)
	ids, _, ok = MetadataCacheChangedSinceOf(s, upTo)
	require.True(t, ok)
	require.Empty(t, ids)
}

// TestPebbleStore_ResetAndWipeMakeBookLogUnlistable: Reset wipes every book
// and WipeByPrefixes wipes them when a prefix covers "book:" (a shorter
// prefix, or keys inside it); neither can name the books, so the library
// change log refuses from before them and a reader rebuilds. A wipe of an
// unrelated prefix leaves both logs listable; one of "metadata_cache:" alone
// moves only the cache log.
func TestPebbleStore_ResetAndWipeMakeBookLogUnlistable(t *testing.T) {
	open := func(t *testing.T) (*PebbleStore, *Book) {
		s, err := NewPebbleStoreInMemory(t.TempDir())
		require.NoError(t, err)
		t.Cleanup(func() { _ = s.Close() })
		s.WaitForWarmup()
		b, err := s.CreateBook(&Book{Title: "A", FilePath: "/lib/A"})
		require.NoError(t, err)
		require.NoError(t, s.PutMetadataCache(&MetadataCandidateCache{BookID: b.ID}))
		return s, b
	}
	t.Run("reset", func(t *testing.T) {
		s, _ := open(t)
		bg, cg := s.LibraryGeneration().Value(), s.MetadataCacheGeneration()
		require.NoError(t, s.Reset())
		_, upTo, ok := BooksChangedSinceOf(s, bg)
		require.False(t, ok)
		require.Equal(t, bg+1, upTo)
		_, _, ok = MetadataCacheChangedSinceOf(s, cg)
		require.False(t, ok)
	})
	for name, prefixes := range map[string][]string{
		"book prefix":      {"book:"},
		"shorter prefix":   {"b"},
		"keys inside book": {"book:0"},
	} {
		t.Run("wipe "+name, func(t *testing.T) {
			s, b := open(t)
			bg, cg := s.LibraryGeneration().Value(), s.MetadataCacheGeneration()
			if name == "keys inside book" {
				prefixes = []string{"book:" + b.ID[:3]}
			}
			n, err := s.WipeByPrefixes(prefixes)
			require.NoError(t, err)
			require.Positive(t, n)
			_, upTo, ok := BooksChangedSinceOf(s, bg)
			require.False(t, ok, "the wiped books cannot be named")
			require.Equal(t, bg+1, upTo)
			ids, _, ok := BooksChangedSinceOf(s, upTo)
			require.True(t, ok)
			require.Empty(t, ids)
			_, _, cacheOK := MetadataCacheChangedSinceOf(s, cg)
			require.True(t, cacheOK, "none of these prefixes covers metadata_cache:")
		})
	}
	t.Run("wipe cache prefix only", func(t *testing.T) {
		s, _ := open(t)
		bg, cg := s.LibraryGeneration().Value(), s.MetadataCacheGeneration()
		_, err := s.WipeByPrefixes([]string{"metadata_cache:"})
		require.NoError(t, err)
		_, _, ok := BooksChangedSinceOf(s, bg)
		require.True(t, ok, "no book was wiped")
		_, _, ok = MetadataCacheChangedSinceOf(s, cg)
		require.False(t, ok)
	})
	t.Run("wipe unrelated prefix", func(t *testing.T) {
		s, _ := open(t)
		require.NoError(t, s.SetRaw("zz_unrelated:1", []byte("x")))
		bg, cg := s.LibraryGeneration().Value(), s.MetadataCacheGeneration()
		n, err := s.WipeByPrefixes([]string{"zz_unrelated:"})
		require.NoError(t, err)
		require.Equal(t, 1, n)
		_, _, ok := BooksChangedSinceOf(s, bg)
		require.True(t, ok)
		_, _, ok = MetadataCacheChangedSinceOf(s, cg)
		require.True(t, ok)
	})
}
