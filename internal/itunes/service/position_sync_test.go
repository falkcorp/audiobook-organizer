// file: internal/itunes/service/position_sync_test.go
// version: 1.1.0
// guid: 0a8b9c6d-1e7f-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-10-07

package itunesservice

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func setupSyncTestStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestPullITunesBookmarks_SeedsPosition(t *testing.T) {
	store := setupSyncTestStore(t)

	bookmark := int64(150000) // 150 seconds in ms
	book, _ := store.CreateBook(&database.Book{
		Title: "Bookmarked", FilePath: "/tmp/b1", Format: "m4b",
		ITunesBookmark: &bookmark,
	})
	_ = store.CreateBookFile(&database.BookFile{
		ID: "f1", BookID: book.ID, FilePath: "/tmp/f1", Duration: 3600,
	})

	seeded := newPositionSync(store).pullBookmarks()
	if seeded != 1 {
		t.Errorf("seeded = %d, want 1", seeded)
	}

	pos, err := store.GetUserPosition(adminUserID, book.ID)
	if err != nil || pos == nil {
		t.Fatalf("position not seeded: %v", err)
	}
	if pos.PositionSeconds != 150.0 {
		t.Errorf("position = %f, want 150.0", pos.PositionSeconds)
	}
}

func TestPullITunesBookmarks_SkipsExisting(t *testing.T) {
	store := setupSyncTestStore(t)

	bookmark := int64(100000)
	book, _ := store.CreateBook(&database.Book{
		Title: "Already Tracked", FilePath: "/tmp/b1", Format: "m4b",
		ITunesBookmark: &bookmark,
	})
	_ = store.CreateBookFile(&database.BookFile{
		ID: "f1", BookID: book.ID, FilePath: "/tmp/f1", Duration: 3600,
	})
	_ = store.SetUserPosition(adminUserID, book.ID, "f1", 200.0)

	seeded := newPositionSync(store).pullBookmarks()
	if seeded != 0 {
		t.Errorf("should skip already-tracked, seeded = %d", seeded)
	}

	pos, _ := store.GetUserPosition(adminUserID, book.ID)
	if pos.PositionSeconds != 200.0 {
		t.Errorf("position = %f, should be unchanged 200.0", pos.PositionSeconds)
	}
}

func TestPullITunesBookmarks_SeedsFinishedFromPlayCount(t *testing.T) {
	store := setupSyncTestStore(t)

	pc := 3
	book, _ := store.CreateBook(&database.Book{
		Title: "Played", FilePath: "/tmp/b1", Format: "m4b",
		ITunesPlayCount: &pc,
	})

	seeded := newPositionSync(store).pullBookmarks()
	if seeded != 1 {
		t.Errorf("seeded = %d, want 1 (finished from play count)", seeded)
	}

	state, _ := store.GetUserBookState(adminUserID, book.ID)
	if state == nil || state.Status != database.UserBookStatusFinished {
		t.Errorf("state = %+v, want finished", state)
	}
}

// Seeding a finish from iTunes is import only: it writes the user state and
// leaves the book row alone. The ITunesPlayCountBumpedAt mark existed only so
// the removed push would not count the finish into iTunes a second time.
func TestPullITunesBookmarks_FinishSeedDoesNotWriteTheBook(t *testing.T) {
	store := setupSyncTestStore(t)

	pc := 2
	book, err := store.CreateBook(&database.Book{
		Title: "Played", FilePath: "/tmp/b2", Format: "m4b",
		ITunesPlayCount: &pc,
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.GetBookByID(book.ID)
	if err != nil {
		t.Fatal(err)
	}

	if seeded := newPositionSync(store).pullBookmarks(); seeded != 1 {
		t.Fatalf("seeded = %d, want 1", seeded)
	}

	after, err := store.GetBookByID(book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ITunesPlayCountBumpedAt != nil {
		t.Errorf("ITunesPlayCountBumpedAt = %v, want nil: the seed must not write the book", after.ITunesPlayCountBumpedAt)
	}
	if (before.UpdatedAt == nil) != (after.UpdatedAt == nil) ||
		(before.UpdatedAt != nil && !after.UpdatedAt.Equal(*before.UpdatedAt)) {
		t.Errorf("book UpdatedAt moved %v -> %v: the seed wrote the book row", before.UpdatedAt, after.UpdatedAt)
	}
	state, _ := store.GetUserBookState(adminUserID, book.ID)
	if state == nil || state.Status != database.UserBookStatusFinished || state.FinishedAt == nil {
		t.Errorf("state = %+v, want finished with a FinishedAt", state)
	}
}

func TestPullITunesBookmarks_NoBookmarkNoSeed(t *testing.T) {
	store := setupSyncTestStore(t)

	_, _ = store.CreateBook(&database.Book{
		Title: "No Bookmark", FilePath: "/tmp/b1", Format: "m4b",
	})

	seeded := newPositionSync(store).pullBookmarks()
	if seeded != 0 {
		t.Errorf("should not seed without bookmark, seeded = %d", seeded)
	}
}

func TestSyncITunesPositions_EndToEnd(t *testing.T) {
	store := setupSyncTestStore(t)

	bookmark := int64(60000)
	book, _ := store.CreateBook(&database.Book{
		Title: "Sync Target", FilePath: "/tmp/b1", Format: "m4b",
		ITunesBookmark: &bookmark,
	})
	_ = store.CreateBookFile(&database.BookFile{
		ID: "f1", BookID: book.ID, FilePath: "/tmp/f1", Duration: 3600,
	})

	if pulled := newPositionSync(store).Sync(); pulled != 1 {
		t.Errorf("pulled = %d, want 1", pulled)
	}
}
