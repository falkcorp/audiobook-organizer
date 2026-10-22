// file: internal/undo/membership_anchor_test.go
// version: 1.2.0
// guid: 3e8b5d27-9a41-4c06-b7f2-1d6a0c93e584
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

// carrySim journals retires into one survivor's slot of one list the way the
// Repairs writer does (a marker on the survivor and a row on the retired
// book, both carrying the record) and reverts them the way the revert does
// (the survivor's stand from its journal, the slot from the neighbour graph).
type carrySim struct {
	t         *testing.T
	survivor  string
	listID    string
	list      []string
	clock     time.Time
	survivorJ []*database.OperationChange // the survivor's journal
	byRetired map[string][2]*database.OperationChange
	notes     []string
}

func newCarrySim(t *testing.T, survivor string, l0 []string) *carrySim {
	return &carrySim{t: t, survivor: survivor, listID: "L", list: slices.Clone(l0),
		clock: time.Unix(1_700_000_000, 0), byRetired: map[string][2]*database.OperationChange{}}
}

func (s *carrySim) tick() time.Time { s.clock = s.clock.Add(time.Second); return s.clock }

func (s *carrySim) retire(op, retired string) {
	s.t.Helper()
	next, rec, ok := CarryMembership(s.list, retired, s.survivor)
	if !ok {
		s.t.Fatalf("%s is not listed in %v", retired, s.list)
	}
	val, err := EncodeMembership(rec)
	if err != nil {
		s.t.Fatal(err)
	}
	s.list = next
	kind := TookKind(UserContentCollection)
	if rec.SurvivorWasMember {
		kind = HeldKind(UserContentCollection)
	}
	now := s.tick()
	marker := &database.OperationChange{ID: op + "/m/" + retired, OperationID: op, BookID: s.survivor,
		ChangeType: ChangeTypeUserContentFollow, FieldName: UserContentField(kind, s.listID),
		OldValue: val, NewValue: retired, CreatedAt: now}
	row := &database.OperationChange{ID: op + "/r/" + retired, OperationID: op, BookID: retired,
		ChangeType: ChangeTypeUserContentFollow, FieldName: UserContentField(UserContentCollection, s.listID),
		OldValue: val, NewValue: s.survivor, CreatedAt: now}
	s.survivorJ = append(s.survivorJ, marker)
	s.byRetired[retired] = [2]*database.OperationChange{row, marker}
}

// revert undoes the retire of one book the way RevertOperation does for a
// row: the survivor's part from its journal (SurvivorStandFor), the slot from
// the neighbour graph, and the marker stamped reverted together with the row.
func (s *carrySim) revert(retired string) error { return s.revertStamping(retired, true) }

// revertStamping is revert, with stamp false for a revert whose process dies
// after the list write and before the stamp (the row and its marker stay
// standing). A book already listed is stamped and reported ErrAlreadyRestored
// unless the survivor it borrowed is still listed and nothing else stands on
// it, which RestoreMember takes out.
func (s *carrySim) revertStamping(retired string, stamp bool) error {
	pair := s.byRetired[retired]
	row, marker := pair[0], pair[1]
	rec, err := DecodeMembership(row.OldValue)
	if err != nil {
		s.t.Fatal(err)
	}
	stand, borrowed := SurvivorStandFor(s.survivorJ, row, UserContentCollection, s.listID)
	g := NewMembershipGraph(s.survivor, UserContentCollection, s.listID)
	g.AddRows([]*database.OperationChange{row})
	g.AddJournal(s.survivor, s.survivorJ)
	// Every book other than the survivor has no journal rows besides the
	// retire rows already in the pool.
	g.SetJournalLoader(func(string) ([]*database.OperationChange, error) { return nil, nil })
	next, note, _, err := RestoreMember(s.list, retired, s.survivor, rec, stand, borrowed, g)
	if err != nil && !errors.Is(err, ErrAlreadyRestored) {
		return err
	}
	if note != "" {
		s.notes = append(s.notes, note)
	}
	if err == nil {
		s.list = next
	}
	if stamp {
		at := s.tick()
		row.RevertedAt, marker.RevertedAt = &at, &at
	}
	return err
}

// revertOp undoes every row of an operation, newest first, as one pass.
func (s *carrySim) revertOp(books []string) error {
	for _, b := range slices.Backward(books) {
		if err := s.revert(b); err != nil {
			return fmt.Errorf("revert %s: %w", b, err)
		}
	}
	return nil
}

func permutations[T any](xs []T) [][]T {
	if len(xs) <= 1 {
		return [][]T{slices.Clone(xs)}
	}
	var out [][]T
	for i := range xs {
		rest := slices.Concat(xs[:i:i], xs[i+1:])
		for _, p := range permutations(rest) {
			out = append(out, append([]T{xs[i]}, p...))
		}
	}
	return out
}

