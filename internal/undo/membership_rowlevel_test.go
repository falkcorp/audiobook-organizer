// file: internal/undo/membership_rowlevel_test.go
// version: 1.0.0
// guid: 7e98c6d5-ae02-4209-a3b4-306133edd303
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

// rowSim models the product at row level, several survivors in one list, every
// book with a journal of its own. A retire journals a marker on its survivor and
// a row on the retired book. A row revert does what the revert does
// (revertListFollow): the roles from the survivor's and the retired book's
// journals (MemberRoleOf), a stale row skipped (NewerCarryStands), the graph
// over every journal, RestoreMember; and the row and its marker are stamped
// reverted together with the list, in one batch. Nothing else stamps a row
// before the operation's end, so a process that dies part way through an
// operation leaves the rows it did not reach, and only those, standing.
type rowSim struct {
	list     []string
	clock    time.Time
	journals map[string][]*database.OperationChange
	rows     map[string]*database.OperationChange // row id -> row
	markers  map[string]*database.OperationChange // row id -> marker
	notes    []string                             // placements by recorded index (reported)
	released int
	leftOut  int
	stale    int
}

func newRowSim(l0 []string) *rowSim {
	return &rowSim{list: slices.Clone(l0), clock: time.Unix(1_700_000_000, 0),
		journals: map[string][]*database.OperationChange{}, rows: map[string]*database.OperationChange{}, markers: map[string]*database.OperationChange{}}
}

func (s *rowSim) tick() time.Time { s.clock = s.clock.Add(time.Second); return s.clock }

// retire returns the row id, or "" when the book is not listed.
func (s *rowSim) retire(op, retired, survivor string) string {
	next, rec, ok := CarryMembership(s.list, retired, survivor)
	if !ok {
		return ""
	}
	val, _ := EncodeMembership(rec)
	s.list = next
	kind := TookKind(UserContentCollection)
	if rec.SurvivorWasMember {
		kind = HeldKind(UserContentCollection)
	}
	now := s.tick()
	id := fmt.Sprintf("%s/%s/%d", op, retired, now.Unix())
	m := &database.OperationChange{ID: id + "/m", OperationID: op, BookID: survivor,
		ChangeType: ChangeTypeUserContentFollow, FieldName: UserContentField(kind, "L"), OldValue: val, NewValue: retired, CreatedAt: now}
	r := &database.OperationChange{ID: id + "/r", OperationID: op, BookID: retired,
		ChangeType: ChangeTypeUserContentFollow, FieldName: UserContentField(UserContentCollection, "L"), OldValue: val, NewValue: survivor, CreatedAt: now}
	s.journals[survivor] = append(s.journals[survivor], m)
	s.journals[retired] = append(s.journals[retired], r)
	s.rows[r.ID], s.markers[r.ID] = r, m
	return r.ID
}

func (s *rowSim) stamp(rows ...*database.OperationChange) {
	at := s.tick()
	for _, r := range rows {
		if r != nil && r.RevertedAt == nil {
			r.RevertedAt = &at
		}
	}
}

// revertRow reverts one row. A refusal is returned and nothing is written.
func (s *rowSim) revertRow(id string) error {
	r := s.rows[id]
	rec, _ := DecodeMembership(r.OldValue)
	sv := r.NewValue
	own := s.journals[r.BookID]
	m := MarkerOf(s.journals[sv], r, UserContentCollection, "L")
	standing := m != nil && m.RevertedAt == nil
	if !standing && NewerCarryStands(own, r, UserContentCollection, "L") != nil {
		s.stale++
		s.stamp(r)
		return nil
	}
	g := NewMembershipGraph(sv, UserContentCollection, "L")
	g.SetRow(r)
	g.AddJournal(sv, s.journals[sv])
	g.AddJournal(r.BookID, own)
	g.SetJournalLoader(func(b string) ([]*database.OperationChange, error) { return s.journals[b], nil })
	role := MemberRoleOf(s.journals[sv], own, r, UserContentCollection, "L")
	next, res, err := RestoreMember(s.list, r.BookID, sv, rec, role, g)
	if err != nil && !errors.Is(err, ErrAlreadyRestored) {
		return err
	}
	if res.Note != "" {
		s.notes = append(s.notes, res.Note)
	}
	if res.Released {
		s.released++
	}
	if res.LeftOut {
		s.leftOut++
	}
	if err == nil {
		s.list = next
	}
	if standing {
		s.stamp(m, r)
	}
	return nil
}

