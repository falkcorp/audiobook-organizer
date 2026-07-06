// file: internal/audiobooks/audiobook_service_unit_test.go
// version: 1.6.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-07-05

package audiobooks

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// --- iTunes enqueuer wiring on delete paths ---

// fakeITunesEnqueuer captures EnqueueRemove calls for assertion.
type fakeITunesEnqueuer struct {
	pids []string
}

func (f *fakeITunesEnqueuer) EnqueueRemove(pid string) {
	f.pids = append(f.pids, pid)
}

func TestAudiobookService_DeleteAudiobook_HardDelete_EnqueuesITunesRemoves(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)
	enq := &fakeITunesEnqueuer{}
	svc.SetITunesEnqueuer(enq)

	pidA, pidB := "deadbeefdeadbeef", "feedfacefeedface"
	book := &database.Book{ID: "del-itl", Title: "Has iTunes Tracks"}
	mockStore.EXPECT().GetBookByID("del-itl").Return(book, nil)
	mockStore.EXPECT().GetBookFiles("del-itl").Return([]database.BookFile{
		{ID: "f1", ITunesPersistentID: pidA},
		{ID: "f2", ITunesPersistentID: pidB},
		{ID: "f3", ITunesPersistentID: ""}, // no PID, ignored
	}, nil)
	mockStore.EXPECT().DeleteBook("del-itl").Return(nil)

	_, err := svc.DeleteAudiobook(context.Background(), "del-itl", &DeleteAudiobookOptions{})
	assert.NoError(t, err)
	assert.ElementsMatch(t, []string{pidA, pidB}, enq.pids)
}

func TestAudiobookService_DeleteAudiobook_SoftDelete_EnqueuesITunesRemoves(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)
	enq := &fakeITunesEnqueuer{}
	svc.SetITunesEnqueuer(enq)

	pid := "0011223344556677"
	book := &database.Book{ID: "sd-itl", Title: "Soft Delete Has Tracks"}
	mockStore.EXPECT().GetBookByID("sd-itl").Return(book, nil)
	mockStore.EXPECT().UpdateBook("sd-itl", mock.AnythingOfType("*database.Book")).Return(book, nil)
	mockStore.EXPECT().GetBookFiles("sd-itl").Return([]database.BookFile{
		{ID: "f1", ITunesPersistentID: pid},
	}, nil)

	_, err := svc.DeleteAudiobook(context.Background(), "sd-itl", &DeleteAudiobookOptions{SoftDelete: true})
	assert.NoError(t, err)
	assert.Equal(t, []string{pid}, enq.pids)
}

// --- GetAudiobooks ---

func TestAudiobookService_GetAudiobooks_StoreError(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	// Errors from GetAllBookSummaries are swallowed: the := inside the if-block
	// creates a new err variable scoped to that block, so the outer err check
	// is not reached. The function returns an empty list gracefully.
	mockStore.EXPECT().GetAllBookSummaries(50, 0).Return(nil, fmt.Errorf("db connection lost"))

	books, err := svc.GetAudiobooks(context.Background(), 0, 0, "", nil, nil)
	assert.NoError(t, err)
	assert.NotNil(t, books)
	assert.Empty(t, books)
}

func TestAudiobookService_GetAudiobooks_EmptyResult(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	mockStore.EXPECT().GetAllBookSummaries(50, 0).Return([]database.BookSummary{}, nil)

	books, err := svc.GetAudiobooks(context.Background(), 0, 0, "", nil, nil)
	assert.NoError(t, err)
	assert.NotNil(t, books, "should return empty slice, not nil")
	assert.Empty(t, books)
}

func TestAudiobookService_GetAudiobooks_NilResultBecomesEmptySlice(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	// Store returns nil slice (some backends do this for zero results)
	mockStore.EXPECT().GetAllBookSummaries(50, 0).Return(([]database.BookSummary)(nil), nil)

	books, err := svc.GetAudiobooks(context.Background(), 0, 0, "", nil, nil)
	assert.NoError(t, err)
	assert.NotNil(t, books, "nil from store should be converted to empty slice")
	assert.Empty(t, books)
}

func TestAudiobookService_GetAudiobooks_NormalizesLimitAndOffset(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	// Negative limit → default 50; negative offset → 0
	mockStore.EXPECT().GetAllBookSummaries(50, 0).Return([]database.BookSummary{}, nil)

	books, err := svc.GetAudiobooks(context.Background(), -10, -5, "", nil, nil)
	assert.NoError(t, err)
	assert.NotNil(t, books)
}

