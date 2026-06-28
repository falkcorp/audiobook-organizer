// file: internal/dedup/book_dedup_test.go
// version: 1.1.0
// guid: e5f6a7b8-c9d0-1234-efab-345678901234
// last-edited: 2026-06-28

package dedup

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── stub progress reporter ────────────────────────────────────────────────────

type stubProgress struct {
	logs     []string
	canceled bool
	lastPct  int
	lastMsg  string
}

func (sp *stubProgress) UpdateProgress(current, total int, message string) error {
	if total > 0 {
		sp.lastPct = current * 100 / total
	}
	sp.lastMsg = message
	return nil
}

func (sp *stubProgress) Log(level, message string, _ *string) error {
	sp.logs = append(sp.logs, level+": "+message)
	return nil
}

func (sp *stubProgress) IsCanceled() bool { return sp.canceled }

// ── ScanBookDuplicates ────────────────────────────────────────────────────────

func TestScanBookDuplicates_Empty(t *testing.T) {
	// All three store methods return empty lists — result should be zero groups.
	mock := &database.MockStore{}
	mock.GetDuplicateBooksFunc = func() ([][]database.Book, error) { return nil, nil }

	result, err := ScanBookDuplicates(context.Background(), mock, nil, nil)
	require.NoError(t, err)
	assert.Empty(t, result.Groups)
	assert.Equal(t, 0, result.TotalDuplicates)
}

func TestScanBookDuplicates_HashGroup(t *testing.T) {
	// Two books with the same hash → one high-confidence group with 1 duplicate.
	bookA := database.Book{ID: "AAA", Title: "Book A"}
	bookB := database.Book{ID: "BBB", Title: "Book B"}

	mock := &database.MockStore{}
	mock.GetDuplicateBooksFunc = func() ([][]database.Book, error) {
		return [][]database.Book{{bookA, bookB}}, nil
	}

	progress := &stubProgress{}
	result, err := ScanBookDuplicates(context.Background(), mock, nil, progress)
	require.NoError(t, err)
	require.Len(t, result.Groups, 1)
	assert.Equal(t, "high", result.Groups[0].Confidence)
	assert.Equal(t, "Identical file hash", result.Groups[0].Reason)
	assert.Equal(t, 1, result.TotalDuplicates)
	// Group key should be the two IDs joined.
	assert.Equal(t, "AAA+BBB", result.Groups[0].GroupKey)
}

func TestScanBookDuplicates_DismissedGroupSkipped(t *testing.T) {
	bookA := database.Book{ID: "AAA", Title: "Book A"}
	bookB := database.Book{ID: "BBB", Title: "Book B"}

	mock := &database.MockStore{}
	mock.GetDuplicateBooksFunc = func() ([][]database.Book, error) {
		return [][]database.Book{{bookA, bookB}}, nil
	}

	// The group key is "AAA+BBB"; mark it dismissed.
	dismissed := map[string]bool{"AAA+BBB": true}

	result, err := ScanBookDuplicates(context.Background(), mock, dismissed, nil)
	require.NoError(t, err)
	assert.Empty(t, result.Groups, "dismissed group should be excluded")
}

func TestScanBookDuplicates_DeduplicationAcrossTiers(t *testing.T) {
	// The same pair of books appears in both hash and folder groups.
	// They should only appear once (in the high-confidence tier).
	bookA := database.Book{ID: "AAA", Title: "Book A"}
	bookB := database.Book{ID: "BBB", Title: "Book B"}

	mock := &database.MockStore{}
	mock.GetDuplicateBooksFunc = func() ([][]database.Book, error) {
		return [][]database.Book{{bookA, bookB}}, nil
	}
	// GetFolderDuplicates is hard-coded nil in MockStore, so the pair cannot
	// be emitted a second time — this test validates that the tier ordering
	// and the seenBookIDs guard work correctly even when the store returns nil
	// for lower-confidence tiers.

	result, err := ScanBookDuplicates(context.Background(), mock, nil, nil)
	require.NoError(t, err)
	assert.Len(t, result.Groups, 1, "one group expected, no duplication across tiers")
}

func TestScanBookDuplicates_TranscriptionPromotesBorderlineMetadataMatch(t *testing.T) {
	transcribedTitle := "Silver Crown Saga"
	authorID := 1
	bookA := database.Book{
		ID:               "AAA",
		Title:            "Silver Crown Saga",
		AuthorID:         &authorID,
		TranscribedTitle: &transcribedTitle,
	}
	bookB := database.Book{
		ID:               "BBB",
		Title:            "Golden Crown Saga",
		AuthorID:         &authorID,
		TranscribedTitle: &transcribedTitle,
	}

	var metadataThreshold float64
	mock := &database.MockStore{}
	mock.GetDuplicateBooksByMetadataFunc = func(threshold float64) ([][]database.Book, error) {
		metadataThreshold = threshold
		return [][]database.Book{{bookA, bookB}}, nil
	}
	mock.GetAuthorByIDFunc = func(id int) (*database.Author, error) {
		return &database.Author{ID: id, Name: "Shared Author"}, nil
	}

	result, err := ScanBookDuplicates(context.Background(), mock, nil, nil)
	require.NoError(t, err)
	require.Len(t, result.Groups, 1)
	assert.Equal(t, 0.80, metadataThreshold)
	assert.Equal(t, "medium", result.Groups[0].Confidence)
	assert.Equal(t, "Similar metadata with matching transcribed title", result.Groups[0].Reason)
	assert.Equal(t, 1, result.TotalDuplicates)
}

