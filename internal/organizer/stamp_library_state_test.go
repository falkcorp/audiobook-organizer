// file: internal/organizer/stamp_library_state_test.go
// version: 1.0.0
// guid: 6d2b8f04-3a71-4e59-b8c2-1f7a09e4d35c
// last-edited: 2026-08-11

package organizer

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/mock"
)

// ---------------------------------------------------------------------------
// Regression: stampOrganizeMetadata must write LibraryState, not only the two
// timestamp/op-id fields.
//
// The dashboard's "Needs Organizing" card counts books where
// library_state == "imported" (memdb_summaries.go). Before this fix the stamp
// wrote LastOrganizeOperationID and LastOrganizedAt but left LibraryState
// untouched, so a book that was PERFECTLY organized stayed "imported" forever
// and the backlog could never be cleared — re-running organize did not help,
// because FilterBooksNeedingOrganization diverts already-correct books into the
// `alreadyCorrect` bucket BEFORE they can reach ReOrganizeInPlace, which is the
// path that does set the state (see its oldPath == targetPath branch).
//
// All three callers of this helper mean "this book is now sitting at its
// correct organized path", so the state belongs in the shared helper rather
// than at each call site.
// ---------------------------------------------------------------------------

func TestStampOrganizeMetadata_SetsLibraryStateOrganized(t *testing.T) {
	imported := "imported"
	book := &database.Book{
		ID:           "book-1",
		Title:        "Already In The Right Place",
		FilePath:     "/library/Author/Title",
		LibraryState: &imported,
	}

	var captured *database.Book
	mockStore := mocks.NewMockStore(t)
	mockStore.On("GetBookByID", "book-1").Return(book, nil)
	mockStore.On("UpdateBook", "book-1", mock.AnythingOfType("*database.Book")).
		Run(func(args mock.Arguments) {
			// Capture the row as it is actually written. Asserting on the
			// in-memory `book` pointer instead would be a blind spot: the
			// helper writes through a freshly hydrated struct, so the value
			// the store receives is the only one that matters.
			captured = args.Get(1).(*database.Book)
		}).
		Return(book, nil)

	svc := NewService(mockStore)

	when := time.Date(2026, 8, 11, 21, 0, 0, 0, time.UTC)
	if err := svc.stampOrganizeMetadata("book-1", "op-organize-1", when); err != nil {
		t.Fatalf("stampOrganizeMetadata returned error: %v", err)
	}

	if captured == nil {
		t.Fatal("UpdateBook was never called — nothing was stamped")
	}

	if captured.LibraryState == nil {
		t.Fatal("LibraryState was left nil — the book stays out of the organized count")
	}
	if got := *captured.LibraryState; got != "organized" {
		t.Fatalf("LibraryState = %q, want %q — a correctly-organized book would stay in the "+
			"\"Needs Organizing\" backlog forever", got, "organized")
	}

	// The pre-existing stamp fields must survive the change.
	if captured.LastOrganizeOperationID == nil || *captured.LastOrganizeOperationID != "op-organize-1" {
		t.Errorf("LastOrganizeOperationID not stamped: %v", captured.LastOrganizeOperationID)
	}
	if captured.LastOrganizedAt == nil || !captured.LastOrganizedAt.Equal(when) {
		t.Errorf("LastOrganizedAt not stamped: %v", captured.LastOrganizedAt)
	}
}

// Control: a book that is already marked "organized" must stay that way — the
// helper must not flap the state or clear it. This exists so the test above
// cannot pass merely because the field happened to be non-empty.
func TestStampOrganizeMetadata_KeepsAlreadyOrganizedState(t *testing.T) {
	organized := "organized"
	book := &database.Book{
		ID:           "book-2",
		Title:        "Previously Organized",
		FilePath:     "/library/Author/Title",
		LibraryState: &organized,
	}

	var captured *database.Book
	mockStore := mocks.NewMockStore(t)
	mockStore.On("GetBookByID", "book-2").Return(book, nil)
	mockStore.On("UpdateBook", "book-2", mock.AnythingOfType("*database.Book")).
		Run(func(args mock.Arguments) {
			captured = args.Get(1).(*database.Book)
		}).
		Return(book, nil)

	svc := NewService(mockStore)

	if err := svc.stampOrganizeMetadata("book-2", "op-organize-2", time.Now()); err != nil {
		t.Fatalf("stampOrganizeMetadata returned error: %v", err)
	}
	if captured == nil || captured.LibraryState == nil || *captured.LibraryState != "organized" {
		t.Fatalf("expected LibraryState to remain \"organized\", got %v", captured.LibraryState)
	}
}