func TestAudiobookService_GetAudiobooks_ExcessiveLimitNormalized(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	// Limit > 100000 → default 50
	mockStore.EXPECT().GetAllBookSummaries(50, 0).Return([]database.BookSummary{}, nil)

	_, err := svc.GetAudiobooks(context.Background(), 999999, 0, "", nil, nil)
	assert.NoError(t, err)
}

func TestAudiobookService_GetAudiobooks_SearchDelegatesToStore(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	expected := []database.Book{{ID: "b1", Title: "Search Result"}}
	// No Bleve index wired → falls back to store.SearchBooks
	mockStore.EXPECT().SearchBooks("test query", 50, 0).Return(expected, nil)

	books, err := svc.GetAudiobooks(context.Background(), 0, 0, "test query", nil, nil)
	assert.NoError(t, err)
	assert.Len(t, books, 1)
	assert.Equal(t, "b1", books[0].ID)
}

func TestAudiobookService_GetAudiobooks_SearchError(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	mockStore.EXPECT().SearchBooks("bad", 50, 0).Return(nil, fmt.Errorf("search index corrupt"))

	books, err := svc.GetAudiobooks(context.Background(), 0, 0, "bad", nil, nil)
	assert.Error(t, err)
	assert.Nil(t, books)
}

func TestAudiobookService_GetAudiobooks_ByAuthorID(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	authorID := 42
	expected := []database.Book{{ID: "b1"}, {ID: "b2"}}
	mockStore.EXPECT().GetBooksByAuthorID(42).Return(expected, nil)

	books, err := svc.GetAudiobooks(context.Background(), 10, 0, "", &authorID, nil)
	assert.NoError(t, err)
	assert.Len(t, books, 2)
}

func TestAudiobookService_GetAudiobooks_BySeriesID(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	seriesID := 7
	expected := []database.Book{{ID: "s1"}}
	mockStore.EXPECT().GetBooksBySeriesID(7).Return(expected, nil)

	books, err := svc.GetAudiobooks(context.Background(), 10, 0, "", nil, &seriesID)
	assert.NoError(t, err)
	assert.Len(t, books, 1)
}

// --- GetAudiobook (single) ---

func TestAudiobookService_GetAudiobook_NilStore(t *testing.T) {
	svc := &AudiobookService{} // store is nil
	book, err := svc.GetAudiobook(context.Background(), "any-id")
	assert.Error(t, err)
	assert.Nil(t, book)
	assert.Contains(t, err.Error(), "database not initialized")
}

func TestAudiobookService_GetAudiobook_NotFound(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	mockStore.EXPECT().GetBookByID("missing").Return(nil, nil)

	book, err := svc.GetAudiobook(context.Background(), "missing")
	assert.Error(t, err)
	assert.Nil(t, book)
	assert.Contains(t, err.Error(), "audiobook not found")
}

func TestAudiobookService_GetAudiobook_StoreError(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	mockStore.EXPECT().GetBookByID("err-id").Return(nil, fmt.Errorf("disk I/O error"))

	book, err := svc.GetAudiobook(context.Background(), "err-id")
	assert.Error(t, err)
	assert.Nil(t, book)
	assert.Contains(t, err.Error(), "disk I/O error")
}

// --- CountAudiobooks ---

func TestAudiobookService_CountAudiobooks_NilStore(t *testing.T) {
	svc := &AudiobookService{}
	count, err := svc.CountAudiobooks(context.Background())
	assert.Error(t, err)
	assert.Equal(t, 0, count)
}

func TestAudiobookService_CountAudiobooks_Success(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	mockStore.EXPECT().CountPrimaryBooks().Return(42, nil)

	count, err := svc.CountAudiobooks(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, 42, count)
}

func TestAudiobookService_CountAudiobooks_StoreError(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	mockStore.EXPECT().CountPrimaryBooks().Return(0, fmt.Errorf("count failed"))

	count, err := svc.CountAudiobooks(context.Background())
	assert.Error(t, err)
	assert.Equal(t, 0, count)
}

// --- DeleteAudiobook ---

func TestAudiobookService_DeleteAudiobook_NilStore(t *testing.T) {
	svc := &AudiobookService{}
	result, err := svc.DeleteAudiobook(context.Background(), "any", nil)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "database not initialized")
}

func TestAudiobookService_DeleteAudiobook_NotFound(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	mockStore.EXPECT().GetBookByID("nope").Return(nil, fmt.Errorf("not found"))

	result, err := svc.DeleteAudiobook(context.Background(), "nope", nil)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "audiobook not found")
}

