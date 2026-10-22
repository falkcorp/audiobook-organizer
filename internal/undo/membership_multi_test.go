// file: internal/undo/membership_multi_test.go
// version: 1.2.0
// guid: 8d4f2a61-7c35-4b90-a1e8-5f3c9b0d7e42
// last-edited: 2026-10-22

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

// multiSim: several survivors share ONE list. Each retire journals a marker on
// its survivor and a row on the retired book, as the Repairs writer does; a
// revert reads the survivor's journal, and any other book's journal through the
// graph's loader (GetBookChanges in the product); with withLoader unset the
// loader fails after two reads.
type multiSim struct {
	t          *testing.T
	list       []string
	clock      time.Time
	withLoader bool
	journals   map[string][]*database.OperationChange // book -> its journal rows
	pair       map[string][2]*database.OperationChange
	notes      []string
}

func newMultiSim(t *testing.T, l0 []string, withLoader bool) *multiSim {
	return &multiSim{t: t, list: slices.Clone(l0), clock: time.Unix(1_700_000_000, 0), withLoader: withLoader,
		journals: map[string][]*database.OperationChange{}, pair: map[string][2]*database.OperationChange{}}
}

func (s *multiSim) tick() time.Time { s.clock = s.clock.Add(time.Second); return s.clock }

func (s *multiSim) retire(op, retired, survivor string) {
	s.t.Helper()
	next, rec, ok := CarryMembership(s.list, retired, survivor)
	if !ok {
		s.t.Fatalf("%s not listed in %v", retired, s.list)
	}
	val, _ := EncodeMembership(rec)
	s.list = next
	kind := TookKind(UserContentCollection)
	if rec.SurvivorWasMember {
		kind = HeldKind(UserContentCollection)
	}
	now := s.tick()
	m := &database.OperationChange{ID: op + "/m/" + retired, OperationID: op, BookID: survivor,
		ChangeType: ChangeTypeUserContentFollow, FieldName: UserContentField(kind, "L"), OldValue: val, NewValue: retired, CreatedAt: now}
	r := &database.OperationChange{ID: op + "/r/" + retired, OperationID: op, BookID: retired,
		ChangeType: ChangeTypeUserContentFollow, FieldName: UserContentField(UserContentCollection, "L"), OldValue: val, NewValue: survivor, CreatedAt: now}
	s.journals[survivor] = append(s.journals[survivor], m)
	s.journals[retired] = append(s.journals[retired], r)
	s.pair[retired] = [2]*database.OperationChange{r, m}
}

func (s *multiSim) revert(retired string) error { return s.revertStamping(retired, true) }

func (s *multiSim) revertStamping(retired string, stamp bool) error {
	r, m := s.pair[retired][0], s.pair[retired][1]
	rec, _ := DecodeMembership(r.OldValue)
	sv := r.NewValue
	stand, borrowed := SurvivorStandFor(s.journals[sv], r, UserContentCollection, "L")
	g := NewMembershipGraph(sv, UserContentCollection, "L")
	g.AddRows([]*database.OperationChange{r})
	g.AddJournal(sv, s.journals[sv])
	// A degraded loader (withLoader false) serves two journals and then fails,
	// as a store error or the per-slot read cap would: the graph must then
	// place nothing it has not fully read, and the caller reports the index.
	reads := 0
	g.SetJournalLoader(func(book string) ([]*database.OperationChange, error) {
		reads++
		if !s.withLoader && reads > 2 {
			return nil, errors.New("journal read failed")
		}
		return s.journals[book], nil
	})
	next, res, err := RestoreMember(s.list, retired, sv, rec, MemberRole{Stand: stand, Borrowed: borrowed}, g)
	note := res.Note
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
		r.RevertedAt, m.RevertedAt = &at, &at
	}
	return err
}

// Two survivors share a list, books next to each other go into different
// survivors, and the operations are reverted oldest first: the book of the
// first operation is back before the second's, exactly, because the walk reads
// the other survivor's journal.
func TestMembershipMulti_TwoSurvivorsShareAList(t *testing.T) {
	t.Parallel()
	l0 := []string{"S1", "a", "X", "Y", "b", "S2"}
	for _, withLoader := range []bool{true, false} {
		s := newMultiSim(t, l0, withLoader)
		s.retire("op1", "X", "S1")
		s.retire("op2", "Y", "S2")
		for _, b := range []string{"X", "Y"} {
			if err := s.revert(b); err != nil {
				t.Fatal(err)
			}
		}
		switch {
		case withLoader:
			ucEq(t, s.list, l0)
			if len(s.notes) != 0 {
				t.Fatalf("with the journals readable the order is exact, notes %v", s.notes)
			}
		case !slices.Equal(s.list, l0) && len(s.notes) == 0:
			t.Fatalf("SILENT wrong order without the journals: got %v want %v", s.list, l0)
		}
	}
}

