// file: internal/itunes/service/position_sync_failclosed_test.go
// version: 1.0.0
// guid: 4c9e2a7f-6d13-4b80-95e1-a7f3d8c2b640
// last-edited: 2026-09-19

package itunesservice

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type unreadablePositionSyncStore struct{ positionSyncStore }

func (unreadablePositionSyncStore) GetUserPosition(string, string) (*database.UserPosition, error) {
	return nil, errors.New("transient read failure")
}

// An unreadable position is NOT "no position yet": seeding over it would
// replace the listener's real position with the iTunes bookmark.
func TestPullITunesBookmarks_UnreadablePositionIsNotSeededOver(t *testing.T) {
	store := setupSyncTestStore(t)
	bookmark := int64(150000)
	book, _ := store.CreateBook(&database.Book{
		Title: "Bookmarked", FilePath: "/tmp/b1", Format: "m4b", ITunesBookmark: &bookmark,
	})
	_ = store.CreateBookFile(&database.BookFile{ID: "f1", BookID: book.ID, FilePath: "/tmp/f1", Duration: 3600})
	_ = store.SetUserPosition(adminUserID, book.ID, "f1", 2000)

	if seeded := newPositionSync(unreadablePositionSyncStore{store}, nil).pullBookmarks(); seeded != 0 {
		t.Fatalf("seeded %d over an unreadable position, want 0", seeded)
	}
	if pos, _ := store.GetUserPosition(adminUserID, book.ID); pos == nil || pos.PositionSeconds != 2000 {
		t.Fatalf("position = %+v, want 2000 untouched", pos)
	}
}
