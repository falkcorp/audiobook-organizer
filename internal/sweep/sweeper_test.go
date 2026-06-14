// file: internal/sweep/sweeper_test.go
// version: 1.0.2
// guid: b2c3d4e5-f6a7-8910-abcd-ef2345678902

package sweep

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// MockBookStore provides a minimal mock for testing sweep functions
type MockBookStore struct {
	tombstones []database.Book
	books      []database.Book
	errors     map[string]error
}

func (m *MockBookStore) ListBookTombstones(limit int) ([]database.Book, error) {
	if err, ok := m.errors["ListBookTombstones"]; ok {
		return nil, err
	}
	if limit < len(m.tombstones) {
		return m.tombstones[:limit], nil
	}
	return m.tombstones, nil
}

func (m *MockBookStore) GetBookByID(id string) (*database.Book, error) {
	if err, ok := m.errors["GetBookByID"]; ok {
		return nil, err
	}
	for i, b := range m.books {
		if b.ID == id {
			return &m.books[i], nil
		}
	}
	return nil, nil
}

func (m *MockBookStore) DeleteBookTombstone(id string) error {
	if err, ok := m.errors["DeleteBookTombstone"]; ok {
		return err
	}
	for i, t := range m.tombstones {
		if t.ID == id {
			m.tombstones = append(m.tombstones[:i], m.tombstones[i+1:]...)
			return nil
		}
	}
	return nil
}

func (m *MockBookStore) GetAllBooks(limit, offset int) ([]database.Book, error) {
	if err, ok := m.errors["GetAllBooks"]; ok {
		return nil, err
	}
	if offset >= len(m.books) {
		return []database.Book{}, nil
	}
	end := offset + limit
	if end > len(m.books) {
		end = len(m.books)
	}
	return m.books[offset:end], nil
}

// Stub out other required BookStore methods
func (m *MockBookStore) CountBooks() (int, error)                               { return 0, nil }
func (m *MockBookStore) CreateBook(book *database.Book) (*database.Book, error) { return nil, nil }
func (m *MockBookStore) UpdateBook(id string, book *database.Book) (*database.Book, error) {
	return nil, nil
}
func (m *MockBookStore) UpdateBookRating(id string, req database.UpdateBookRatingRequest) error {
	return nil
}
func (m *MockBookStore) DeleteBook(id string) error                            { return nil }
func (m *MockBookStore) SetLastWrittenAt(id string, t time.Time) error         { return nil }
func (m *MockBookStore) MarkITunesSynced(bookIDs []string) (int64, error)      { return 0, nil }
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
func (m *MockBookStore) GetBookTombstone(id string) (*database.Book, error)   { return nil, nil }
func (m *MockBookStore) GetITunesDirtyBooks() ([]database.Book, error)        { return nil, nil }
func (m *MockBookStore) GetITunesPurgePendingBooks() ([]database.Book, error) { return nil, nil }
func (m *MockBookStore) GetQuarantinedBooks(limit, offset int) ([]database.Book, error) {
	return nil, nil
}
func (m *MockBookStore) CountQuarantinedBooks() (int, error) { return 0, nil }
func (m *MockBookStore) GetAllBookSummaries(limit, offset int) ([]database.BookSummary, error) {
	return nil, nil
}
func (m *MockBookStore) RevertBookToVersion(id string, ts time.Time) (*database.Book, error) {
	return nil, nil
}
func (m *MockBookStore) PruneBookSnapshots(id string, keepCount int) (int, error) { return 0, nil }
func (m *MockBookStore) CreateBookTombstone(book *database.Book) error            { return nil }
func (m *MockBookStore) GetScanFailCount(pathHash string) (int, error)            { return 0, nil }
func (m *MockBookStore) IncrScanFailCount(pathHash string) (int, error)           { return 0, nil }
func (m *MockBookStore) ResetScanFailCount(pathHash string) error                 { return nil }
func (m *MockBookStore) MergeChapterBooks(primaryID string, srcIDs []string, commonTitle string, totalDuration float64) error {
	return nil
}
func (m *MockBookStore) FlagMetadataHashDuplicate(primaryID, duplicateID string) error { return nil }
func (m *MockBookStore) RecomputeBookAggregates(_ string) error                        { return nil }
func (m *MockBookStore) ListBookIDs() ([]string, error)                                { return nil, nil }
func (m *MockBookStore) ListBooksByITunesPID(limit, offset int) ([]database.Book, error) {
	return nil, nil
}

