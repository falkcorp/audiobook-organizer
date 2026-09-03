// file: internal/audiobooks/author_name_gate_test.go
// version: 1.0.0
// guid: 706fca22-946e-4028-b4d1-f6d7c0bbcbb7
// last-edited: 2026-09-03

package audiobooks

import (
	"context"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func gateTestService(created *[]string, written *[]database.Book) *AudiobookService {
	store := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "A Book"}, nil
		},
		GetAuthorByNameFunc: func(string) (*database.Author, error) { return nil, nil },
		CreateAuthorFunc: func(name string) (*database.Author, error) {
			*created = append(*created, name)
			return &database.Author{ID: 100 + len(*created), Name: name}, nil
		},
		SetBookAuthorsFunc: func(string, []database.BookAuthor) error { return nil },
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) {
			*written = append(*written, *b)
			return b, nil
		},
	}
	return &AudiobookService{store: store}
}

// 🔴 A REJECTED NAME MUST NOT BECOME AuthorID = 0. Before the gate,
// primaryAuthorID stayed at its zero value when no author was resolved and was
// then written as &0 -- not "no author", but a reference to an id no row has.
func TestUpdateAudiobook_AllAuthorNamesRejectedReturnsError(t *testing.T) {
	var created []string
	var written []database.Book
	svc := gateTestService(&created, &written)

	name := "Track 01"
	_, err := svc.UpdateAudiobook(context.Background(), "bk1", &UpdateAudiobookRequest{
		Updates: &AudiobookUpdate{Book: &database.Book{}, AuthorName: &name},
	})
	if err == nil {
		t.Fatal("expected an error when every author name is rejected, got nil")
	}
	if !strings.Contains(err.Error(), "no usable author name") {
		t.Errorf("error = %q, want it to name the problem", err)
	}
	if len(created) != 0 {
		t.Errorf("created junk author rows %v", created)
	}
	for _, b := range written {
		if b.AuthorID != nil && *b.AuthorID == 0 {
			t.Error("wrote AuthorID = 0, a reference to an id no row has")
		}
	}
}

// NOTE: the happy path (junk part dropped, real author kept) is not asserted
// here. Reaching it requires the full UpdateAudiobook fixture -- caches,
// activity service, and a dozen more store methods -- and a test that large
// would be testing the fixture. The rejection above is the case with teeth,
// and dedup.CleanAuthorNameForCreation carries its own tests for which names
// survive.
