// file: internal/sweep/archive_sweep_orphans_test.go
// version: 1.0.0
// guid: 8b2e4c71-5d93-4a06-b1f8-2c7e9a4d06b3
// last-edited: 2026-09-19

package sweep

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// SweepArchivedBooks used to os.Remove() every file a soft-deleted book owned
// and then DeleteBook it — which never tears down book_file rows, so every
// swept book left its file rows naming a book with no row. For a dedup-merge
// loser (merge.MergeBooks soft-deletes it and deliberately leaves its files on
// it) that is the loser's own audio deleted off disk, with no protected-path
// check and no config opt-in. A book that still owns file rows must now be
// left alone: files on disk, rows, and the book row itself.
//
// softDeletedListing feeds the sweep its soft-deleted candidates. The sweep
// enumerates with GetAllBooksCore, which EXCLUDES soft-deleted books — so
// against a bare PebbleStore it sees nothing and any assertion here would pass
// vacuously (see the KNOWN INERT note on SweepArchivedBooks). The wrapper
// supplies what the enumeration is meant to return; every other call is the
// real store.
type softDeletedListing struct {
	*database.PebbleStore
	ids []string
}

func (s softDeletedListing) GetAllBooksCore(_, _ int) ([]database.BookCore, error) {
	var out []database.BookCore
	for _, id := range s.ids {
		b, err := s.GetBookByID(id)
		if err != nil {
			return nil, err
		}
		if b != nil {
			out = append(out, database.BookCore{ID: b.ID, MarkedForDeletion: b.MarkedForDeletion, MarkedForDeletionAt: b.MarkedForDeletionAt})
		}
	}
	return out, nil
}

func TestSweepArchivedBooks_BookOwningFilesIsNotSwept(t *testing.T) {
	pebble, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "pebble"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = pebble.Close() })
	store := &softDeletedListing{PebbleStore: pebble}

	dir := t.TempDir()
	audio := filepath.Join(dir, "loser.mp3")
	if err := os.WriteFile(audio, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	yes := true
	old := time.Now().AddDate(0, 0, -40)
	b, err := store.CreateBook(&database.Book{
		Title: "Merged-away loser", FilePath: audio,
		MarkedForDeletion: &yes, MarkedForDeletionAt: &old,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: audio, FileSize: 5}); err != nil {
		t.Fatal(err)
	}
	store.ids = []string{b.ID}

	if got := SweepArchivedBooks(store); got != 0 {
		t.Errorf("cleaned = %d, want 0: the book still owns a book_file row", got)
	}
	if _, err := os.Stat(audio); err != nil {
		t.Errorf("sweep removed %s from disk: %v", audio, err)
	}
	files, err := store.GetBookFiles(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("book_file rows = %d, want 1", len(files))
	}
	if got, _ := store.GetBookByID(b.ID); got == nil {
		t.Errorf("book %s was hard-deleted; its %d book_file row(s) are now orphaned", b.ID, len(files))
	}
}

// A soft-deleted book past retention that owns NO file rows is still swept.
func TestSweepArchivedBooks_FilelessBookIsSwept(t *testing.T) {
	pebble, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "pebble"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = pebble.Close() })
	store := &softDeletedListing{PebbleStore: pebble}
	yes := true
	old := time.Now().AddDate(0, 0, -40)
	b, err := store.CreateBook(&database.Book{Title: "Empty shell", MarkedForDeletion: &yes, MarkedForDeletionAt: &old})
	if err != nil {
		t.Fatal(err)
	}
	store.ids = []string{b.ID}
	if got := SweepArchivedBooks(store); got != 1 {
		t.Errorf("cleaned = %d, want 1", got)
	}
	if got, _ := store.GetBookByID(b.ID); got != nil {
		t.Errorf("fileless book %s survived the sweep", b.ID)
	}
}
