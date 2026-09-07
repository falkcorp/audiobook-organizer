// file: internal/organizer/stamp_library_state_test.go
// version: 1.1.0
// guid: 6d2b8f04-3a71-4e59-b8c2-1f7a09e4d35c
// last-edited: 2026-09-07

package organizer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
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

// ---------------------------------------------------------------------------
// Regression: the automatic, unattributed organize path must still stamp.
//
// Post-scan auto-organize (server.go) organizes with an EMPTY operation id.
// That means "there is no operation to attribute this to", not "attribute it to
// the empty operation" — so the stamp must still record that the book is
// organized, and must NOT blank the id of whichever run last organized it.
//
// This is the second half of the library_state revert. The scanner fix stops a
// rescan reverting the state; this is what puts a reverted book back, because
// the automatic path is the one that runs unattended after every scan.
// ---------------------------------------------------------------------------

func TestStampOrganizeMetadata_EmptyOperationIDStillStampsButKeepsAttribution(t *testing.T) {
	imported := "imported"
	priorOp := "op-organize-previous"
	book := &database.Book{
		ID:                      "book-3",
		Title:                   "Reverted By A Rescan",
		FilePath:                "/library/Author/Title",
		LibraryState:            &imported,
		LastOrganizeOperationID: &priorOp,
	}

	var captured *database.Book
	mockStore := mocks.NewMockStore(t)
	mockStore.On("GetBookByID", "book-3").Return(book, nil)
	mockStore.On("UpdateBook", "book-3", mock.AnythingOfType("*database.Book")).
		Run(func(args mock.Arguments) { captured = args.Get(1).(*database.Book) }).
		Return(book, nil)

	svc := NewService(mockStore)
	when := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	if err := svc.stampOrganizeMetadata("book-3", "", when); err != nil {
		t.Fatalf("stampOrganizeMetadata returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("UpdateBook was never called — an unattributed organize stamped nothing, " +
			"so a book reverted by a rescan stays invisible to the ABS layer")
	}
	if captured.LibraryState == nil || *captured.LibraryState != "organized" {
		t.Fatalf("LibraryState = %v, want \"organized\": being organized is a fact about the "+
			"book, not something that requires an operation to attribute it to",
			derefStr(captured.LibraryState))
	}
	if captured.LastOrganizedAt == nil || !captured.LastOrganizedAt.Equal(when) {
		t.Errorf("LastOrganizedAt = %v, want %v", captured.LastOrganizedAt, when)
	}
	// The attribution is the ONLY conditional part.
	if captured.LastOrganizeOperationID == nil {
		t.Fatal("LastOrganizeOperationID was blanked to nil by an unattributed organize")
	}
	if got := *captured.LastOrganizeOperationID; got != priorOp {
		t.Fatalf("LastOrganizeOperationID = %q, want the previous id %q preserved — "+
			"the automatic path runs unattended after every scan, so blanking it here "+
			"erases the attribution for most books in the library", got, priorOp)
	}
}

// ---------------------------------------------------------------------------
// Regression: the bulk already-correct stamp must not be gated on operationID.
//
// This is the loop that carried the gate. Post-scan auto-organize passes an
// EMPTY operation id and every already-organized book lands in this bucket, so
// gating here meant the automatic path — the one that runs after every scan,
// unattended — stamped nothing at all. Combined with the scanner reverting
// library_state on rescan, that left organized books permanently "imported" and
// therefore invisible to the ABS layer, with nothing in the system able to
// restore them.
// ---------------------------------------------------------------------------

func TestStampAlreadyCorrect_StampsWithNoOperationID(t *testing.T) {
	const n = 25 // more than the worker count, so the pool is genuinely exercised
	books := make([]database.Book, 0, n)
	mockStore := mocks.NewMockStore(t)

	var mu sync.Mutex
	stampedState := map[string]string{}

	for i := range n {
		id := fmt.Sprintf("book-%02d", i)
		imported := "imported"
		row := &database.Book{ID: id, Title: id, FilePath: "/library/" + id, LibraryState: &imported}
		books = append(books, *row)
		mockStore.On("GetBookByID", id).Return(row, nil)
		mockStore.On("UpdateBook", id, mock.AnythingOfType("*database.Book")).
			Run(func(args mock.Arguments) {
				got := args.Get(1).(*database.Book)
				mu.Lock()
				defer mu.Unlock()
				if got.LibraryState != nil {
					stampedState[got.ID] = *got.LibraryState
				}
			}).Return(row, nil)
	}

	svc := NewService(mockStore)
	// The empty operation id is the whole point.
	got := svc.stampAlreadyCorrect(context.Background(), books, "", logger.New("test"))

	if got != n {
		t.Fatalf("stampAlreadyCorrect reported %d stamped, want %d", got, n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(stampedState) != n {
		t.Fatalf("only %d of %d already-correct books were stamped with no operation id — "+
			"post-scan auto-organize would leave the rest invisible to the ABS layer",
			len(stampedState), n)
	}
	for id, state := range stampedState {
		if state != "organized" {
			t.Errorf("book %s was stamped %q, want \"organized\"", id, state)
		}
	}
}

// TestStampAlreadyCorrect_CountsOnlyStampsThatLanded pins the returned count to
// writes that actually succeeded. Returning len() regardless — what the
// sequential version did — reported every book as already-correct even when its
// write failed, so the summary disagreed with the database and the backlog the
// number was meant to explain stayed full.
func TestStampAlreadyCorrect_CountsOnlyStampsThatLanded(t *testing.T) {
	imported := "imported"
	ok1 := &database.Book{ID: "ok-1", LibraryState: &imported}
	bad := &database.Book{ID: "bad", LibraryState: &imported}

	mockStore := mocks.NewMockStore(t)
	mockStore.On("GetBookByID", "ok-1").Return(ok1, nil)
	mockStore.On("UpdateBook", "ok-1", mock.Anything).Return(ok1, nil)
	// The failing book fails at hydrate, the realistic shape (row deleted between
	// the filter pass and the stamp).
	mockStore.On("GetBookByID", "bad").Return(nil, errors.New("row is gone"))

	svc := NewService(mockStore)
	got := svc.stampAlreadyCorrect(context.Background(),
		[]database.Book{*ok1, *bad}, "op-1", logger.New("test"))

	if got != 1 {
		t.Fatalf("stampAlreadyCorrect = %d, want 1 — a failed write must not be counted as "+
			"already-correct, or the summary claims work the database did not record", got)
	}
}

// TestStampAlreadyCorrect_EmptyInputTouchesNothing guards the early return: a
// run with nothing already-correct must not start workers or write rows. The
// mock has no expectations, so any store call fails the test.
func TestStampAlreadyCorrect_EmptyInputTouchesNothing(t *testing.T) {
	svc := NewService(mocks.NewMockStore(t))
	if got := svc.stampAlreadyCorrect(context.Background(), nil, "op-1", logger.New("test")); got != 0 {
		t.Fatalf("stampAlreadyCorrect on an empty slice = %d, want 0", got)
	}
}
