// file: internal/metafetch/isbn_locks_test.go
// version: 1.0.0
// guid: 9b7f1c2e-6d3a-4f8b-9e21-7c5a0d4b8e13
// last-edited: 2026-09-02

package metafetch

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// isbnStubSource answers every search with the same results. Named "Audible"
// it also feeds the ASIN pass.
type isbnStubSource struct {
	name    string
	results []metadata.BookMetadata
}

func (s *isbnStubSource) Name() string { return s.name }
func (s *isbnStubSource) SearchByTitle(_ context.Context, _ string) ([]metadata.BookMetadata, error) {
	return s.results, nil
}
func (s *isbnStubSource) SearchByTitleAndAuthor(_ context.Context, _, _ string) ([]metadata.BookMetadata, error) {
	return s.results, nil
}

// isbnLockFixture is a book with BOTH identifiers blank so the enrichment has
// a reason to write, plus a store that records locks and captures writes.
func isbnLockFixture(t *testing.T, locked ...string) (*database.MockStore, *[]*database.Book) {
	t.Helper()
	var writes []*database.Book
	store := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Neuromancer"}, nil
		},
		UpdateBookFunc: func(_ string, book *database.Book) (*database.Book, error) {
			cp := *book
			writes = append(writes, &cp)
			return book, nil
		},
		GetMetadataFieldStatesFunc: func(bookID string) ([]database.MetadataFieldState, error) {
			rows := make([]database.MetadataFieldState, 0, len(locked))
			for _, key := range locked {
				rows = append(rows, database.MetadataFieldState{BookID: bookID, Field: key, OverrideLocked: true})
			}
			return rows, nil
		},
	}
	return store, &writes
}

func isbnHit() []metadata.BookMetadata {
	return []metadata.BookMetadata{{Title: "Neuromancer", ISBN: "9780441569595", ASIN: "B000SEGUDE"}}
}

// A user who locked isbn13 blank keeps the blank even when a provider has the
// number; the unlocked asin sibling is still filled.
func TestEnrichBookISBN_LockedBlankISBNStaysBlank(t *testing.T) {
	store, writes := isbnLockFixture(t, database.FieldKeyISBN13)
	svc := NewISBNService(store, []metadata.MetadataSource{&isbnStubSource{name: "Audible", results: isbnHit()}})

	found, err := svc.EnrichBookISBN(context.Background(), "b1")
	if err != nil {
		t.Fatalf("EnrichBookISBN: %v", err)
	}
	if !found {
		t.Fatal("found=false; the unlocked ASIN should still have been filled")
	}
	if len(*writes) != 1 {
		t.Fatalf("UpdateBook called %d times, want 1 (ASIN only)", len(*writes))
	}
	got := (*writes)[0]
	if got.ISBN13 != nil {
		t.Errorf("ISBN13 = %q, want nil: the user locked it blank", *got.ISBN13)
	}
	if got.ISBN10 != nil {
		t.Errorf("ISBN10 = %q, want nil: the hit was a 13-digit number", *got.ISBN10)
	}
	if got.ASIN == nil || *got.ASIN != "B000SEGUDE" {
		t.Errorf("ASIN = %v, want B000SEGUDE: asin is not locked", got.ASIN)
	}
}

// Both ISBN columns locked: no ISBN search result is written at all, while a
// locked asin blocks the ASIN pass on its own.
func TestEnrichBookISBN_EveryIdentifierLockedWritesNothing(t *testing.T) {
	store, writes := isbnLockFixture(t, database.FieldKeyISBN10, database.FieldKeyISBN13, database.FieldKeyASIN)
	svc := NewISBNService(store, []metadata.MetadataSource{&isbnStubSource{name: "Audible", results: isbnHit()}})

	found, err := svc.EnrichBookISBN(context.Background(), "b1")
	if err != nil {
		t.Fatalf("EnrichBookISBN: %v", err)
	}
	if found {
		t.Error("found=true, want false: every identifier is user-locked")
	}
	if len(*writes) != 0 {
		t.Fatalf("UpdateBook called %d times, want 0", len(*writes))
	}
}

// Locked asin alone: the ISBN is filled, the ASIN is not.
func TestEnrichBookISBN_LockedASINKeepsISBNUnlocked(t *testing.T) {
	store, writes := isbnLockFixture(t, database.FieldKeyASIN)
	svc := NewISBNService(store, []metadata.MetadataSource{&isbnStubSource{name: "Audible", results: isbnHit()}})

	found, err := svc.EnrichBookISBN(context.Background(), "b1")
	if err != nil {
		t.Fatalf("EnrichBookISBN: %v", err)
	}
	if !found {
		t.Fatal("found=false; ISBN13 is unlocked and the provider had it")
	}
	if len(*writes) != 1 {
		t.Fatalf("UpdateBook called %d times, want 1 (ISBN only)", len(*writes))
	}
	got := (*writes)[0]
	if got.ISBN13 == nil || *got.ISBN13 != "9780441569595" {
		t.Errorf("ISBN13 = %v, want 9780441569595", got.ISBN13)
	}
	if got.ASIN != nil {
		t.Errorf("ASIN = %q, want nil: the user locked it blank", *got.ASIN)
	}
}

// Lock rows unreadable: refuse to write anything rather than guess.
func TestEnrichBookISBN_LockReadErrorRefusesToWrite(t *testing.T) {
	store, writes := isbnLockFixture(t)
	store.GetMetadataFieldStatesFunc = func(string) ([]database.MetadataFieldState, error) {
		return nil, errors.New("pebble: closed")
	}
	svc := NewISBNService(store, []metadata.MetadataSource{&isbnStubSource{name: "Audible", results: isbnHit()}})

	found, err := svc.EnrichBookISBN(context.Background(), "b1")
	if !errors.Is(err, database.ErrFieldLocksUnavailable) {
		t.Fatalf("err = %v, want ErrFieldLocksUnavailable", err)
	}
	if found {
		t.Error("found=true on a refused enrichment")
	}
	if len(*writes) != 0 {
		t.Fatalf("UpdateBook called %d times, want 0", len(*writes))
	}
}