// multiHistoryCase is one fuzz case: a list with two survivors (each listed or
// not), four books retired in four operations alternating between them.
type multiOp struct {
	id, survivor string
	books        []string
}

func multiCases(seed1, seed2 uint64, n int) (out []struct {
	l0  []string
	ops []multiOp
}) {
	rng := rand.New(rand.NewPCG(seed1, seed2))
	for range n {
		size := 5 + rng.IntN(5)
		var l0 []string
		for i := range size {
			l0 = append(l0, fmt.Sprintf("b%d", i))
		}
		for _, sv := range []string{"S1", "S2"} {
			if rng.IntN(2) == 0 {
				l0 = slices.Insert(l0, rng.IntN(len(l0)+1), sv)
			}
		}
		pool := slices.DeleteFunc(slices.Clone(l0), func(b string) bool { return b == "S1" || b == "S2" })
		rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		pool = pool[:4]
		out = append(out, struct {
			l0  []string
			ops []multiOp
		}{l0, []multiOp{{"op0", "S1", pool[0:1]}, {"op1", "S2", pool[1:2]}, {"op2", "S1", pool[2:3]}, {"op3", "S2", pool[3:4]}}})
	}
	return out
}

// runMultiFuzz reverts the four operations in every order and counts how each
// ends: exact, reported (an index fallback said so), refused, or silently
// wrong (the thing that must never happen).
func runMultiFuzz(t *testing.T, withLoader bool) (total, exact, noted, refused, silent int) {
	t.Helper()
	for _, c := range multiCases(3, 9, 2000) {
		for _, order := range permutations([]int{0, 1, 2, 3}) {
			total++
			s := newMultiSim(t, c.l0, withLoader)
			for _, o := range c.ops {
				for _, b := range o.books {
					s.retire(o.id, b, o.survivor)
				}
			}
			bad := false
			for _, oi := range order {
				for _, b := range slices.Backward(c.ops[oi].books) {
					if err := s.revert(b); err != nil {
						bad = true
					}
				}
			}
			switch {
			case bad:
				refused++
			case slices.Equal(s.list, c.l0) && len(s.notes) == 0:
				exact++
			case slices.Equal(s.list, c.l0):
				noted++ // right, by the index, and said so
			case len(s.notes) > 0:
				noted++
			default:
				silent++
				if silent < 4 {
					t.Logf("silent: l0=%v ops=%v order=%v got=%v", c.l0, c.ops, order, s.list)
				}
			}
		}
	}
	return
}

// With a loader that fails after two reads (a store error, or the per-slot
// read cap): a slot is used only when every journal it rests on was read, so
// whatever cannot be ordered is reported by index and nothing is silently
// wrong.
func TestMembershipMulti_GuardNeverLeavesASilentWrongOrder(t *testing.T) {
	t.Parallel()
	total, exact, noted, refused, silent := runMultiFuzz(t, false)
	t.Logf("two survivors, journal reads failing after two: total=%d exact=%d reported=%d refused=%d silent-wrong=%d", total, exact, noted, refused, silent)
	if silent != 0 {
		t.Fatalf("%d silent wrong results", silent)
	}
	if refused != 0 {
		t.Fatalf("%d refused", refused)
	}
}

// With every journal the walk reaches readable, two survivors in one list are
// exact in every operation order: nothing silent, and nothing left to the
// index either.
func TestMembershipMulti_ExactWithTheOtherSurvivorsJournals(t *testing.T) {
	t.Parallel()
	total, exact, noted, refused, silent := runMultiFuzz(t, true)
	t.Logf("two survivors, journals read: total=%d exact=%d reported=%d refused=%d silent-wrong=%d", total, exact, noted, refused, silent)
	if silent != 0 {
		t.Fatalf("%d silent wrong results", silent)
	}
	if refused != 0 {
		t.Fatalf("%d refused", refused)
	}
	if noted != 0 {
		t.Fatalf("%d placed by the index, want 0", noted)
	}
}