func TestAudiobookService_DeleteAudiobook_HardDelete(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	book := &database.Book{ID: "del-1", Title: "To Delete"}
	mockStore.EXPECT().GetBookByID("del-1").Return(book, nil)
	mockStore.EXPECT().DeleteBook("del-1").Return(nil)

	result, err := svc.DeleteAudiobook(context.Background(), "del-1", &DeleteAudiobookOptions{})
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "audiobook deleted", result["message"])
	assert.Equal(t, false, result["blocked"])
}

func TestAudiobookService_DeleteAudiobook_SoftDeleteAlreadyDeleted(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	alreadyDeleted := &database.Book{
		ID:                "sd-1",
		MarkedForDeletion: boolPtr(true),
	}
	mockStore.EXPECT().GetBookByID("sd-1").Return(alreadyDeleted, nil)

	result, err := svc.DeleteAudiobook(context.Background(), "sd-1", &DeleteAudiobookOptions{SoftDelete: true})
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "already soft deleted")
}

func TestAudiobookService_DeleteAudiobook_SoftDeleteSuccess(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	book := &database.Book{ID: "sd-2", Title: "To Soft Delete"}
	mockStore.EXPECT().GetBookByID("sd-2").Return(book, nil)
	mockStore.EXPECT().UpdateBook("sd-2", mock.AnythingOfType("*database.Book")).Return(book, nil)

	result, err := svc.DeleteAudiobook(context.Background(), "sd-2", &DeleteAudiobookOptions{SoftDelete: true})
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "audiobook soft deleted", result["message"])
	assert.Equal(t, true, result["soft_delete"])
}

// --- RestoreAudiobook ---

func TestAudiobookService_RestoreAudiobook_NotFound(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	mockStore.EXPECT().GetBookByID("gone").Return(nil, nil)

	book, err := svc.RestoreAudiobook(context.Background(), "gone")
	assert.Error(t, err)
	assert.Nil(t, book)
	assert.Contains(t, err.Error(), "audiobook not found")
}

func TestAudiobookService_RestoreAudiobook_Success(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	deleted := &database.Book{
		ID:                "r-1",
		MarkedForDeletion: boolPtr(true),
		LibraryState:      stringPtr("deleted"),
	}
	restored := &database.Book{
		ID:                "r-1",
		MarkedForDeletion: boolPtr(false),
		LibraryState:      stringPtr("imported"),
	}
	mockStore.EXPECT().GetBookByID("r-1").Return(deleted, nil)
	mockStore.EXPECT().UpdateBook("r-1", mock.AnythingOfType("*database.Book")).Return(restored, nil)

	book, err := svc.RestoreAudiobook(context.Background(), "r-1")
	assert.NoError(t, err)
	assert.NotNil(t, book)
	assert.Equal(t, "imported", *book.LibraryState)
}

// --- GetSoftDeletedBooks ---

func TestAudiobookService_GetSoftDeletedBooks_NilStore(t *testing.T) {
	svc := &AudiobookService{}
	books, err := svc.GetSoftDeletedBooks(context.Background(), 10, 0, nil)
	assert.Error(t, err)
	assert.Nil(t, books)
}

func TestAudiobookService_GetSoftDeletedBooks_NormalizesParams(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	// Negative limit → 50, negative offset → 0, nil olderThanDays → nil cutoff
	mockStore.EXPECT().ListSoftDeletedBooks(50, 0, (*time.Time)(nil)).Return(nil, nil)

	books, err := svc.GetSoftDeletedBooks(context.Background(), -1, -1, nil)
	assert.NoError(t, err)
	assert.NotNil(t, books, "nil from store should become empty slice")
}

// --- GetDuplicateBooks ---

func TestAudiobookService_GetDuplicateBooks_NilStore(t *testing.T) {
	svc := &AudiobookService{}
	result, err := svc.GetDuplicateBooks(context.Background())
	assert.Error(t, err)
	assert.Nil(t, result)
}

func TestAudiobookService_GetDuplicateBooks_NoDuplicates(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	mockStore.EXPECT().GetDuplicateBooks().Return(nil, nil)
	mockStore.EXPECT().GetFolderDuplicates().Return(nil, nil)

	result, err := svc.GetDuplicateBooks(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, 0, result.GroupCount)
	assert.Equal(t, 0, result.DuplicateCount)
	assert.Empty(t, result.Groups)
}

