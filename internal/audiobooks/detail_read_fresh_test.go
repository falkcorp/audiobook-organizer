// file: internal/audiobooks/detail_read_fresh_test.go
// version: 1.0.0
// guid: 1b5bf471-f635-478e-bf3f-1cd0837c914d
// last-edited: 2026-10-06

package audiobooks_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A write that does not go through the AudiobookService -- a Repairs fixer,
// the scanner, a metadata apply -- shows on the next detail read. Until
// 2026-10-06 GetAudiobook kept a 24h per-book cache that only the service's
// own edits cleared, so a book read before a fixer's write kept its old
// title on the detail page after the write.
func TestGetAudiobook_SeesAWriteMadeOutsideTheService(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	book, err := store.CreateBook(&database.Book{Title: "of 3", FilePath: "/library/fresh.m4b", Format: "m4b"})
	require.NoError(t, err)

	svc := audiobooks.NewAudiobookService(store)
	got, err := svc.GetAudiobook(context.Background(), book.ID)
	require.NoError(t, err)
	require.Equal(t, "of 3", got.Title)

	_, err = store.ModifyBook(book.ID, func(cur *database.Book) error {
		cur.Title = "2 of 3"
		return nil
	})
	require.NoError(t, err)

	got, err = svc.GetAudiobook(context.Background(), book.ID)
	require.NoError(t, err)
	require.Equal(t, "2 of 3", got.Title, "the detail read served a copy cached before the write")
}
