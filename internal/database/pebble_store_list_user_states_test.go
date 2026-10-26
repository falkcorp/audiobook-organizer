// file: internal/database/pebble_store_list_user_states_test.go
// version: 1.0.1
// guid: 3b8f6d02-91c4-4e57-a6d3-5c0e2f7a9b14
// last-edited: 2026-10-26

package database

import (
	"context"
	"errors"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

func TestListUserBookStates_ReturnsOnlyThatUsersRows(t *testing.T) {
	p := openPlaybackStore(t)
	for _, s := range []UserBookState{
		{UserID: "alice", BookID: "b1", Status: UserBookStatusFinished, ProgressPct: 100},
		{UserID: "alice", BookID: "b2", Status: UserBookStatusInProgress, ProgressPct: 40},
		{UserID: "bob", BookID: "b1", Status: UserBookStatusUnstarted},
		// "alice2" shares the prefix text "alice": the scan is bounded by the
		// separator, so it must not leak into alice's map.
		{UserID: "alice2", BookID: "b9", Status: UserBookStatusFinished},
	} {
		s := s
		if err := p.SetUserBookState(&s); err != nil {
			t.Fatal(err)
		}
	}
	got, err := p.ListUserBookStates(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["b1"] == nil || got["b1"].Status != UserBookStatusFinished ||
		got["b2"] == nil || got["b2"].ProgressPct != 40 {
		t.Fatalf("alice's rows = %+v, want exactly b1 (finished) and b2 (40%%)", got)
	}
	if empty, err := p.ListUserBookStates(context.Background(), "nobody"); err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("a user with no rows = %v, %v; want an empty non-nil map and no error", empty, err)
	}
}

func TestListUserBookStates_RejectsAnIdThatCouldReadAnotherUsersRows(t *testing.T) {
	p := openPlaybackStore(t)
	for _, id := range []string{"", "a:b"} {
		if _, err := p.ListUserBookStates(context.Background(), id); err == nil {
			t.Errorf("ListUserBookStates(%q) succeeded; an empty id means no viewer and a ':' id crosses the key separator", id)
		}
	}
}

// An unreadable row is an error, never "no state": a caller that evaluated it
// as a nil state would match the book under any negated or unstarted filter.
func TestListUserBookStates_UndecodableRowIsAnError(t *testing.T) {
	p := openPlaybackStore(t)
	if err := p.SetUserBookState(&UserBookState{UserID: "alice", BookID: "b1", Status: UserBookStatusFinished}); err != nil {
		t.Fatal(err)
	}
	if err := p.db.Set([]byte("ubs:alice:b2"), []byte("{not json"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	got, err := p.ListUserBookStates(context.Background(), "alice")
	if !errors.Is(err, ErrUserBookStateUndecodable) || got != nil {
		t.Fatalf("ListUserBookStates over a corrupt row = %v, %v; want nil and ErrUserBookStateUndecodable", got, err)
	}
}

// A request that has gone away stops the scan: the evaluator does not wait on a
// read nobody will use. A cancelled context is the error, and no partial map is
// returned for a caller to mistake for the viewer's whole state.
func TestListUserBookStates_StopsForACancelledContext(t *testing.T) {
	p := openPlaybackStore(t)
	for _, id := range []string{"b1", "b2", "b3"} {
		if err := p.SetUserBookState(&UserBookState{UserID: "alice", BookID: id, Status: UserBookStatusFinished}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := p.ListUserBookStates(ctx, "alice")
	if !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("ListUserBookStates with a cancelled context = %v, %v; want nil and context.Canceled", got, err)
	}
}
