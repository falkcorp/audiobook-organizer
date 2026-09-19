// file: internal/database/pebble_store_playback_corrupt_test.go
// version: 1.0.0
// guid: 2f7c9a1e-5b36-4d80-8e4a-a1c6d3b9f025
// last-edited: 2026-09-19

package database

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

func openPlaybackStore(t *testing.T) *PebbleStore {
	t.Helper()
	p, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

// A position row that does not decode is NOT "no position": reading it as
// empty let callers merge against 0 and rewind the listener.
func TestUserPositions_UndecodableRowIsAnError(t *testing.T) {
	p := openPlaybackStore(t)
	if err := p.SetUserPosition("u1", "b1", "abs", 3600); err != nil {
		t.Fatal(err)
	}
	if err := p.db.Set([]byte("upos:u1:b1:zz"), []byte("{not json"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if pos, err := p.GetUserPosition("u1", "b1"); !errors.Is(err, ErrUserPositionUndecodable) {
		t.Fatalf("GetUserPosition over a corrupt row = %v, %v; want ErrUserPositionUndecodable", pos, err)
	}
	if list, err := p.ListUserPositionsForBook("u1", "b1"); !errors.Is(err, ErrUserPositionUndecodable) {
		t.Fatalf("ListUserPositionsForBook over a corrupt row = %v, %v; want ErrUserPositionUndecodable", list, err)
	}
}

// Zero matching rows stays (nil, nil).
func TestUserPositions_NoRowsIsNilNil(t *testing.T) {
	p := openPlaybackStore(t)
	if pos, err := p.GetUserPosition("u1", "b1"); pos != nil || err != nil {
		t.Fatalf("GetUserPosition with no rows = %v, %v; want nil, nil", pos, err)
	}
	if list, err := p.ListUserPositionsForBook("u1", "b1"); list != nil || err != nil {
		t.Fatalf("ListUserPositionsForBook with no rows = %v, %v; want nil, nil", list, err)
	}
}

// Clearing deletes by key, so a corrupt row does not make a reset (or the
// repair that relies on it) impossible.
func TestClearUserPositions_RemovesUndecodableRows(t *testing.T) {
	p := openPlaybackStore(t)
	if err := p.SetUserPosition("u1", "b1", "abs", 3600); err != nil {
		t.Fatal(err)
	}
	if err := p.db.Set([]byte("upos:u1:b1:zz"), []byte("{not json"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := p.ClearUserPositions("u1", "b1"); err != nil {
		t.Fatalf("ClearUserPositions: %v", err)
	}
	if pos, err := p.GetUserPosition("u1", "b1"); pos != nil || err != nil {
		t.Fatalf("after clear = %v, %v; want nil, nil", pos, err)
	}
}

// Overwriting an UNDECODABLE state row (the repair path) must not leave the
// old status index entry behind: the old status is unknown, so every status
// entry for the book except the new one is dropped.
func TestSetUserBookState_OverUndecodableRowDropsStaleStatusIndex(t *testing.T) {
	p := openPlaybackStore(t)
	if err := p.SetUserBookState(&UserBookState{UserID: "u1", BookID: "b1", Status: UserBookStatusFinished}); err != nil {
		t.Fatal(err)
	}
	if err := p.db.Set([]byte("ubs:u1:b1"), []byte("{not json"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := p.SetUserBookState(&UserBookState{UserID: "u1", BookID: "b1", Status: UserBookStatusInProgress}); err != nil {
		t.Fatal(err)
	}
	if got, err := p.ListUserBookStatesByStatus("u1", UserBookStatusFinished, 10, 0); err != nil || len(got) != 0 {
		t.Fatalf("finished list after overwrite = %+v, %v; want empty (stale index entry)", got, err)
	}
	if got, err := p.ListUserBookStatesByStatus("u1", UserBookStatusInProgress, 10, 0); err != nil || len(got) != 1 {
		t.Fatalf("in_progress list = %+v, %v; want the book", got, err)
	}
}
