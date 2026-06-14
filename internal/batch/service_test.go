// file: internal/batch/service_test.go
// version: 1.0.2
// guid: b2c3d4e5-f6a7-b8c9-0d1e-2f3a4b5c6d7e

package batch

import (
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// MockBookStore is a test double for database.BookStore
type MockBookStore struct {
	books    map[string]*database.Book
	getErr   error
	setErr   error
	delErr   error
	delCnt   int
	updCnt   int
	updateFn func(id string, book *database.Book) error
}

func NewMockBookStore() *MockBookStore {
	return &MockBookStore{
		books: make(map[string]*database.Book),
	}
}

func (m *MockBookStore) GetBookByID(id string) (*database.Book, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	book, ok := m.books[id]
	if !ok {
		return nil, nil
	}
	// Return a deep copy to avoid mutation effects
	return &database.Book{
		ID:                   book.ID,
		Title:                book.Title,
		Format:               book.Format,
		AuthorID:             book.AuthorID,
		SeriesID:             book.SeriesID,
		SeriesSequence:       book.SeriesSequence,
		VersionGroupID:       book.VersionGroupID,
		IsPrimaryVersion:     book.IsPrimaryVersion,
		Narrator:             book.Narrator,
		Publisher:            book.Publisher,
		Language:             book.Language,
		Description:          book.Description,
		AudiobookReleaseYear: book.AudiobookReleaseYear,
		MarkedForDeletion:    book.MarkedForDeletion,
		MarkedForDeletionAt:  book.MarkedForDeletionAt,
		VersionNotes:         book.VersionNotes,
		FilePath:             book.FilePath,
		LibraryState:         book.LibraryState,
	}, nil
}

func (m *MockBookStore) UpdateBook(id string, book *database.Book) (*database.Book, error) {
	if m.updateFn != nil {
		return book, m.updateFn(id, book)
	}
	if m.setErr != nil {
		return nil, m.setErr
	}
	m.updCnt++
	m.books[id] = book
	return book, nil
}

func (m *MockBookStore) DeleteBook(id string) error {
	if m.delErr != nil {
		return m.delErr
	}
	m.delCnt++
	delete(m.books, id)
	return nil
}

// Stub implementations for other BookStore methods not used by BatchService
func (m *MockBookStore) GetAllBooks(limit, offset int) ([]database.Book, error) { return nil, nil }
func (m *MockBookStore) GetAllBookSummaries(limit, offset int) ([]database.BookSummary, error) {
	return nil, nil
}
func (m *MockBookStore) GetBookByFilePath(path string) (*database.Book, error) { return nil, nil }
func (m *MockBookStore) GetBookByITunesPersistentID(persistentID string) (*database.Book, error) {
	return nil, nil
}
func (m *MockBookStore) GetBookByFileHash(hash string) (*database.Book, error) { return nil, nil }
func (m *MockBookStore) GetBookIDsByISBNASIN(isbn10, isbn13, asin string) ([]string, error) {
	return nil, nil
}
func (m *MockBookStore) GetBookByOriginalHash(hash string) (*database.Book, error)  { return nil, nil }
func (m *MockBookStore) GetBookByOrganizedHash(hash string) (*database.Book, error) { return nil, nil }
func (m *MockBookStore) GetDuplicateBooks() ([][]database.Book, error)              { return nil, nil }
func (m *MockBookStore) GetFolderDuplicates() ([][]database.Book, error)            { return nil, nil }
func (m *MockBookStore) GetDuplicateBooksByMetadata(threshold float64) ([][]database.Book, error) {
	return nil, nil
}
func (m *MockBookStore) GetBooksByTitleInDir(normalizedTitle, dirPath string) ([]database.Book, error) {
	return nil, nil
}
func (m *MockBookStore) GetBooksBySeriesID(seriesID int) ([]database.Book, error) { return nil, nil }
func (m *MockBookStore) GetBooksByAuthorID(authorID int) ([]database.Book, error) { return nil, nil }
func (m *MockBookStore) GetBooksByVersionGroup(groupID string) ([]database.Book, error) {
	return nil, nil
}
func (m *MockBookStore) GetBooksByMetadataSourceHash(hash string) ([]database.Book, error) {
	return nil, nil
}
func (m *MockBookStore) SearchBooks(query string, limit, offset int) ([]database.Book, error) {
	return nil, nil
}
func (m *MockBookStore) CountBooks() (int, error)                { return len(m.books), nil }
func (m *MockBookStore) GetDistinctGenres() ([]string, error)    { return nil, nil }
func (m *MockBookStore) GetDistinctLanguages() ([]string, error) { return nil, nil }
func (m *MockBookStore) ListSoftDeletedBooks(limit, offset int, olderThan *time.Time) ([]database.Book, error) {
	return nil, nil
}
func (m *MockBookStore) GetBookSnapshots(id string, limit int) ([]database.BookSnapshot, error) {
	return nil, nil
}
func (m *MockBookStore) GetBookAtVersion(id string, ts time.Time) (*database.Book, error) {
	return nil, nil
}
func (m *MockBookStore) GetBookTombstone(id string) (*database.Book, error)    { return nil, nil }
func (m *MockBookStore) ListBookTombstones(limit int) ([]database.Book, error) { return nil, nil }
func (m *MockBookStore) GetITunesDirtyBooks() ([]database.Book, error)         { return nil, nil }
func (m *MockBookStore) GetITunesPurgePendingBooks() ([]database.Book, error)  { return nil, nil }
func (m *MockBookStore) GetQuarantinedBooks(limit, offset int) ([]database.Book, error) {
	return nil, nil
}
func (m *MockBookStore) CountQuarantinedBooks() (int, error)                    { return 0, nil }
func (m *MockBookStore) CreateBook(book *database.Book) (*database.Book, error) { return book, nil }
func (m *MockBookStore) UpdateBookRating(id string, req database.UpdateBookRatingRequest) error {
	return nil
}
func (m *MockBookStore) SetLastWrittenAt(id string, t time.Time) error { return nil }
func (m *MockBookStore) MarkITunesSynced(bookIDs []string) (int64, error) {
	return int64(len(bookIDs)), nil
}
func (m *MockBookStore) RevertBookToVersion(id string, ts time.Time) (*database.Book, error) {
	return nil, nil
}
func (m *MockBookStore) PruneBookSnapshots(id string, keepCount int) (int, error) { return 0, nil }
func (m *MockBookStore) CreateBookTombstone(book *database.Book) error            { return nil }
func (m *MockBookStore) DeleteBookTombstone(id string) error                      { return nil }
func (m *MockBookStore) GetScanFailCount(pathHash string) (int, error)            { return 0, nil }
func (m *MockBookStore) IncrScanFailCount(pathHash string) (int, error)           { return 1, nil }
func (m *MockBookStore) ResetScanFailCount(pathHash string) error                 { return nil }
func (m *MockBookStore) MergeChapterBooks(primaryID string, srcIDs []string, commonTitle string, totalDuration float64) error {
	return nil
}
func (m *MockBookStore) GetMergeResultSummary(primaryID string) (*database.Book, error) {
	return nil, nil
}
func (m *MockBookStore) AddBookTag(id, tag string) error                               { return nil }
func (m *MockBookStore) RemoveBookTag(id, tag string) error                            { return nil }
func (m *MockBookStore) GetBookTags(id string) ([]string, error)                       { return nil, nil }
func (m *MockBookStore) GetBooksWithTag(tag string) ([]string, error)                  { return nil, nil }
func (m *MockBookStore) GetAllBookTags() ([]string, error)                             { return nil, nil }
func (m *MockBookStore) AddBookUserTag(id, tag string) error                           { return nil }
func (m *MockBookStore) RemoveBookUserTag(id, tag string) error                        { return nil }
func (m *MockBookStore) GetBookUserTags(id string) ([]string, error)                   { return nil, nil }
func (m *MockBookStore) GetBooksWithUserTag(tag string) ([]string, error)              { return nil, nil }
func (m *MockBookStore) GetAllBookUserTags() ([]string, error)                         { return nil, nil }
func (m *MockBookStore) AdjustRating(id string, delta int) (*database.Book, error)     { return nil, nil }
func (m *MockBookStore) FlagMetadataHashDuplicate(primaryID, duplicateID string) error { return nil }
func (m *MockBookStore) RecomputeBookAggregates(_ string) error                        { return nil }
func (m *MockBookStore) ListBookIDs() ([]string, error)                                { return nil, nil }
func (m *MockBookStore) ListBooksByITunesPID(limit, offset int) ([]database.Book, error) {
	return nil, nil
}

// Helper to create a test book
func testBook(id, title string) *database.Book {
	return &database.Book{
		ID:       id,
		Title:    title,
		Format:   "mp3",
		FilePath: "/test/path/" + title,
	}
}

// Test 1: UpdateAudiobooks with empty request
func TestUpdateAudiobooks_EmptyRequest(t *testing.T) {
	store := NewMockBookStore()
	bs := NewBatchService(store)

	req := &BatchUpdateRequest{
		IDs:     []string{},
		Updates: map[string]any{"title": "New Title"},
	}

	resp := bs.UpdateAudiobooks(req)

	if resp.Total != 0 {
		t.Errorf("expected Total=0, got %d", resp.Total)
	}
	if resp.Success != 0 {
		t.Errorf("expected Success=0, got %d", resp.Success)
	}
	if resp.Failed != 0 {
		t.Errorf("expected Failed=0, got %d", resp.Failed)
	}
}

// Test 2: UpdateAudiobooks with single successful update
func TestUpdateAudiobooks_SingleSuccess(t *testing.T) {
	store := NewMockBookStore()
	store.books["book1"] = testBook("book1", "Original Title")
	bs := NewBatchService(store)

	req := &BatchUpdateRequest{
		IDs:     []string{"book1"},
		Updates: map[string]any{"title": "Updated Title"},
	}

	resp := bs.UpdateAudiobooks(req)

	if resp.Total != 1 {
		t.Errorf("expected Total=1, got %d", resp.Total)
	}
	if resp.Success != 1 {
		t.Errorf("expected Success=1, got %d", resp.Success)
	}
	if resp.Failed != 0 {
		t.Errorf("expected Failed=0, got %d", resp.Failed)
	}
	if len(resp.Results) != 1 || !resp.Results[0].Success {
		t.Errorf("expected 1 successful result, got %+v", resp.Results)
	}

	// Verify the update was applied
	book, _ := store.GetBookByID("book1")
	if book.Title != "Updated Title" {
		t.Errorf("expected title 'Updated Title', got '%s'", book.Title)
	}
}

// Test 3: UpdateAudiobooks with not-found error
func TestUpdateAudiobooks_NotFound(t *testing.T) {
	store := NewMockBookStore()
	bs := NewBatchService(store)

	req := &BatchUpdateRequest{
		IDs:     []string{"nonexistent"},
		Updates: map[string]any{"title": "Updated Title"},
	}

	resp := bs.UpdateAudiobooks(req)

	if resp.Total != 1 {
		t.Errorf("expected Total=1, got %d", resp.Total)
	}
	if resp.Failed != 1 {
		t.Errorf("expected Failed=1, got %d", resp.Failed)
	}
	if resp.Success != 0 {
		t.Errorf("expected Success=0, got %d", resp.Success)
	}
	if len(resp.Results) != 1 || resp.Results[0].Error == "" {
		t.Errorf("expected error result, got %+v", resp.Results)
	}
}

// Test 4: ExecuteOperations with mixed actions
func TestExecuteOperations_MixedActions(t *testing.T) {
	store := NewMockBookStore()
	store.books["book1"] = testBook("book1", "Book 1")
	store.books["book2"] = testBook("book2", "Book 2")
	store.books["book3"] = testBook("book3", "Book 3")
	bs := NewBatchService(store)

	req := &BatchOperationsRequest{
		Operations: []BatchOperationItem{
			{
				ID:      "book1",
				Action:  "update",
				Updates: map[string]any{"title": "Updated Book 1"},
			},
			{
				ID:     "book2",
				Action: "delete",
			},
			{
				ID:     "book3",
				Action: "restore", // shouldn't affect unmarked book
			},
		},
	}

	resp := bs.ExecuteOperations(req)

	if resp.Total != 3 {
		t.Errorf("expected Total=3, got %d", resp.Total)
	}
	if resp.Success != 3 {
		t.Errorf("expected Success=3, got %d", resp.Success)
	}
	if resp.Failed != 0 {
		t.Errorf("expected Failed=0, got %d", resp.Failed)
	}

	// Verify update was applied
	book1, _ := store.GetBookByID("book1")
	if book1.Title != "Updated Book 1" {
		t.Errorf("expected book1 title 'Updated Book 1', got '%s'", book1.Title)
	}

	// Verify soft delete was applied
	book2, _ := store.GetBookByID("book2")
	if book2 == nil || book2.MarkedForDeletion == nil || !*book2.MarkedForDeletion {
		t.Errorf("expected book2 to be soft-deleted, got %+v", book2)
	}

	// Verify restore leaves book3 unmarked
	book3, _ := store.GetBookByID("book3")
	if book3 == nil || book3.MarkedForDeletion == nil || *book3.MarkedForDeletion {
		t.Errorf("expected book3 to be unmarked after restore, got %+v", book3)
	}
}

// Test 5: ExecuteOperations with hard delete
func TestExecuteOperations_HardDelete(t *testing.T) {
	store := NewMockBookStore()
	store.books["book1"] = testBook("book1", "Book 1")
	bs := NewBatchService(store)

	req := &BatchOperationsRequest{
		Operations: []BatchOperationItem{
			{
				ID:         "book1",
				Action:     "delete",
				HardDelete: true,
			},
		},
	}

	resp := bs.ExecuteOperations(req)

	if resp.Success != 1 {
		t.Errorf("expected Success=1, got %d", resp.Success)
	}

	// Verify hard delete removed the book
	book1, _ := store.GetBookByID("book1")
	if book1 != nil {
		t.Errorf("expected book1 to be deleted, but it still exists")
	}
	if store.delCnt != 1 {
		t.Errorf("expected DeleteBook to be called once, was called %d times", store.delCnt)
	}
}

// Test 6: ExecuteOperations with unknown action
func TestExecuteOperations_UnknownAction(t *testing.T) {
	store := NewMockBookStore()
	store.books["book1"] = testBook("book1", "Book 1")
	bs := NewBatchService(store)

	req := &BatchOperationsRequest{
		Operations: []BatchOperationItem{
			{
				ID:     "book1",
				Action: "unknown",
			},
		},
	}

	resp := bs.ExecuteOperations(req)

	if resp.Failed != 1 {
		t.Errorf("expected Failed=1, got %d", resp.Failed)
	}
	if resp.Success != 0 {
		t.Errorf("expected Success=0, got %d", resp.Success)
	}
	if len(resp.Results) != 1 || resp.Results[0].Error == "" {
		t.Errorf("expected error in result, got %+v", resp.Results)
	}
}

// Test 7: applyUpdates with multiple field types
func TestApplyUpdates_MultipleFields(t *testing.T) {
	book := testBook("book1", "Original")

	updates := map[string]any{
		"title":               "New Title",
		"author_id":           float64(42),
		"series_id":           float64(7),
		"narrator":            "John Doe",
		"publisher":           "Test Pub",
		"language":            "en",
		"format":              "m4b",
		"marked_for_deletion": true,
	}

	applyUpdates(book, updates)

	if book.Title != "New Title" {
		t.Errorf("expected title 'New Title', got '%s'", book.Title)
	}
	if book.AuthorID == nil || *book.AuthorID != 42 {
		t.Errorf("expected author_id 42, got %v", book.AuthorID)
	}
	if book.SeriesID == nil || *book.SeriesID != 7 {
		t.Errorf("expected series_id 7, got %v", book.SeriesID)
	}
	if book.Narrator == nil || *book.Narrator != "John Doe" {
		t.Errorf("expected narrator 'John Doe', got %v", book.Narrator)
	}
	if book.Publisher == nil || *book.Publisher != "Test Pub" {
		t.Errorf("expected publisher 'Test Pub', got %v", book.Publisher)
	}
	if book.Language == nil || *book.Language != "en" {
		t.Errorf("expected language 'en', got %v", book.Language)
	}
	if book.Format != "m4b" {
		t.Errorf("expected format 'm4b', got '%s'", book.Format)
	}
	if book.MarkedForDeletion == nil || !*book.MarkedForDeletion {
		t.Errorf("expected marked_for_deletion true, got %v", book.MarkedForDeletion)
	}
	if book.MarkedForDeletionAt == nil {
		t.Errorf("expected MarkedForDeletionAt to be set")
	}
}

// Test 8: UpdateBook error handling
func TestUpdateAudiobooks_UpdateError(t *testing.T) {
	store := NewMockBookStore()
	store.books["book1"] = testBook("book1", "Book 1")
	store.setErr = errors.New("database write failed")
	bs := NewBatchService(store)

	req := &BatchUpdateRequest{
		IDs:     []string{"book1"},
		Updates: map[string]any{"title": "New Title"},
	}

	resp := bs.UpdateAudiobooks(req)

	if resp.Failed != 1 {
		t.Errorf("expected Failed=1, got %d", resp.Failed)
	}
	if resp.Success != 0 {
		t.Errorf("expected Success=0, got %d", resp.Success)
	}
	if len(resp.Results) != 1 || resp.Results[0].Error == "" {
		t.Errorf("expected error in result, got %+v", resp.Results)
	}
}

// Test 9: ExecuteOperations delete with error
func TestExecuteOperations_DeleteError(t *testing.T) {
	store := NewMockBookStore()
	store.books["book1"] = testBook("book1", "Book 1")
	store.delErr = errors.New("cannot delete marked books")
	bs := NewBatchService(store)

	req := &BatchOperationsRequest{
		Operations: []BatchOperationItem{
			{
				ID:         "book1",
				Action:     "delete",
				HardDelete: true,
			},
		},
	}

	resp := bs.ExecuteOperations(req)

	if resp.Failed != 1 {
		t.Errorf("expected Failed=1, got %d", resp.Failed)
	}
	if resp.Success != 0 {
		t.Errorf("expected Success=0, got %d", resp.Success)
	}
}

// Test 10: UpdateAudiobooks with multiple IDs, some successful some not
func TestUpdateAudiobooks_PartialSuccess(t *testing.T) {
	store := NewMockBookStore()
	store.books["book1"] = testBook("book1", "Book 1")
	// book2 doesn't exist
	store.books["book3"] = testBook("book3", "Book 3")
	bs := NewBatchService(store)

	req := &BatchUpdateRequest{
		IDs:     []string{"book1", "book2", "book3"},
		Updates: map[string]any{"title": "Updated"},
	}

	resp := bs.UpdateAudiobooks(req)

	if resp.Total != 3 {
		t.Errorf("expected Total=3, got %d", resp.Total)
	}
	if resp.Success != 2 {
		t.Errorf("expected Success=2, got %d", resp.Success)
	}
	if resp.Failed != 1 {
		t.Errorf("expected Failed=1, got %d", resp.Failed)
	}
}

// Test 11: ExecuteOperations with soft delete and verify timestamp
func TestExecuteOperations_SoftDeleteTimestamp(t *testing.T) {
	store := NewMockBookStore()
	store.books["book1"] = testBook("book1", "Book 1")
	bs := NewBatchService(store)

	beforeTime := time.Now()
	req := &BatchOperationsRequest{
		Operations: []BatchOperationItem{
			{
				ID:     "book1",
				Action: "delete",
			},
		},
	}

	resp := bs.ExecuteOperations(req)
	afterTime := time.Now()

	if resp.Success != 1 {
		t.Errorf("expected Success=1, got %d", resp.Success)
	}

	book1, _ := store.GetBookByID("book1")
	if book1.MarkedForDeletionAt == nil {
		t.Errorf("expected MarkedForDeletionAt to be set")
	} else if book1.MarkedForDeletionAt.Before(beforeTime) || book1.MarkedForDeletionAt.After(afterTime) {
		t.Errorf("expected timestamp between %v and %v, got %v", beforeTime, afterTime, book1.MarkedForDeletionAt)
	}
}

// Test 12: applyUpdates with nil series_id to clear it
func TestApplyUpdates_ClearSeriesID(t *testing.T) {
	sid := 42
	book := &database.Book{
		ID:       "book1",
		Title:    "Test",
		SeriesID: &sid,
	}

	updates := map[string]any{"series_id": nil}
	applyUpdates(book, updates)

	if book.SeriesID != nil {
		t.Errorf("expected series_id to be nil, got %v", book.SeriesID)
	}
}
