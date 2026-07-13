// file: internal/organizer/reorganize_inplace_test.go
// version: 1.0.0
// guid: 7a1c9e4b-2f6d-4a83-9e0c-5b8d3f7a1c62
// last-edited: 2026-07-13

package organizer

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/mock"
)

// ---------------------------------------------------------------------------
// Regression: ReOrganizeInPlace must not silently swallow a book_files
// UpdateBookFile failure after the physical directory move has already
// succeeded. Before the fix, the error was discarded (`_ = ...`) and no
// rescan was triggered, leaving the DB row pointing at the now-nonexistent
// old path until a manual rescan. The fix marks the book NeedsRescan so it
// self-heals on the next library scan.
// ---------------------------------------------------------------------------

func TestReOrganizeInPlace_UpdateBookFileError_MarksNeedsRescan(t *testing.T) {
	rootDir := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:             rootDir,
		FolderNamingPattern: "{author}/{title}",
		FileNamingPattern:   "{title}",
	}

	oldPath := filepath.Join(rootDir, "src")
	if err := os.MkdirAll(oldPath, 0775); err != nil {
		t.Fatalf("setup: mkdir oldPath: %v", err)
	}
	childFile := filepath.Join(oldPath, "ch1.mp3")
	if err := os.WriteFile(childFile, []byte("audio"), 0644); err != nil {
		t.Fatalf("setup: write child file: %v", err)
	}

	book := &database.Book{
		ID:       "book-1",
		Title:    "NewTitle",
		FilePath: oldPath,
		Author:   &database.Author{Name: "NewAuthor"},
	}

	mockStore := mocks.NewMockStore(t)
	mockStore.On("GetBookByID", "book-1").Return(book, nil)
	mockStore.On("UpdateBook", "book-1", mock.AnythingOfType("*database.Book")).Return(book, nil)
	mockStore.On("GetBookFiles", "book-1").Return([]database.BookFile{
		{ID: "bf-1", FilePath: childFile},
	}, nil)
	mockStore.On("UpdateBookFile", "bf-1", mock.AnythingOfType("*database.BookFile")).
		Return(fmt.Errorf("simulated db write failure"))
	mockStore.On("MarkNeedsRescan", "book-1").Return(nil)

	svc := NewService(mockStore)
	log := &noopLogger{}

	newPath, err := svc.ReOrganizeInPlace(book, log)
	if err != nil {
		t.Fatalf("ReOrganizeInPlace returned error: %v", err)
	}
	if newPath == oldPath {
		t.Fatalf("expected target path to differ from old path, got %q", newPath)
	}
	if _, statErr := os.Stat(newPath); statErr != nil {
		t.Fatalf("expected moved directory at %s: %v", newPath, statErr)
	}

	mockStore.AssertCalled(t, "MarkNeedsRescan", "book-1")
}

// ---------------------------------------------------------------------------
// Control: when UpdateBookFile succeeds, ReOrganizeInPlace must NOT mark the
// book for rescan (proves the new call is gated on the failure, not
// unconditional — a test that passed before and after the fix would prove
// nothing).
// ---------------------------------------------------------------------------

func TestReOrganizeInPlace_UpdateBookFileSuccess_NoRescan(t *testing.T) {
	rootDir := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:             rootDir,
		FolderNamingPattern: "{author}/{title}",
		FileNamingPattern:   "{title}",
	}

	oldPath := filepath.Join(rootDir, "src2")
	if err := os.MkdirAll(oldPath, 0775); err != nil {
		t.Fatalf("setup: mkdir oldPath: %v", err)
	}
	childFile := filepath.Join(oldPath, "ch1.mp3")
	if err := os.WriteFile(childFile, []byte("audio"), 0644); err != nil {
		t.Fatalf("setup: write child file: %v", err)
	}

	book := &database.Book{
		ID:       "book-2",
		Title:    "OtherTitle",
		FilePath: oldPath,
		Author:   &database.Author{Name: "OtherAuthor"},
	}

	mockStore := mocks.NewMockStore(t)
	mockStore.On("GetBookByID", "book-2").Return(book, nil)
	mockStore.On("UpdateBook", "book-2", mock.AnythingOfType("*database.Book")).Return(book, nil)
	mockStore.On("GetBookFiles", "book-2").Return([]database.BookFile{
		{ID: "bf-2", FilePath: childFile},
	}, nil)
	mockStore.On("UpdateBookFile", "bf-2", mock.AnythingOfType("*database.BookFile")).Return(nil)

	svc := NewService(mockStore)
	log := &noopLogger{}

	if _, err := svc.ReOrganizeInPlace(book, log); err != nil {
		t.Fatalf("ReOrganizeInPlace returned error: %v", err)
	}

	mockStore.AssertNotCalled(t, "MarkNeedsRescan", mock.Anything)
}
