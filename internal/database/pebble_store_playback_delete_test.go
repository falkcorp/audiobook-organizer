// file: internal/database/pebble_store_playback_delete_test.go
// version: 1.0.0
// guid: e055b205-91af-45ee-b047-d926f034607b
// last-edited: 2026-10-05

package database

import "testing"

// DeleteUserBookState removes the row and its status index entry, so the
// book is listed under no status, and deleting a missing row is fine.
func TestDeleteUserBookState(t *testing.T) {
	p := openPlaybackStore(t)
	if err := p.SetUserBookState(&UserBookState{UserID: "u1", BookID: "b1", Status: UserBookStatusFinished, ProgressPct: 100}); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteUserBookState("u1", "b1"); err != nil {
		t.Fatal(err)
	}
	s, err := p.GetUserBookState("u1", "b1")
	if err != nil || s != nil {
		t.Fatalf("state after delete = %+v, %v; want nil, nil", s, err)
	}
	rows, err := p.ListUserBookStatesByStatus("u1", UserBookStatusFinished, 10, 0)
	if err != nil || len(rows) != 0 {
		t.Fatalf("finished list after delete = %d rows, %v; want none", len(rows), err)
	}
	if err := p.DeleteUserBookState("u1", "b1"); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if err := p.DeleteUserBookState("", "b1"); err == nil {
		t.Fatal("delete with no user must fail")
	}
}
