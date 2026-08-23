// file: internal/server/organize_service_test.go
// version: 1.2.1
// guid: d4e5f6a7-b8c9-d0e1-f2a3-b4c5d6e7f8a9
// last-edited: 2026-08-22

package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

func TestOrganizeService_FilterBooksNeedingOrganization(t *testing.T) {
	mockDB := &database.MockStore{}
	os := NewOrganizeService(mockDB)

	books := []database.Book{
		{ID: "1", Title: "Book 1", FilePath: "/import/book1.m4b"},
		{ID: "2", Title: "Book 2", FilePath: "/library/book2.m4b"},
	}

	testLog := logger.New("test")
	filtered, alreadyCorrect := os.FilterBooksNeedingOrganization(books, testLog)

	// Should filter out books already in library
	if len(filtered)+len(alreadyCorrect) > len(books) {
		t.Errorf("expected total filtered to not exceed input, got filtered=%d alreadyCorrect=%d", len(filtered), len(alreadyCorrect))
	}
}

func TestOrganizeService_PerformOrganize_NoBooksToOrganize(t *testing.T) {
	// PerformOrganize's non-BookIDs branch pages through GetAllBooksCore
	// (internal/organizer/service.go), never GetAllBooks — GetAllBooksFunc
	// here was inert: MockStore.GetAllBooksCore defaults to (nil, nil)
	// regardless of it, so this test used to pass vacuously.
	mockDB := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			return []database.BookCore{}, nil
		},
	}
	svc := NewOrganizeService(mockDB)

	ctx := context.Background()
	testLog := logger.New("test")
	req := &OrganizeRequest{}

	err := svc.PerformOrganize(ctx, req, testLog)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// TestOrganizeService_PerformOrganize_WithBooks is the anti-over-suppression
// twin of TestOrganizeService_PerformOrganize_NoBooksToOrganize: it feeds
// PerformOrganize a real book from GetAllBooksCore and asserts that the
// book-processing side effects it should trigger (filtering via
// GetBookFiles/GetAuthorByID, then an actual organize via CreateBook) really
// fired. Without this, a PerformOrganize that silently no-ops on every book
// would still pass every test in this file.
func TestOrganizeService_PerformOrganize_WithBooks(t *testing.T) {
	// Pin the filesystem: PerformOrganize's non-BookIDs branch reaches
	// orgSvc.organizeBooks, which does real file I/O under
	// config.AppConfig.RootDir. An unsandboxed run would write outside the
	// test. config.AppConfig is process-global and shared across every test
	// in this binary, so it must be snapshotted and restored — otherwise
	// this test leaks its RootDir into every sibling test that runs after
	// it.
	origCfg := config.AppConfig
	rootDir := t.TempDir()
	srcDir := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:              rootDir,
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
		OrganizationStrategy: "copy",
	}
	t.Cleanup(func() { config.AppConfig = origCfg })

	srcFile := filepath.Join(srcDir, "book1.mp3")
	if err := os.WriteFile(srcFile, []byte("audio-data"), 0o644); err != nil {
		t.Fatalf("setup: write source file: %v", err)
	}

	authorID := 7

	var (
		mu                  sync.Mutex
		getBookFilesCalled  bool
		getAuthorByIDCalled bool
		createBookCalled    bool
		createdBook         *database.Book
	)

	mockDB := &database.MockStore{
		// PerformOrganize pages through GetAllBooksCore in
		// fetchPageSize(1000)-book pages and stops as soon as a page comes
		// back short, so one book on the first page is enough — no
		// multi-page simulation needed.
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			if offset > 0 {
				return []database.BookCore{}, nil
			}
			return []database.BookCore{
				{
					ID:       "book-1",
					Title:    "Test Book",
					AuthorID: &authorID,
					FilePath: srcFile,
					Format:   "mp3",
				},
			}, nil
		},
		// FilterBooksNeedingOrganization relies on book_files (rather than
		// os.Stat) to decide whether a book outside RootDir is ready to
		// organize.
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			mu.Lock()
			getBookFilesCalled = true
			mu.Unlock()
			return []database.BookFile{{ID: "bf-1", BookID: bookID, FilePath: srcFile}}, nil
		},
		// The organize path resolves the author name through the store
		// (BookCore.ToBook() never populates book.Author, only AuthorID).
		GetAuthorByIDFunc: func(id int) (*database.Author, error) {
			mu.Lock()
			getAuthorByIDCalled = true
			mu.Unlock()
			return &database.Author{ID: id, Name: "Test Author"}, nil
		},
		// The book is outside RootDir, so organizeBooks routes it through
		// CreateOrganizedVersion, which persists the organized copy via
		// CreateBook.
		CreateBookFunc: func(book *database.Book) (*database.Book, error) {
			mu.Lock()
			createBookCalled = true
			createdBook = book
			mu.Unlock()
			return book, nil
		},
	}

	svc := NewOrganizeService(mockDB)
	ctx := context.Background()
	testLog := logger.New("test")
	req := &OrganizeRequest{}

	err := svc.PerformOrganize(ctx, req, testLog)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if !getBookFilesCalled {
		t.Error("expected GetBookFiles to be called while filtering the book returned by GetAllBooksCore")
	}
	if !getAuthorByIDCalled {
		t.Error("expected GetAuthorByID to be called while resolving the book's author")
	}
	if !createBookCalled || createdBook == nil {
		t.Fatal("expected CreateBook to be called with the organized copy — PerformOrganize did not actually process the book GetAllBooksCore returned")
	}
	if createdBook.FilePath == srcFile || !strings.HasPrefix(createdBook.FilePath, rootDir) {
		t.Errorf("expected the organized copy's path to be a new path under RootDir %s, got %s", rootDir, createdBook.FilePath)
	}
	if _, statErr := os.Stat(createdBook.FilePath); statErr != nil {
		t.Errorf("expected the organized copy to exist on disk at %s: %v", createdBook.FilePath, statErr)
	}
}
