// file: internal/database/pebble_store_bookmarks_test.go
// version: 1.0.0
// guid: c95887b8-c5f3-4469-b0e8-d053d02bf1ea
// last-edited: 2026-07-30

package database

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
)

func newPebbleStoreForBookmarks(t *testing.T) *PebbleStore {
	t.Helper()
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "bookmarks-db"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestCreateBookmark_ThenList(t *testing.T) {
	store := newPebbleStoreForBookmarks(t)

	b1 := progress.Bookmark{UserID: "u1", ItemID: "i1", TimeSec: 10, Title: "first"}
	b2 := progress.Bookmark{UserID: "u1", ItemID: "i1", TimeSec: 30, Title: "second"}

	if err := store.CreateBookmark(b1); err != nil {
		t.Fatalf("CreateBookmark(b1): %v", err)
	}
	if err := store.CreateBookmark(b2); err != nil {
		t.Fatalf("CreateBookmark(b2): %v", err)
	}

	got, err := store.ListBookmarks("u1", "i1")
	if err != nil {
		t.Fatalf("ListBookmarks: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 bookmarks, got %d: %+v", len(got), got)
	}
}

func TestCreateBookmark_UpsertSameTimeUpdatesTitle(t *testing.T) {
	store := newPebbleStoreForBookmarks(t)

	if err := store.CreateBookmark(progress.Bookmark{UserID: "u1", ItemID: "i1", TimeSec: 30, Title: "A"}); err != nil {
		t.Fatalf("CreateBookmark(A): %v", err)
	}
	first, err := store.ListBookmarks("u1", "i1")
	if err != nil {
		t.Fatalf("ListBookmarks (first): %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("expected 1 bookmark after first create, got %d", len(first))
	}
	firstCreatedAt := first[0].CreatedAt

	if err := store.CreateBookmark(progress.Bookmark{UserID: "u1", ItemID: "i1", TimeSec: 30, Title: "B"}); err != nil {
		t.Fatalf("CreateBookmark(B): %v", err)
	}

	got, err := store.ListBookmarks("u1", "i1")
	if err != nil {
		t.Fatalf("ListBookmarks (second): %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 bookmark after upsert, got %d: %+v", len(got), got)
	}
	if got[0].Title != "B" {
		t.Errorf("expected title %q after upsert, got %q", "B", got[0].Title)
	}
	if got[0].CreatedAt != firstCreatedAt {
		t.Errorf("expected CreatedAt preserved across upsert: first=%d, second=%d", firstCreatedAt, got[0].CreatedAt)
	}
	if got[0].UpdatedAt < firstCreatedAt {
		t.Errorf("expected UpdatedAt to advance, got %d (created at %d)", got[0].UpdatedAt, firstCreatedAt)
	}
}

func TestCreateBookmark_IntAndFloatTimeCollide(t *testing.T) {
	store := newPebbleStoreForBookmarks(t)

	intTime, err := progress.ParseTimeSec("12")
	if err != nil {
		t.Fatalf("ParseTimeSec(12): %v", err)
	}
	floatTime, err := progress.ParseTimeSec("12.0")
	if err != nil {
		t.Fatalf("ParseTimeSec(12.0): %v", err)
	}

	if err := store.CreateBookmark(progress.Bookmark{UserID: "u1", ItemID: "i1", TimeSec: intTime, Title: "int-encoded"}); err != nil {
		t.Fatalf("CreateBookmark(int): %v", err)
	}
	if err := store.CreateBookmark(progress.Bookmark{UserID: "u1", ItemID: "i1", TimeSec: floatTime, Title: "float-encoded"}); err != nil {
		t.Fatalf("CreateBookmark(float): %v", err)
	}

	got, err := store.ListBookmarks("u1", "i1")
	if err != nil {
		t.Fatalf("ListBookmarks: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 bookmark (int/float time collide), got %d: %+v", len(got), got)
	}
}

func TestUpdateBookmarkTitle_NoSuchBookmarkErrors(t *testing.T) {
	store := newPebbleStoreForBookmarks(t)

	if err := store.UpdateBookmarkTitle("u1", "i1", 42, "new title"); err == nil {
		t.Error("expected error updating a nonexistent bookmark, got nil")
	}

	got, err := store.ListBookmarks("u1", "i1")
	if err != nil {
		t.Fatalf("ListBookmarks: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected UpdateBookmarkTitle on missing key to not create one, got %d bookmarks", len(got))
	}
}

func TestDeleteBookmark_RemovesOnlyThatOne(t *testing.T) {
	store := newPebbleStoreForBookmarks(t)

	if err := store.CreateBookmark(progress.Bookmark{UserID: "u1", ItemID: "i1", TimeSec: 10, Title: "keep-me"}); err != nil {
		t.Fatalf("CreateBookmark(10): %v", err)
	}
	if err := store.CreateBookmark(progress.Bookmark{UserID: "u1", ItemID: "i1", TimeSec: 20, Title: "delete-me"}); err != nil {
		t.Fatalf("CreateBookmark(20): %v", err)
	}

	if err := store.DeleteBookmark("u1", "i1", 20); err != nil {
		t.Fatalf("DeleteBookmark(20): %v", err)
	}

	got, err := store.ListBookmarks("u1", "i1")
	if err != nil {
		t.Fatalf("ListBookmarks: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 bookmark remaining, got %d: %+v", len(got), got)
	}
	if got[0].Title != "keep-me" {
		t.Errorf("expected remaining bookmark title %q, got %q", "keep-me", got[0].Title)
	}
}

func TestListBookmarks_ScopedToUserAndItem(t *testing.T) {
	store := newPebbleStoreForBookmarks(t)

	if err := store.CreateBookmark(progress.Bookmark{UserID: "u1", ItemID: "i1", TimeSec: 10, Title: "u1-i1"}); err != nil {
		t.Fatalf("CreateBookmark(u1,i1): %v", err)
	}
	if err := store.CreateBookmark(progress.Bookmark{UserID: "u2", ItemID: "i1", TimeSec: 10, Title: "u2-i1"}); err != nil {
		t.Fatalf("CreateBookmark(u2,i1): %v", err)
	}
	if err := store.CreateBookmark(progress.Bookmark{UserID: "u1", ItemID: "i2", TimeSec: 10, Title: "u1-i2"}); err != nil {
		t.Fatalf("CreateBookmark(u1,i2): %v", err)
	}

	got, err := store.ListBookmarks("u1", "i1")
	if err != nil {
		t.Fatalf("ListBookmarks(u1,i1): %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 bookmark for (u1,i1), got %d: %+v", len(got), got)
	}
	if got[0].Title != "u1-i1" {
		t.Errorf("expected title %q, got %q", "u1-i1", got[0].Title)
	}
}

func TestListBookmarksForUser_AggregatesAcrossItemsButNotOtherUsers(t *testing.T) {
	store := newPebbleStoreForBookmarks(t)

	if err := store.CreateBookmark(progress.Bookmark{UserID: "u1", ItemID: "i1", TimeSec: 10, Title: "u1-i1"}); err != nil {
		t.Fatalf("CreateBookmark(u1,i1): %v", err)
	}
	if err := store.CreateBookmark(progress.Bookmark{UserID: "u1", ItemID: "i2", TimeSec: 20, Title: "u1-i2"}); err != nil {
		t.Fatalf("CreateBookmark(u1,i2): %v", err)
	}
	if err := store.CreateBookmark(progress.Bookmark{UserID: "u2", ItemID: "i1", TimeSec: 10, Title: "u2-i1"}); err != nil {
		t.Fatalf("CreateBookmark(u2,i1): %v", err)
	}

	got, err := store.ListBookmarksForUser("u1")
	if err != nil {
		t.Fatalf("ListBookmarksForUser(u1): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 bookmarks for u1 across both items, got %d: %+v", len(got), got)
	}
	titles := map[string]bool{}
	for _, b := range got {
		titles[b.Title] = true
		if b.UserID != "u1" {
			t.Errorf("ListBookmarksForUser(u1) returned a bookmark for user %q", b.UserID)
		}
	}
	if !titles["u1-i1"] || !titles["u1-i2"] {
		t.Errorf("expected both u1-i1 and u1-i2 present, got %+v", got)
	}
}

func TestConcurrentCreateBookmark_DifferentTimesNoRace(t *testing.T) {
	store := newPebbleStoreForBookmarks(t)

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = store.CreateBookmark(progress.Bookmark{
				UserID:  "u1",
				ItemID:  "i1",
				TimeSec: float64(i * 10),
				Title:   fmt.Sprintf("bookmark-%d", i),
			})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("CreateBookmark(%d) failed: %v", i, err)
		}
	}

	got, err := store.ListBookmarks("u1", "i1")
	if err != nil {
		t.Fatalf("ListBookmarks: %v", err)
	}
	if len(got) != n {
		t.Fatalf("expected %d bookmarks, got %d: %+v", n, len(got), got)
	}
}

