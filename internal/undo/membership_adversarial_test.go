// file: internal/undo/membership_adversarial_test.go
// version: 1.2.0
// guid: 4a9d17c3-e605-4b82-8f31-c27d5b09e164
// last-edited: 2026-10-30

package undo

import (
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// advSim is an adversarial row-level model of the carry and its revert, with
// a differential oracle (every placement is also computed with every journal
// handed in), a preflight oracle (CheckMembershipFollow before the op and
// before each row against the real outcome), hand edits, and clock modes.
type advSim struct {
	list    []string
	clock   time.Time
	rng     *rand.Rand
	cfg     advCfg
	all     []*database.OperationChange // the list's whole journal, in creation order
	rows    map[string]*database.OperationChange
	markers map[string]*database.OperationChange
	notes   []string
	touched map[string]bool
	seq     int
	ticks   int
	hw      int64 // the journal clock's high-water mark (CreatedSeq / RevertedSeq), ns
	stepAt  int
	st      *advStats
	log     *[]string
	// orphans are the markers injected with no live row (injectOrphan), by id.
	orphans map[string]*database.OperationChange
}

type advCfg struct {
	survivors  int
	minSize    int
	maxSize    int
	steps      int
	maxRows    int
	chain      bool
	crashMid   int  // 1/n an op's revert stops after some rows
	handRemove int  // 1/n per step: the owner removes a listed book
	handReAdd  int  // 1/n per step: the owner puts an absent l0 book back at a random place
	svBias     int  // 1/n: retire a listed survivor when one is listed
	tsMode     int  // 0: row 1µs after marker; 1: marker and row at one instant; 2: clock may step back
	mono       bool // the journal clock: every stamp strictly after the last (the store does this)
	orphan     int  // 1/n per step: a marker is journaled whose retire never got a live row (a crash between the marker and the row, or a failed void)
}

type advStats struct {
	total, exact, reported, refusedClean, refusedManual, silentFinal, silentInter, silentPresence int
	preRows, preMismatchRefuse, preMismatchAlready, sameMismatchRefuse, sameMismatchAlready       int
	manualCases                                                                                   int
	orphansInjected, orphansMissed                                                                int
	samples                                                                                       []string
	perKind                                                                                       map[string]int
}

func (st *advStats) sample(s string) {
	if st.perKind == nil {
		st.perKind = map[string]int{}
	}
	k := strings.SplitN(s, " ", 2)[0]
	if st.perKind[k] < advSamplesPerKind {
		st.perKind[k]++
		st.samples = append(st.samples, s)
	}
}

var advSamplesPerKind = 8

var advTraceSeed, advTraceCase uint64 = 0, 1 << 62

func (s *advSim) tick() time.Time {
	d := time.Second
	if s.cfg.tsMode == 2 && s.rng.IntN(6) == 0 {
		d = -time.Duration(1+s.rng.IntN(3)) * time.Second
	}
	s.ticks++
	if s.cfg.tsMode == 3 && s.ticks == s.stepAt {
		d = -time.Duration(1+s.rng.IntN(3)) * time.Second
	}
	s.clock = s.clock.Add(d)
	return s.clock
}

// seqOf is the journal clock: max(wall, last+1ns), as the store's persisted
// high-water mark gives every journal write (row, marker and stamp alike). With
// mono the rows carry it as CreatedSeq / RevertedSeq while CreatedAt and
// RevertedAt stay the wall time, which may step back; without it they carry no
// sequence and the decisions fall back to the wall time (rows written before the
// sequences existed).
func (s *advSim) seqOf(t time.Time) int64 {
	if !s.cfg.mono {
		return 0
	}
	if ns := t.UnixNano(); ns > s.hw {
		s.hw = ns
	} else {
		s.hw++
	}
	return s.hw
}

func (s *advSim) newID(op, b string) string {
	s.seq++
	return fmt.Sprintf("%06d-%s-%s", s.seq, op, b) // sorts by creation, like a ULID
}

func (s *advSim) retire(op, retired, survivor string) string {
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
	rowAt := now.Add(time.Microsecond)
	switch s.cfg.tsMode {
	case 1:
		rowAt = now
	case 4:
		rowAt = now.Add(-time.Microsecond) // the clock stepped back between the marker and the row
	}
	m := &database.OperationChange{ID: s.newID(op, survivor), OperationID: op, BookID: survivor,
		ChangeType: ChangeTypeUserContentFollow, FieldName: UserContentField(kind, "L"), OldValue: val, NewValue: retired, CreatedAt: now, CreatedSeq: s.seqOf(now)}
	r := &database.OperationChange{ID: s.newID(op, retired), OperationID: op, BookID: retired,
		ChangeType: ChangeTypeUserContentFollow, FieldName: UserContentField(UserContentCollection, "L"), OldValue: val, NewValue: survivor, CreatedAt: rowAt, CreatedSeq: s.seqOf(rowAt)}
	s.all = append(s.all, m, r)
	s.rows[r.ID], s.markers[r.ID] = r, m
	return r.ID
}

// injectOrphan journals a survivor-side marker for the retire of retired into
// survivor the way a crash between JournalSteps' two commits left it (an older
// build), or a failed void left it: the marker, and no row of the retire (kind 0)
// or a voided one (kind 1). The list write never ran, so the list is as it was.
// A quarter of them are stamped reverted at once (their operation was reverted).
// It reports whether one was written.
func (s *advSim) injectOrphan(retired, survivor string, kind int) bool {
	_, rec, ok := CarryMembership(s.list, retired, survivor)
	if !ok {
		return false
	}
	val, _ := EncodeMembership(rec)
	field := TookKind(UserContentCollection)
	if rec.SurvivorWasMember {
		field = HeldKind(UserContentCollection)
	}
	op := fmt.Sprintf("orphan%d", s.seq)
	now := s.tick()
	m := &database.OperationChange{ID: s.newID(op, survivor), OperationID: op, BookID: survivor,
		ChangeType: ChangeTypeUserContentFollow, FieldName: UserContentField(field, "L"), OldValue: val, NewValue: retired, CreatedAt: now, CreatedSeq: s.seqOf(now)}
	s.all = append(s.all, m)
	if kind == 1 {
		at := s.tick()
		rowAt := s.tick()
		v := &database.OperationChange{ID: s.newID(op, retired), OperationID: op, BookID: retired, ChangeType: ChangeTypeUserContentFollow,
			FieldName: UserContentField(UserContentCollection, "L"), OldValue: val, NewValue: survivor, CreatedAt: at, CreatedSeq: s.seqOf(at),
			RevertedAt: &rowAt, RevertedSeq: s.seqOf(rowAt), Voided: true}
		s.all = append(s.all, v)
	}
	if s.rng.IntN(4) == 0 {
		s.stamp(m)
	}
	s.orphans[m.ID] = m
	s.st.orphansInjected++
	return true
}

func (s *advSim) stamp(rows ...*database.OperationChange) {
	at := s.tick()
	for _, r := range rows {
		if r != nil && r.RevertedAt == nil {
			r.RevertedAt, r.RevertedSeq = &at, s.seqOf(at)
		}
	}
}

type advOutcome int

const (
	outWrote advOutcome = iota
	outAlready
	outRefused
)

func advClassOf(err error) advOutcome {
	switch {
	case err == nil:
		return outWrote
	case errors.Is(err, ErrAlreadyRestored):
		return outAlready
	}
	return outRefused
}

// graph is the order graph of row r, built the way a revert builds it: the
// row itself and every row of the list's journal, in one read.
func (s *advSim) graph(r *database.OperationChange) *MembershipGraph {
	g := NewMembershipGraph(r.NewValue, UserContentCollection, "L")
	g.SetRow(r)
	g.AddRows(s.all)
	return g
}

// preflight is CheckMembershipFollow on the state as it is now.
func (s *advSim) preflight(id string) advOutcome {
	r := s.rows[id]
	return advClassOf(CheckMembershipFollow(s.list, r, UserContentCollection, "L", s.all))
}

func (s *advSim) revertRow(id string) (advOutcome, error) {
	r := s.rows[id]
	rec, _ := DecodeMembership(r.OldValue)
	sv := r.NewValue
	m := MarkerOf(s.all, r, UserContentCollection, "L")
	standing := m != nil && m.RevertedAt == nil
	role := MemberRoleOf(s.all, r, UserContentCollection, "L")
	next, res, err := RestoreMember(s.list, r.BookID, sv, rec, role, s.graph(r))
	if err != nil && !errors.Is(err, ErrAlreadyRestored) {
		return outRefused, err
	}
	if res.Note != "" {
		s.notes = append(s.notes, res.Note)
	}
	if res.Kept != "" {
		s.notes = append(s.notes, res.Kept)
	}
	if err == nil {
		s.list = next
	}
	if standing {
		s.stamp(m, r)
	}
	if err == nil {
		return outWrote, nil
	}
	return outAlready, nil
}

func runAdv(t *testing.T, seed uint64, n int, cfg advCfg) advStats {
	t.Helper()
	var st advStats
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	allSvs := []string{"S1", "S2", "S3", "S4", "S5"}
	for caseN := range n {
		size := cfg.minSize + rng.IntN(cfg.maxSize-cfg.minSize+1)
		var l0 []string
		for i := range size {
			l0 = append(l0, fmt.Sprintf("b%d", i))
		}
		svs := allSvs[:cfg.survivors]
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
		var log []string
		s := &advSim{list: slices.Clone(l0), clock: time.Unix(1_700_000_000, 0), rng: rng, cfg: cfg,
			rows:    map[string]*database.OperationChange{},
			markers: map[string]*database.OperationChange{}, orphans: map[string]*database.OperationChange{}, touched: map[string]bool{}, st: &st, log: &log, stepAt: 1 + rng.IntN(3*cfg.steps)}
		type op struct {
			id   string
			rows []string
		}
		var ops []*op
		refused := false
		manual := false
		isDone := func(o *op) bool {
			return !slices.ContainsFunc(o.rows, func(id string) bool { return s.rows[id].RevertedAt == nil })
		}
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
				if in[b] && !s.touched[b] {
					want = append(want, b)
				}
			}
			for _, b := range s.list {
				if orig[b] && !s.touched[b] {
					got = append(got, b)
				}
			}
			if !slices.Equal(want, got) {
				st.silentInter++
				st.sample(fmt.Sprintf("INTER case%d %s l0=%v log=%v list=%v", caseN, when, l0, log, s.list))
			}
		}
		retiredNow := func(b string) bool {
			for _, r := range s.rows {
				if r.BookID == b && r.RevertedAt == nil {
					return true
				}
			}
			return false
		}
		revertOp := func(o *op) {
			rows := slices.Clone(o.rows)
			slices.Reverse(rows)
			rows = slices.DeleteFunc(rows, func(id string) bool { return s.rows[id].RevertedAt != nil })
			// The preflight judges every row against the state before the revert.
			pre := map[string]advOutcome{}
			for _, id := range rows {
				pre[id] = s.preflight(id)
			}
			if cfg.crashMid > 0 && len(rows) > 0 && rng.IntN(cfg.crashMid) == 0 {
				rows = rows[:rng.IntN(len(rows))]
				log = append(log, "R"+o.id+"(crash)")
			} else {
				log = append(log, "R"+o.id)
			}
			for _, id := range rows {
				same := s.preflight(id)
				got, err := s.revertRow(id)
				st.preRows++
				if (pre[id] == outRefused) != (got == outRefused) {
					st.preMismatchRefuse++
					st.sample(fmt.Sprintf("PRE-REFUSE row=%s pre=%d got=%d err=%v log=%v", id, pre[id], got, err, log))
				} else if pre[id] != got {
					st.preMismatchAlready++
				}
				if (same == outRefused) != (got == outRefused) {
					st.sameMismatchRefuse++
					st.sample(fmt.Sprintf("SAME-REFUSE row=%s same=%d got=%d err=%v log=%v", id, same, got, err, log))
				} else if same != got {
					st.sameMismatchAlready++
				}
				if err != nil {
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
			if seed == advTraceSeed && uint64(caseN) == advTraceCase && len(log) > 0 {
				t.Logf("  %-24s -> %v notes=%d", log[len(log)-1], s.list, len(s.notes))
			}
			if cfg.handRemove > 0 && rng.IntN(cfg.handRemove) == 0 && len(s.list) > 1 {
				b := s.list[rng.IntN(len(s.list))]
				s.list = slices.DeleteFunc(slices.Clone(s.list), func(x string) bool { return x == b })
				s.touched[b] = true
				manual = true
				log = append(log, "HANDRM("+b+")")
				continue
			}
			if cfg.handReAdd > 0 && rng.IntN(cfg.handReAdd) == 0 {
				var absent []string
				for _, b := range l0 {
					if !slices.Contains(s.list, b) {
						absent = append(absent, b)
					}
				}
				if len(absent) > 0 {
					b := absent[rng.IntN(len(absent))]
					s.list = slices.Insert(slices.Clone(s.list), rng.IntN(len(s.list)+1), b)
					s.touched[b] = true
					manual = true
					log = append(log, "HANDADD("+b+")")
					continue
				}
			}
			if cfg.orphan > 0 && rng.IntN(cfg.orphan) == 0 && len(s.list) > 1 {
				b, sv := s.list[rng.IntN(len(s.list))], svs[rng.IntN(len(svs))]
				if b != sv && !retiredNow(sv) && s.injectOrphan(b, sv, rng.IntN(2)) {
					log = append(log, "ORPHAN("+b+">"+sv+")")
				}
				continue
			}
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
			if cfg.svBias > 0 && rng.IntN(cfg.svBias) == 0 {
				slices.SortStableFunc(cand, func(a, b string) int {
					switch {
					case isSv(a) && !isSv(b):
						return -1
					case isSv(b) && !isSv(a):
						return 1
					}
					return 0
				})
			}
			o := &op{id: fmt.Sprintf("op%d", len(ops))}
			var into []string
			k := 1 + rng.IntN(cfg.maxRows)
			for _, b := range cand[:min(len(cand), k)] {
				sv := svs[rng.IntN(len(svs))]
				if sv == b || retiredNow(sv) || !slices.Contains(s.list, b) {
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
		for guard := 0; guard < 80 && !refused; guard++ {
			if seed == advTraceSeed && uint64(caseN) == advTraceCase && len(log) > 0 {
				t.Logf("  %-24s -> %v notes=%d", log[len(log)-1], s.list, len(s.notes))
			}
			l := live()
			if len(l) == 0 {
				break
			}
			revertOp(l[rng.IntN(len(l))])
		}
		if seed == advTraceSeed && uint64(caseN) == advTraceCase && len(log) > 0 {
			t.Logf("  %-24s -> %v notes=%d", log[len(log)-1], s.list, len(s.notes))
		}
		// Every standing orphan is reported, and nothing else is (the lister is
		// what the revert reports from).
		gotOrphans := map[string]bool{}
		for _, m := range ignoredOf(s.all, UserContentCollection, "L") {
			if m.Standing {
				gotOrphans[m.ID] = true
			}
		}
		wantOrphans := map[string]bool{}
		for id, m := range s.orphans {
			if m.RevertedAt == nil {
				wantOrphans[id] = true
			}
		}
		if !maps.Equal(gotOrphans, wantOrphans) {
			st.orphansMissed++
			st.sample(fmt.Sprintf("ORPHAN-REPORT case%d log=%v want=%v got=%v", caseN, log, wantOrphans, gotOrphans))
		}
		st.total++
		if manual {
			st.manualCases++
		}
		// Untouched books of l0 keep l0's order whatever happened.
		var want, got []string
		for _, b := range l0 {
			if !s.touched[b] && slices.Contains(s.list, b) {
				want = append(want, b)
			}
		}
		for _, b := range s.list {
			if orig[b] && !s.touched[b] {
				got = append(got, b)
			}
		}
		orderOK := slices.Equal(want, got)
		switch {
		case refused || len(live()) > 0:
			if manual {
				st.refusedManual++
			} else {
				st.refusedClean++
				st.sample(fmt.Sprintf("REFUSED case%d l0=%v log=%v list=%v", caseN, l0, log, s.list))
			}
			if !orderOK && len(s.notes) == 0 {
				st.silentFinal++
				st.sample(fmt.Sprintf("SILENT-ORDER(refused) case%d l0=%v log=%v list=%v", caseN, l0, log, s.list))
			}
		case len(s.notes) > 0:
			st.reported++
			st.sample(fmt.Sprintf("REPORTED case%d l0=%v log=%v list=%v notes=%v", caseN, l0, log, s.list, s.notes))
		case !manual && slices.Equal(s.list, l0):
			st.exact++
		case manual && orderOK && !slices.ContainsFunc(s.list, func(b string) bool { return !orig[b] }) &&
			!slices.ContainsFunc(l0, func(b string) bool { return !s.touched[b] && !slices.Contains(s.list, b) }):
			st.exact++
		default:
			st.silentFinal++
			if slices.ContainsFunc(s.list, func(b string) bool { return !orig[b] }) || slices.ContainsFunc(l0, func(b string) bool { return !s.touched[b] && !slices.Contains(s.list, b) }) {
				st.silentPresence++
			}
			st.sample(fmt.Sprintf("SILENT case%d l0=%v log=%v list=%v touched=%v", caseN, l0, log, s.list, s.touched))
		}
	}
	return st
}

func (st advStats) String() string {
	return fmt.Sprintf("total=%d exact=%d reported=%d refusedClean=%d refusedManual=%d silentF=%d(presence %d) silentI=%d | preflight(pre-op) refuse-mismatch=%d already-mismatch=%d same-state refuse-mismatch=%d already-mismatch=%d of %d rows | manual cases %d",
		st.total, st.exact, st.reported, st.refusedClean, st.refusedManual, st.silentFinal, st.silentPresence, st.silentInter,
		st.preMismatchRefuse, st.preMismatchAlready, st.sameMismatchRefuse, st.sameMismatchAlready, st.preRows, st.manualCases)
}

// advSuite is one adversarial run: a model configuration over several seeds.
type advSuite struct {
	name   string
	seeds  []uint64
	n      int
	cfg    advCfg
	manual bool // the owner edits the list by hand during the run
}

func advSuites() []advSuite {
	base := advCfg{survivors: 4, minSize: 3, maxSize: 12, steps: 16, maxRows: 3, chain: true, svBias: 2}
	with := func(f func(c *advCfg)) advCfg { c := base; f(&c); return c }
	return []advSuite{
		{"base3", []uint64{11, 12, 13}, 4000, advCfg{survivors: 3, minSize: 5, maxSize: 9, steps: 12, maxRows: 2, chain: true}, false},
		{"deep4", []uint64{21, 22, 23}, 3000, base, false},
		{"deep5crash", []uint64{31, 32}, 2500, advCfg{survivors: 5, minSize: 4, maxSize: 14, steps: 22, maxRows: 3, chain: true, svBias: 2, crashMid: 4}, false},
		// Every pair of rows written at one instant: the order must come from the
		// journal's ids, not its timestamps.
		{"sharedTS", []uint64{41, 42}, 3000, with(func(c *advCfg) { c.tsMode = 1 }), false},
		// The wall clock steps back; the journal clock (mono) keeps the order.
		{"clockBack", []uint64{51, 52}, 3000, with(func(c *advCfg) { c.tsMode, c.mono = 2, true }), false},
		{"oneStepBack", []uint64{53, 54}, 3000, with(func(c *advCfg) { c.tsMode, c.mono = 3, true }), false},
		{"rowBeforeMarker", []uint64{55}, 3000, with(func(c *advCfg) { c.tsMode, c.mono = 4, true }), false},
		// Markers with no live row (a crash between a retire's marker and its row, or
		// a failed void) among the real retires: ignored, reported, and no history
		// is wrong. The wall clock steps back in the second one.
		{"orphans", []uint64{91, 92}, 3000, with(func(c *advCfg) { c.orphan = 4 }), false},
		{"orphansClockBack", []uint64{93}, 3000, with(func(c *advCfg) { c.orphan, c.tsMode, c.mono = 3, 2, true }), false},
		{"orphansCrash", []uint64{94}, 2500, advCfg{survivors: 5, minSize: 4, maxSize: 14, steps: 22, maxRows: 3, chain: true, svBias: 2, crashMid: 4, orphan: 3}, false},
		{"orphansHand", []uint64{95}, 3000, advCfg{survivors: 4, minSize: 4, maxSize: 12, steps: 16, maxRows: 3, chain: true, svBias: 2, handRemove: 14, handReAdd: 14, orphan: 4}, true},
		{"handEdits", []uint64{61, 62}, 3000, advCfg{survivors: 4, minSize: 4, maxSize: 12, steps: 16, maxRows: 3, chain: true, svBias: 2, handRemove: 14, handReAdd: 14}, true},
		{"handAddOnly", []uint64{71}, 3000, advCfg{survivors: 3, minSize: 4, maxSize: 10, steps: 14, maxRows: 2, chain: true, handReAdd: 8}, true},
		{"handRmOnly", []uint64{81}, 3000, advCfg{survivors: 3, minSize: 4, maxSize: 10, steps: 14, maxRows: 2, chain: true, handRemove: 8}, true},
	}
}

// requireAdvSuite is the claim of one suite. Without hand edits every history is
// exact: no refusal, no placement by index, nothing silent, and the preflight
// predicts every revert's refusal. With hand edits the claim is the weaker one
// the owner asked for: nothing is ever silent (every untouched book ends in its
// original place or the revert said something), and every history is either
// exact, reported, or refused with a reason.
func requireAdvSuite(t *testing.T, su advSuite, seed uint64, n int, st advStats) {
	t.Helper()
	t.Logf("%-15s seed=%d total=%d exact=%d reported=%d refusedClean=%d refusedManual=%d silentFinal=%d silentInter=%d orphans=%d unreported=%d", su.name, seed,
		st.total, st.exact, st.reported, st.refusedClean, st.refusedManual, st.silentFinal, st.silentInter, st.orphansInjected, st.orphansMissed)
	for _, smp := range st.samples {
		t.Logf("    %s", smp)
	}
	require.Equal(t, n, st.total)
	require.Zero(t, st.silentFinal, "silent wrong final lists")
	require.Zero(t, st.silentInter, "silent wrong lists after an operation")
	require.Zero(t, st.silentPresence, "a book lost or duplicated with no note")
	require.Zero(t, st.orphansMissed, "a standing orphan marker was not reported, or a live marker was")
	if su.cfg.orphan > 0 {
		require.Positive(t, st.orphansInjected, "the suite injected no orphan")
	}
	require.Zero(t, st.refusedClean, "a refusal with no hand edit")
	require.Zero(t, st.preMismatchRefuse, "the preflight disagreed with the revert about a refusal")
	require.Zero(t, st.sameMismatchRefuse, "the preflight disagreed with the revert about a refusal (same state)")
	if su.manual {
		require.Equal(t, st.total, st.exact+st.reported+st.refusedManual, "every history is exact, reported or refused")
		return
	}
	require.Equal(t, st.total, st.exact, "without hand edits every history is exact")
	require.Zero(t, st.reported)
}

// advShortDivisor is how much smaller each suite runs under -short: one seed
// and a tenth of the histories. The full table is ~660 CPU-seconds under -race
// (deep4 alone 88 s at GOMAXPROCS=2, measured 2026-10-30), and on a shared
// Woodpecker agent it pushed the whole internal/undo package past its 25 m
// timeout (pipelines 1837 and 1838). CI runs -short, so CI gets the ~25x
// cheaper budget; an unflagged run (nightly, or `go test ./internal/undo`
// by hand) still walks every seed at full depth.
const advShortDivisor = 10

// advBudget returns the seeds and history count a suite runs with under the
// current -short setting.
func advBudget(su advSuite) ([]uint64, int) {
	if !testing.Short() {
		return su.seeds, su.n
	}
	n := su.n / advShortDivisor
	if n < 100 {
		n = 100
	}
	return su.seeds[:1], n
}

func TestMembershipAdversarial_Suites(t *testing.T) {
	for _, su := range advSuites() {
		t.Run(su.name, func(t *testing.T) {
			t.Parallel()
			seeds, n := advBudget(su)
			for _, seed := range seeds {
				requireAdvSuite(t, su, seed, n, runAdv(t, seed, n, su.cfg))
			}
		})
	}
}
