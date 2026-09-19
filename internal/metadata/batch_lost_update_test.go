// file: internal/metadata/batch_lost_update_test.go
// version: 1.0.0
// guid: 8c3f5d21-7b49-4e06-a1d8-2f5b9c0e4a76
// last-edited: 2026-09-19

package metadata

import (
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// batchRaceStore commits a concurrent write at the moment BatchUpdateMetadata
// reads the book it is about to change. That is the window a worker cannot
// protect by reading first: the author and series resolution below the read does
// store IO, so the worker holds its copy of the row across a real gap, and the
// workers run concurrently with each other as well.
type batchRaceStore struct {
	batchUpdateStore
	once   sync.Once
	onRead func(bookID string)
}

func (s *batchRaceStore) GetBookByID(id string) (*database.Book, error) {
	book, err := s.batchUpdateStore.GetBookByID(id)
	if book != nil {
		s.once.Do(func() { s.onRead(book.ID) })
	}
	return book, err
}

func setupBatchPebbleStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	store, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("create pebble store: %v", err)
	}
	if err := database.RunMigrations(store); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestBatchUpdateMetadata_DoesNotRevertConcurrentWrite pins the bulk metadata
// apply against lost updates.
//
// A worker read the book, resolved author and series (store IO), then wrote the
// whole row back. Anything committed in that gap -- another apply worker, a
// scan, an enrichment -- was reverted, with no error, because the write itself
// succeeds.
//
// AudibleRatingOverall is the probe: the apply never sets it, and UpdateBook's
// preserve-on-nil guard does not cover it, so a stale-snapshot write erases it.
func TestBatchUpdateMetadata_DoesNotRevertConcurrentWrite(t *testing.T) {
	base := setupBatchPebbleStore(t)

	seeded, err := base.CreateBook(&database.Book{Title: "Old Title", FilePath: "/books/raced.m4b"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	race := &batchRaceStore{batchUpdateStore: base}
	race.onRead = func(bookID string) {
		if _, merr := base.ModifyBook(bookID, func(cur *database.Book) error {
			cur.AudibleRatingOverall = new(4.7)
			return nil
		}); merr != nil {
			t.Errorf("concurrent enrichment write failed: %v", merr)
		}
	}

	errs, ok := BatchUpdateMetadata([]MetadataUpdate{{
		BookID:  seeded.ID,
		Updates: map[string]any{"title": "New Title"},
	}}, race, false)
	if len(errs) != 0 {
		t.Fatalf("BatchUpdateMetadata returned errors: %v", errs)
	}
	if ok != 1 {
		t.Fatalf("successCount = %d, want 1", ok)
	}

	got, err := base.GetBookByID(seeded.ID)
	if err != nil || got == nil {
		t.Fatalf("expected book after apply, err=%v", err)
	}

	// The concurrent writer's column survived...
	if got.AudibleRatingOverall == nil {
		t.Errorf("AudibleRatingOverall wiped by the bulk apply: want 4.7, got nil")
	} else if *got.AudibleRatingOverall != 4.7 {
		t.Errorf("AudibleRatingOverall: want 4.7, got %v", *got.AudibleRatingOverall)
	}

	// ...and the apply's own field still landed.
	if got.Title != "New Title" {
		t.Errorf("Title: want %q, got %q", "New Title", got.Title)
	}
}
