// file: internal/audiobooks/organize_fetch_intent_test.go
// version: 1.0.1
// guid: 0d6a8b3e-47c1-4e92-b5f0-93a7c2e1d864
// last-edited: 2026-11-17

package audiobooks

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

// The fetch-first callback NewOrganizeService wires into the organizer is an
// unattended caller of metafetch's FetchMetadataForBook: a retired book (in the
// trash, or merged into another) is refused before any provider is searched,
// and comes back as the organizer's own skip sentinel (its package cannot
// import metafetch) that still wraps metafetch.ErrBookRetired.
func TestNewOrganizeService_FetchFirstCallbackIsUnattended(t *testing.T) {
	yes := true
	into := "survivor-book-id"
	for name, retire := range map[string]func(*database.Book){
		"soft_deleted": func(b *database.Book) { b.MarkedForDeletion = &yes },
		"merged_into":  func(b *database.Book) { b.MergedIntoBookID = &into },
	} {
		t.Run(name, func(t *testing.T) {
			store, err := database.NewPebbleStore(t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			book := &database.Book{Title: "The Long Halloween", FilePath: "/lib/tlh/book.m4b"}
			retire(book)
			created, err := store.CreateBook(book)
			require.NoError(t, err)

			svc := NewOrganizeService(store)
			resp, err := svc.FetchMetadataForBook(context.Background(), created.ID)
			require.ErrorIs(t, err, organizer.ErrFetchSkippedRetired)
			require.ErrorIs(t, err, metafetch.ErrBookRetired)
			require.Nil(t, resp)
		})
	}
}
