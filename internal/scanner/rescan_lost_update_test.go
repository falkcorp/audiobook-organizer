// file: internal/scanner/rescan_lost_update_test.go
// version: 1.0.0
// guid: 4d8a1c72-6e05-4b93-8f21-0a7c3e9b5d14
// last-edited: 2026-09-19

package scanner

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// rescanRaceStore fires a concurrent write exactly once, at the moment
// saveBookToDatabase reads the row it is about to overlay. That is the window a
// caller-side "read -> merge -> UpdateBook" cannot protect: the row the caller
// holds is already stale by the time it writes, so the write reverts whatever
// landed in between.
type rescanRaceStore struct {
	scannerStore
	once   sync.Once
	onRead func(bookID string)
}

func (s *rescanRaceStore) GetBookByFilePath(path string) (*database.Book, error) {
	book, err := s.scannerStore.GetBookByFilePath(path)
	// Only on a rescan: the first import's lookup returns nil, and the
	// concurrent writer needs a row to write to.
	if book != nil {
		s.once.Do(func() { s.onRead(book.ID) })
	}
	return book, err
}

// TestSaveBookToDatabase_RescanDoesNotRevertConcurrentWrite pins the rescan
// merge against lost updates.
//
// TestSaveBookToDatabase_RescanPreservesEnrichedFields already proves the merge
// keeps fields that were present when the row was READ. This one covers the
// other half: a field written by someone else AFTER that read. A metadata apply,
// an AI parse write-back and a scan all touch the same row, and the scanner's
// merge held a snapshot across the gap, so the enrichment was silently rolled
// back — with no error anywhere, because the write itself succeeds.
//
// AudibleRatingOverall is the probe because it is neither scanner-owned (the
// merge never overlays it) nor covered by UpdateBook's preserve-on-nil guard,
// so a stale-snapshot write really does erase it.
func TestSaveBookToDatabase_RescanDoesNotRevertConcurrentWrite(t *testing.T) {
	base, cleanup := setupPebbleStore(t)
	defer cleanup()

	race := &rescanRaceStore{scannerStore: base}
	race.onRead = func(bookID string) {
		// The concurrent writer: lands between the scanner's read and its write.
		if _, err := base.ModifyBook(bookID, func(cur *database.Book) error {
			cur.AudibleRatingOverall = new(4.7)
			return nil
		}); err != nil {
			t.Errorf("concurrent enrichment write failed: %v", err)
		}
	}

	prevStore := database.GetGlobalStore()
	database.SetGlobalStore(base)
	SetStore(race)
	t.Cleanup(func() {
		database.SetGlobalStore(prevStore)
		SetStore(nil)
	})

	prevConfig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prevConfig })

	rootDir := t.TempDir()
	config.AppConfig.RootDir = rootDir

	filePath := filepath.Join(rootDir, "raced-book.m4b")
	if err := os.WriteFile(filePath, []byte("raced book content"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	if err := saveBookToDatabase(context.Background(), &Book{
		FilePath: filePath,
		Title:    "Original Title",
		Format:   ".m4b",
		Duration: 100,
		Narrator: "Original Narrator",
	}); err != nil {
		t.Fatalf("initial saveBookToDatabase failed: %v", err)
	}

	// The rescan. race.onRead fires inside it, enriching the row after the
	// scanner has read it and before the scanner writes.
	if err := saveBookToDatabase(context.Background(), &Book{
		FilePath: filePath,
		Title:    "Rescanned Title",
		Format:   ".m4b",
		Duration: 100,
		Narrator: "Rescanned Narrator",
	}); err != nil {
		t.Fatalf("rescan saveBookToDatabase failed: %v", err)
	}

	got, err := base.GetBookByFilePath(filePath)
	if err != nil || got == nil {
		t.Fatalf("expected book after rescan, err=%v", err)
	}

	// The concurrent writer's column survived.
	assertF64(t, "AudibleRatingOverall", got.AudibleRatingOverall, 4.7)

	// ...and the scanner's own write still landed. A merge that protected the
	// concurrent column by skipping its own write would also pass the check
	// above, so both halves are asserted.
	if got.Title != "Rescanned Title" {
		t.Errorf("Title: want %q, got %q", "Rescanned Title", got.Title)
	}
	if got.Narrator == nil || *got.Narrator != "Rescanned Narrator" {
		t.Errorf("Narrator: want %q, got %v", "Rescanned Narrator", got.Narrator)
	}
}