// TestConcurrentCreateBookmark_SameTimeUpsertNoRace exercises N concurrent
// upserts at the SAME (userID, itemID, time) key -- the shape
// TestConcurrentCreateBookmark_DifferentTimesNoRace does not cover, since it
// only spans distinct times. Proves the get-then-set upsert path in
// CreateBookmark is serialized (via createBookmarkMu) so a concurrent
// same-key upsert cannot corrupt CreatedAt or leave two rows behind.
func TestConcurrentCreateBookmark_SameTimeUpsertNoRace(t *testing.T) {
	store := newPebbleStoreForBookmarks(t)

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = store.CreateBookmark(progress.Bookmark{
				UserID:  "u1",
				ItemID:  "i1",
				TimeSec: 30,
				Title:   fmt.Sprintf("racer-%d", i),
			})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("CreateBookmark(%d) failed: %v", i, err)
		}
	}

	got, err := store.ListBookmarks("u1", "i1")
	if err != nil {
		t.Fatalf("ListBookmarks: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 bookmark after %d concurrent same-key upserts, got %d: %+v", n, len(got), got)
	}
	if got[0].CreatedAt == 0 {
		t.Errorf("expected CreatedAt to be set, got 0")
	}
	if got[0].UpdatedAt < got[0].CreatedAt {
		t.Errorf("expected UpdatedAt >= CreatedAt, got UpdatedAt=%d CreatedAt=%d", got[0].UpdatedAt, got[0].CreatedAt)
	}
}
