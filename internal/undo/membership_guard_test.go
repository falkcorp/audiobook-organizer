// file: internal/undo/membership_guard_test.go
// version: 1.1.0
// guid: 352f989b-8de0-44c0-b537-7e18620e3abf
// last-edited: 2026-10-22

package undo

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// l0 = [S, L, X]. L was retired into S by a record without anchors
// (Anchored false), X is retired into S with anchors (prev L). Reverting X: the
// backward walk dead-ends at L (no record names its place), so the slot is
// unbounded on one side and must not be guessed; the head is not a bound the
// walk reached.
func TestSlot_DeadEndBackwardWalkIsNotGuessed(t *testing.T) {
	t.Parallel()
	t0 := time.Unix(1_700_000_000, 0)
	enc := func(r MembershipRecord) string { v, _ := EncodeMembership(r); return v }
	recL := MembershipRecord{Index: 1, SurvivorWasMember: true} // legacy, no anchors
	recX := MembershipRecord{Index: 1, SurvivorWasMember: true, Anchored: true, Prev: "L"}
	held := UserContentField(HeldKind(UserContentCollection), "L1")
	main := UserContentField(UserContentCollection, "L1")
	mL := &database.OperationChange{ID: "op1/m", OperationID: "op1", BookID: "S", ChangeType: ChangeTypeUserContentFollow, FieldName: held, OldValue: enc(recL), NewValue: "L", CreatedAt: t0}
	rL := &database.OperationChange{ID: "op1/r", OperationID: "op1", BookID: "L", ChangeType: ChangeTypeUserContentFollow, FieldName: main, OldValue: enc(recL), NewValue: "S", CreatedAt: t0}
	mX := &database.OperationChange{ID: "op2/m", OperationID: "op2", BookID: "S", ChangeType: ChangeTypeUserContentFollow, FieldName: held, OldValue: enc(recX), NewValue: "X", CreatedAt: t0.Add(time.Second)}
	rX := &database.OperationChange{ID: "op2/r", OperationID: "op2", BookID: "X", ChangeType: ChangeTypeUserContentFollow, FieldName: main, OldValue: enc(recX), NewValue: "S", CreatedAt: t0.Add(time.Second)}
	j := map[string][]*database.OperationChange{"S": {mL, mX}, "L": {rL}, "X": {rX}}
	g := NewMembershipGraph("S", UserContentCollection, "L1")
	g.SetRow(rX)
	g.AddJournal("S", j["S"])
	g.SetJournalLoader(func(b string) ([]*database.OperationChange, error) { return j[b], nil })
	stand, _ := SurvivorStandFor(j["S"], rX, UserContentCollection, "L1")
	next, note, err := RestoreMembership([]string{"S"}, "X", "S", recX, stand, g)
	if err != nil {
		t.Fatal(err)
	}
	if note == "" {
		t.Fatalf("placed X at %v with no note: the walk before X dead-ends at L, which no record places", next)
	}
}

// longHistory is a list [z] and a book p0 whose place rests on a chain of n
// further retired books p1..pn, each recorded right after the next one and the
// last right after the listed book z, every record in the journal of its own
// book only (so the graph has to read them one by one).
func longHistory(n int) (g *MembershipGraph, loads func() int) {
	t0 := time.Unix(1_700_000_000, 0)
	journals := map[string][]*database.OperationChange{}
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
		journals[row.BookID] = []*database.OperationChange{row}
		if i == 0 {
			row0 = row
		}
	}
	g = NewMembershipGraph("S", UserContentCollection, "L")
	g.SetRow(row0)
	reads := 0
	g.SetJournalLoader(func(b string) ([]*database.OperationChange, error) { reads++; return journals[b], nil })
	return g, func() int { return reads }
}

// The most journals one placement reads is maxJournalLoads: a history that needs
// exactly that many is placed, one that needs one more is not, and says it is
// the cap that stopped it (so the report names the cause).
func TestSlot_JournalReadCap(t *testing.T) {
	t.Parallel()
	lastOK := -1
	for n := 1; n <= maxJournalLoads+10; n++ {
		g, reads := longHistory(n)
		at, ok := g.slot([]string{"z"}, "p0")
		switch {
		case ok:
			lastOK = n
			if at != 1 {
				t.Fatalf("n=%d: placed at %d, want 1 (right after z)", n, at)
			}
			if reads() > maxJournalLoads {
				t.Fatalf("n=%d: %d journals read, cap %d", n, reads(), maxJournalLoads)
			}
		case g.Err() == nil:
			t.Fatalf("n=%d: not placed and no reason given", n)
		case !strings.Contains(g.Err().Error(), fmt.Sprint(maxJournalLoads)):
			t.Fatalf("n=%d: the reason does not name the cap: %v", n, g.Err())
		default:
			if reads() > maxJournalLoads {
				t.Fatalf("n=%d: %d journals read past the cap %d", n, reads(), maxJournalLoads)
			}
		}
	}
	if lastOK < 0 {
		t.Fatal("no history within the cap was placed")
	}
	g, reads := longHistory(lastOK)
	if _, ok := g.slot([]string{"z"}, "p0"); !ok || reads() != maxJournalLoads {
		t.Fatalf("the longest placed history reads %d journals (ok=%v), want exactly the cap %d", reads(), ok, maxJournalLoads)
	}
	if g, _ := longHistory(lastOK + 1); func() bool { _, ok := g.slot([]string{"z"}, "p0"); return ok }() {
		t.Fatalf("a history one journal past the cap was placed")
	}
}

// A graph with no loader places a book only when every journal its place rests
// on was handed in; a book whose journal was not handed in may hold the record
// that moves the answer.
func TestSlot_NoLoaderPlacesOnlyWhatWasHandedIn(t *testing.T) {
	t.Parallel()
	g, _ := longHistory(3)
	g.SetJournalLoader(nil)
	if _, ok := g.slot([]string{"z"}, "p0"); ok {
		t.Fatal("placed from one journal, the rest of the chain not handed in")
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
