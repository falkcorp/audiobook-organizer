// file: internal/undo/membership_history_test.go
// version: 1.2.0
// guid: b4d17e62-0c9a-4f35-8e71-52a6d0f3c918
// last-edited: 2026-10-22

package undo

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
)

// These tests run whole histories (retires and reverts mixed, a book retired
// again after it was restored, a row that failed and is retried, rows of
// different operations interleaved) through carrySim, which journals and
// reverts the way the Repairs writer and RevertOperation do, and require the
// exact original list at the end and an order-correct list at every step.
// They were first written as an independent review of the anchored restore;
// each scenario below was a failing case then.

// relOrderOK: the books of l0 (the survivor aside) that are in cur appear in
// l0's relative order.
func relOrderOK(cur, l0 []string, s string) bool {
	var want, got []string
	in := map[string]bool{}
	for _, b := range cur {
		in[b] = true
	}
	for _, b := range l0 {
		if b != s && in[b] {
			want = append(want, b)
		}
	}
	for _, b := range cur {
		if b != s {
			got = append(got, b)
		}
	}
	return slices.Equal(want, got)
}

func randomL0(rng *rand.Rand) []string {
	n := 4 + rng.IntN(5)
	l0 := []string{}
	for i := range n {
		l0 = append(l0, fmt.Sprintf("b%d", i))
	}
	if rng.IntN(2) == 0 {
		l0 = slices.Insert(l0, rng.IntN(len(l0)+1), "S")
	}
	return l0
}

func nonSurvivors(l0 []string) []string {
	return slices.DeleteFunc(slices.Clone(l0), func(b string) bool { return b == "S" })
}

func requireExact(t *testing.T, name string, sim *carrySim, l0 []string) {
	t.Helper()
	if !slices.Equal(sim.list, l0) {
		t.Fatalf("%s: list is %v, want %v (notes %v)", name, sim.list, l0, sim.notes)
	}
	if len(sim.notes) > 0 {
		t.Fatalf("%s: fell back to the index: %v", name, sim.notes)
	}
}

// A book retired, restored and retired again: the records of both retires
// are kept, so its true predecessor edge is not lost with the earlier one.
func TestMembershipHistory_ReRetireAfterRevertKeepsEveryRecord(t *testing.T) {
	t.Parallel()
	l0 := []string{"b0", "b1", "b2", "b3", "S", "b4"}
	sim := newCarrySim(t, "S", l0)
	rev := func(bs ...string) {
		for _, b := range bs {
			if err := sim.revert(b); err != nil {
				t.Fatal(err)
			}
		}
	}
	sim.retire("op0", "b2")
	sim.retire("op1", "b3")
	sim.retire("op1", "b1")
	sim.retire("op2", "b4")
	sim.retire("op2", "b0")
	rev("b1", "b3")
	sim.retire("op3", "b3")
	sim.retire("op3", "b1")
	rev("b0", "b4")
	rev("b1", "b3")
	rev("b2")
	requireExact(t, "re-retire", sim, l0)
}

// The survivor is borrowed transitively: a held retire written while a
// borrowing marker stood borrows too, so the survivor leaves only with the
// last of them, even when each operation is reverted whole.
func TestMembershipHistory_BorrowedSurvivorLeavesWithTheLastBorrower(t *testing.T) {
	t.Parallel()
	l0 := []string{"b0", "b1", "b2", "b3"}
	sim := newCarrySim(t, "S", l0)
	listed := func(step string, want bool) {
		t.Helper()
		if got := slices.Contains(sim.list, "S"); got != want {
			t.Fatalf("after %s the survivor listed=%v, want %v (list %v)", step, got, want, sim.list)
		}
	}
	sim.retire("op0", "b0")
	listed("retire op0", true)
	sim.retire("op1", "b1")
	sim.retire("op1", "b2")
	listed("retire op1", true)
	if err := sim.revertOp([]string{"b0"}); err != nil {
		t.Fatal(err)
	}
	listed("revert op0", true) // op1 stands on it
	sim.retire("op2", "b0")
	sim.retire("op2", "b3")
	listed("retire op2", true)
	if err := sim.revertOp([]string{"b1", "b2"}); err != nil {
		t.Fatal(err)
	}
	listed("revert op1", true) // op2 stands on it
	if err := sim.revertOp([]string{"b0", "b3"}); err != nil {
		t.Fatal(err)
	}
	listed("revert op2", false)
	requireExact(t, "transitive borrow", sim, l0)
}

