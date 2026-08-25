// file: internal/scanner/create_book_files_aggregates_test.go
// version: 1.1.0
// guid: 3a7c9e14-5d82-4b06-9f31-c2e8a5407db6
// last-edited: 2026-08-25

package scanner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// TestCreateBookFilesForBookKeepsAggregatesAfterPathNormalization pins the
// interaction between two writes that createBookFilesForBook makes back to back.
//
// BatchUpsertBookFiles recomputes the book's FileSize/Duration aggregates and
// persists them. createBookFilesForBook then normalizes Book.FilePath from the
// file to its parent directory with a second UpdateBook. That second write used
// to send dbBook — the copy loaded at the top of the function, before the batch
// wrote anything. UpdateBook's preserve-on-nil guard covers nine fields and
// FileSize is not one of them, so the stale nil was written straight through and
// erased the aggregate that had just been computed, inside a single call.
//
// The branch only fires when Book.FilePath points at a file rather than a
// directory — single-file audiobooks — which is the most common shape in the
// library, so the erasure was the normal case rather than an edge case.
//
// WHY FileSize AND NOT Duration: this function sets BookFile.FileSize from
// os.Stat but never sets BookFile.Duration (durations arrive later, from a
// separate probe pass). FileSize is therefore the only aggregate this path can
// produce, and asserting on it needs no audio fixture.
func TestCreateBookFilesForBookKeepsAggregatesAfterPathNormalization(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	dir := t.TempDir()
	audioPath := filepath.Join(dir, "book.mp3")
	const wantSize = 4096
	if err := os.WriteFile(audioPath, make([]byte, wantSize), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// FilePath points at the FILE, not the directory: that is what makes the
	// normalization branch run at all.
	book, err := store.CreateBook(&database.Book{FilePath: audioPath, Title: "Single File Book"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	if book.FileSize != nil {
		t.Fatalf("precondition failed: a freshly created book should carry no FileSize, got %d", *book.FileSize)
	}

	// segmentFiles is passed explicitly so this does not depend on
	// config.AppConfig.SupportedExtensions being populated by another test.
	createBookFilesForBook(audioPath, []string{audioPath}, logger.New("test"), normalizeToDirectory)

	got, err := store.GetBookByID(book.ID)
	if err != nil {
		t.Fatalf("GetBookByID: %v", err)
	}
	if got == nil {
		t.Fatal("book disappeared")
	}

	// The normalization itself must still happen — this guards against the
	// tempting "fix" of simply deleting the second write.
	if got.FilePath != dir {
		t.Errorf("FilePath not normalized to the parent directory: got %q, want %q", got.FilePath, dir)
	}

	// The aggregate the batch write computed must survive the normalization.
	if got.FileSize == nil {
		t.Fatalf("FileSize was erased by the FilePath normalization write-back; want %d", wantSize)
	}
	if *got.FileSize != wantSize {
		t.Errorf("FileSize = %d, want %d", *got.FileSize, wantSize)
	}
}
