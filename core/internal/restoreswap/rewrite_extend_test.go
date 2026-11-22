// file: core/internal/restoreswap/rewrite_extend_test.go
// version: 1.0.0
// guid: 6992fb52-bada-46ac-ab58-98984b57f33a
// last-edited: 2026-11-22

package restoreswap

import (
	"context"
	"testing"
)

// TestRewriteFinishAsksForStartTimeBeforeTheDeletion (05-health §5H.3.4):
// phase 4 deletes the old store, tens of GB in one filesystem call that
// shows no progress, so it asks for start time first and gets its own
// budget; it asks once, while the set-aside is still there, and not at all
// for a repeat that has nothing left to delete.
func TestRewriteFinishAsksForStartTimeBeforeTheDeletion(t *testing.T) {
	db := dbPathOf("state")
	mem, s, in, aside := rewriteSwapped(t, db)
	var steps []string
	var presentAtCall []bool
	s.o.ExtendStart = func(step string) {
		steps = append(steps, step)
		ok, _ := exists(mem, aside)
		presentAtCall = append(presentAtCall, ok)
	}
	if err := s.finishRewriteSwap(context.Background(), in.ID, rewriteStepID, db); err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0] != stepRewriteDelete {
		t.Fatalf("ExtendStart calls = %v, want one %q", steps, stepRewriteDelete)
	}
	if !presentAtCall[0] {
		t.Error("ExtendStart was called after the set-aside was already deleted")
	}
	if ok, _ := exists(mem, aside); ok {
		t.Error("the set-aside survived phase 4")
	}
	if err := s.finishRewriteSwap(context.Background(), in.ID, rewriteStepID, db); err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 {
		t.Errorf("a repeated phase 4 made %d more ExtendStart calls", len(steps)-1)
	}
}

// TestRewriteFinishWithoutExtendStart: the offline swaps run under no start
// timeout and pass no ExtendStart; phase 4 must not need one.
func TestRewriteFinishWithoutExtendStart(t *testing.T) {
	db := dbPathOf("outside")
	_, s, in, _ := rewriteSwapped(t, db)
	if s.o.ExtendStart != nil {
		t.Fatal("fixture: the swapper has an ExtendStart")
	}
	if err := s.finishRewriteSwap(context.Background(), in.ID, rewriteStepID, db); err != nil {
		t.Fatal(err)
	}
}
