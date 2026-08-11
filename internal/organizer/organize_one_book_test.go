// file: internal/organizer/organize_one_book_test.go
// version: 1.0.0
// guid: 6e2a91d4-3c58-4b7f-8a06-1d94f2e7b350
// last-edited: 2026-08-11

package organizer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// ---------------------------------------------------------------------------
// Regression: the post-scan auto-organize hook (server.AutoOrganizeFn) called
// Organizer.OrganizeBook — the SINGLE-FILE path — for every book it touched.
// Any book whose file_path is a directory therefore failed outright with
// "…is a directory but single-file organize was requested". Production logged
// 588 failures of exactly that shape in one post-scan run on 2026-08-11.
//
// The fix hoists the three-way decision into Service.OrganizeOneBook so both
// the worker loop and the post-scan hook share it. These tests assert on which
// PATH was taken, not on whether an artifact was produced — a directory book
// routed to the directory path fails for a *different, recognisable* reason
// when its book_files rows are missing, and that difference is the signal.
// ---------------------------------------------------------------------------

const singleFileOrganizeErr = "single-file organize was requested"

// directoryBookFixture builds a book whose file_path is a real directory
// living OUTSIDE RootDir, so OrganizeOneBook cannot route it to
// ReOrganizeInPlace and must choose between the single-file and directory
// paths on its own.
func directoryBookFixture(t *testing.T) *database.Book {
	t.Helper()

	rootDir := t.TempDir()
	srcParent := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:              rootDir,
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
		OrganizationStrategy: "copy",
	}
	if strings.HasPrefix(srcParent, rootDir) {
		t.Fatalf("fixture invalid: source %s is under RootDir %s", srcParent, rootDir)
	}

	bookDir := filepath.Join(srcParent, "Multi Part Book")
	if err := os.MkdirAll(bookDir, 0o775); err != nil {
		t.Fatalf("setup: mkdir book dir: %v", err)
	}
	for _, name := range []string{"part1.mp3", "part2.mp3"} {
		if err := os.WriteFile(filepath.Join(bookDir, name), []byte("audio"), 0o644); err != nil {
			t.Fatalf("setup: write %s: %v", name, err)
		}
	}

	return &database.Book{
		ID:       "book-dir-1",
		Title:    "Multi Part Book",
		FilePath: bookDir,
		Author:   &database.Author{Name: "Some Author"},
	}
}

// TestOrganizeBook_RejectsDirectory is the negative control, and it is the
// behaviour the old post-scan hook was walking into on every multi-file book.
// It is asserted here rather than described in a comment so that a change to
// Organizer.OrganizeBook's contract breaks this file loudly instead of quietly
// invalidating the test below.
func TestOrganizeBook_RejectsDirectory(t *testing.T) {
	book := directoryBookFixture(t)
	org := NewOrganizer(&config.AppConfig)

	_, _, err := org.OrganizeBook(book)
	if err == nil {
		t.Fatal("negative control broken: OrganizeBook accepted a directory; the routing bug this file guards can no longer occur, so rewrite these tests rather than deleting them")
	}
	if !strings.Contains(err.Error(), singleFileOrganizeErr) {
		t.Fatalf("negative control broken: expected error containing %q, got: %v", singleFileOrganizeErr, err)
	}
}

// TestOrganizeOneBook_DirectoryBook_TakesDirectoryPath fails against the old
// code: routing a directory book through Organizer.OrganizeBook produces the
// single-file error, and this asserts that error is NOT what comes back.
//
// The book deliberately has zero book_files rows, so the directory path fails
// with its own distinctive "no segments tracked" message. That is the point —
// it proves which branch ran without depending on a successful copy.
func TestOrganizeOneBook_DirectoryBook_TakesDirectoryPath(t *testing.T) {
	book := directoryBookFixture(t)

	mockStore := mocks.NewMockStore(t)
	mockStore.On("GetBookFiles", "book-dir-1").Return([]database.BookFile{}, nil)

	svc := NewService(mockStore)
	org := NewOrganizer(&config.AppConfig)

	_, err := svc.OrganizeOneBook(org, book, logger.New("test"))
	if err == nil {
		t.Fatal("expected the directory path to reject a book with no tracked segments")
	}
	if strings.Contains(err.Error(), singleFileOrganizeErr) {
		t.Fatalf("OrganizeOneBook routed a directory book to the SINGLE-FILE path — this is the 2026-08-11 production defect: %v", err)
	}
	if !strings.Contains(err.Error(), "no segments tracked") {
		t.Fatalf("expected the directory path's own error, got: %v", err)
	}
	mockStore.AssertExpectations(t)
}

// TestOrganizeOneBook_DirectoryBook_OrganizesSegments covers the happy path:
// with book_files present the directory branch actually moves the segments.
func TestOrganizeOneBook_DirectoryBook_OrganizesSegments(t *testing.T) {
	book := directoryBookFixture(t)

	mockStore := mocks.NewMockStore(t)
	mockStore.On("GetBookFiles", "book-dir-1").Return([]database.BookFile{
		{ID: "bf-1", FilePath: filepath.Join(book.FilePath, "part1.mp3")},
		{ID: "bf-2", FilePath: filepath.Join(book.FilePath, "part2.mp3")},
	}, nil)

	svc := NewService(mockStore)
	org := NewOrganizer(&config.AppConfig)

	targetDir, err := svc.OrganizeOneBook(org, book, logger.New("test"))
	if err != nil {
		t.Fatalf("OrganizeOneBook on a multi-file book: %v", err)
	}
	if !strings.HasPrefix(targetDir, config.AppConfig.RootDir) {
		t.Fatalf("expected target %s under RootDir %s", targetDir, config.AppConfig.RootDir)
	}
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		t.Fatalf("read target dir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 organized segments in %s, got %d", targetDir, len(entries))
	}
}

// TestOrganizeOneBook_SingleFileBook_TakesSingleFilePath guards the other
// direction: the shared method must not send an ordinary one-file book down
// the directory branch, which would fail on its (absent) book_files rows.
func TestOrganizeOneBook_SingleFileBook_TakesSingleFilePath(t *testing.T) {
	rootDir := t.TempDir()
	srcParent := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:              rootDir,
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
		OrganizationStrategy: "copy",
	}

	srcFile := filepath.Join(srcParent, "solo.mp3")
	if err := os.WriteFile(srcFile, []byte("audio"), 0o644); err != nil {
		t.Fatalf("setup: write source file: %v", err)
	}

	book := &database.Book{
		ID:       "book-file-1",
		Title:    "Solo Book",
		FilePath: srcFile,
		Format:   "mp3",
		Author:   &database.Author{Name: "Some Author"},
	}

	// No GetBookFiles expectation: if the directory branch were taken this
	// mock would fail the test on an unexpected call.
	mockStore := mocks.NewMockStore(t)
	svc := NewService(mockStore)
	org := NewOrganizer(&config.AppConfig)

	newPath, err := svc.OrganizeOneBook(org, book, logger.New("test"))
	if err != nil {
		t.Fatalf("OrganizeOneBook on a single-file book: %v", err)
	}
	if !strings.HasPrefix(newPath, rootDir) {
		t.Fatalf("expected organized path %s under RootDir %s", newPath, rootDir)
	}
	if _, statErr := os.Stat(newPath); statErr != nil {
		t.Fatalf("organized file missing at %s: %v", newPath, statErr)
	}
}
