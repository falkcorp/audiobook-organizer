// file: internal/seriesdenumber/recheck_test.go
// version: 1.0.0
// guid: 52c8e1a7-96d4-4b03-8f2a-0d7b3e6c4a19
// last-edited: 2026-11-17

package seriesdenumber

import "testing"

func seqp(n int) *int { return &n }

func TestRecheck_APositionTakenSinceThePlanHoldsTheMove(t *testing.T) {
	moves := []Move{{BookID: "m1", FromSeriesID: 5, Identity: "b:m1", NewSeq: 3}}
	keep, held := Recheck("Court of Owls", moves, []Book{{ID: "x", Seq: seqp(3)}}, 0)
	if len(keep) != 0 || len(held) != 1 || held[0].Code != HoldCollision || held[0].BookID != "m1" {
		t.Fatalf("keep=%v held=%+v, want the move held as a collision", keep, held)
	}
}

func TestRecheck_ATrashedBookKeepsItsPosition(t *testing.T) {
	// A trashed book is listed in existing like a live one (the caller reads the
	// trash); the decision cannot tell and must not.
	keep, held := Recheck("Court of Owls",
		[]Move{{BookID: "m1", Identity: "b:m1", NewSeq: 2}},
		[]Book{{ID: "t", Seq: seqp(2)}}, 0)
	if len(keep) != 0 || len(held) != 1 {
		t.Fatalf("keep=%v held=%+v", keep, held)
	}
}

func TestRecheck_UnseenMembersHoldEveryMove(t *testing.T) {
	moves := []Move{{BookID: "a", NewSeq: 1}, {BookID: "b", NewSeq: 2}}
	keep, held := Recheck("Court of Owls", moves, nil, 1)
	if len(keep) != 0 || len(held) != 2 || held[0].Code != HoldTargetUnseen {
		t.Fatalf("keep=%v held=%+v", keep, held)
	}
}

func TestRecheck_AFreePositionKeepsTheMove(t *testing.T) {
	moves := []Move{{BookID: "a", Identity: "b:a", NewSeq: 4}}
	keep, held := Recheck("Court of Owls", moves, []Book{{ID: "x", Seq: seqp(3)}, {ID: "y"}}, 0)
	if len(keep) != 1 || len(held) != 0 {
		t.Fatalf("keep=%v held=%+v", keep, held)
	}
}

func TestRecheck_VersionsOfOneBookAreHeldTogether(t *testing.T) {
	moves := []Move{
		{BookID: "v1", Identity: "g:one", NewSeq: 2},
		{BookID: "v2", Identity: "g:one", NewSeq: 2},
	}
	keep, held := Recheck("Court of Owls", moves, []Book{{ID: "x", Seq: seqp(2)}}, 0)
	if len(keep) != 0 || len(held) != 2 {
		t.Fatalf("keep=%v held=%+v, want both versions held", keep, held)
	}
}
