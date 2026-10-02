// file: internal/database/book_change_log_test.go
// version: 1.0.0
// guid: 0b1d4a52-6f0e-4c1a-9d37-5e8a2c7b1f63
// last-edited: 2026-10-01

package database

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/cache"
)

// TestBookChangeLog_ListsWhatChangedOrRefuses: each book once, in write
// order; a bump with no record, or a start older than the log holds, is not
// listable.
func TestBookChangeLog_ListsWhatChangedOrRefuses(t *testing.T) {
	var g cache.Generation
	var l bookChangeLog
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

	for i := 0; i < bookChangeLogCap; i++ {
		l.bump(&g, "x")
	}
	_, _, ok = l.since(&g, 4)
	require.False(t, ok, "older than the log holds")
	ids, _, ok = l.since(&g, g.Value()-1)
	require.True(t, ok)
	require.Equal(t, []string{"x"}, ids)
}

// TestPebbleStore_BooksChangedSince: create, update and delete are logged
// against their book.
func TestPebbleStore_BooksChangedSince(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
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
