// file: internal/metafetch/service_apply_nilbook_test.go
// version: 1.0.0
// guid: 9e4b1c07-52d8-4a63-b8f1-3c70da95e26b
// last-edited: 2026-08-19

package metafetch

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestApplyMetadataCandidate_NilBookFromStoreIsAnError covers the SA5011 fix.
//
// database.MockStore.UpdateBook returns (nil, nil) whenever UpdateBookFunc is
// unset — so any test that builds a mock without it, as the fixture below does
// deliberately, used to reach `updatedBook.Title` and PANIC. A nil check for the
// same variable sat thirty lines further down, after the dereference that would
// already have crashed, which is what staticcheck was pointing at.
//
// The contract every real Store honors is that a nil book comes with an error
// (PebbleStore.UpdateBook returns one on every such path). Rather than routing
// around a violation of it, the apply path now reports it: a store that claims
// success while returning nothing is a bug wherever it happens.
func TestApplyMetadataCandidate_NilBookFromStoreIsAnError(t *testing.T) {
	mock := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Test Book"}, nil
		},
		// UpdateBookFunc deliberately unset: MockStore then returns (nil, nil),
		// which is the exact shape that used to panic.
	}

	svc := NewService(mock)

	_, err := svc.ApplyMetadataCandidate("test-book-id-001", MetadataCandidate{
		Title:  "Test Book",
		Author: "Some Author",
		Source: "Audible",
		Score:  1.0,
	}, nil)

	if err == nil {
		t.Fatal("ApplyMetadataCandidate returned nil error when the store handed back " +
			"a nil book — the nil is being carried forward instead of reported")
	}
	if !strings.Contains(err.Error(), "returned no book") {
		t.Errorf("error = %q, want it to name the nil book the store returned", err)
	}
}