// TestMembershipAnchors_ExactInAnyRevertOrder retires books of a list into a
// survivor in several operations, then undoes the operations in every order:
// every book, and the survivor, must be back in its exact original position.
func TestMembershipAnchors_ExactInAnyRevertOrder(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(7, 11))
	for caseN := range 120 {
		n := 4 + rng.IntN(5) // list length 4..8 before the survivor joins
		l0 := make([]string, 0, n+1)
		for i := range n {
			l0 = append(l0, fmt.Sprintf("b%d", i))
		}
		survivorListed := rng.IntN(2) == 0
		if survivorListed {
			l0 = slices.Insert(l0, rng.IntN(len(l0)+1), "S")
		}
		// Retire a random subset of the other books, in a random order, grouped
		// into 2..4 operations.
		var pool []string
		for _, b := range l0 {
			if b != "S" {
				pool = append(pool, b)
			}
		}
		rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		pool = pool[:2+rng.IntN(min(4, len(pool))-1)]
		nOps := min(2+rng.IntN(3), len(pool))
		ops := make([][]string, nOps)
		for i, b := range pool {
			ops[i%nOps] = append(ops[i%nOps], b)
		}
		opIDs := make([]int, nOps)
		for i := range opIDs {
			opIDs[i] = i
		}
		for _, order := range permutations(opIDs) {
			name := fmt.Sprintf("case%d/%v/ops%v/order%v", caseN, l0, ops, order)
			sim := newCarrySim(t, "S", l0)
			for oi, books := range ops {
				for _, b := range books {
					sim.retire(fmt.Sprintf("op%d", oi), b)
				}
			}
			for _, oi := range order {
				for _, b := range slices.Backward(ops[oi]) { // newest row first
					if err := sim.revert(b); err != nil {
						t.Fatalf("%s: revert %s: %v", name, b, err)
					}
				}
			}
			if !slices.Equal(sim.list, l0) {
				t.Fatalf("%s: list is %v, want %v (notes %v)", name, sim.list, l0, sim.notes)
			}
			if len(sim.notes) > 0 {
				t.Fatalf("%s: fell back to the index: %v", name, sim.notes)
			}
		}
	}
}

func TestMembershipAnchors_CarryRecordsNeighbours(t *testing.T) {
	t.Parallel()
	_, rec, _ := CarryMembership([]string{"a", "r", "b"}, "r", "s")
	if !rec.Anchored || rec.Prev != "a" || rec.Next != "b" {
		t.Fatalf("record %+v", rec)
	}
	_, rec, _ = CarryMembership([]string{"r", "b"}, "r", "s")
	if !rec.Anchored || rec.Prev != "" || rec.Next != "b" {
		t.Fatalf("head record %+v", rec)
	}
	_, rec, _ = CarryMembership([]string{"a", "r"}, "r", "s")
	if !rec.Anchored || rec.Prev != "a" || rec.Next != "" {
		t.Fatalf("tail record %+v", rec)
	}
}

// A row journaled before the anchors existed has none: it is put back by its
// index, and the revert says so.
func TestMembershipAnchors_OldRowUsesIndexAndSaysSo(t *testing.T) {
	t.Parallel()
	rec := MembershipRecord{Index: 1} // not Anchored
	got, note, err := RestoreMembership([]string{"a", "s", "c"}, "r", "s", rec, SurvivorDefault, nil)
	if err != nil {
		t.Fatal(err)
	}
	ucEq(t, got, []string{"a", "r", "c"}) // replaced slot: the survivor swaps back
	if note != "" {
		t.Fatalf("a replaced slot needs no position: note %q", note)
	}
	got, note, err = RestoreMembership([]string{"a", "s", "c"}, "r", "s", MembershipRecord{Index: 2, SurvivorWasMember: true}, SurvivorDefault, nil)
	if err != nil {
		t.Fatal(err)
	}
	ucEq(t, got, []string{"a", "s", "r", "c"})
	if !strings.Contains(note, "predates neighbour anchors") {
		t.Fatalf("note %q", note)
	}
}

// With no neighbour of the book found in the list (or no graph), the index is
// used and reported.
func TestMembershipAnchors_NoNeighbourResolvesFallsBackAndSaysSo(t *testing.T) {
	t.Parallel()
	rec := MembershipRecord{Index: 1, SurvivorWasMember: true, Anchored: true, Prev: "gone1", Next: "gone2"}
	g := NewMembershipGraph("s", UserContentCollection, "L")
	got, note, err := RestoreMembership([]string{"x", "s", "y"}, "r", "s", rec, SurvivorDefault, g)
	if err != nil {
		t.Fatal(err)
	}
	ucEq(t, got, []string{"x", "r", "s", "y"})
	if !strings.Contains(note, "could not be ordered") {
		t.Fatalf("note %q", note)
	}
	// An anchored record with no graph at all is the same fallback.
	_, note, _ = RestoreMembership([]string{"x", "s", "y"}, "r", "s", rec, SurvivorDefault, nil)
	if note == "" {
		t.Fatal("a fallback must be reported")
	}
}

