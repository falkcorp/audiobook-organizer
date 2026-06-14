// file: internal/itunes/rebuild_test.go
// version: 1.0.1
// guid: 1c2d3e4f-5a6b-7c8d-9e0f-1a2b3c4d5e6f

package itunes

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// mockRebuildStore is a mock implementation of RebuildStore for testing.
type mockRebuildStore struct {
	books     map[string]*database.Book
	bookFiles map[string][]database.BookFile
}

func (m *mockRebuildStore) GetAllBooks(pageSize, offset int) ([]database.Book, error) {
	var result []database.Book
	idx := 0
	for _, book := range m.books {
		if idx >= offset && idx < offset+pageSize {
			result = append(result, *book)
		}
		idx++
	}
	return result, nil
}

func (m *mockRebuildStore) GetBookByID(id string) (*database.Book, error) {
	if book, ok := m.books[id]; ok {
		return book, nil
	}
	return nil, nil
}

func (m *mockRebuildStore) CountBooks() (int, error) {
	return len(m.books), nil
}

func (m *mockRebuildStore) GetAllBookSummaries(limit, offset int) ([]database.BookSummary, error) {
	return nil, nil
}

func (m *mockRebuildStore) GetBookByFilePath(path string) (*database.Book, error) {
	return nil, nil
}

func (m *mockRebuildStore) GetBookByITunesPersistentID(persistentID string) (*database.Book, error) {
	return nil, nil
}

func (m *mockRebuildStore) GetBookByFileHash(hash string) (*database.Book, error) {
	return nil, nil
}

func (m *mockRebuildStore) GetBookIDsByISBNASIN(isbn10, isbn13, asin string) ([]string, error) {
	return nil, nil
}

func (m *mockRebuildStore) GetBookByOriginalHash(hash string) (*database.Book, error) {
	return nil, nil
}

func (m *mockRebuildStore) GetBookByOrganizedHash(hash string) (*database.Book, error) {
	return nil, nil
}

func (m *mockRebuildStore) GetDuplicateBooks() ([][]database.Book, error) {
	return nil, nil
}

func (m *mockRebuildStore) GetFolderDuplicates() ([][]database.Book, error) {
	return nil, nil
}

func (m *mockRebuildStore) GetDuplicateBooksByMetadata(threshold float64) ([][]database.Book, error) {
	return nil, nil
}

func (m *mockRebuildStore) GetBooksByTitleInDir(normalizedTitle, dirPath string) ([]database.Book, error) {
	return nil, nil
}

func (m *mockRebuildStore) GetBooksBySeriesID(seriesID int) ([]database.Book, error) {
	return nil, nil
}

func (m *mockRebuildStore) GetBooksByAuthorID(authorID int) ([]database.Book, error) {
	return nil, nil
}

func (m *mockRebuildStore) GetBooksByVersionGroup(groupID string) ([]database.Book, error) {
	return nil, nil
}

func (m *mockRebuildStore) GetBooksByMetadataSourceHash(hash string) ([]database.Book, error) {
	return nil, nil
}

func (m *mockRebuildStore) SearchBooks(query string, limit, offset int) ([]database.Book, error) {
	return nil, nil
}

func (m *mockRebuildStore) GetDistinctGenres() ([]string, error) {
	return nil, nil
}

func (m *mockRebuildStore) GetDistinctLanguages() ([]string, error) {
	return nil, nil
}

func (m *mockRebuildStore) ListSoftDeletedBooks(limit, offset int, olderThan *time.Time) ([]database.Book, error) {
	return nil, nil
}

func (m *mockRebuildStore) GetBookSnapshots(id string, limit int) ([]database.BookSnapshot, error) {
	return nil, nil
}

func (m *mockRebuildStore) GetBookAtVersion(id string, ts time.Time) (*database.Book, error) {
	return nil, nil
}

func (m *mockRebuildStore) GetBookTombstone(id string) (*database.Book, error) {
	return nil, nil
}

func (m *mockRebuildStore) ListBookTombstones(limit int) ([]database.Book, error) {
	return nil, nil
}

func (m *mockRebuildStore) GetITunesDirtyBooks() ([]database.Book, error) {
	return nil, nil
}

func (m *mockRebuildStore) GetITunesPurgePendingBooks() ([]database.Book, error) {
	return nil, nil
}