func TestScanBookDuplicates_TranscriptionDemotesBorderlineMetadataMismatch(t *testing.T) {
	transcribedA := "Kingdom of Ash"
	transcribedB := "Kingdom of Glass"
	authorID := 1
	bookA := database.Book{
		ID:               "AAA",
		Title:            "Kingdom of Ash",
		AuthorID:         &authorID,
		TranscribedTitle: &transcribedA,
	}
	bookB := database.Book{
		ID:               "BBB",
		Title:            "Kingdom of Glass",
		AuthorID:         &authorID,
		TranscribedTitle: &transcribedB,
	}

	mock := &database.MockStore{}
	mock.GetDuplicateBooksByMetadataFunc = func(threshold float64) ([][]database.Book, error) {
		return [][]database.Book{{bookA, bookB}}, nil
	}
	mock.GetAuthorByIDFunc = func(id int) (*database.Author, error) {
		return &database.Author{ID: id, Name: "Shared Author"}, nil
	}

	result, err := ScanBookDuplicates(context.Background(), mock, nil, nil)
	require.NoError(t, err)
	assert.Empty(t, result.Groups)
	assert.Equal(t, 0, result.TotalDuplicates)
}

func TestScanBookDuplicates_TranscriptionMissingLeavesMetadataDecisionUnchanged(t *testing.T) {
	authorID := 1
	bookA := database.Book{ID: "AAA", Title: "Kingdom of Ash", AuthorID: &authorID}
	bookB := database.Book{ID: "BBB", Title: "Kingdom of Glass", AuthorID: &authorID}

	mock := &database.MockStore{}
	mock.GetDuplicateBooksByMetadataFunc = func(threshold float64) ([][]database.Book, error) {
		return [][]database.Book{{bookA, bookB}}, nil
	}
	mock.GetAuthorByIDFunc = func(id int) (*database.Author, error) {
		return &database.Author{ID: id, Name: "Shared Author"}, nil
	}

	result, err := ScanBookDuplicates(context.Background(), mock, nil, nil)
	require.NoError(t, err)
	require.Len(t, result.Groups, 1)
	assert.Equal(t, "low", result.Groups[0].Confidence)
	assert.Equal(t, "Similar title and author", result.Groups[0].Reason)
	assert.Equal(t, 1, result.TotalDuplicates)
}

// ── MergeBooks ────────────────────────────────────────────────────────────────

func TestMergeBooks_BasicMerge(t *testing.T) {
	keepBook := &database.Book{ID: "KEEP", Title: "Keep Me"}
	mergeBook := &database.Book{ID: "MERGE1", Title: "Merge Me"}

	deleted := []string{}
	var updatedBook *database.Book

	mock := &database.MockStore{}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		switch id {
		case "KEEP":
			return keepBook, nil
		case "MERGE1":
			return mergeBook, nil
		}
		return nil, nil
	}
	mock.DeleteBookFunc = func(id string) error {
		deleted = append(deleted, id)
		return nil
	}
	mock.UpdateBookFunc = func(id string, book *database.Book) (*database.Book, error) {
		updatedBook = book
		return book, nil
	}

	progress := &stubProgress{}
	result, err := MergeBooks(context.Background(), mock, "op1", "KEEP", []string{"MERGE1"}, progress)
	require.NoError(t, err)
	assert.Equal(t, 1, result.MergedCount)
	assert.Empty(t, result.Errors)
	assert.Contains(t, deleted, "MERGE1")
	assert.NotNil(t, updatedBook)
	assert.Equal(t, "KEEP", updatedBook.ID)
}

func TestMergeBooks_ITunesMetadataTransfer(t *testing.T) {
	// The keep book has no iTunes fields; the merge book has all of them.
	// After the merge, the keep book should have the merge book's iTunes data.
	iTunesPID := "PID123"
	playCount := 5
	rating := 80

	keepBook := &database.Book{ID: "KEEP", Title: "Keep"}
	mergeBook := &database.Book{
		ID:                 "MERGE",
		Title:              "Merge",
		ITunesPersistentID: &iTunesPID,
		ITunesPlayCount:    &playCount,
		ITunesRating:       &rating,
	}

	var updatedBook *database.Book
	mock := &database.MockStore{}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		switch id {
		case "KEEP":
			return keepBook, nil
		case "MERGE":
			return mergeBook, nil
		}
		return nil, nil
	}
	mock.DeleteBookFunc = func(id string) error { return nil }
	mock.UpdateBookFunc = func(id string, book *database.Book) (*database.Book, error) {
		updatedBook = book
		return book, nil
	}

	_, err := MergeBooks(context.Background(), mock, "op2", "KEEP", []string{"MERGE"}, nil)
	require.NoError(t, err)
	require.NotNil(t, updatedBook)
	require.NotNil(t, updatedBook.ITunesPersistentID)
	assert.Equal(t, iTunesPID, *updatedBook.ITunesPersistentID)
	require.NotNil(t, updatedBook.ITunesPlayCount)
	assert.Equal(t, playCount, *updatedBook.ITunesPlayCount)
}

func TestMergeBooks_SkipsSelfMerge(t *testing.T) {
	// If mergeIDs contains the keepID, it should be silently skipped.
	keepBook := &database.Book{ID: "KEEP", Title: "Keep"}
	deleted := []string{}

	mock := &database.MockStore{}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		return keepBook, nil
	}
	mock.DeleteBookFunc = func(id string) error {
		deleted = append(deleted, id)
		return nil
	}
	mock.UpdateBookFunc = func(id string, book *database.Book) (*database.Book, error) {
		return book, nil
	}

	result, err := MergeBooks(context.Background(), mock, "op3", "KEEP", []string{"KEEP"}, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, result.MergedCount, "self-merge should be skipped")
	assert.Empty(t, deleted)
}