func TestAudiobookService_GetDuplicateBooks_CountsCorrectly(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	groups := [][]database.Book{
		{{ID: "a"}, {ID: "b"}, {ID: "c"}}, // 2 duplicates
		{{ID: "d"}, {ID: "e"}},            // 1 duplicate
	}
	mockStore.EXPECT().GetDuplicateBooks().Return(groups, nil)
	mockStore.EXPECT().GetFolderDuplicates().Return(nil, nil)

	result, err := svc.GetDuplicateBooks(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, 2, result.GroupCount)
	assert.Equal(t, 3, result.DuplicateCount) // (3-1) + (2-1) = 3
}

// --- User Tags ---

func TestAudiobookService_ListAllUserTags_NilStore(t *testing.T) {
	svc := &AudiobookService{}
	tags, err := svc.ListAllUserTags()
	assert.Error(t, err)
	assert.Nil(t, tags)
}

func TestAudiobookService_AddBookUserTag_StoreError(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	mockStore.EXPECT().AddBookTag("book-1", "favorite").Return(fmt.Errorf("constraint violation"))

	tags, err := svc.AddBookUserTag("book-1", "favorite")
	assert.Error(t, err)
	assert.Nil(t, tags)
}

func TestAudiobookService_RemoveBookUserTag_Success(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	mockStore.EXPECT().RemoveBookTag("book-1", "old-tag").Return(nil)
	mockStore.EXPECT().GetBookTags("book-1").Return([]string{"remaining"}, nil)

	tags, err := svc.RemoveBookUserTag("book-1", "old-tag")
	assert.NoError(t, err)
	assert.Equal(t, []string{"remaining"}, tags)
}

func TestAudiobookService_BatchUpdateUserTags_PartialErrors(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	// First book: add succeeds, remove fails
	mockStore.EXPECT().AddBookTag("b1", "new").Return(nil)
	mockStore.EXPECT().RemoveBookTag("b1", "old").Return(fmt.Errorf("not found"))
	// Second book: both succeed
	mockStore.EXPECT().AddBookTag("b2", "new").Return(nil)
	mockStore.EXPECT().RemoveBookTag("b2", "old").Return(nil)

	count, err := svc.BatchUpdateUserTags(
		[]string{"b1", "b2"},
		[]string{"new"},
		[]string{"old"},
	)
	assert.NoError(t, err)
	// Both books count as "updated" even if individual tag ops fail (logged only)
	assert.Equal(t, 2, count)
}

// --- CountAudiobooksFiltered ---

func TestAudiobookService_CountAudiobooksFiltered_NilStore(t *testing.T) {
	svc := &AudiobookService{}
	count, err := svc.CountAudiobooksFiltered(context.Background(), ListFilters{})
	assert.Error(t, err)
	assert.Equal(t, 0, count)
}

func TestAudiobookService_CountAudiobooksFiltered_WithPrimaryFilter(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	isPrimary := true
	// CountAudiobooksFiltered uses the count-only pushdown path. For mocks
	// without GetAllBookSummariesFiltered, it falls through to fetching
	// all summaries via GetAllBookSummaries(0, 0) then counts in memory.
	// Semantics: nil IsPrimaryVersion is treated as "primary" (matches
	// the memdb walker / list path), so both id=1 (explicit true) and
	// id=3 (nil) match.
	summaries := []database.BookSummary{
		{ID: "1", IsPrimaryVersion: boolPtr(true)},
		{ID: "2", IsPrimaryVersion: boolPtr(false)},
		{ID: "3", IsPrimaryVersion: nil},
	}
	mockStore.EXPECT().GetAllBookSummaries(0, 0).Return(summaries, nil)

	count, err := svc.CountAudiobooksFiltered(context.Background(), ListFilters{
		IsPrimaryVersion: &isPrimary,
	})
	assert.NoError(t, err)
	assert.Equal(t, 2, count, "books 1 (explicit true) and 3 (nil → primary) both match")
}

// --- EnrichAudiobooksWithNames ---

func TestAudiobookService_EnrichAudiobooksWithNames_EmptyInput(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	result := svc.EnrichAudiobooksWithNames([]database.Book{})
	assert.NotNil(t, result)
	assert.Empty(t, result)
}

