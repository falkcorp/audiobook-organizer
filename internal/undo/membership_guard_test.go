// file: internal/undo/membership_guard_test.go
// version: 1.2.1
// guid: 352f989b-8de0-44c0-b537-7e18620e3abf
// last-edited: 2026-10-23

package undo

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// l0 = [S, L, X]. L left the list without a record that names its place (the
// owner took it out by hand, say), X is retired into S with the anchor prev L.
// Reverting X: the backward walk dead-ends at L (no record names its place), so
// the slot is unbounded on one side and must not be guessed; the head is not a
// bound the walk reached.
func TestSlot_DeadEndBackwardWalkIsNotGuessed(t *testing.T) {
	t.Parallel()
	t0 := time.Unix(1_700_000_000, 0)
	enc := func(r MembershipRecord) string { v, _ := EncodeMembership(r); return v }
	recX := MembershipRecord{Index: 1, SurvivorWasMember: true, Anchored: true, Prev: "L"}
	held := UserContentField(HeldKind(UserContentCollection), "L1")
	main := UserContentField(UserContentCollection, "L1")
	mX := &database.OperationChange{ID: "op2/m", OperationID: "op2", BookID: "S", ChangeType: ChangeTypeUserContentFollow, FieldName: held, OldValue: enc(recX), NewValue: "X", CreatedAt: t0.Add(time.Second)}
	rX := &database.OperationChange{ID: "op2/r", OperationID: "op2", BookID: "X", ChangeType: ChangeTypeUserContentFollow, FieldName: main, OldValue: enc(recX), NewValue: "S", CreatedAt: t0.Add(time.Second)}
	rows := []*database.OperationChange{mX, rX}
	g := NewMembershipGraph("S", UserContentCollection, "L1")
	g.SetRow(rX)
	g.AddRows(rows)
	stand, _ := SurvivorStandFor(rows, rX, UserContentCollection, "L1")
	next, res, err := RestoreMember([]string{"S"}, "X", "S", recX, MemberRole{Stand: stand}, g)
	if err != nil {
		t.Fatal(err)
	}
	if res.Note == "" {
		t.Fatalf("placed X at %v with no note: the walk before X dead-ends at L, which no record places", next)
	}
}

// longHistory is a list [z] and a book p0 whose place rests on a chain of n
// further retired books p1..pn, each recorded right after the next one and the
// last right after the listed book z.
func longHistory(n int) (g *MembershipGraph) {
	t0 := time.Unix(1_700_000_000, 0)
	var rows []*database.OperationChange
	var row0 *database.OperationChange
	for i := 0; i <= n; i++ {
		prev := "z"
		if i < n {
			prev = fmt.Sprintf("p%d", i+1)
		}
		rec, _ := EncodeMembership(MembershipRecord{Index: 1, SurvivorWasMember: true, Anchored: true, Prev: prev})
		row := &database.OperationChange{ID: fmt.Sprintf("r%d", i), OperationID: fmt.Sprintf("op%d", i), BookID: fmt.Sprintf("p%d", i),
			ChangeType: ChangeTypeUserContentFollow, FieldName: UserContentField(UserContentCollection, "L"),
			OldValue: rec, NewValue: "S", CreatedAt: t0.Add(time.Duration(i) * time.Second)}
		rows = append(rows, row)
		if i == 0 {
			row0 = row
		}
	}
	g = NewMembershipGraph("S", UserContentCollection, "L")
	g.SetRow(row0)
	g.AddRows(rows)
	return g
}

// The graph holds the whole journal of the list, so a history of any length is
// placed: there is no read cap to stop it (the earlier per-book loader stopped
// past 128 journals and fell back to the index).
func TestSlot_LongHistoriesAreAlwaysPlaced(t *testing.T) {
	t.Parallel()
	for _, n := range []int{1, 2, 127, 128, 129, 130, 400} {
		g := longHistory(n)
		at, ok := g.slot([]string{"z"}, "p0")
		if !ok || at != 1 {
			t.Fatalf("n=%d: placed at %d ok=%v, want 1 (right after z)", n, at, ok)
		}
	}
}

// H3: a carry journaled its marker and row and the process died before the list
// write (journal first, so the rows are never voided). Another operation is
// reverted while that marker stands, then the crashed one: the survivor must not
// be left in the list for a retire that never wrote.
func TestCrashedCarryWithAJournaledMarkerNeverStrandsASurvivor(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(5, 6))
	silent, total := 0, 0
	for range 2000 {
		l0 := randomL0(rng)
		pool := nonSurvivors(l0)
		rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		sim := newCarrySim(t, "S", l0)
		sim.retire("opA", pool[0])
		total++
		_, rec, _ := CarryMembership(sim.list, pool[1], "S")
		val, _ := EncodeMembership(rec)
		kind := TookKind(UserContentCollection)
		if rec.SurvivorWasMember {
			kind = HeldKind(UserContentCollection)
		}
		now := sim.tick()
		ghostM := &database.OperationChange{ID: "opC/m", OperationID: "opC", BookID: "S", ChangeType: ChangeTypeUserContentFollow,
			FieldName: UserContentField(kind, "L"), OldValue: val, NewValue: pool[1], CreatedAt: now}
		ghostR := &database.OperationChange{ID: "opC/r", OperationID: "opC", BookID: pool[1], ChangeType: ChangeTypeUserContentFollow,
			FieldName: UserContentField(UserContentCollection, "L"), OldValue: val, NewValue: "S", CreatedAt: now}
		sim.survivorJ = append(sim.survivorJ, ghostM)
		sim.byRetired[pool[1]] = [2]*database.OperationChange{ghostR, ghostM}
		if err := sim.revert(pool[0]); err != nil {
			t.Fatalf("revert of the first operation: %v", err)
		}
		if err := sim.revert(pool[1]); err != nil && !errors.Is(err, ErrAlreadyRestored) {
			t.Fatalf("revert of the crashed operation: %v", err)
		}
		if !slices.Equal(sim.list, l0) && len(sim.notes) == 0 {
			silent++
		}
	}
	t.Logf("crashed carry: total=%d silent=%d", total, silent)
	if silent > 0 {
		t.Errorf("%d silent wrong lists after reverting the crashed operation too", silent)
	}
}
