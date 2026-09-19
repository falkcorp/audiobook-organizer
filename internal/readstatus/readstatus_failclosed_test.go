// file: internal/readstatus/readstatus_failclosed_test.go
// version: 1.1.0
// guid: 3e8b5d17-6a2c-4f91-8d40-c2a7e9f5b6d1
// last-edited: 2026-09-19

package readstatus

import (
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// unreadableStateStore fails GetUserBookState the way an I/O or decode error
// does, as opposed to "no row yet" (nil, nil).
type unreadableStateStore struct{ Store }

func (unreadableStateStore) GetUserBookState(string, string) (*database.UserBookState, error) {
	return nil, errors.New("transient read failure")
}

// seedTombstoned stores a state row carrying fields a fresh row would lose.
func seedTombstoned(t *testing.T) database.Store {
	t.Helper()
	store := setupReadTestStore(t)
	_ = store.SetUserPosition("u1", "b1", "s1", 300)
	reset := time.Now().Add(-time.Hour)
	if err := store.SetUserBookState(&database.UserBookState{UserID: "u1", BookID: "b1",
		Status: database.UserBookStatusInProgress, ProgressResetAt: &reset,
		HideFromContinueListening: true, StatusManual: true}); err != nil {
		t.Fatal(err)
	}
	return store
}

func assertStateIntact(t *testing.T, store database.Store) {
	t.Helper()
	st, err := store.GetUserBookState("u1", "b1")
	if err != nil || st == nil || st.ProgressResetAt == nil || !st.HideFromContinueListening || !st.StatusManual {
		t.Fatalf("stored state was overwritten after an unreadable read: %+v, %v", st, err)
	}
}

func TestRecompute_UnreadableStateFailsClosed(t *testing.T) {
	store := seedTombstoned(t)
	_, err := RecomputeUserBookState(unreadableStateStore{store}, "u1", "b1")
	if !errors.Is(err, ErrStateUnreadable) {
		t.Fatalf("recompute over an unreadable state: err = %v, want ErrStateUnreadable", err)
	}
	assertStateIntact(t, store)
}

func TestSetManualStatus_UnreadableStateFailsClosed(t *testing.T) {
	for _, status := range []string{database.UserBookStatusFinished, ""} {
		store := seedTombstoned(t)
		_, err := SetManualStatus(unreadableStateStore{store}, "u1", "b1", status)
		if !errors.Is(err, ErrStateUnreadable) {
			t.Fatalf("SetManualStatus(%q) over an unreadable state: err = %v, want ErrStateUnreadable", status, err)
		}
		assertStateIntact(t, store)
	}
}

// unreadablePositionsStore fails the positions read the same way.
type unreadablePositionsStore struct{ Store }

func (unreadablePositionsStore) ListUserPositionsForBook(string, string) ([]database.UserPosition, error) {
	return nil, errors.New("transient read failure")
}

func TestRecompute_UnreadablePositionsFailClosed(t *testing.T) {
	store := seedTombstoned(t)
	_, err := RecomputeUserBookState(unreadablePositionsStore{store}, "u1", "b1")
	if !errors.Is(err, ErrPositionsUnreadable) {
		t.Fatalf("recompute over unreadable positions: err = %v, want ErrPositionsUnreadable", err)
	}
	assertStateIntact(t, store)
}

// Clearing a manual status must not drop the flag when the recompute it
// needs cannot read the positions.
func TestClearManualStatus_UnreadablePositionsKeepsTheFlag(t *testing.T) {
	store := seedTombstoned(t)
	_, err := SetManualStatus(unreadablePositionsStore{store}, "u1", "b1", "")
	if !errors.Is(err, ErrPositionsUnreadable) {
		t.Fatalf("clear over unreadable positions: err = %v, want ErrPositionsUnreadable", err)
	}
	assertStateIntact(t, store)
}