func TestSweepTombstones_EmptyList(t *testing.T) {
	store := &MockBookStore{
		tombstones: []database.Book{},
		books:      []database.Book{},
	}

	result, err := SweepTombstones(store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
	if result.TombstonesCleaned != 0 {
		t.Errorf("expected 0 tombstones cleaned, got %d", result.TombstonesCleaned)
	}
}

func TestSweepTombstones_RemoveOrphanedFile(t *testing.T) {
	// Create a temporary file to test deletion
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test_book.mp3")
	if err := os.WriteFile(tmpFile, []byte("test data"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(tmpFile); err != nil {
		t.Fatalf("test file should exist: %v", err)
	}

	store := &MockBookStore{
		tombstones: []database.Book{
			{
				ID:       "book1",
				FilePath: tmpFile,
			},
		},
		books: []database.Book{},
	}

	result, err := SweepTombstones(store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.TombstonesCleaned != 1 {
		t.Errorf("expected 1 tombstone cleaned, got %d", result.TombstonesCleaned)
	}

	// Verify file was deleted
	if _, err := os.Stat(tmpFile); err == nil {
		t.Fatal("file should have been deleted")
	}
}

func TestSweepTombstones_KeepLiveBook(t *testing.T) {
	store := &MockBookStore{
		tombstones: []database.Book{
			{
				ID:       "book1",
				FilePath: "/nonexistent/path.mp3",
			},
		},
		books: []database.Book{
			{
				ID:       "book1",
				Title:    "Test Book",
				FilePath: "/some/path.mp3",
			},
		},
	}

	result, err := SweepTombstones(store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.TombstonesCleaned != 1 {
		t.Errorf("expected 1 tombstone cleaned (live book removal), got %d", result.TombstonesCleaned)
	}

	if len(store.tombstones) != 0 {
		t.Errorf("tombstone should have been deleted from store, but %d remain", len(store.tombstones))
	}
}

func TestAuditFileConsistency_NoMissingFiles(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "book.mp3")
	if err := os.WriteFile(tmpFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	store := &MockBookStore{
		books: []database.Book{
			{
				ID:       "book1",
				Title:    "Existing Book",
				FilePath: tmpFile,
			},
		},
	}

	result, err := AuditFileConsistency(store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.MissingFiles) != 0 {
		t.Errorf("expected 0 missing files, got %d", len(result.MissingFiles))
	}
}

func TestAuditFileConsistency_ReportMissing(t *testing.T) {
	store := &MockBookStore{
		books: []database.Book{
			{
				ID:       "book1",
				Title:    "Missing Book",
				FilePath: "/nonexistent/path/book.mp3",
			},
			{
				ID:       "book2",
				Title:    "Another Missing",
				FilePath: "/also/does/not/exist.mp3",
			},
		},
	}

	result, err := AuditFileConsistency(store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.MissingFiles) != 2 {
		t.Errorf("expected 2 missing files, got %d", len(result.MissingFiles))
	}
}

func TestAuditFileConsistency_SkipEmptyPaths(t *testing.T) {
	store := &MockBookStore{
		books: []database.Book{
			{
				ID:       "book1",
				Title:    "Book With No Path",
				FilePath: "",
			},
		},
	}

	result, err := AuditFileConsistency(store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.MissingFiles) != 0 {
		t.Errorf("expected 0 missing files (should skip empty paths), got %d", len(result.MissingFiles))
	}
}