// Intermediate states: after every whole-operation revert, in every order,
// the books that are listed are in their original relative order.
func TestMembershipHistory_IntermediateOrder(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(99, 1))
	for caseN := range 400 {
		l0 := randomL0(rng)
		pool := nonSurvivors(l0)
		rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		pool = pool[:2+rng.IntN(min(4, len(pool))-1)]
		nOps := min(2+rng.IntN(3), len(pool))
		ops := make([][]string, nOps)
		for i, b := range pool {
			ops[i%nOps] = append(ops[i%nOps], b)
		}
		idx := make([]int, nOps)
		for i := range idx {
			idx[i] = i
		}
		for _, order := range permutations(idx) {
			sim := newCarrySim(t, "S", l0)
			for oi, books := range ops {
				for _, b := range books {
					sim.retire(fmt.Sprintf("op%d", oi), b)
				}
			}
			for _, oi := range order {
				if err := sim.revertOp(ops[oi]); err != nil {
					t.Fatalf("case%d l0=%v ops=%v order=%v: %v", caseN, l0, ops, order, err)
				}
				if !relOrderOK(sim.list, l0, "S") {
					t.Fatalf("case%d l0=%v ops=%v order=%v after op%d list=%v", caseN, l0, ops, order, oi, sim.list)
				}
			}
			requireExact(t, fmt.Sprintf("case%d l0=%v ops=%v order=%v", caseN, l0, ops, order), sim, l0)
		}
	}
}

// Retires and reverts interleaved in time: an operation may be reverted before
// later ones are written, a restored book may be retired again, and the rest
// are reverted in random order. Every step keeps the original relative order,
// the end is the exact original list, and nothing is refused or guessed.
func TestMembershipHistory_InterleavedTimeline(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(5, 77))
	const cases = 3000
	for caseN := range cases {
		l0 := randomL0(rng)
		sim := newCarrySim(t, "S", l0)
		type op struct {
			id    string
			books []string
			done  bool
		}
		var ops []*op
		var log []string
		fail := func(format string, a ...any) {
			t.Helper()
			t.Fatalf("case%d l0=%v log=%v: %s", caseN, l0, log, fmt.Sprintf(format, a...))
		}
		revert := func(o *op) {
			t.Helper()
			if err := sim.revertOp(o.books); err != nil {
				fail("%v", err)
			}
			o.done = true
			log = append(log, "R"+o.id)
			if !relOrderOK(sim.list, l0, "S") {
				fail("relative order broken: list=%v", sim.list)
			}
		}
		for st, steps := 0, 3+rng.IntN(6); st < steps; st++ {
			var live []*op
			for _, o := range ops {
				if !o.done {
					live = append(live, o)
				}
			}
			if len(live) > 0 && rng.IntN(2) == 0 {
				revert(live[rng.IntN(len(live))])
				continue
			}
			var cand []string
			for _, b := range sim.list {
				if b != "S" {
					cand = append(cand, b)
				}
			}
			if len(cand) < 2 {
				continue
			}
			rng.Shuffle(len(cand), func(i, j int) { cand[i], cand[j] = cand[j], cand[i] })
			o := &op{id: fmt.Sprintf("op%d", len(ops)), books: cand[:1+rng.IntN(2)]}
			for _, b := range o.books {
				sim.retire(o.id, b)
			}
			ops = append(ops, o)
			log = append(log, fmt.Sprintf("%s%v", o.id, o.books))
		}
		var live []*op
		for _, o := range ops {
			if !o.done {
				live = append(live, o)
			}
		}
		rng.Shuffle(len(live), func(i, j int) { live[i], live[j] = live[j], live[i] })
		for _, o := range live {
			revert(o)
		}
		if !slices.Equal(sim.list, l0) || len(sim.notes) > 0 {
			fail("final list=%v notes=%v", sim.list, sim.notes)
		}
	}
	t.Logf("%d timelines, 0 failures", cases)
}

// Rows of different operations interleaved with one another (one row of A,
// then rows of B, then A's other row): exact.
func TestMembershipHistory_CrossOperationRowInterleave(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(8, 8))
	const cases = 1000
	for caseN := range cases {
		l0 := randomL0(rng)
		pool := nonSurvivors(l0)
		rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		ops := [][]string{pool[:2], pool[2:4]}
		sim := newCarrySim(t, "S", l0)
		for oi, books := range ops {
			for _, b := range books {
				sim.retire(fmt.Sprintf("op%d", oi), b)
			}
		}
		a := rng.IntN(2)
		b := 1 - a
		seq := []string{ops[a][1], ops[b][1], ops[b][0], ops[a][0]}
		for _, x := range seq {
			if err := sim.revert(x); err != nil {
				t.Fatalf("case%d l0=%v ops=%v seq=%v: %v", caseN, l0, ops, seq, err)
			}
		}
		requireExact(t, fmt.Sprintf("case%d l0=%v ops=%v seq=%v", caseN, l0, ops, seq), sim, l0)
	}
	t.Logf("%d interleavings, 0 failures", cases)
}