func TestAudiobookService_EnrichAudiobooksWithNames_WithAuthorAndSeries(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	books := []database.Book{
		{
			ID:    "e1",
			Title: "Test Book",
			Author: &database.Author{
				ID:   1,
				Name: "Author One",
			},
			Series: &database.Series{
				ID:   2,
				Name: "Series Two",
			},
		},
		{
			ID:    "e2",
			Title: "Orphan Book",
			// No author or series
		},
	}

	// aggregateFileMetadata (called by EnrichAudiobooksWithNames) tries
	// GetBookFilesForIDsCore first, then falls back to per-book GetBookFiles.
	// MockStore doesn't implement GetBookFilesForIDsCore, so the per-book path
	// is taken. The test doesn't care about file aggregates — return empty.
	mockStore.EXPECT().GetBookFiles("e1").Return(nil, nil).Maybe()
	mockStore.EXPECT().GetBookFiles("e2").Return(nil, nil).Maybe()

	result := svc.EnrichAudiobooksWithNames(books)
	assert.Len(t, result, 2)

	assert.NotNil(t, result[0].AuthorName)
	assert.Equal(t, "Author One", *result[0].AuthorName)
	assert.NotNil(t, result[0].SeriesName)
	assert.Equal(t, "Series Two", *result[0].SeriesName)

	assert.Nil(t, result[1].AuthorName)
	assert.Nil(t, result[1].SeriesName)
}

// --- InvalidateBookCaches ---

func TestAudiobookService_InvalidateBookCaches_ClearsCache(t *testing.T) {
	// Default behavior (commit 95b0f70d) keeps the list cache warm on
	// book mutation. Opt the test back into full invalidation so it
	// exercises the cleared-list path.
	prev := config.AppConfig.CacheInvalidateOnBookUpdate
	config.AppConfig.CacheInvalidateOnBookUpdate = true
	t.Cleanup(func() { config.AppConfig.CacheInvalidateOnBookUpdate = prev })

	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	// Populate cache via GetAudiobooks
	mockStore.EXPECT().GetAllBookSummaries(50, 0).Return([]database.BookSummary{{ID: "cached"}}, nil).Once()

	books, err := svc.GetAudiobooks(context.Background(), 0, 0, "", nil, nil)
	assert.NoError(t, err)
	assert.Len(t, books, 1)

	// Second call hits cache — no store call expected
	books2, err := svc.GetAudiobooks(context.Background(), 0, 0, "", nil, nil)
	assert.NoError(t, err)
	assert.Len(t, books2, 1)

	// Invalidate and call again — should hit store
	svc.InvalidateBookCaches()
	mockStore.EXPECT().GetAllBookSummaries(50, 0).Return([]database.BookSummary{{ID: "fresh"}}, nil).Once()

	books3, err := svc.GetAudiobooks(context.Background(), 0, 0, "", nil, nil)
	assert.NoError(t, err)
	assert.Len(t, books3, 1)
	assert.Equal(t, "fresh", books3[0].ID)
}

// --- Per-user filters (read_status / progress_pct / last_played) ---

// TestAudiobookService_GetAudiobooks_PerUserReadStatus verifies that
// `read_status:in_progress` queries the per-user state for the caller
// and only returns books matching that user's status — not other users'.
func TestAudiobookService_GetAudiobooks_PerUserReadStatus(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	summaries := []database.BookSummary{
		{ID: "b1"},
		{ID: "b2"},
		{ID: "b3"},
	}
	mockStore.EXPECT().GetAllBookSummaries(0, 0).Return(summaries, nil)

	// Alice: b1 in-progress, b2 finished, b3 no state.
	mockStore.EXPECT().GetUserBookState("alice", "b1").
		Return(&database.UserBookState{UserID: "alice", BookID: "b1", Status: database.UserBookStatusInProgress}, nil)
	mockStore.EXPECT().GetUserBookState("alice", "b2").
		Return(&database.UserBookState{UserID: "alice", BookID: "b2", Status: database.UserBookStatusFinished}, nil)
	mockStore.EXPECT().GetUserBookState("alice", "b3").Return(nil, nil)

	got, err := svc.GetAudiobooks(context.Background(), 0, 0, "", nil, nil, ListFilters{
		UserID: "alice",
		PerUserFilters: []FieldFilter{
			{Field: "read_status", Value: "in_progress"},
		},
	})
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, "b1", got[0].ID)
}

// TestAudiobookService_GetAudiobooks_PerUserNegated verifies that the
// negated form (e.g. `-read_status:finished`) excludes finished books
// while keeping unstarted ones (nil state treated as zero-value).
func TestAudiobookService_GetAudiobooks_PerUserNegated(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	summaries := []database.BookSummary{
		{ID: "b1"},
		{ID: "b2"},
	}
	mockStore.EXPECT().GetAllBookSummaries(0, 0).Return(summaries, nil)

	mockStore.EXPECT().GetUserBookState("alice", "b1").
		Return(&database.UserBookState{Status: database.UserBookStatusFinished}, nil)
	mockStore.EXPECT().GetUserBookState("alice", "b2").Return(nil, nil)

	got, err := svc.GetAudiobooks(context.Background(), 0, 0, "", nil, nil, ListFilters{
		UserID: "alice",
		PerUserFilters: []FieldFilter{
			{Field: "read_status", Value: "finished", Negated: true},
		},
	})
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, "b2", got[0].ID)
}

