// file: internal/metafetch/apply_author_creation_gate_test.go
// version: 1.0.0
// guid: 2f9d6b3a-7e14-4c85-9a0b-6d1e8c4f2a97
// last-edited: 2026-09-25

package metafetch

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApplyMetadataToBook_JunkProviderAuthorCreatesNoAuthor: an Audible apply
// on 2026-09-15 returned the author "Epigraph" for ASIN B0BYTJ23J7 and the apply
// minted it, because the only check was IsGarbageValue's 15-word list. A junk
// provider author must neither be created nor LOOKED UP (prod already holds an
// "Epigraph" row, and linking it is the same damage), and the book keeps its
// existing credit.
func TestApplyMetadataToBook_JunkProviderAuthorCreatesNoAuthor(t *testing.T) {
	for _, junk := range []string{
		"Epigraph", "- Epigraph", "read by narrator", "14 BBY", "13 short stories",
		"Book 1 (Unabridged)", "(c) 2001 Stephen Hawking", "02-25",
	} {
		t.Run(junk, func(t *testing.T) {
			store, book, writes := coAuthorFixture()
			var looked, created []string
			inner := store.GetAuthorByNameFunc
			store.GetAuthorByNameFunc = func(name string) (*database.Author, error) {
				looked = append(looked, name)
				if name == junk {
					// An existing junk row, as in prod.
					return &database.Author{ID: 99, Name: name}, nil
				}
				return inner(name)
			}
			store.CreateAuthorFunc = func(name string) (*database.Author, error) {
				created = append(created, name)
				return &database.Author{ID: 100, Name: name}, nil
			}

			svc := NewService(store)
			_, err := svc.ApplyMetadataToBook(book, metadata.BookMetadata{Title: "Good Omens", Author: junk})
			require.NoError(t, err)
			assert.Empty(t, created, "a junk provider author was created")
			assert.NotContains(t, looked, junk, "a junk provider author was looked up, so an existing junk row could be linked")
			assert.Equal(t, []int{1, 2}, finalJoin(*writes), "the book's credits changed")
			require.NotNil(t, book.AuthorID)
			assert.Equal(t, 1, *book.AuthorID)
		})
	}
}

// TestApplyMetadataToBook_RealProviderAuthorStillCredited guards the other
// direction: a real single-word pen name from a provider is still added.
func TestApplyMetadataToBook_RealProviderAuthorStillCredited(t *testing.T) {
	store, book, writes := coAuthorFixture()
	store.CreateAuthorFunc = func(name string) (*database.Author, error) {
		return &database.Author{ID: 3, Name: name}, nil
	}
	svc := NewService(store)
	_, err := svc.ApplyMetadataToBook(book, metadata.BookMetadata{Title: "Good Omens", Author: "Zogarth"})
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2, 3}, finalJoin(*writes))
}