// A retry after a failed row: the operation's first pass restores one row, the
// other FAILS and its marker is left standing (a marker is stamped only with
// its restored row); another operation is reverted meanwhile; then the retry
// restores the failed row. Exact, never refused.
func TestMembershipHistory_RetryAfterFailedRowLeavesItsMarkerStanding(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(4, 4))
	const cases = 2000
	for caseN := range cases {
		l0 := randomL0(rng)
		pool := nonSurvivors(l0)
		rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		ops := [][]string{pool[:2], pool[2:4]}
		sim := newCarrySim(t, "S", l0)
		for oi, books := range ops {
			for _, b := range books {
				sim.retire(fmt.Sprintf("op%d", oi), b)
			}
		}
		a := rng.IntN(2)
		b := 1 - a
		name := fmt.Sprintf("case%d l0=%v ops=%v a=%d", caseN, l0, ops, a)
		for _, x := range []string{ops[a][1], ops[b][1], ops[b][0], ops[a][0]} {
			// ops[a][0]'s first attempt failed: it is simply not reverted
			// until the retry, which is the last step.
			if err := sim.revert(x); err != nil {
				t.Fatalf("%s revert %s: %v (list %v)", name, x, err, sim.list)
			}
		}
		requireExact(t, name, sim, l0)
	}
	t.Logf("%d retries, 0 failures", cases)
}

// Every permutation of the four rows of two two-row operations.
func TestMembershipHistory_AllRowPermutations(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(12, 34))
	total := 0
	for caseN := range 300 {
		l0 := randomL0(rng)
		pool := nonSurvivors(l0)
		rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		ops := [][]string{pool[:2], pool[2:4]}
		for _, seq := range permutations(pool[:4]) {
			total++
			sim := newCarrySim(t, "S", l0)
			for oi, books := range ops {
				for _, b := range books {
					sim.retire(fmt.Sprintf("op%d", oi), b)
				}
			}
			for _, x := range seq {
				if err := sim.revert(x); err != nil {
					t.Fatalf("case%d l0=%v ops=%v seq=%v: %v", caseN, l0, ops, seq, err)
				}
			}
			requireExact(t, fmt.Sprintf("case%d l0=%v ops=%v seq=%v", caseN, l0, ops, seq), sim, l0)
		}
	}
	t.Logf("%d row orders, 0 failures", total)
}

// Any history at row level: retires of one to three books, reverts of any
// single live row at any time (no operation grouping, so rows of different
// operations interleave freely and a restored book is retired again), then
// the remaining rows in random order. Every step keeps the original relative
// order of the books, the end is the exact original list, nothing is refused
// and nothing falls back to the index.
func TestMembershipHistory_AnyRowHistory(t *testing.T) {
	t.Parallel()
	const cases = 3000
	for caseN := range cases {
		rng := rand.New(rand.NewPCG(uint64(caseN/1500)+1, uint64(caseN/1500)*13+13))
		for range caseN % 1500 { // a distinct stream per case: skip ahead
			rng.Uint64()
		}
		l0 := make([]string, 5+rng.IntN(8))
		for i := range l0 {
			l0[i] = fmt.Sprintf("b%d", i)
		}
		if rng.IntN(2) == 0 {
			l0 = slices.Insert(l0, rng.IntN(len(l0)+1), "S")
		}
		sim := newCarrySim(t, "S", l0)
		live := map[string]bool{}
		var log []string
		liveBooks := func() []string {
			var lv []string
			for b := range live {
				lv = append(lv, b)
			}
			slices.Sort(lv)
			return lv
		}
		revert := func(b string) {
			t.Helper()
			if err := sim.revert(b); err != nil {
				t.Fatalf("case%d l0=%v log=%v: revert %s: %v", caseN, l0, log, b, err)
			}
			delete(live, b)
			log = append(log, "R"+b)
			if !relOrderOK(sim.list, l0, "S") {
				t.Fatalf("case%d l0=%v log=%v: relative order broken: %v", caseN, l0, log, sim.list)
			}
		}
		opN := 0
		for st, steps := 0, 6+rng.IntN(20); st < steps; st++ {
			if lv := liveBooks(); len(lv) > 0 && rng.IntN(2) == 0 {
				revert(lv[rng.IntN(len(lv))])
				continue
			}
			var cand []string
			for _, b := range sim.list {
				if b != "S" {
					cand = append(cand, b)
				}
			}
			if len(cand) < 2 {
				continue
			}
			op := fmt.Sprintf("op%d", opN)
			opN++
			rng.Shuffle(len(cand), func(i, j int) { cand[i], cand[j] = cand[j], cand[i] })
			for _, b := range cand[:1+rng.IntN(min(3, len(cand)-1))] {
				sim.retire(op, b)
				live[b] = true
				log = append(log, op+":"+b)
			}
		}
		for lv := liveBooks(); len(lv) > 0; lv = liveBooks() {
			revert(lv[rng.IntN(len(lv))])
		}
		if !slices.Equal(sim.list, l0) || len(sim.notes) > 0 {
			t.Fatalf("case%d l0=%v log=%v: list=%v notes=%v", caseN, l0, log, sim.list, sim.notes)
		}
	}
	t.Logf("%d row-level histories, 0 failures", cases)
}
