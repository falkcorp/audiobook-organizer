// file: internal/metafetch/apply_authors_fill_only_test.go
// version: 1.0.0
// guid: 3f8a1c6e-2b7d-4e95-a0c4-9d6e1b2f7a58
// last-edited: 2026-09-14

package metafetch

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// coAuthorFixture is a book credited to [A (id 1), B (id 2)] and a store that
// records every SetBookAuthors write. Authors by name: A=1, B=2, C=3.
func coAuthorFixture() (*database.MockStore, *database.Book, *[][]database.BookAuthor) {
	a := 1
	book := &database.Book{ID: "b1", Title: "Good Omens", AuthorID: &a}
	joins := []database.BookAuthor{
		{BookID: "b1", AuthorID: 1, Role: "author", Position: 0},
		{BookID: "b1", AuthorID: 2, Role: "author", Position: 1},
	}
	var writes [][]database.BookAuthor
	ids := map[string]int{"A": 1, "B": 2, "C": 3}
	store := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			if id != book.ID {
				return nil, nil
			}
			clone := *book
			return &clone, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) { return b, nil },
		GetBookAuthorsFunc: func(string) ([]database.BookAuthor, error) {
			return append([]database.BookAuthor(nil), joins...), nil
		},
		SetBookAuthorsFunc: func(_ string, authors []database.BookAuthor) error {
			writes = append(writes, append([]database.BookAuthor(nil), authors...))
			joins = append([]database.BookAuthor(nil), authors...)
			return nil
		},
		GetAuthorByNameFunc: func(name string) (*database.Author, error) {
			if id, ok := ids[name]; ok {
				return &database.Author{ID: id, Name: name}, nil
			}
			return nil, nil
		},
		GetAuthorByIDFunc: func(id int) (*database.Author, error) {
			for n, i := range ids {
				if i == id {
					return &database.Author{ID: i, Name: n}, nil
				}
			}
			return nil, nil
		},
	}
	return store, book, &writes
}

func authorIDs(joins []database.BookAuthor) []int {
	out := make([]int, 0, len(joins))
	for _, j := range joins {
		out = append(out, j.AuthorID)
	}
	return out
}

// finalJoin is the join as the last write left it, or the untouched original.
func finalJoin(writes [][]database.BookAuthor) []int {
	if len(writes) == 0 {
		return []int{1, 2}
	}
	return authorIDs(writes[len(writes)-1])
}

// The fetch path (ApplyMetadataToBook) is fill-only: a candidate naming only
// A must not drop co-author B. Before the fix it wrote [A] and B was gone.
func TestApplyMetadataToBook_SingleAuthorCandidateKeepsCoAuthor(t *testing.T) {
	store, book, writes := coAuthorFixture()
	svc := NewService(store)
	_, err := svc.ApplyMetadataToBook(book, metadata.BookMetadata{Title: "Good Omens", Author: "A"})
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2}, finalJoin(*writes), "co-author B was dropped by a fill-only apply")
	require.NotNil(t, book.AuthorID)
	assert.Equal(t, 1, *book.AuthorID)
}

// The batch paths (batch-apply-one, batch-apply-candidates, the review lane)
// call ApplyMetadataCandidateWithOptions without ReplaceAuthors: fill-only.
func TestApplyMetadataCandidate_BatchPathKeepsCoAuthor(t *testing.T) {
	store, _, writes := coAuthorFixture()
	svc := NewService(store)
	_, err := svc.ApplyMetadataCandidateWithOptions("b1",
		MetadataCandidate{Title: "Good Omens", Author: "A", Source: "audible"}, nil, ApplyOptions{})
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2}, finalJoin(*writes), "co-author B was dropped by a batch apply")
}

// Fill-only with a candidate naming a DIFFERENT author appends it; existing
// links are never removed and the primary AuthorID is not repointed.
func TestApplyMetadataToBook_NewAuthorIsAppendedNotReplaced(t *testing.T) {
	store, book, writes := coAuthorFixture()
	svc := NewService(store)
	_, err := svc.ApplyMetadataToBook(book, metadata.BookMetadata{Title: "Good Omens", Author: "C"})
	require.NoError(t, err)
	require.NotEmpty(t, *writes)
	last := (*writes)[len(*writes)-1]
	assert.Equal(t, []int{1, 2, 3}, authorIDs(last))
	assert.Equal(t, 2, last[2].Position)
	assert.Equal(t, 1, *book.AuthorID, "fill-only must not repoint the primary author")
}

// Only an explicit ReplaceAuthors apply replaces the join.
func TestApplyMetadataCandidate_ExplicitReplaceOverwritesAuthors(t *testing.T) {
	store, _, writes := coAuthorFixture()
	svc := NewService(store)
	_, err := svc.ApplyMetadataCandidateWithOptions("b1",
		MetadataCandidate{Title: "Good Omens", Author: "C", Source: "audible"}, nil, ApplyOptions{ReplaceAuthors: true})
	require.NoError(t, err)
	assert.Equal(t, []int{3}, finalJoin(*writes))
}
