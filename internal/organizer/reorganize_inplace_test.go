// file: internal/organizer/reorganize_inplace_test.go
// version: 1.1.0
// guid: 7a1c9e4b-2f6d-4a83-9e0c-5b8d3f7a1c62
// last-edited: 2026-09-05

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

// TestReOrganizeInPlace_SingleFile_UpdatesBookFileRow pins the download-breakage
// fix: a SINGLE-FILE book that moves must have its book_file row repointed to the
// new path. Before 2026-09-05 the row update ran only for directory books, so the
// row kept the pre-move path and the download/stream endpoint 404'd on every
// single-file book an apply had renamed.
func TestReOrganizeInPlace_SingleFile_UpdatesBookFileRow(t *testing.T) {
	rootDir := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:             rootDir,
		FolderNamingPattern: "{author}/{title}",
		FileNamingPattern:   "{title}",
	}

	// A real single audio file living at a "wrong" path under the library root.
	oldDir := filepath.Join(rootDir, "Old Folder")
	if err := os.MkdirAll(oldDir, 0775); err != nil {
		t.Fatalf("setup: mkdir: %v", err)
	}
	oldPath := filepath.Join(oldDir, "old.mp3")
	if err := os.WriteFile(oldPath, []byte("audio"), 0644); err != nil {
		t.Fatalf("setup: write file: %v", err)
	}

	book := &database.Book{
		ID:       "book-sf",
		Title:    "NewTitle",
		FilePath: oldPath,
		Author:   &database.Author{Name: "NewAuthor"},
	}

	var wroteRow *database.BookFile
	mockStore := mocks.NewMockStore(t)
	mockStore.On("GetBookByID", "book-sf").Return(book, nil)
	mockStore.On("UpdateBook", "book-sf", mock.AnythingOfType("*database.Book")).Return(book, nil)
	mockStore.On("GetBookFiles", "book-sf").Return([]database.BookFile{
		{ID: "bf-sf", FilePath: oldPath},
	}, nil)
	mockStore.On("UpdateBookFile", "bf-sf", mock.AnythingOfType("*database.BookFile")).
		Run(func(args mock.Arguments) { wroteRow = args.Get(1).(*database.BookFile) }).
		Return(nil)

	svc := NewService(mockStore)
	log := &noopLogger{}

	newPath, err := svc.ReOrganizeInPlace(book, log)
	if err != nil {
		t.Fatalf("ReOrganizeInPlace: %v", err)
	}
	if newPath == oldPath {
		t.Fatalf("expected the book to move; target == source == %s", oldPath)
	}
	if wroteRow == nil {
		t.Fatal("UpdateBookFile was never called — the single-file row was left pointing at the old path")
	}
	if wroteRow.FilePath != newPath {
		t.Fatalf("row repointed to %q, want the moved-to path %q", wroteRow.FilePath, newPath)
	}
	// The bytes must actually be at the path the row now names.
	if _, statErr := os.Stat(wroteRow.FilePath); statErr != nil {
		t.Fatalf("row points at %q but nothing is there: %v", wroteRow.FilePath, statErr)
	}
	mockStore.AssertNotCalled(t, "MarkNeedsRescan", mock.Anything)
}

// TestReOrganizeInPlace_DirectoryPrefix_DoesNotRewriteSibling pins the separator
// in the directory prefix match: a book at ".../Book 2" must not drag a sibling
// row at ".../Book 20/..." along with it. Without the trailing separator,
// HasPrefix(".../Book 20/x", ".../Book 2") is true and the sibling's path is
// corrupted.
func TestReOrganizeInPlace_DirectoryPrefix_DoesNotRewriteSibling(t *testing.T) {
	rootDir := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:             rootDir,
		FolderNamingPattern: "{author}/{title}",
		FileNamingPattern:   "{title}",
	}

	oldPath := filepath.Join(rootDir, "Book 2") // the directory book that moves
	if err := os.MkdirAll(oldPath, 0775); err != nil {
		t.Fatalf("setup: mkdir: %v", err)
	}
	child := filepath.Join(oldPath, "ch1.mp3")
	if err := os.WriteFile(child, []byte("audio"), 0644); err != nil {
		t.Fatalf("setup: write child: %v", err)
	}
	sibling := filepath.Join(rootDir, "Book 20", "ch1.mp3") // shares the "Book 2" prefix

	book := &database.Book{
		ID:       "book-dir",
		Title:    "NewTitle",
		FilePath: oldPath,
		Author:   &database.Author{Name: "NewAuthor"},
	}

	var updated []string
	mockStore := mocks.NewMockStore(t)
	mockStore.On("GetBookByID", "book-dir").Return(book, nil)
	mockStore.On("UpdateBook", "book-dir", mock.AnythingOfType("*database.Book")).Return(book, nil)
	mockStore.On("GetBookFiles", "book-dir").Return([]database.BookFile{
		{ID: "child", FilePath: child},
		{ID: "sibling", FilePath: sibling},
	}, nil)
	mockStore.On("UpdateBookFile", mock.Anything, mock.AnythingOfType("*database.BookFile")).
		Run(func(args mock.Arguments) { updated = append(updated, args.Get(0).(string)) }).
		Return(nil)

	svc := NewService(mockStore)
	if _, err := svc.ReOrganizeInPlace(book, &noopLogger{}); err != nil {
		t.Fatalf("ReOrganizeInPlace: %v", err)
	}
	for _, id := range updated {
		if id == "sibling" {
			t.Fatalf("the sibling row ('Book 20') was rewritten by a 'Book 2' move — prefix separator missing")
		}
	}
	if len(updated) != 1 || updated[0] != "child" {
		t.Fatalf("expected only the child row rewritten, got %v", updated)
	}
}