// An anchor that is a book another retire took out resolves through that
// retire's record: [a r1 r2 b], r2 retired first, then r1; reverting r1 first
// still puts it before r2's slot, and r2 then goes right after it.
func TestMembershipAnchors_AnchorsResolveThroughOtherRetires(t *testing.T) {
	t.Parallel()
	l0 := []string{"a", "r1", "r2", "b", "S"}
	for _, order := range [][]string{{"r1", "r2"}, {"r2", "r1"}} {
		sim := newCarrySim(t, "S", l0)
		sim.retire("op1", "r1")
		sim.retire("op2", "r2")
		for _, b := range order {
			if err := sim.revert(b); err != nil {
				t.Fatal(err)
			}
		}
		ucEq(t, sim.list, l0)
		if len(sim.notes) > 0 {
			t.Fatalf("notes %v", sim.notes)
		}
	}
}

// The rows of one operation are reverted together but in no fixed order, and
// the operations in any order: every such order restores the list. (Rows of
// different operations interleaved with one another are covered by
// TestMembershipHistory_AllRowPermutations.)
func TestMembershipAnchors_ExactInAnyRowOrderWithinOperations(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(3, 5))
	for caseN := range 60 {
		l0 := []string{"b0", "b1", "b2", "b3", "b4", "b5"}
		if rng.IntN(2) == 0 {
			l0 = slices.Insert(l0, rng.IntN(len(l0)+1), "S")
		}
		pool := slices.DeleteFunc(slices.Clone(l0), func(b string) bool { return b == "S" })
		rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		ops := [][]string{pool[:2], pool[2:4]}
		for _, opOrder := range permutations([]int{0, 1}) {
			for _, rows0 := range permutations(ops[opOrder[0]]) {
				for _, rows1 := range permutations(ops[opOrder[1]]) {
					name := fmt.Sprintf("case%d/%v/ops%v/ops%v rows%v then %v", caseN, l0, ops, opOrder, rows0, rows1)
					sim := newCarrySim(t, "S", l0)
					for oi, books := range ops {
						for _, b := range books {
							sim.retire(fmt.Sprintf("op%d", oi), b)
						}
					}
					for _, b := range slices.Concat(rows0, rows1) {
						if err := sim.revert(b); err != nil {
							t.Fatalf("%s: revert %s: %v", name, b, err)
						}
					}
					if !slices.Equal(sim.list, l0) || len(sim.notes) > 0 {
						t.Fatalf("%s: list is %v, want %v (notes %v)", name, sim.list, l0, sim.notes)
					}
				}
			}
		}
	}
}

// A neighbour another survivor absorbed (or the owner deleted) after the
// retire is a dead end of the walk, never a place to step through: the records
// then order the book against the books listed on one side only, which is not
// enough to place it (a book of the other side may belong between), so the
// recorded index is used and the revert says so. (With the journals of the
// other survivors readable the walk can go on, see
// TestMembershipHistory_TwoSurvivorsShareAList.)
func TestMembershipAnchors_NeighbourGoneToAnotherSurvivorIsADeadEnd(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct{ replacement, want []string }{
		"absorbed by another survivor": {[]string{"a", "T", "S"}, []string{"a", "T", "r", "S"}},
		"deleted":                      {[]string{"a", "S"}, []string{"a", "S", "r"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sim := newCarrySim(t, "S", []string{"a", "q", "r", "S"})
			sim.retire("op1", "r") // r's recorded neighbours: q before, S after
			sim.list = slices.Clone(c.replacement)
			if err := sim.revert("r"); err != nil {
				t.Fatal(err)
			}
			ucEq(t, sim.list, c.want)
			if len(sim.notes) != 1 || !strings.Contains(sim.notes[0], "recorded index") {
				t.Fatalf("the index fallback must be reported, notes %v", sim.notes)
			}
		})
	}
}

// A survivor that took a retired book's slot is that book's node: a book
// retired next to it records the survivor as its neighbour, and reverting in
// the other order still lands it by the original book.
func TestMembershipAnchors_SurvivorStandsInForTheBookItReplaced(t *testing.T) {
	t.Parallel()
	l0 := []string{"a", "r1", "r2", "b"}
	for _, order := range [][]string{{"r1", "r2"}, {"r2", "r1"}} {
		sim := newCarrySim(t, "S", l0)
		sim.retire("op1", "r1") // S takes r1's slot: [a S r2 b]
		sim.retire("op2", "r2") // r2 removed, S already listed: [a S b], prev anchor is S
		for _, b := range order {
			if err := sim.revert(b); err != nil {
				t.Fatalf("%v: %v", order, err)
			}
		}
		ucEq(t, sim.list, l0)
		if len(sim.notes) > 0 {
			t.Fatalf("%v: fell back to the index: %v", order, sim.notes)
		}
	}
}
