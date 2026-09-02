// file: internal/merge/provisional_guard_test.go
// version: 1.1.0
// guid: 51f8f6c7-7a87-45e9-b9fa-cecc30246566
// last-edited: 2026-09-02

package merge

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
)

// A merge picks a winner and soft-deletes the losers. Doing that without a
// trustworthy file hash means deciding on title/author similarity alone, which
// this repo has measured to be wrong often enough to lose books. These tests
// pin that the guard actually REFUSES -- the happy-path tests only prove it does
// not break a merge that should succeed.

func provisionalFile(bookID string) database.BookFile {
	return database.BookFile{
		ID: bookID + "-f1", BookID: bookID, FilePath: "/tmp/" + bookID + ".m4b",
		Scan: database.ScanState{NeedsDeep: true},
	}
}

func scannedFile(bookID string) database.BookFile {
	return database.BookFile{
		ID: bookID + "-f1", BookID: bookID, FilePath: "/tmp/" + bookID + ".m4b",
		FileHash: "hash-" + bookID,
	}
}

func TestMergeBooks_RefusesAProvisionalBook(t *testing.T) {
	for _, tc := range []struct {
		name          string
		book1, book2  []database.BookFile
		wantMentioned string
	}{
		{
			name:          "the primary is provisional",
			book1:         []database.BookFile{provisionalFile("book-1")},
			book2:         []database.BookFile{scannedFile("book-2")},
			wantMentioned: "book-1",
		},
		{
			name:          "a loser is provisional",
			book1:         []database.BookFile{scannedFile("book-1")},
			book2:         []database.BookFile{provisionalFile("book-2")},
			wantMentioned: "book-2",
		},
		{
			// ANY, not ALL: one un-scanned file among many is still the file we
			// know least about, and it is exactly the one that could make this
			// pair a false match.
			name:          "one provisional file among scanned ones",
			book1:         []database.BookFile{scannedFile("book-1"), provisionalFile("book-1")},
			book2:         []database.BookFile{scannedFile("book-2")},
			wantMentioned: "book-1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mockStore := mocks.NewMockStore(t)
			svc := NewService(mockStore)

			book1 := newBook("book-1", "A", "m4b", "/tmp/a.m4b")
			book2 := newBook("book-2", "A", "m4b", "/tmp/b.m4b")
			mockStore.EXPECT().GetBookByID("book-1").Return(book1, nil)
			mockStore.EXPECT().GetBookByID("book-2").Return(book2, nil)
			mockStore.EXPECT().GetBookFiles("book-1").Return(tc.book1, nil).Maybe()
			mockStore.EXPECT().GetBookFiles("book-2").Return(tc.book2, nil).Maybe()

			_, err := svc.MergeBooks([]string{"book-1", "book-2"}, "book-1")

			require.Error(t, err, "a merge involving a provisional book must be refused")
			var provisional *ProvisionalScanError
			require.True(t, errors.As(err, &provisional), "refusal must be the typed ProvisionalScanError so handlers can 409 it: %v", err)
			assert.Equal(t, tc.wantMentioned, provisional.BookID)
			assert.Contains(t, err.Error(), "awaiting a full scan")
			assert.Contains(t, err.Error(), tc.wantMentioned,
				"the error must name WHICH book is unscanned, or the operator cannot act on it")
			// No mutation may have happened. mocks.NewMockStore(t) asserts on
			// cleanup that no unexpected call was made, and neither UpdateBook
			// nor SoftDeleteBook is stubbed here -- so any write fails the test.
		})
	}
}

// A read error means we cannot TELL whether the book is provisional. Fail closed:
// refusing a merge is recoverable, merging two different books is not.
func TestMergeBooks_FailsClosedWhenScanStateIsUnreadable(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewService(mockStore)

	book1 := newBook("book-1", "A", "m4b", "/tmp/a.m4b")
	book2 := newBook("book-2", "A", "m4b", "/tmp/b.m4b")
	mockStore.EXPECT().GetBookByID("book-1").Return(book1, nil)
	mockStore.EXPECT().GetBookByID("book-2").Return(book2, nil)
	mockStore.EXPECT().GetBookFiles("book-1").Return(nil, fmt.Errorf("pebble: io error")).Maybe()
	mockStore.EXPECT().GetBookFiles("book-2").Return(nil, fmt.Errorf("pebble: io error")).Maybe()

	_, err := svc.MergeBooks([]string{"book-1", "book-2"}, "book-1")

	require.Error(t, err, "an unreadable scan state must block the merge, not be ignored")
	assert.Contains(t, err.Error(), "cannot verify scan state")
	assert.Contains(t, err.Error(), "pebble: io error", "the underlying cause must survive")
}

// The converse, so the guard cannot be satisfied by refusing everything: a book
// whose files are all scanned merges normally.
func TestMergeBooks_AllowsFullyScannedBooks(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewService(mockStore)

	book1 := newBook("book-1", "A", "m4b", "/tmp/a.m4b")
	book2 := newBook("book-2", "A", "m4b", "/tmp/b.m4b")
	mockStore.EXPECT().GetBookByID("book-1").Return(book1, nil)
	mockStore.EXPECT().GetBookByID("book-2").Return(book2, nil)
	mockStore.EXPECT().GetBookFiles("book-1").Return([]database.BookFile{scannedFile("book-1")}, nil)
	mockStore.EXPECT().GetBookFiles("book-2").Return([]database.BookFile{scannedFile("book-2")}, nil)
	mockStore.EXPECT().UpdateBook("book-1", mock.Anything).Return(book1, nil)
	mockStore.EXPECT().UpdateBook("book-2", mock.Anything).Return(book2, nil)
	mockStore.EXPECT().GetExternalIDsForBook("book-2").Return(nil, nil)
	mockStore.EXPECT().ReassignExternalIDs("book-2", "book-1").Return(nil)
	mockStore.EXPECT().GetBookByID("book-2").Return(book2, nil)
	mockStore.EXPECT().UpdateBook("book-2", mock.Anything).Return(book2, nil)

	result, err := svc.MergeBooks([]string{"book-1", "book-2"}, "book-1")
	require.NoError(t, err, "a fully scanned pair must still merge")
	assert.Equal(t, "book-1", result.PrimaryID)
}
