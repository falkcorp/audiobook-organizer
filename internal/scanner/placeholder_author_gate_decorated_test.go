// file: internal/scanner/placeholder_author_gate_decorated_test.go
// version: 1.0.0
// guid: 4f1b7c02-9d63-4a18-8e57-2c6b0af39d41
// last-edited: 2026-09-01

package scanner

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/require"
)

// placeholderAuthors.is decides whether AI parsing is POINTLESS for a book, so a
// decorated placeholder reading as a real author does not merely record a bad
// value -- it permanently skips the book that most needs nominating.
//
// This was the fourth authorname.IsPlaceholder call site, and the only one still
// comparing the raw stored name when the other three were normalised. It is a
// latent hole rather than a live one: of 12,644 distinct author names in a
// 60,000-book production sample, zero currently change verdict under stripping.
// A test is still owed, because "no such row today" is not a property the code
// can rely on -- extractFromFilename produced exactly these strings until the
// guards above it were fixed.
func TestPlaceholderAuthorGateStripsTheEditionSuffix(t *testing.T) {
	for _, name := range []string{
		"Unknown Author (Unabridged)",
		"Unknown Author [Unabridged]",
		"Unknown Author (Unabridged) (2019)",
	} {
		t.Run(name, func(t *testing.T) {
			store := dbmocks.NewMockStore(t)
			SetStore(store)
			t.Cleanup(func() { SetStore(nil) })
			store.EXPECT().GetAuthorByID(7).Return(&database.Author{ID: 7, Name: name}, nil)

			require.True(t, newPlaceholderAuthors().is(7),
				"a decorated placeholder read as a real author; the AI nomination gate "+
					"closes on this book permanently")
		})
	}

	// The converse, so the above cannot pass by treating every author as the
	// placeholder and nominating the whole library.
	t.Run("a real author is not the placeholder", func(t *testing.T) {
		store := dbmocks.NewMockStore(t)
		SetStore(store)
		t.Cleanup(func() { SetStore(nil) })
		store.EXPECT().GetAuthorByID(8).Return(&database.Author{ID: 8, Name: "Terry Pratchett (Unabridged)"}, nil)

		require.False(t, newPlaceholderAuthors().is(8),
			"a real author carrying an edition suffix was treated as the placeholder")
	})
}
