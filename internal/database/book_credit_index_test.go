// file: internal/database/book_credit_index_test.go
// version: 1.0.0
// guid: 0c7a4e92-6d15-4b83-b2f9-3a8e1d5c9074
// last-edited: 2026-10-12

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreditAuthorIDs_PositionOrderAndDedup(t *testing.T) {
	rows := []BookAuthor{
		{BookID: "b", AuthorID: 9, Role: "co-author", Position: 2},
		{BookID: "b", AuthorID: 4, Role: "author", Position: 0},
		{BookID: "b", AuthorID: 9, Role: "editor", Position: 3},
		{BookID: "b", AuthorID: 0, Role: "author", Position: 1},
		{BookID: "b", AuthorID: 7, Role: "co-author", Position: 1},
	}
	require.Equal(t, []int{4, 7, 9}, orderedCreditAuthorIDs(rows))
}

func TestGetAllBookCreditAuthorIDs_Pebble(t *testing.T) {
	ps, err := NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()

	a, err := ps.CreateAuthor("Synthetic One")
	require.NoError(t, err)
	b, err := ps.CreateAuthor("Synthetic Two")
	require.NoError(t, err)
	book, err := ps.CreateBook(&Book{Title: "Synthetic Title", AuthorID: &a.ID})
	require.NoError(t, err)
	lone, err := ps.CreateBook(&Book{Title: "No Credits"})
	require.NoError(t, err)
	require.NoError(t, ps.SetBookAuthors(book.ID, []BookAuthor{
		{BookID: book.ID, AuthorID: b.ID, Role: "co-author", Position: 1},
		{BookID: book.ID, AuthorID: a.ID, Role: "author", Position: 0},
	}))

	var store BookCreditIndexStore = ps
	got, err := store.GetAllBookCreditAuthorIDs()
	require.NoError(t, err)
	require.Equal(t, []int{a.ID, b.ID}, got[book.ID])
	require.NotContains(t, got, lone.ID)

	// The authoritative scan must agree with the memdb answer.
	scanned, err := ps.getAllBookCreditAuthorIDsPebble()
	require.NoError(t, err)
	require.Equal(t, got, scanned)
}