func (m *mockRebuildStore) GetQuarantinedBooks(limit, offset int) ([]database.Book, error) {
	return nil, nil
}

func (m *mockRebuildStore) CountQuarantinedBooks() (int, error) {
	return 0, nil
}

func (m *mockRebuildStore) GetBookFiles(bookID string) ([]database.BookFile, error) {
	if files, ok := m.bookFiles[bookID]; ok {
		return files, nil
	}
	return []database.BookFile{}, nil
}

// GetAuthorByID is a no-op for the mock — tests build books with an
// inline Author pointer, so the AuthorID lookup path isn't exercised.
// Returning (nil, nil) trips the helper's "nothing found" branch
// safely (resolveAuthorName already handles a nil store gracefully).
func (m *mockRebuildStore) GetAuthorByID(id int) (*database.Author, error) {
	return nil, nil
}
func (m *mockRebuildStore) ListBookIDs() ([]string, error) { return nil, nil }
func (m *mockRebuildStore) ListBooksByITunesPID(limit, offset int) ([]database.Book, error) {
	return nil, nil
}

func TestBuildNewTrackFromBook(t *testing.T) {
	// Create a mock book
	bookID := "book-1"
	title := "Test Audiobook"
	duration := 3600 // 1 hour in seconds
	fileSize := int64(100000000)
	genre := "Science Fiction"
	narrator := "John Smith"

	book := &database.Book{
		ID:       bookID,
		Title:    title,
		FilePath: "/path/to/audiobook.m4b",
		Duration: &duration,
		FileSize: &fileSize,
		Genre:    &genre,
		Narrator: &narrator,
		Author: &database.Author{
			Name: "Author Name",
		},
	}

	store := &mockRebuildStore{
		books: map[string]*database.Book{bookID: book},
		bookFiles: map[string][]database.BookFile{
			bookID: {{
				ITunesPath: "/itunes/books/Test Audiobook.m4b",
			}},
		},
	}

	track := buildNewTrackFromBook(store, book)

	// Verify track fields
	if track.Name != title {
		t.Errorf("expected Name=%q, got %q", title, track.Name)
	}
	if track.Album != title {
		t.Errorf("expected Album=%q, got %q", title, track.Album)
	}
	if track.Artist != "Author Name" {
		t.Errorf("expected Artist=Author Name, got %q", track.Artist)
	}
	if track.Genre != genre {
		t.Errorf("expected Genre=%q, got %q", genre, track.Genre)
	}
	if track.TotalTime != duration*1000 {
		t.Errorf("expected TotalTime=%d, got %d", duration*1000, track.TotalTime)
	}
	if track.Size != int(fileSize) {
		t.Errorf("expected Size=%d, got %d", fileSize, track.Size)
	}
	if track.Location != "/itunes/books/Test Audiobook.m4b" {
		t.Errorf("expected Location from BookFile, got %q", track.Location)
	}
}

func TestResolveAuthorName(t *testing.T) {
	tests := []struct {
		name     string
		book     *database.Book
		expected string
	}{
		{
			name: "author inline",
			book: &database.Book{
				Author: &database.Author{Name: "Direct Author"},
			},
			expected: "Direct Author",
		},
		{
			name:     "no author",
			book:     &database.Book{},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resolveAuthorName(nil, tt.book)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestBuildNewTrackFromBookWithDefaults(t *testing.T) {
	// Test with minimal book data
	bookID := "minimal-book"
	title := "Minimal Audiobook"

	book := &database.Book{
		ID:       bookID,
		Title:    title,
		FilePath: "/path/to/book.m4b",
	}

	store := &mockRebuildStore{
		books: map[string]*database.Book{bookID: book},
	}

	track := buildNewTrackFromBook(store, book)

	// Verify defaults
	if track.Name != title {
		t.Errorf("expected Name=%q, got %q", title, track.Name)
	}
	if track.Genre != "Audiobook" {
		t.Errorf("expected Genre=Audiobook, got %q", track.Genre)
	}
	if track.Location != book.FilePath {
		t.Errorf("expected Location from FilePath, got %q", track.Location)
	}
	if track.TotalTime != 0 {
		t.Errorf("expected TotalTime=0 (no duration), got %d", track.TotalTime)
	}
}