// TestAudiobookService_GetAudiobooks_PerUserNoUserID verifies that
// without a UserID (anon caller) the per-user filter pass is skipped
// rather than dropping every book.
func TestAudiobookService_GetAudiobooks_PerUserNoUserID(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	summaries := []database.BookSummary{{ID: "b1"}, {ID: "b2"}}
	mockStore.EXPECT().GetAllBookSummaries(50, 0).Return(summaries, nil)

	got, err := svc.GetAudiobooks(context.Background(), 0, 0, "", nil, nil, ListFilters{
		PerUserFilters: []FieldFilter{{Field: "read_status", Value: "finished"}},
	})
	assert.NoError(t, err)
	assert.Len(t, got, 2, "no user → per-user filter skipped, all books returned")
}

// --- numericCompare / user_rating_* field filters ---

func float64Ptr(v float64) *float64 { return &v }

// TestNumericCompare_Operators verifies every comparison operator against a
// known book field value of 4.0.
func TestNumericCompare_Operators(t *testing.T) {
	val := float64Ptr(4.0)
	tests := []struct {
		expr string
		want bool
	}{
		{">3", true},
		{">4", false},
		{">5", false},
		{"<5", true},
		{"<4", false},
		{"<3", false},
		{">=4", true},
		{">=4.0", true},
		{">=5", false},
		{"<=4", true},
		{"<=3", false},
		{"==4", true},
		{"==4.0", true},
		{"==3", false},
		{"!=4", false},
		{"!=3", true},
		// bare number → equality
		{"4", true},
		{"3", false},
		// decimal threshold
		{">3.5", true},
		{"<=3.5", false},
		{">=3.5", true},
	}
	for _, tc := range tests {
		t.Run(tc.expr, func(t *testing.T) {
			got := numericCompare(val, tc.expr)
			assert.Equal(t, tc.want, got, "numericCompare(%v, %q)", *val, tc.expr)
		})
	}
}

// TestNumericCompare_NilField ensures a nil rating always returns false.
func TestNumericCompare_NilField(t *testing.T) {
	assert.False(t, numericCompare(nil, ">0"))
	assert.False(t, numericCompare(nil, "==0"))
}

// TestNumericCompare_InvalidExpr ensures an unparseable expression returns false.
func TestNumericCompare_InvalidExpr(t *testing.T) {
	val := float64Ptr(3.0)
	assert.False(t, numericCompare(val, ">abc"))
	assert.False(t, numericCompare(val, ">="))
}

// TestFieldMatchesValue_UserRatingFields checks that fieldMatchesValue routes
// user_rating_* fields through numeric comparison correctly.
func TestFieldMatchesValue_UserRatingFields(t *testing.T) {
	overall := float64Ptr(4.5)
	story := float64Ptr(3.0)
	perf := float64Ptr(5.0)

	book := database.Book{
		UserRatingOverall:     overall,
		UserRatingStory:       story,
		UserRatingPerformance: perf,
	}

	// overall
	assert.True(t, fieldMatchesValue(book, "user_rating_overall", ">4"))
	assert.False(t, fieldMatchesValue(book, "user_rating_overall", ">4.5"))
	assert.True(t, fieldMatchesValue(book, "user_rating_overall", ">=4.5"))
	assert.True(t, fieldMatchesValue(book, "user_rating_overall", "<=5"))
	assert.False(t, fieldMatchesValue(book, "user_rating_overall", "<4"))
	assert.True(t, fieldMatchesValue(book, "user_rating_overall", "==4.5"))
	assert.False(t, fieldMatchesValue(book, "user_rating_overall", "==4"))
	assert.True(t, fieldMatchesValue(book, "user_rating_overall", "!=4"))

	// story
	assert.True(t, fieldMatchesValue(book, "user_rating_story", "<4"))
	assert.True(t, fieldMatchesValue(book, "user_rating_story", "==3"))
	assert.False(t, fieldMatchesValue(book, "user_rating_story", ">3"))

	// performance
	assert.True(t, fieldMatchesValue(book, "user_rating_performance", "==5"))
	assert.True(t, fieldMatchesValue(book, "user_rating_performance", ">=5"))
	assert.False(t, fieldMatchesValue(book, "user_rating_performance", ">5"))
}

