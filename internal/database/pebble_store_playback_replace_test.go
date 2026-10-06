// file: internal/database/pebble_store_playback_replace_test.go
// version: 1.0.0
// guid: 1f9490d4-811f-4a9c-a1a1-24fde3fbc539
// last-edited: 2026-10-05

package database

import (
	"testing"
	"time"
)

// ReplaceUserPositions leaves exactly the given rows, keeps each UpdatedAt
// (zero included), does not touch another book's or user's rows, and
// writes nothing when a row is invalid.
func TestReplaceUserPositions(t *testing.T) {
	p := openPlaybackStore(t)
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, seg := range []string{"a", "b", "c"} {
		if err := p.SetUserPositionAt("u1", "b1", seg, 10, t0); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.SetUserPositionAt("u1", "b2", "a", 5, t0); err != nil {
		t.Fatal(err)
	}
	if err := p.SetUserPositionAt("u2", "b1", "a", 6, t0); err != nil {
		t.Fatal(err)
	}
	want := []UserPosition{{SegmentID: "b", PositionSeconds: 99, UpdatedAt: t0.Add(time.Hour)}, {SegmentID: "d", PositionSeconds: 7}}
	if err := p.ReplaceUserPositions("u1", "b1", want); err != nil {
		t.Fatal(err)
	}
	got, err := p.ListUserPositionsForBook("u1", "b1")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]UserPosition{}
	for _, r := range got {
		by[r.SegmentID] = r
	}
	if len(by) != 2 || by["b"].PositionSeconds != 99 || !by["b"].UpdatedAt.Equal(t0.Add(time.Hour)) || by["d"].PositionSeconds != 7 || !by["d"].UpdatedAt.IsZero() {
		t.Fatalf("rows after replace = %+v", got)
	}
	for _, other := range [][2]string{{"u1", "b2"}, {"u2", "b1"}} {
		rows, err := p.ListUserPositionsForBook(other[0], other[1])
		if err != nil || len(rows) != 1 {
			t.Fatalf("%v rows = %v, %v; want their own one untouched", other, rows, err)
		}
	}
	if err := p.ReplaceUserPositions("u1", "b1", []UserPosition{{SegmentID: "x", PositionSeconds: 1}, {SegmentID: ""}}); err == nil {
		t.Fatal("a row with no segment must fail")
	}
	if got2, _ := p.ListUserPositionsForBook("u1", "b1"); len(got2) != 2 {
		t.Fatalf("a refused replace changed the rows: %+v", got2)
	}
	if err := p.ReplaceUserPositions("u1", "b1", nil); err != nil {
		t.Fatal(err)
	}
	if got3, _ := p.ListUserPositionsForBook("u1", "b1"); len(got3) != 0 {
		t.Fatalf("empty replace left %+v", got3)
	}
}

// E: SetUserPositionAt keeps a zero UpdatedAt as zero (an undated legacy
// row), instead of stamping it "now" and making it the freshest listen.
func TestSetUserPositionAt_KeepsZeroUpdatedAt(t *testing.T) {
	p := openPlaybackStore(t)
	if err := p.SetUserPositionAt("u1", "b1", "a", 4000, time.Time{}); err != nil {
		t.Fatal(err)
	}
	rows, err := p.ListUserPositionsForBook("u1", "b1")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %v, %v", rows, err)
	}
	if !rows[0].UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt = %v, want zero (undated)", rows[0].UpdatedAt)
	}
}
