// file: internal/merge/ident_dirty_test.go
// version: 1.0.0
// guid: 0f4f4c67-02ef-49d5-a914-d4bbb5a0b6d8
// last-edited: 2026-10-16

package merge

import (
	"testing"

	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A successful merge marks the surviving primary for the identification
// driver; a refused merge marks nothing.
func TestMergeMarksPrimaryDirty(t *testing.T) {
	store := setupTestStore(t)
	ps := database.AsPebbleStore(store)
	require.NotNil(t, ps)
	b1 := &database.Book{ID: ulid.Make().String(), Title: "Title 000123", Format: "mp3", FilePath: "/tmp/t000123.mp3"}
	b2 := &database.Book{ID: ulid.Make().String(), Title: "Title 000123", Format: "m4b", FilePath: "/tmp/t000123.m4b"}
	_, err := store.CreateBook(b1)
	require.NoError(t, err)
	_, err = store.CreateBook(b2)
	require.NoError(t, err)

	ms := NewService(store)
	_, err = ms.MergeBooks([]string{b1.ID}, "")
	require.Error(t, err)
	page, err := ps.IdentDirtyPage("", 10)
	require.NoError(t, err)
	require.Empty(t, page, "a refused merge marked a book")

	res, err := ms.MergeBooks([]string{b1.ID, b2.ID}, "")
	require.NoError(t, err)
	page, err = ps.IdentDirtyPage("", 10)
	require.NoError(t, err)
	ids := make([]string, len(page))
	for i, e := range page {
		ids[i] = e.BookID
	}
	require.Contains(t, ids, res.PrimaryID, "the surviving primary must be marked")
}
