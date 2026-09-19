// file: internal/scanner/rescan_lost_update_test.go
// version: 1.1.0
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

// hashDupRaceStore fires a concurrent write when saveBookToDatabase looks the
// existing book up BY HASH — the read the version-linking branch then writes
// back whole.
type hashDupRaceStore struct {
	scannerStore
	once   sync.Once
	onRead func(bookID string)
}

func (s *hashDupRaceStore) GetBookByFileHash(hash string) (*database.Book, error) {
	book, err := s.scannerStore.GetBookByFileHash(hash)
	if book != nil {
		s.once.Do(func() { s.onRead(book.ID) })
	}
	return book, err
}

// seedHashDupRace imports one book, then wires a store that fires onRead the
// next time that book is found by hash, and returns the seeded row's ID.
func seedHashDupRace(t *testing.T, onRead func(base *database.PebbleStore, bookID string)) (*database.PebbleStore, string, string) {
	t.Helper()

	base, cleanup := setupPebbleStore(t)
	t.Cleanup(cleanup)

	race := &hashDupRaceStore{scannerStore: base}
	race.onRead = func(bookID string) { onRead(base, bookID) }

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

	firstPath := filepath.Join(rootDir, "first-copy.m4b")
	secondPath := filepath.Join(rootDir, "second-copy.m4b")
	for _, p := range []string{firstPath, secondPath} {
		if err := os.WriteFile(p, []byte("identical content"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	if err := saveBookToDatabase(context.Background(), &Book{
		FilePath: firstPath,
		Title:    "Shared Title",
		Format:   ".m4b",
		FileHash: "sharedhash",
		Duration: 100,
	}); err != nil {
		t.Fatalf("seeding saveBookToDatabase failed: %v", err)
	}

	first, err := base.GetBookByFilePath(firstPath)
	if err != nil || first == nil {
		t.Fatalf("expected seeded book, err=%v", err)
	}
	return base, first.ID, secondPath
}

// importSecondCopy scans a second file carrying the same hash, which drives the
// hash-duplicate version-linking branch.
func importSecondCopy(t *testing.T, path string) {
	t.Helper()
	if err := saveBookToDatabase(context.Background(), &Book{
		FilePath: path,
		Title:    "Shared Title",
		Format:   ".m4b",
		FileHash: "sharedhash",
		Duration: 100,
	}); err != nil {
		t.Fatalf("second-copy saveBookToDatabase failed: %v", err)
	}
}

// TestVersionLink_DoesNotRevertConcurrentWrite covers the hash-duplicate
// version-linking branch, which set VersionGroupID/IsPrimaryVersion on a Book
// value read earlier and wrote the WHOLE row back. Everything another writer
// committed in between was reverted.
func TestVersionLink_DoesNotRevertConcurrentWrite(t *testing.T) {
	base, firstID, secondPath := seedHashDupRace(t, func(base *database.PebbleStore, bookID string) {
		if _, err := base.ModifyBook(bookID, func(cur *database.Book) error {
			cur.AudibleRatingOverall = new(4.7)
			return nil
		}); err != nil {
			t.Errorf("concurrent enrichment write failed: %v", err)
		}
	})

	importSecondCopy(t, secondPath)

	got, err := base.GetBookByID(firstID)
	if err != nil || got == nil {
		t.Fatalf("expected first book, err=%v", err)
	}
	assertF64(t, "AudibleRatingOverall", got.AudibleRatingOverall, 4.7)
	if got.VersionGroupID == nil || *got.VersionGroupID == "" {
		t.Errorf("version link did not land: VersionGroupID is %v", got.VersionGroupID)
	}
}

// TestVersionLink_JoinsAGroupLandedByAnotherWriter covers the failure mode that
// a naive precondition re-check would introduce.
//
// If another writer links the existing row first, the scanner must not overwrite
// their group — but it must not mint its own either: the new row would carry a
// group ID no other row belongs to, an orphan version group. The new row joins
// the group that is actually on the existing row.
func TestVersionLink_JoinsAGroupLandedByAnotherWriter(t *testing.T) {
	const otherGroup = "vg-landed-elsewhere"

	base, firstID, secondPath := seedHashDupRace(t, func(base *database.PebbleStore, bookID string) {
		if _, err := base.ModifyBook(bookID, func(cur *database.Book) error {
			cur.VersionGroupID = new(otherGroup)
			cur.IsPrimaryVersion = new(true)
			return nil
		}); err != nil {
			t.Errorf("concurrent version-link write failed: %v", err)
		}
	})

	importSecondCopy(t, secondPath)

	first, err := base.GetBookByID(firstID)
	if err != nil || first == nil {
		t.Fatalf("expected first book, err=%v", err)
	}
	if first.VersionGroupID == nil || *first.VersionGroupID != otherGroup {
		t.Errorf("the other writer's group was overwritten: want %q, got %q",
			otherGroup, derefString(first.VersionGroupID))
	}

	second, err := base.GetBookByFilePath(secondPath)
	if err != nil || second == nil {
		t.Fatalf("expected second book, err=%v", err)
	}
	if second.VersionGroupID == nil {
		t.Fatalf("second copy carries no version group at all")
	}
	if *second.VersionGroupID != otherGroup {
		t.Errorf("second copy minted an ORPHAN group %q that no other row belongs to; want it to join %q",
			*second.VersionGroupID, otherGroup)
	}
}

// derefString renders a *string for a failure message without panicking on nil.
func derefString(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