// The revert of one row writes the list and dies before its marker is
// stamped; another operation on the same survivor is reverted next; then the
// first row's retry finds its book listed. The survivor must still leave with
// the last borrower (RestoreMember takes it out on the retry).
func TestMembershipHistory_CrashBetweenWriteAndStampLeavesNoSurvivorBehind(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(1, 2))
	for caseN := range 2000 {
		l0 := mkHistoryL0(rng)
		pool := slices.DeleteFunc(slices.Clone(l0), func(b string) bool { return b == "S" })
		rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		sim := newCarrySim(t, "S", l0)
		sim.retire("opA", pool[0])
		sim.retire("opB", pool[1])
		// opA's book is back in the list with its row and marker still standing (as if
		// the owner put it back by hand).
		if err := sim.revertStamping(pool[0], false); err != nil {
			t.Fatalf("case%d l0=%v: %v", caseN, l0, err)
		}
		if err := sim.revert(pool[1]); err != nil {
			t.Fatalf("case%d l0=%v: revert B: %v", caseN, l0, err)
		}
		// opA's retry: its book is listed already.
		if err := sim.revert(pool[0]); err != nil && !errors.Is(err, ErrAlreadyRestored) {
			t.Fatalf("case%d l0=%v: retry A: %v", caseN, l0, err)
		}
		if !slices.Equal(sim.list, l0) {
			t.Fatalf("case%d l0=%v A=%s B=%s: list is %v (notes %v)", caseN, l0, pool[0], pool[1], sim.list, sim.notes)
		}
	}
}

// A marker journaled by an apply whose list write was refused is voided and
// must not count: a revert that reads it afterwards leaves the lists exact.
// (An apply and a revert on the same list never interleave: undo.LockList.)
func TestMembershipHistory_VoidedMarkerOfARefusedCarryIsIgnored(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(5, 6))
	for caseN := range 2000 {
		l0 := mkHistoryL0(rng)
		pool := slices.DeleteFunc(slices.Clone(l0), func(b string) bool { return b == "S" })
		rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		sim := newCarrySim(t, "S", l0)
		sim.retire("opA", pool[0])
		_, rec, _ := CarryMembership(sim.list, pool[1], "S")
		val, _ := EncodeMembership(rec)
		kind := TookKind(UserContentCollection)
		if rec.SurvivorWasMember {
			kind = HeldKind(UserContentCollection)
		}
		sim.survivorJ = append(sim.survivorJ, &database.OperationChange{ID: "opC/m", OperationID: "opC", BookID: "S",
			ChangeType: ChangeTypeUserContentFollow, FieldName: UserContentField(kind, "L"), OldValue: val, NewValue: pool[1],
			CreatedAt: sim.tick(), Voided: true})
		if err := sim.revert(pool[0]); err != nil {
			t.Fatalf("case%d l0=%v: %v", caseN, l0, err)
		}
		if !slices.Equal(sim.list, l0) {
			t.Fatalf("case%d l0=%v: list is %v", caseN, l0, sim.list)
		}
	}
}

// mkHistoryL0 is a list of 4..8 books with the survivor "S" in it half the time.
func mkHistoryL0(rng *rand.Rand) []string {
	n := 4 + rng.IntN(5)
	var l0 []string
	for i := range n {
		l0 = append(l0, fmt.Sprintf("b%d", i))
	}
	if rng.IntN(2) == 0 {
		l0 = slices.Insert(l0, rng.IntN(len(l0)+1), "S")
	}
	return l0
}

// Two levels of borrowing: the survivor entered the list for A (took), B
// relied on it (held), A is reverted, C relies on it again (held, on B's
// account), B is reverted: the survivor must still be listed for C, and leaves
// with C. The survivor's presence is asserted after every step.
func TestMembershipHistory_TwoLevelBorrowKeepsTheSurvivorForTheLastBorrower(t *testing.T) {
	t.Parallel()
	l0 := []string{"a", "A", "B", "C", "d"}
	sim := newCarrySim(t, "S", l0)
	listed := func(step string, want bool) {
		t.Helper()
		if got := slices.Contains(sim.list, "S"); got != want {
			t.Fatalf("after %s the survivor listed=%v, want %v (list %v)", step, got, want, sim.list)
		}
	}
	revert := func(book string) {
		t.Helper()
		if err := sim.revert(book); err != nil {
			t.Fatalf("revert %s: %v", book, err)
		}
	}
	sim.retire("opA", "A") // took: S enters in A's slot
	listed("retire A", true)
	sim.retire("opB", "B") // held: S already listed on A's account
	revert("A")
	listed("revert A", true) // B still relies on it
	sim.retire("opC", "C")   // held: S is listed on B's account
	listed("retire C", true)
	revert("B")
	listed("revert B", true) // C still relies on it
	revert("C")
	listed("revert C", false)
	ucEq(t, sim.list, l0)
	if len(sim.notes) != 0 {
		t.Fatalf("notes %v", sim.notes)
	}
}

