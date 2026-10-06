// file: internal/readstatus/readstatus_undated_test.go
// version: 1.0.0
// guid: ae477977-e81a-4ed8-9798-de127c538696
// last-edited: 2026-10-06

package readstatus

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func timestampWriter(t *testing.T, store database.Store) database.UserPositionTimestampWriter {
	t.Helper()
	w, ok := database.AsCapability[database.UserPositionTimestampWriter](store)
	if !ok {
		t.Fatal("store cannot write a timestamped position")
	}
	return w
}

// #3777 review NIT: a recompute over positions that carry no timestamp
// (legacy rows) used to write a state with no LastActivityAt, which the merge
// rule cannot place in time. It is dated when the recompute runs.
func TestRecompute_UndatedPositionsGetARecordTimestamp(t *testing.T) {
	store := setupReadTestStore(t)
	if err := timestampWriter(t, store).SetUserPositionAt("u1", "b1", "s1", 300, time.Time{}); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	state, err := RecomputeUserBookState(store, "u1", "b1")
	if err != nil {
		t.Fatalf("recompute: %v", err)
	}
	after := time.Now()
	if state.LastActivityAt.IsZero() || state.LastActivityAt.Before(before) || state.LastActivityAt.After(after) {
		t.Fatalf("LastActivityAt = %v, want the recompute's time in [%v, %v]", state.LastActivityAt, before, after)
	}
	stored, err := store.GetUserBookState("u1", "b1")
	if err != nil {
		t.Fatal(err)
	}
	if !stored.LastActivityAt.Equal(state.LastActivityAt) {
		t.Fatalf("stored LastActivityAt = %v, want %v", stored.LastActivityAt, state.LastActivityAt)
	}
}

// A dated position still dates the record by its own time, not "now".
func TestRecompute_DatedPositionKeepsItsTime(t *testing.T) {
	store := setupReadTestStore(t)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if err := timestampWriter(t, store).SetUserPositionAt("u1", "b1", "s2", 120, at); err != nil {
		t.Fatal(err)
	}
	state, err := RecomputeUserBookState(store, "u1", "b1")
	if err != nil {
		t.Fatalf("recompute: %v", err)
	}
	if !state.LastActivityAt.Equal(at) {
		t.Fatalf("LastActivityAt = %v, want the position's %v", state.LastActivityAt, at)
	}
}

// A stored row with no positions holds no listening to date; stamping it
// "now" would make a drained merge residue the newest side of the next merge.
func TestRecompute_NoPositionsIsNotDated(t *testing.T) {
	store := setupReadTestStore(t)
	if err := store.SetUserBookState(&database.UserBookState{UserID: "u1", BookID: "b1", HideFromContinueListening: true}); err != nil {
		t.Fatal(err)
	}
	state, err := RecomputeUserBookState(store, "u1", "b1")
	if err != nil {
		t.Fatalf("recompute: %v", err)
	}
	if !state.LastActivityAt.IsZero() {
		t.Fatalf("LastActivityAt = %v, want zero for a row with no positions", state.LastActivityAt)
	}
}