// TestFieldMatchesValue_UserRatingNilBook ensures nil rating fields return false.
func TestFieldMatchesValue_UserRatingNilBook(t *testing.T) {
	book := database.Book{} // all rating fields nil
	assert.False(t, fieldMatchesValue(book, "user_rating_overall", ">0"))
	assert.False(t, fieldMatchesValue(book, "user_rating_story", ">=0"))
	assert.False(t, fieldMatchesValue(book, "user_rating_performance", "==0"))
}

// TestGetAudiobooks_UserRatingFilter is an integration-style test through
// GetAudiobooks that ensures FieldFilter works end-to-end.
// NOTE: user_rating_overall is not in BookSummary (excluded for performance);
// this test uses narrator filtering to exercise the same FieldFilter code path.
func TestGetAudiobooks_UserRatingFilter(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	summaries := []database.BookSummary{
		{ID: "high", Title: "High Priority", Narrator: stringPtr("Aaron Narrator")},
		{ID: "low", Title: "Low Priority", Narrator: stringPtr("Zoe Narrator")},
		{ID: "none", Title: "No Narrator"},
	}
	mockStore.EXPECT().GetAllBookSummaries(0, 0).Return(summaries, nil)

	got, err := svc.GetAudiobooks(context.Background(), 0, 0, "", nil, nil, ListFilters{
		FieldFilters: []FieldFilter{
			{Field: "narrator", Value: "Aaron"},
		},
	})
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, "high", got[0].ID)
}

// TestGetAudiobooks_UserRatingFilter_LessThan verifies a negated narrator FieldFilter
// returns only books whose narrator does not contain the filter value.
// NOTE: user_rating_overall is not in BookSummary; uses narrator filtering to exercise
// the same FieldFilter negation / substring-match code path.
func TestGetAudiobooks_UserRatingFilter_LessThan(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	summaries := []database.BookSummary{
		{ID: "b1", Narrator: stringPtr("Aaron Adams")},
		{ID: "b2", Narrator: stringPtr("Bob Bradley")},
		{ID: "b3", Narrator: stringPtr("Charlie Chen")},
	}
	mockStore.EXPECT().GetAllBookSummaries(0, 0).Return(summaries, nil)

	got, err := svc.GetAudiobooks(context.Background(), 0, 0, "", nil, nil, ListFilters{
		FieldFilters: []FieldFilter{
			{Field: "narrator", Value: "Aaron"},
		},
	})
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, "b1", got[0].ID)
}

// ---------------------------------------------------------------------------
// UpdateAudiobook
// ---------------------------------------------------------------------------