// relativeOrderOK: the books of l0 that are not survivors, and are listed in
// cur, appear in cur in the order l0 has them, and cur has no other non-survivor.
func relativeOrderOK(cur, l0 []string, isSurvivor func(string) bool) bool {
	in := map[string]bool{}
	for _, b := range cur {
		in[b] = true
	}
	var want, got []string
	for _, b := range l0 {
		if !isSurvivor(b) && in[b] {
			want = append(want, b)
		}
	}
	for _, b := range cur {
		if !isSurvivor(b) {
			got = append(got, b)
		}
	}
	return slices.Equal(want, got)
}

// Random histories over a list with two or three survivors: operations that
// retire one or two books each (each row into any survivor), reverted at any
// time and in any order, books retired again after a revert. After EVERY revert
// the books are in their original relative order; at the end the list is the
// original one. Nothing may end silently wrong, and with the journals readable
// nothing is left to the index.
func TestMembershipMulti_InterleavedTimelines(t *testing.T) {
	t.Parallel()
	for _, withLoader := range []bool{true, false} {
		rng := rand.New(rand.NewPCG(21, 22))
		var exact, reported, interWrong, refused, silent, total int
		for caseN := range 4000 {
			size := 6 + rng.IntN(5)
			var l0 []string
			for i := range size {
				l0 = append(l0, fmt.Sprintf("b%d", i))
			}
			survivors := []string{"S1", "S2", "S3"}[:2+rng.IntN(2)]
			for _, sv := range survivors {
				if rng.IntN(2) == 0 {
					l0 = slices.Insert(l0, rng.IntN(len(l0)+1), sv)
				}
			}
			isSurvivor := func(b string) bool { return slices.Contains(survivors, b) }
			sim := newMultiSim(t, l0, withLoader)
			type op struct {
				id    string
				books []string
				done  bool
			}
			var ops []*op
			var log []string
			failed := false
			revertOp := func(o *op) {
				for _, b := range slices.Backward(o.books) {
					if err := sim.revert(b); err != nil {
						failed = true
						return
					}
				}
				o.done = true
				log = append(log, "R"+o.id)
				if !relativeOrderOK(sim.list, l0, isSurvivor) && len(sim.notes) == 0 { // once an index fallback was reported the order is the report's to carry
					interWrong++
					if interWrong < 4 {
						t.Logf("INTER loader=%v case%d l0=%v log=%v list=%v notes=%v", withLoader, caseN, l0, log, sim.list, sim.notes)
					}
				}
			}
			for step := 0; step < 3+rng.IntN(7) && !failed; step++ {
				var live []*op
				for _, o := range ops {
					if !o.done {
						live = append(live, o)
					}
				}
				if len(live) > 0 && rng.IntN(2) == 0 {
					revertOp(live[rng.IntN(len(live))])
					continue
				}
				var cand []string
				for _, b := range sim.list {
					if !isSurvivor(b) {
						cand = append(cand, b)
					}
				}
				if len(cand) < 2 {
					continue
				}
				rng.Shuffle(len(cand), func(i, j int) { cand[i], cand[j] = cand[j], cand[i] })
				o := &op{id: fmt.Sprintf("op%d", len(ops)), books: cand[:1+rng.IntN(2)]}
				var into []string
				for _, b := range o.books {
					sv := survivors[rng.IntN(len(survivors))]
					sim.retire(o.id, b, sv)
					into = append(into, b+">"+sv)
				}
				ops = append(ops, o)
				log = append(log, fmt.Sprintf("%s%v", o.id, into))
			}
			var live []*op
			for _, o := range ops {
				if !o.done {
					live = append(live, o)
				}
			}
			rng.Shuffle(len(live), func(i, j int) { live[i], live[j] = live[j], live[i] })
			for _, o := range live {
				if !failed {
					revertOp(o)
				}
			}
			total++
			switch {
			case failed:
				refused++
			case slices.Equal(sim.list, l0) && len(sim.notes) == 0:
				exact++
			case len(sim.notes) > 0:
				reported++
			default:
				silent++
				if silent < 4 {
					t.Logf("FINAL loader=%v case%d l0=%v log=%v list=%v", withLoader, caseN, l0, log, sim.list)
				}
			}
		}
		t.Logf("interleaved timelines, loader=%v: total=%d exact=%d reported=%d refused=%d silent-wrong=%d intermediate-wrong=%d",
			withLoader, total, exact, reported, refused, silent, interWrong)
		if silent != 0 || refused != 0 {
			t.Errorf("loader=%v: %d silent wrong, %d refused", withLoader, silent, refused)
		}
		if withLoader && (reported != 0 || interWrong != 0) {
			t.Errorf("with the journals readable: %d reported, %d wrong in between; want 0", reported, interWrong)
		}
	}
}