type rowSimCfg struct {
	survivors int
	chain     bool // a survivor may itself be retired into another survivor
	crashMid  int  // 1/n chance an operation's revert dies after some of its rows
	steps     int
}

type rowSimStats struct{ total, exact, reported, refused, silentFinal, silentInter, released, leftOut, stale int }

func runRowSim(t *testing.T, seed uint64, n int, cfg rowSimCfg) rowSimStats {
	t.Helper()
	var st rowSimStats
	rng := rand.New(rand.NewPCG(seed, seed*7+1))
	for caseN := range n {
		size := 5 + rng.IntN(5)
		var l0 []string
		for i := range size {
			l0 = append(l0, fmt.Sprintf("b%d", i))
		}
		svs := []string{"S1", "S2", "S3"}[:cfg.survivors]
		for _, sv := range svs {
			if rng.IntN(2) == 0 {
				l0 = slices.Insert(l0, rng.IntN(len(l0)+1), sv)
			}
		}
		isSv := func(b string) bool { return slices.Contains(svs, b) }
		orig := map[string]bool{}
		for _, b := range l0 {
			orig[b] = true
		}
		s := newRowSim(l0)
		type op struct {
			id   string
			rows []string // in journal order
		}
		var ops []*op
		var log []string
		refused := false
		isDone := func(o *op) bool {
			return !slices.ContainsFunc(o.rows, func(id string) bool { return s.rows[id].RevertedAt == nil })
		}
		// A book of l0 that is not a survivor on borrowed account keeps l0's
		// order against every other such book, whatever has been reverted.
		checkInter := func(when string) {
			if len(s.notes) > 0 {
				return
			}
			in := map[string]bool{}
			for _, b := range s.list {
				in[b] = true
			}
			var want, got []string
			for _, b := range l0 {
				if in[b] && (!isSv(b) || orig[b]) {
					want = append(want, b)
				}
			}
			for _, b := range s.list {
				if orig[b] {
					got = append(got, b)
				}
			}
			if !slices.Equal(want, got) {
				st.silentInter++
				if st.silentInter <= 3 {
					t.Logf("INTER case%d %s l0=%v log=%v list=%v", caseN, when, l0, log, s.list)
				}
			}
		}
		// revertOp reverts the rows of one operation, newest first, as
		// RevertOperation does; a crash stops it after some of them.
		revertOp := func(o *op) {
			rows := slices.Clone(o.rows)
			slices.Reverse(rows)
			rows = slices.DeleteFunc(rows, func(id string) bool { return s.rows[id].RevertedAt != nil })
			if cfg.crashMid > 0 && len(rows) > 0 && rng.IntN(cfg.crashMid) == 0 {
				rows = rows[:rng.IntN(len(rows))]
				log = append(log, "R"+o.id+"(crash)")
			} else {
				log = append(log, "R"+o.id)
			}
			for _, id := range rows {
				if err := s.revertRow(id); err != nil {
					refused = true
					log = append(log, "REFUSE "+id+": "+err.Error())
					return
				}
			}
			checkInter(o.id)
		}
		live := func() []*op {
			var out []*op
			for _, o := range ops {
				if !isDone(o) {
					out = append(out, o)
				}
			}
			return out
		}
		for step := 0; step < cfg.steps && !refused; step++ {
			if l := live(); len(l) > 0 && rng.IntN(2) == 0 {
				revertOp(l[rng.IntN(len(l))])
				continue
			}
			var cand []string
			for _, b := range s.list {
				if !isSv(b) || cfg.chain {
					cand = append(cand, b)
				}
			}
			if len(cand) < 1 {
				continue
			}
			rng.Shuffle(len(cand), func(i, j int) { cand[i], cand[j] = cand[j], cand[i] })
			o := &op{id: fmt.Sprintf("op%d", len(ops))}
			// A book that is itself retired and not yet restored cannot be a survivor.
			retiredNow := func(b string) bool {
				for _, r := range s.rows {
					if r.BookID == b && r.RevertedAt == nil {
						return true
					}
				}
				return false
			}
			var into []string
			for _, b := range cand[:min(len(cand), 1+rng.IntN(2))] {
				sv := svs[rng.IntN(len(svs))]
				if sv == b || retiredNow(sv) {
					continue
				}
				if id := s.retire(o.id, b, sv); id != "" {
					o.rows = append(o.rows, id)
					into = append(into, b+">"+sv)
				}
			}
			if len(o.rows) == 0 {
				continue
			}
			ops = append(ops, o)
			log = append(log, fmt.Sprintf("%s%v", o.id, into))
		}
		// Revert everything left, retrying what a crash left, until done.
		for guard := 0; guard < 60 && !refused; guard++ {
			l := live()
			if len(l) == 0 {
				break
			}
			revertOp(l[rng.IntN(len(l))])
		}
		st.total++
		st.released += s.released
		st.leftOut += s.leftOut
		st.stale += s.stale
		switch {
		case refused || len(live()) > 0:
			st.refused++
			if st.refused <= 2 {
				t.Logf("REFUSED case%d l0=%v log=%v list=%v", caseN, l0, log, s.list)
			}
		case len(s.notes) > 0:
			st.reported++
			if st.reported <= 2 {
				t.Logf("REPORTED case%d l0=%v log=%v list=%v notes=%v", caseN, l0, log, s.list, s.notes)
			}
		case slices.Equal(s.list, l0):
			st.exact++
		default:
			st.silentFinal++
			if st.silentFinal <= 4 {
				t.Logf("SILENT case%d l0=%v log=%v list=%v", caseN, l0, log, s.list)
			}
		}
	}
	return st
}

