// file: internal/database/user_state_test.go
// version: 1.1.0
// guid: 5d9e2c1a-4b8f-4f70-a7c6-2e8d0f1b9a47
// last-edited: 2026-09-13

package database

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// A Finished write over an unfinished stored row keeps a stamp the caller
// carried in (merge carry, undo restore), rather than dating it now.
func TestSetUserBookState_CarriedStampOverUnfinishedRowIsKept(t *testing.T) {
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if err := store.SetUserBookState(&UserBookState{UserID: "u1", BookID: "b1", Status: UserBookStatusInProgress}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	carried := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := store.SetUserBookState(&UserBookState{UserID: "u1", BookID: "b1", Status: UserBookStatusFinished, FinishedAt: &carried}); err != nil {
		t.Fatalf("carry: %v", err)
	}
	got, err := store.GetUserBookState("u1", "b1")
	if err != nil || got == nil || got.FinishedAt == nil {
		t.Fatalf("get: %+v / %v", got, err)
	}
	if !got.FinishedAt.Equal(carried) {
		t.Errorf("FinishedAt = %v, want the carried %v", got.FinishedAt, carried)
	}
}

// A stored row that cannot be decoded is overwritten, but a Finished write
// over it is not dated now: the unreadable row may already be that finish.
func TestSetUserBookState_UnreadablePrevIsNotStampedNow(t *testing.T) {
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if err := store.db.Set([]byte("ubs:u1:b1"), []byte("{not json"), pebble.Sync); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	if err := store.SetUserBookState(&UserBookState{UserID: "u1", BookID: "b1", Status: UserBookStatusFinished}); err != nil {
		t.Fatalf("set over corrupt row: %v", err)
	}
	got, err := store.GetUserBookState("u1", "b1")
	if err != nil || got == nil {
		t.Fatalf("the corrupt row was not rewritten: %+v / %v", got, err)
	}
	if got.Status != UserBookStatusFinished {
		t.Errorf("Status = %q, want finished", got.Status)
	}
	if got.FinishedAt != nil {
		t.Errorf("FinishedAt = %v, want nil: an unreadable prior row must not date the finish now", got.FinishedAt)
	}
}

func TestUserPosition_Lifecycle(t *testing.T) {
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if err := store.SetUserPosition("u1", "b1", "seg1", 300.5); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := store.SetUserPosition("u1", "b1", "seg2", 125.0); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Wait a tick so the updated_at ordering is strict.
	time.Sleep(2 * time.Millisecond)
	if err := store.SetUserPosition("u1", "b1", "seg3", 45.0); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Get latest across segments — returns whichever has the most
	// recent UpdatedAt.
	latest, err := store.GetUserPosition("u1", "b1")
	if err != nil || latest == nil {
		t.Fatalf("GetUserPosition: %v, %v", latest, err)
	}
	if latest.SegmentID != "seg3" {
		t.Errorf("latest SegmentID = %q, want seg3", latest.SegmentID)
	}

	// List all positions for the book.
	all, err := store.ListUserPositionsForBook("u1", "b1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("got %d positions, want 3", len(all))
	}

	// Different user → empty.
	empty, _ := store.ListUserPositionsForBook("u2", "b1")
	if len(empty) != 0 {
		t.Errorf("different user should have no positions, got %d", len(empty))
	}

	// Clear.
	if err := store.ClearUserPositions("u1", "b1"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	after, _ := store.ListUserPositionsForBook("u1", "b1")
	if len(after) != 0 {
		t.Errorf("after clear: %d positions, want 0", len(after))
	}
}

func TestUserBookState_SetGet(t *testing.T) {
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	s := &UserBookState{
		UserID: "u1", BookID: "b1",
		Status:               UserBookStatusInProgress,
		StatusManual:         false,
		LastActivityAt:       time.Now(),
		LastSegmentID:        "seg1",
		TotalListenedSeconds: 180,
		ProgressPct:          25,
	}
	if err := store.SetUserBookState(s); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := store.GetUserBookState("u1", "b1")
	if err != nil || got == nil {
		t.Fatalf("get: %v / %v", got, err)
	}
	if got.Status != UserBookStatusInProgress {
		t.Errorf("Status = %q, want in_progress", got.Status)
	}
	if got.ProgressPct != 25 {
		t.Errorf("ProgressPct = %d, want 25", got.ProgressPct)
	}
}

func TestUserBookState_StatusIndexMaintained(t *testing.T) {
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// Start as in_progress.
	if err := store.SetUserBookState(&UserBookState{
		UserID: "u1", BookID: "b1", Status: UserBookStatusInProgress,
	}); err != nil {
		t.Fatalf("set 1: %v", err)
	}

	inProgress, _ := store.ListUserBookStatesByStatus("u1", UserBookStatusInProgress, 0, 0)
	if len(inProgress) != 1 {
		t.Errorf("in_progress = %d, want 1", len(inProgress))
	}

	// Transition to finished. The status index should drop the
	// in_progress entry and add a finished one.
	if err := store.SetUserBookState(&UserBookState{
		UserID: "u1", BookID: "b1", Status: UserBookStatusFinished,
	}); err != nil {
		t.Fatalf("set 2: %v", err)
	}
	inProgress2, _ := store.ListUserBookStatesByStatus("u1", UserBookStatusInProgress, 0, 0)
	if len(inProgress2) != 0 {
		t.Errorf("after transition in_progress still = %d, want 0 (stale index)", len(inProgress2))
	}
	finished, _ := store.ListUserBookStatesByStatus("u1", UserBookStatusFinished, 0, 0)
	if len(finished) != 1 {
		t.Errorf("finished = %d, want 1", len(finished))
	}
}

func TestUserBookState_ListByStatus_MultipleBooks(t *testing.T) {
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	for i, bookID := range []string{"b1", "b2", "b3"} {
		status := UserBookStatusInProgress
		if i == 2 {
			status = UserBookStatusFinished
		}
		_ = store.SetUserBookState(&UserBookState{
			UserID: "u1", BookID: bookID, Status: status,
		})
	}

	inProg, _ := store.ListUserBookStatesByStatus("u1", UserBookStatusInProgress, 0, 0)
	if len(inProg) != 2 {
		t.Errorf("u1 in_progress = %d, want 2", len(inProg))
	}
	fin, _ := store.ListUserBookStatesByStatus("u1", UserBookStatusFinished, 0, 0)
	if len(fin) != 1 {
		t.Errorf("u1 finished = %d, want 1", len(fin))
	}

	// Different user sees nothing.
	other, _ := store.ListUserBookStatesByStatus("u2", UserBookStatusInProgress, 0, 0)
	if len(other) != 0 {
		t.Errorf("u2 in_progress = %d, want 0 (per-user isolation)", len(other))
	}
}

func TestListUserPositionsSince(t *testing.T) {
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	_ = store.SetUserPosition("u1", "b1", "s1", 10)
	time.Sleep(5 * time.Millisecond)
	cutoff := time.Now()
	time.Sleep(5 * time.Millisecond)
	_ = store.SetUserPosition("u1", "b2", "s1", 20)
	_ = store.SetUserPosition("u1", "b3", "s1", 30)

	after, err := store.ListUserPositionsSince("u1", cutoff)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(after) != 2 {
		t.Errorf("positions since cutoff = %d, want 2", len(after))
	}
}
