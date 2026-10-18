// file: internal/itunes/service/position_sync_lock_test.go
// version: 1.0.0
// guid: 15be1527-21f2-4973-813c-f2dbcd42a53c
// last-edited: 2026-10-18

package itunesservice

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// hookedPositionSyncStore runs onCheck once, after the bookmark seed's "no
// position yet" read: between the check and the seed.
type hookedPositionSyncStore struct {
	positionSyncStore
	once    sync.Once
	onCheck func()
}

func (s *hookedPositionSyncStore) GetUserPosition(userID, bookID string) (*database.UserPosition, error) {
	p, err := s.positionSyncStore.GetUserPosition(userID, bookID)
	if s.onCheck != nil {
		s.once.Do(s.onCheck)
	}
	return p, err
}

func seedBookmarkedBook(t *testing.T, store *database.PebbleStore, title string, bookmarkMs int64, playCount *int) *database.Book {
	t.Helper()
	book, err := store.CreateBook(&database.Book{
		Title: title, FilePath: "/tmp/" + title, Format: "m4b",
		ITunesBookmark: &bookmarkMs, ITunesPlayCount: playCount,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookFile(&database.BookFile{
		ID: "f-" + title, BookID: book.ID, FilePath: "/tmp/f-" + title, Duration: 3600,
	}); err != nil {
		t.Fatal(err)
	}
	return book
}

// The per-book seed waits for the (admin, book) user-state stripe.
func TestPullITunesBookmarks_HoldsUserStateLock(t *testing.T) {
	store := setupSyncTestStore(t)
	book := seedBookmarkedBook(t, store, "locked", 150000, nil)
	unlock := database.LockUserBookState(adminUserID, book.ID)
	done := make(chan int, 1)
	go func() { done <- newPositionSync(store).pullBookmarks() }()
	select {
	case n := <-done:
		unlock()
		t.Fatalf("pullBookmarks seeded %d while the user-state lock was held", n)
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	select {
	case n := <-done:
		if n != 1 {
			t.Fatalf("seeded = %d, want 1", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("pullBookmarks never finished after the lock was released")
	}
}

// A device sync landing between the seed's "no position yet" check and the
// seed is not rewound to the iTunes bookmark: the seed holds the stripe, so
// the client waits and writes on top. Before 2026-10-18 the bookmark seed
// took no stripe, and the client's 2000s position was overwritten with the
// 150s bookmark.
func TestPullITunesBookmarks_UserStateRaceKeepsLockedWrite(t *testing.T) {
	store := setupSyncTestStore(t)
	book := seedBookmarkedBook(t, store, "raced", 150000, nil)
	var wait func() error
	hooked := &hookedPositionSyncStore{positionSyncStore: store}
	hooked.onCheck = func() {
		done := make(chan error, 1)
		go func() {
			defer database.LockUserBookState(adminUserID, book.ID)()
			done <- store.SetUserPosition(adminUserID, book.ID, "f-raced", 2000)
		}()
		var landedErr error
		landed := false
		select {
		case landedErr = <-done:
			landed = true
		case <-time.After(200 * time.Millisecond):
		}
		wait = func() error {
			if landed {
				return landedErr
			}
			return <-done
		}
	}
	newPositionSync(hooked).pullBookmarks()
	if err := wait(); err != nil {
		t.Fatal(err)
	}
	pos, err := store.GetUserPosition(adminUserID, book.ID)
	if err != nil || pos == nil {
		t.Fatalf("position = %+v, %v", pos, err)
	}
	if pos.PositionSeconds != 2000 {
		t.Fatalf("position = %v, want the client's 2000 (the seed rewound it to the bookmark)", pos.PositionSeconds)
	}
}

// Many books on the worker pool: every bookmark is seeded once, a
// bookmarked book's recompute makes its play-count seed skip (the per-book
// order), and an un-bookmarked played book is seeded finished. Run under
// -race for the pool's counters.
func TestPullITunesBookmarks_UserStateWorkerPoolSeedsEveryBook(t *testing.T) {
	store := setupSyncTestStore(t)
	one := 1
	const n = 40
	var bookmarked, played []*database.Book
	for i := 0; i < n; i++ {
		bookmarked = append(bookmarked, seedBookmarkedBook(t, store, fmt.Sprintf("bm%d", i), int64(1000*(i+1)), &one))
	}
	for i := 0; i < n; i++ {
		b, err := store.CreateBook(&database.Book{
			Title: fmt.Sprintf("pc%d", i), FilePath: fmt.Sprintf("/tmp/pc%d", i), Format: "m4b", ITunesPlayCount: &one,
		})
		if err != nil {
			t.Fatal(err)
		}
		played = append(played, b)
	}
	if got := newPositionSync(store).pullBookmarks(); got != 2*n {
		t.Fatalf("seeded = %d, want %d", got, 2*n)
	}
	for i, b := range bookmarked {
		pos, err := store.GetUserPosition(adminUserID, b.ID)
		if err != nil || pos == nil || pos.PositionSeconds != float64(i+1) {
			t.Fatalf("book %s position = %+v, %v", b.ID, pos, err)
		}
		st, err := store.GetUserBookState(adminUserID, b.ID)
		if err != nil || st == nil || st.Status == database.UserBookStatusFinished {
			t.Fatalf("bookmarked book %s state = %+v, %v (want the recompute's, not a finished seed)", b.ID, st, err)
		}
	}
	for _, b := range played {
		st, err := store.GetUserBookState(adminUserID, b.ID)
		if err != nil || st == nil || st.Status != database.UserBookStatusFinished {
			t.Fatalf("played book %s state = %+v, %v (want finished)", b.ID, st, err)
		}
	}
}