// reportRowSim logs the counts and fails on any silent wrong answer, in the
// final list or after any operation. With wantExact it also fails on any
// placement reported by index and any refusal (every journal is readable here,
// so the claim is that the order is exact and every revert goes through); without
// it, reported placements and refusals are counted and logged only.
func reportRowSim(t *testing.T, name string, st rowSimStats, wantExact bool) {
	t.Helper()
	t.Logf("%s: total=%d exact=%d reported=%d refused=%d silent-final=%d silent-intermediate=%d (survivors released %d, books left out %d, stale rows skipped %d)",
		name, st.total, st.exact, st.reported, st.refused, st.silentFinal, st.silentInter, st.released, st.leftOut, st.stale)
	if st.silentFinal > 0 || st.silentInter > 0 {
		t.Errorf("%s: %d silent wrong final, %d silent wrong intermediate", name, st.silentFinal, st.silentInter)
	}
	if wantExact && (st.refused > 0 || st.reported > 0 || st.exact != st.total) {
		t.Errorf("%s: %d refused, %d reported by index, %d exact of %d (claim: exact when every journal is readable)", name, st.refused, st.reported, st.exact, st.total)
	}
}

func TestRowSim_TwoSurvivorsExact(t *testing.T) {
	t.Parallel()
	reportRowSim(t, "2 survivors", runRowSim(t, 101, 6000, rowSimCfg{survivors: 2, steps: 10}), true)
}

func TestRowSim_ThreeSurvivorsExact(t *testing.T) {
	t.Parallel()
	reportRowSim(t, "3 survivors", runRowSim(t, 202, 6000, rowSimCfg{survivors: 3, steps: 12}), true)
}

// A process that dies part way through an operation's revert leaves the rows it
// did not reach standing; the retry finishes them.
func TestRowSim_CrashMidOperationExact(t *testing.T) {
	t.Parallel()
	reportRowSim(t, "3 survivors, crashes", runRowSim(t, 303, 6000, rowSimCfg{survivors: 3, steps: 10, crashMid: 3}), true)
}

// A survivor may itself be retired into another survivor (the duplicate-copies
// fixer does), listed on borrowed account or not. Reverting such a history out
// of order can refuse (a survivor that stands in a slot and was retired since
// cannot be swapped back) or fall back to the recorded index and say so; it
// must never leave a wrong list without saying so.
func TestRowSim_SurvivorChainsNeverSilent(t *testing.T) {
	t.Parallel()
	reportRowSim(t, "3 survivors, chains", runRowSim(t, 505, 6000, rowSimCfg{survivors: 3, steps: 10, chain: true}), false)
}

func TestRowSim_ChainsAndCrashesNeverSilent(t *testing.T) {
	t.Parallel()
	reportRowSim(t, "chains and crashes", runRowSim(t, 606, 6000, rowSimCfg{survivors: 3, steps: 12, chain: true, crashMid: 4}), false)
}

func TestRowSim_SmallChainCasesNeverSilent(t *testing.T) {
	t.Parallel()
	for _, steps := range []int{4, 5, 6, 8} {
		reportRowSim(t, fmt.Sprintf("2 survivors, chains and crashes, steps=%d", steps),
			runRowSim(t, uint64(900+steps), 20000, rowSimCfg{survivors: 2, steps: steps, chain: true, crashMid: 2}), false)
	}
}

