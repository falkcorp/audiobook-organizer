// file: internal/readstatus/readstatus_test.go
// version: 1.0.1
// last-edited: 2026-09-19
// guid: 9e2a8c4d-5b1f-4f70-a7c6-2d8e0f1b9a57

package readstatus

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// setupReadTestStore creates a PebbleStore + seeds one book with
// three 10-minute (600-second) segments. Total book duration = 1800s.
func setupReadTestStore(t *testing.T) database.Store {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// The owner book must exist: book_file writers refuse rows whose book
	// is missing (never orphan a book_file row).
	if _, err := store.CreateBook(&database.Book{ID: "b1", Title: "Read Status Book", FilePath: "/tmp/b1"}); err != nil {
		t.Fatalf("create book: %v", err)
	}

	for i, segID := range []string{"s1", "s2", "s3"} {
		if err := store.CreateBookFile(&database.BookFile{
			ID: segID, BookID: "b1",
			FilePath:    "/tmp/" + segID,
			TrackNumber: i + 1,
			Duration:    600, // seconds
		}); err != nil {
			t.Fatalf("create bookfile %s: %v", segID, err)
		}
	}
	return store
}

func TestRecompute_Unstarted(t *testing.T) {
	store := setupReadTestStore(t)
	state, err := RecomputeUserBookState(store, "u1", "b1")
	if err != nil {
		t.Fatalf("recompute: %v", err)
	}
	// No positions, no existing state → nil / no-op.
	if state != nil {
		t.Errorf("expected nil state for never-touched book, got %+v", state)
	}
}

func TestRecompute_InProgress(t *testing.T) {
	store := setupReadTestStore(t)
	// Listened 300s of the first segment (of 1800 total = 16.6%).
	_ = store.SetUserPosition("u1", "b1", "s1", 300)

	state, err := RecomputeUserBookState(store, "u1", "b1")
	if err != nil || state == nil {
		t.Fatalf("recompute: %v / %v", state, err)
	}
	if state.Status != database.UserBookStatusInProgress {
		t.Errorf("Status = %q, want in_progress", state.Status)
	}
	if state.ProgressPct != 16 {
		t.Errorf("ProgressPct = %d, want 16", state.ProgressPct)
	}
	if state.LastSegmentID != "s1" {
		t.Errorf("LastSegmentID = %q, want s1", state.LastSegmentID)
	}
}

func TestRecompute_AutoFinished(t *testing.T) {
	store := setupReadTestStore(t)
	// Three segments fully played = 1800s / 1800s = 100%.
	_ = store.SetUserPosition("u1", "b1", "s1", 600)
	_ = store.SetUserPosition("u1", "b1", "s2", 600)
	_ = store.SetUserPosition("u1", "b1", "s3", 600)

	state, err := RecomputeUserBookState(store, "u1", "b1")
	if err != nil || state == nil {
		t.Fatalf("recompute: %v / %v", state, err)
	}
	if state.Status != database.UserBookStatusFinished {
		t.Errorf("Status = %q, want finished", state.Status)
	}
	if state.ProgressPct != 100 {
		t.Errorf("ProgressPct = %d, want 100", state.ProgressPct)
	}
}

func TestRecompute_ThresholdBoundary(t *testing.T) {
	store := setupReadTestStore(t)
	// Exactly 95% — right at the threshold. 1800 * 0.95 = 1710.
	_ = store.SetUserPosition("u1", "b1", "s1", 600)
	_ = store.SetUserPosition("u1", "b1", "s2", 600)
	_ = store.SetUserPosition("u1", "b1", "s3", 510) // 600+600+510 = 1710

	state, _ := RecomputeUserBookState(store, "u1", "b1")
	if state == nil {
		t.Fatal("state nil")
	}
	if state.Status != database.UserBookStatusFinished {
		t.Errorf("At boundary threshold 95%%, Status = %q, want finished", state.Status)
	}
}

func TestRecompute_OverflowCapped(t *testing.T) {
	store := setupReadTestStore(t)
	// Client reports position past end of segment (e.g. seeked to
	// end). Should be capped at segment duration, not double-count.
	_ = store.SetUserPosition("u1", "b1", "s1", 9999)

	state, _ := RecomputeUserBookState(store, "u1", "b1")
	if state == nil {
		t.Fatal("state nil")
	}
	if state.TotalListenedSeconds != 600 {
		t.Errorf("TotalListenedSeconds = %g, want 600 (capped)", state.TotalListenedSeconds)
	}
}

func TestRecompute_ManualOverrideNotClobbered(t *testing.T) {
	store := setupReadTestStore(t)
	// User manually marks book as abandoned.
	_, _ = SetManualStatus(store, "u1", "b1", database.UserBookStatusAbandoned)

	// Later they listen to a bit. Recompute must NOT flip status
	// away from abandoned.
	_ = store.SetUserPosition("u1", "b1", "s1", 300)
	state, _ := RecomputeUserBookState(store, "u1", "b1")
	if state.Status != database.UserBookStatusAbandoned {
		t.Errorf("Manual status clobbered by recompute: %q, want abandoned", state.Status)
	}
	// But the computed progress fields DO refresh.
	if state.ProgressPct == 0 {
		t.Error("Manual flag should preserve status but still refresh progress")
	}
}

func TestSetManualStatus_ClearReturnsToAuto(t *testing.T) {
	store := setupReadTestStore(t)

	// Play a bit, auto-status = in_progress.
	_ = store.SetUserPosition("u1", "b1", "s1", 300)
	_, _ = RecomputeUserBookState(store, "u1", "b1")

	// Manually force to finished.
	_, _ = SetManualStatus(store, "u1", "b1", database.UserBookStatusFinished)
	s, _ := store.GetUserBookState("u1", "b1")
	if s.Status != database.UserBookStatusFinished || !s.StatusManual {
		t.Fatalf("after manual set: %+v", s)
	}

	// Clear manual — should auto-recompute back to in_progress.
	state, _ := SetManualStatus(store, "u1", "b1", "")
	if state == nil {
		t.Fatal("state nil after clear")
	}
	if state.StatusManual {
		t.Error("StatusManual should be false after clear")
	}
	if state.Status != database.UserBookStatusInProgress {
		t.Errorf("Status = %q, want in_progress after clearing manual override", state.Status)
	}
}

func TestRecompute_PerUserIsolation(t *testing.T) {
	store := setupReadTestStore(t)

	_ = store.SetUserPosition("u1", "b1", "s1", 600)
	_ = store.SetUserPosition("u2", "b1", "s1", 30)

	s1, _ := RecomputeUserBookState(store, "u1", "b1")
	s2, _ := RecomputeUserBookState(store, "u2", "b1")

	if s1.Status != database.UserBookStatusInProgress {
		t.Errorf("u1 status = %q", s1.Status)
	}
	if s2.Status != database.UserBookStatusInProgress {
		t.Errorf("u2 status = %q", s2.Status)
	}
	if s1.TotalListenedSeconds == s2.TotalListenedSeconds {
		t.Errorf("per-user state not isolated: u1=%g u2=%g",
			s1.TotalListenedSeconds, s2.TotalListenedSeconds)
	}
}