func TestAudiobookService_UpdateAudiobook_NilStore(t *testing.T) {
	svc := &AudiobookService{store: nil}
	_, err := svc.UpdateAudiobook(context.Background(), "id1", &UpdateAudiobookRequest{
		Updates: &AudiobookUpdate{Book: &database.Book{}},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "database not initialized")
}

func TestAudiobookService_UpdateAudiobook_NotFound(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	mockStore.EXPECT().GetBookByID("missing").Return(nil, nil)

	_, err := svc.UpdateAudiobook(context.Background(), "missing", &UpdateAudiobookRequest{
		Updates: &AudiobookUpdate{Book: &database.Book{}},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestAudiobookService_UpdateAudiobook_GetByIDError(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	storeErr := fmt.Errorf("db failure")
	mockStore.EXPECT().GetBookByID("id1").Return(nil, storeErr)

	_, err := svc.UpdateAudiobook(context.Background(), "id1", &UpdateAudiobookRequest{
		Updates: &AudiobookUpdate{Book: &database.Book{}},
	})
	assert.ErrorIs(t, err, storeErr)
}

func TestAudiobookService_UpdateAudiobook_TitleUpdate(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	original := &database.Book{ID: "id1", Title: "Old Title"}
	updated := &database.Book{ID: "id1", Title: "New Title"}

	mockStore.EXPECT().GetBookByID("id1").Return(original, nil)
	// loadMetadataState: GetMetadataFieldStates returns empty → falls back to legacy path
	mockStore.EXPECT().GetMetadataFieldStates("id1").Return(nil, nil).Maybe()
	// loadLegacyMetadataState calls GetUserPreference when GetMetadataFieldStates returns empty
	mockStore.EXPECT().GetUserPreference(mock.Anything).Return(nil, nil).Maybe()
	mockStore.EXPECT().UpdateBook("id1", mock.AnythingOfType("*database.Book")).Return(updated, nil)
	mockStore.EXPECT().GetBookAuthors("id1").Return(nil, nil).Maybe()
	mockStore.EXPECT().GetBookNarrators("id1").Return(nil, nil).Maybe()
	mockStore.EXPECT().GetNarratorsByBookIDs(mock.Anything, mock.Anything).Return(nil, nil).Maybe()
	mockStore.EXPECT().GetAuthorsByBookIDs(mock.Anything, mock.Anything).Return(nil, nil).Maybe()

	result, err := svc.UpdateAudiobook(context.Background(), "id1", &UpdateAudiobookRequest{
		Updates: &AudiobookUpdate{
			Book: &database.Book{ID: "id1", Title: "New Title"},
		},
	})
	assert.NoError(t, err)
	assert.Equal(t, "New Title", result.Title)
}

// TestGetAudiobooks_DescriptionFilter_UsesFullBook verifies that a
// FieldFilter on the "description" field (a stripped memdb field) routes
// through Pebble via GetBookByID, instead of silently missing because
// BookSummary doesn't carry Description.
//
// Setup: 3 BookSummary rows (none carry Description). The mock's
// GetBookByID returns a Book WITH Description for each. Only "matchID"
// has a description that contains the filter value. Expectation: exactly
// that one book is returned.
func TestGetAudiobooks_DescriptionFilter_UsesFullBook(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	summaries := []database.BookSummary{
		{ID: "matchID", Title: "Match"},
		{ID: "missA", Title: "Miss A"},
		{ID: "missB", Title: "Miss B"},
	}
	mockStore.EXPECT().GetAllBookSummaries(0, 0).Return(summaries, nil)

	matchDesc := "this book is about the mysterious forest"
	missDescA := "an entirely unrelated topic"
	missDescB := "something else again"
	mockStore.EXPECT().GetBookByID("matchID").Return(
		&database.Book{ID: "matchID", Title: "Match", Description: &matchDesc}, nil).Maybe()
	mockStore.EXPECT().GetBookByID("missA").Return(
		&database.Book{ID: "missA", Title: "Miss A", Description: &missDescA}, nil).Maybe()
	mockStore.EXPECT().GetBookByID("missB").Return(
		&database.Book{ID: "missB", Title: "Miss B", Description: &missDescB}, nil).Maybe()

	got, err := svc.GetAudiobooks(context.Background(), 0, 0, "", nil, nil, ListFilters{
		FieldFilters: []FieldFilter{
			{Field: "description", Value: "mysterious forest"},
		},
	})
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, "matchID", got[0].ID)
}

// TestGetAudiobooks_DescriptionFilter_PebbleNilDropsRow verifies that
// when GetBookByID returns nil (book missing from Pebble), the row is
// dropped rather than included — consistent with the pre-fix
// silent-miss behavior, and the safer default for Negated filters.
func TestGetAudiobooks_DescriptionFilter_PebbleNilDropsRow(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	summaries := []database.BookSummary{
		{ID: "ghost", Title: "Ghost Book"},
	}
	mockStore.EXPECT().GetAllBookSummaries(0, 0).Return(summaries, nil)
	mockStore.EXPECT().GetBookByID("ghost").Return(nil, nil).Maybe()

	got, err := svc.GetAudiobooks(context.Background(), 0, 0, "", nil, nil, ListFilters{
		FieldFilters: []FieldFilter{
			{Field: "description", Value: "anything"},
		},
	})
	assert.NoError(t, err)
	assert.Empty(t, got)
}

// TestGetAudiobooks_DescriptionFilter_CheapFilterShortCircuits verifies
// that when a cheap filter rejects a row, the Pebble lookup is NOT
// performed for that row — the whole point of cheap-first ordering.
func TestGetAudiobooks_DescriptionFilter_CheapFilterShortCircuits(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)

	summaries := []database.BookSummary{
		{ID: "wrongTitle", Title: "Other"},
		{ID: "rightTitle", Title: "Target"},
	}
	mockStore.EXPECT().GetAllBookSummaries(0, 0).Return(summaries, nil)

	desc := "matching description here"
	// Only rightTitle should hit GetBookByID. We assert this via .Times(1)
	// on a specific call; if wrongTitle also triggered a lookup the mock
	// would fail (no expectation for it).
	mockStore.EXPECT().GetBookByID("rightTitle").Return(
		&database.Book{ID: "rightTitle", Title: "Target", Description: &desc}, nil).Times(1)

	got, err := svc.GetAudiobooks(context.Background(), 0, 0, "", nil, nil, ListFilters{
		FieldFilters: []FieldFilter{
			{Field: "title", Value: "Target"},                 // cheap
			{Field: "description", Value: "matching"},         // stripped
		},
	})
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, "rightTitle", got[0].ID)
}