// A survivor (S1) listed only on borrowed account (it took X's slot, then also
// stands for Y) is itself retired into S3, an original member. The older
// operations are reverted first and the retire of S1 last: S1 must not come back
// into a list it never belonged to.
func TestSurvivorChain_BorrowedSurvivorDoesNotReturn(t *testing.T) {
	t.Parallel()
	l0 := []string{"a", "X", "Y", "S3"}
	s := newRowSim(l0)
	rev := func(ids ...string) {
		t.Helper()
		for _, id := range slices.Backward(ids) {
			if err := s.revertRow(id); err != nil {
				t.Fatalf("revert %s: %v", id, err)
			}
		}
	}
	a := s.retire("opA", "X", "S1")  // took
	b := s.retire("opB", "Y", "S1")  // held, borrowing
	rev(a)                           // X back, S1 stays for B
	d := s.retire("opD", "S1", "S3") // S1 retired into S3 (held)
	rev(b)                           // Y back; S1 is not listed, nothing to take out
	rev(d)                           // S1 must stay out
	if !slices.Equal(s.list, l0) || len(s.notes) > 0 {
		t.Fatalf("got %v (notes %v), want %v exactly", s.list, s.notes, l0)
	}
	if s.leftOut != 1 {
		t.Fatalf("books left out = %d, want 1 (S1)", s.leftOut)
	}
}

// The same with the retire of S1 reverted while S1 is still borrowed: it goes
// back, and leaves with the last retire that stands on it.
func TestSurvivorChain_StillBorrowedSurvivorReturnsThenLeaves(t *testing.T) {
	t.Parallel()
	l0 := []string{"a", "X", "Y", "S3"}
	s := newRowSim(l0)
	a := s.retire("opA", "X", "S1")
	b := s.retire("opB", "Y", "S1")
	d := s.retire("opD", "S1", "S3")
	for _, id := range []string{d, b, a} {
		if err := s.revertRow(id); err != nil {
			t.Fatalf("revert %s: %v", id, err)
		}
	}
	if !slices.Equal(s.list, l0) || len(s.notes) > 0 {
		t.Fatalf("got %v (notes %v), want %v exactly", s.list, s.notes, l0)
	}
}

// The fuzz's first failing shape: S3 entered the list by a took of its own.
func TestSurvivorChain_SurvivorListedByATookOfItsOwn(t *testing.T) {
	t.Parallel()
	l0 := []string{"b0", "b1", "b2", "b3", "b4", "b5"}
	s := newRowSim(l0)
	rev := func(ids ...string) {
		t.Helper()
		for _, id := range slices.Backward(ids) {
			if err := s.revertRow(id); err != nil {
				t.Fatalf("revert %s: %v", id, err)
			}
		}
	}
	a1 := s.retire("op0", "b4", "S2")
	a2 := s.retire("op0", "b3", "S1")
	c := s.retire("op1", "b1", "S3")
	b := s.retire("op2", "b2", "S1")
	rev(a1, a2)
	d := s.retire("op3", "S1", "S3")
	rev(b)
	rev(d)
	rev(c)
	if !slices.Equal(s.list, l0) || len(s.notes) > 0 {
		t.Fatalf("got %v (notes %v), want %v exactly", s.list, s.notes, l0)
	}
}

// A row whose own stamp was lost (a death before it) is retried after a newer
// retire took the same book out again: it is stale, and putting the book back
// would list a book a standing retire removed.
func TestStaleRowIsNotRetriedAgainstANewerRetire(t *testing.T) {
	t.Parallel()
	l0 := []string{"a", "X", "S3", "b"}
	s := newRowSim(l0)
	old := s.retire("op2", "X", "S3") // held
	if err := s.revertRow(old); err != nil {
		t.Fatal(err)
	}
	// The process died before the row's own stamp: the row stands again.
	s.rows[old].RevertedAt, s.markers[old].RevertedAt = nil, nil
	s.markers[old].RevertedAt = new(time.Time)
	*s.markers[old].RevertedAt = s.tick()
	fresh := s.retire("op5", "X", "S1") // S1 is not listed: it takes X's slot
	want := slices.Clone(s.list)
	if err := s.revertRow(old); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.list, want) || s.stale != 1 {
		t.Fatalf("list %v (stale skipped %d), want %v untouched and one stale row skipped", s.list, s.stale, want)
	}
	if err := s.revertRow(fresh); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.list, l0) {
		t.Fatalf("after reverting the newer retire: %v, want %v", s.list, l0)
	}
}
