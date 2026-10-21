// file: internal/decisions/store_test.go
// version: 1.1.0
// guid: e91a7d04-3b58-4c62-a0f7-5d28c61b94e3
// last-edited: 2026-10-21

package decisions

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRecord_RejectWritesLogVerdictAndEveryIndex(t *testing.T) {
	s, kv := newStore(t)
	in := humanReject("b3", "b1", "b2")
	rec := mustRecord(t, s, in)

	require.Equal(t, SchemaVersion, rec.SchemaVersion)
	require.Equal(t, []string{"b1", "b2", "b3"}, rec.EntityIDs, "ids are stored sorted")
	require.Equal(t, subjectOf(t, in), rec.SubjectKey)
	require.Equal(t, FamilySameWork, rec.Family)
	require.Equal(t, "u1", rec.ActorUserID)
	require.False(t, rec.DecidedAt.IsZero())

	got, err := s.Get(rec.ID)
	require.NoError(t, err)
	require.Equal(t, rec, got)

	require.Equal(t, 1, countPrefix(t, kv, prefixRecord))
	require.Equal(t, 1, countPrefix(t, kv, prefixVerdict))
	require.Equal(t, 1, countPrefix(t, kv, prefixActive), "a live reject has one active-index row")
	require.Equal(t, 3, countPrefix(t, kv, prefixEdge), "3 entities -> 3 pair edges")
	require.Equal(t, 0, countPrefix(t, kv, prefixMember))
	require.Equal(t, 3, countPrefix(t, kv, prefixEntity), "one history row per entity")
	require.Equal(t, 1, countPrefix(t, kv, prefixOp))

	v, err := s.Verdict(testKind, rec.SubjectKey)
	require.NoError(t, err)
	require.Equal(t, StateActive, v.State)
	require.Equal(t, rec.ID, v.LatestID)
	require.Equal(t, rec.ID, v.RejectID)
	require.True(t, v.Live())
}

func TestRecord_Validation(t *testing.T) {
	s, kv := newStore(t)
	mut := func(f func(*Input)) Input { in := humanReject("b1", "b2"); f(&in); return in }
	cases := map[string]Input{
		"no op kind":            mut(func(i *Input) { i.OpKind = "" }),
		"no entity type":        mut(func(i *Input) { i.EntityType = "" }),
		"no entities":           mut(func(i *Input) { i.EntityIDs = nil }),
		"empty entity id":       mut(func(i *Input) { i.EntityIDs = []string{"b1", ""} }),
		"control char in id":    mut(func(i *Input) { i.EntityIDs = []string{"b1", "b\x002"} }),
		"unknown decision":      mut(func(i *Input) { i.Decision = "maybe" }),
		"unknown source":        mut(func(i *Input) { i.Source = "robot" }),
		"human without actor":   mut(func(i *Input) { i.ActorUserID = " " }),
		"reject without reason": mut(func(i *Input) { i.ReasonCode = "" }),
		"reason not in kind":    mut(func(i *Input) { i.ReasonCode = "wrong_book" }),
		"reason text too long":  mut(func(i *Input) { i.ReasonText = string(make([]byte, MaxReasonTextLen+1)) }),
		"set reject no family":  mut(func(i *Input) { i.Family = "" }),
		"typo family":           mut(func(i *Input) { i.Family = "same-work" }),
		"never-merge family":    mut(func(i *Input) { i.Family = FamilyAny }),
		"leading space id":      mut(func(i *Input) { i.EntityIDs = []string{" b1", "b2"} }),
		"trailing space id":     mut(func(i *Input) { i.EntityIDs = []string{"b1", "b2\t"} }),
		"unreject by auto":      mut(func(i *Input) { i.Decision, i.Source, i.ActorUserID = DecisionUnreject, SourceAuto, "" }),
		"unreject by llm":       mut(func(i *Input) { i.Decision, i.Source, i.ActorUserID = DecisionUnreject, SourceLLM, "" }),
		"reopen without hash":   mut(func(i *Input) { i.Decision, i.ReasonCode, i.MaterialHash = DecisionReopened, "", "" }),
		"too many paths": mut(func(i *Input) {
			i.Snapshot.Entities = []EntitySnapshot{{EntityType: "book", EntityID: "b1", Paths: ids("/p", MaxSnapshotPaths+1)}}
		}),
	}
	for name, in := range cases {
		_, err := s.Record(in)
		require.ErrorIs(t, err, ErrInvalid, name)
	}
	require.Equal(t, 0, countPrefix(t, kv, "decision:"), "a refused input writes nothing")

	// What is allowed: an auto decision needs no user, a one-entity reject
	// needs no family, and "other" always passes.
	auto := mut(func(i *Input) { i.Source, i.ActorUserID, i.ReasonCode = SourceAuto, "", ReasonOther })
	mustRecord(t, s, auto)
	single := humanReject("only")
	single.Family = ""
	mustRecord(t, s, single)
}

func TestRecord_SupersedesChainAndHistory(t *testing.T) {
	s, _ := newStore(t)
	in := humanReject("b1", "b2")
	r1 := mustRecord(t, s, in)
	r2 := mustRecord(t, s, labelInput(DecisionSkip, "b1", "b2"))
	r3 := mustRecord(t, s, labelInput(DecisionApplied, "b2", "b1")) // id order is irrelevant

	require.Equal(t, "", r1.SupersedesID)
	require.Equal(t, r1.ID, r2.SupersedesID)
	require.Equal(t, r2.ID, r3.SupersedesID)

	hist, err := s.History(testKind, r1.SubjectKey)
	require.NoError(t, err)
	require.Len(t, hist, 3)
	require.Equal(t, []string{r3.ID, r2.ID, r1.ID}, []string{hist[0].ID, hist[1].ID, hist[2].ID})

	none, err := s.History(testKind, strings.Repeat("0", 64))
	require.NoError(t, err)
	require.Empty(t, none)
}

func TestRecord_UnrejectLiftsVetoKeepsTheRecord(t *testing.T) {
	s, kv := newStore(t)
	rec := mustRecord(t, s, humanReject("b1", "b2", "b3"))
	require.True(t, mustVetoed(t, s, testType, "b1", "b2", FamilySameWork))

	un := mustRecord(t, s, labelInput(DecisionUnreject, "b1", "b2", "b3"))
	require.Equal(t, rec.ID, un.SupersedesID)

	require.False(t, mustVetoed(t, s, testType, "b1", "b2", FamilySameWork))
	require.Equal(t, 0, countPrefix(t, kv, prefixEdge))
	require.Equal(t, 0, countPrefix(t, kv, prefixActive), "the unreject removed the active-index row")
	require.Equal(t, 2, countPrefix(t, kv, prefixRecord), "the reject and the unreject both stay in the log")
	v, err := s.Verdict(testKind, rec.SubjectKey)
	require.NoError(t, err)
	require.Equal(t, StateUnrejected, v.State)
	require.Empty(t, v.RejectID)
	require.False(t, v.Live())

	// Reject again after the unreject: vetoed again, edges rebuilt.
	mustRecord(t, s, humanReject("b1", "b2", "b3"))
	require.True(t, mustVetoed(t, s, testType, "b2", "b3", FamilySameWork))
	require.Equal(t, 3, countPrefix(t, kv, prefixEdge))
}

func TestRecord_UnrejectNeedsAnActiveReject(t *testing.T) {
	s, kv := newStore(t)
	_, err := s.Record(labelInput(DecisionUnreject, "b1", "b2"))
	require.ErrorIs(t, err, ErrNotRejected)
	reopen := labelInput(DecisionReopened, "b1", "b2")
	reopen.MaterialHash = "h"
	_, err = s.Record(reopen)
	require.ErrorIs(t, err, ErrNotRejected)

	mustRecord(t, s, labelInput(DecisionApplied, "b1", "b2"))
	_, err = s.Record(labelInput(DecisionUnreject, "b1", "b2"))
	require.ErrorIs(t, err, ErrNotRejected, "applied is not a reject")
	require.Equal(t, 1, countPrefix(t, kv, prefixRecord), "a refused unreject leaves no record")
}

func TestRecord_RejectAgainReplacesEdgesWithoutLeaking(t *testing.T) {
	s, kv := newStore(t)
	mustRecord(t, s, humanReject("b1", "b2", "b3"))
	in := humanReject("b1", "b2", "b3")
	in.ReasonCode = "different_authors"
	mustRecord(t, s, in)
	require.Equal(t, 3, countPrefix(t, kv, prefixEdge), "the second reject replaced the first's edges, not added to them")
	require.Equal(t, 2, countPrefix(t, kv, prefixRecord))

	mustRecord(t, s, labelInput(DecisionUnreject, "b1", "b2", "b3"))
	require.Equal(t, 0, countPrefix(t, kv, prefixEdge))
}

// Two rejects can veto the same pair. Un-rejecting one must not lift the
// veto the other still holds.
func TestRecord_SharedPairSurvivesUnrejectOfOneOwner(t *testing.T) {
	s, kv := newStore(t)
	mustRecord(t, s, humanReject("b1", "b2", "b3"))    // pairs 12 13 23
	other := mustRecord(t, s, humanReject("b1", "b2")) // pair 12
	require.Equal(t, 4, countPrefix(t, kv, prefixEdge), "the shared pair has one row per owner")

	mustRecord(t, s, labelInput(DecisionUnreject, "b1", "b2", "b3"))
	require.True(t, mustVetoed(t, s, testType, "b1", "b2", FamilySameWork), "still held by the second reject")
	require.False(t, mustVetoed(t, s, testType, "b1", "b3", FamilySameWork), "only the first reject held this pair")

	mustRecord(t, s, labelInput(DecisionUnreject, "b2", "b1"))
	require.False(t, mustVetoed(t, s, testType, "b1", "b2", FamilySameWork))
	require.Equal(t, 0, countPrefix(t, kv, prefixEdge))
	_ = other
}

func TestRecord_LabelsNeverVeto(t *testing.T) {
	s, kv := newStore(t)
	for _, d := range []Decision{DecisionApprove, DecisionApplied, DecisionAutoApplied, DecisionUndone, DecisionSkip} {
		in := labelInput(d, "b1", "b2")
		if d == DecisionAutoApplied {
			in.Source, in.ActorUserID = SourceAuto, ""
		}
		mustRecord(t, s, in)
	}
	sub := subjectOf(t, labelInput(DecisionSkip, "b1", "b2"))
	v, err := s.Verdict(testKind, sub)
	require.NoError(t, err)
	require.Equal(t, StateNone, v.State)
	require.False(t, v.Live())
	require.False(t, mustVetoed(t, s, testType, "b1", "b2", FamilySameWork))
	require.Equal(t, 0, countPrefix(t, kv, prefixEdge))
	st, err := s.Lookup(testKind, sub, "")
	require.NoError(t, err)
	require.False(t, st.Blocked)
	require.Equal(t, 5, countPrefix(t, kv, prefixRecord), "but every one is kept as a labelled example")
}

func TestRecord_UndoAfterApplyDoesNotVetoAndLaterRejectStillWorks(t *testing.T) {
	s, _ := newStore(t)
	mustRecord(t, s, labelInput(DecisionApplied, "b1", "b2"))
	mustRecord(t, s, labelInput(DecisionUndone, "b1", "b2"))
	require.False(t, mustVetoed(t, s, testType, "b1", "b2", FamilySameWork), "an undo is not a veto")
	mustRecord(t, s, humanReject("b1", "b2"))
	require.True(t, mustVetoed(t, s, testType, "b1", "b2", FamilySameWork))
	// A later apply label does not lift a reject.
	mustRecord(t, s, labelInput(DecisionApplied, "b1", "b2"))
	require.True(t, mustVetoed(t, s, testType, "b1", "b2", FamilySameWork))
}

func TestRecord_VetoIsScopedToItsFamily(t *testing.T) {
	s, kv := newStore(t)
	mustRecord(t, s, humanReject("b1", "b2")) // FamilySameWork
	require.True(t, mustVetoed(t, s, testType, "b1", "b2", FamilySameWork))
	require.False(t, mustVetoed(t, s, testType, "b1", "b2", FamilySameCopy), "a same-work reject says nothing about same-copy")
	require.False(t, mustVetoed(t, s, "author", "b1", "b2", FamilySameWork), "entity types do not mix")

	// What PR8's never-merge override will write: FamilyAny, directly.
	forceEdge(t, kv, testType, "b8", "b9", FamilyAny)
	for _, fam := range append(slices.Clone(Families), FamilyAny) {
		require.True(t, mustVetoed(t, s, testType, "b8", "b9", fam), fam)
		require.True(t, snapVetoed(t, mustLoad(t, s, nil), testType, "b9", "b8", fam), "snapshot: "+fam)
	}
	require.False(t, mustVetoed(t, s, testType, "b8", "b1", FamilySameWork))
}

func TestRecord_SubjectsAreKindScoped(t *testing.T) {
	s, _ := newStore(t)
	rec := mustRecord(t, s, humanReject("b1", "b2"))
	other := humanReject("b1", "b2")
	other.OpKind, other.ReasonCode = "dedup:book-candidate", "different_books_same_title"
	st, err := s.Lookup(other.OpKind, subjectOf(t, other), "")
	require.NoError(t, err)
	require.False(t, st.Blocked, "a reject blocks only its own proposal kind")
	st, err = s.Lookup(testKind, rec.SubjectKey, "")
	require.NoError(t, err)
	require.True(t, st.Blocked)
}

func TestLookup_MaterialChangeReopens(t *testing.T) {
	s, _ := newStore(t)
	h1 := MaterialHash(map[string]string{"title": "Old Value"})
	h2 := MaterialHash(map[string]string{"title": "New Value"})
	in := Input{
		OpKind: "repairs:repair-junk-titles", EntityType: testType, EntityIDs: []string{"b1"}, Scope: "row-1",
		Decision: DecisionReject, Source: SourceHuman, ActorUserID: "u1",
		ReasonCode: "proposed_value_wrong", MaterialHash: h1,
	}
	rec := mustRecord(t, s, in)

	st, err := s.Lookup(in.OpKind, rec.SubjectKey, h1)
	require.NoError(t, err)
	require.True(t, st.Blocked)
	require.Equal(t, StateActive, st.State)

	st, err = s.Lookup(in.OpKind, rec.SubjectKey, h2)
	require.NoError(t, err)
	require.False(t, st.Blocked, "different data -> re-opened")
	require.Equal(t, StateReopened, st.State)

	st, err = s.Lookup(in.OpKind, rec.SubjectKey, "")
	require.NoError(t, err)
	require.True(t, st.Blocked, "a caller with no material hash cannot reopen")

	// A sticky reject (no material hash) is never reopened by data.
	sticky := in
	sticky.Scope, sticky.MaterialHash = "row-2", ""
	srec := mustRecord(t, s, sticky)
	st, err = s.Lookup(in.OpKind, srec.SubjectKey, h2)
	require.NoError(t, err)
	require.True(t, st.Blocked)

	// Recording the reopen persists it. It allows the new suggestion and leaves
	// the rejected value blocked (see reopen_test.go).
	re := in
	re.Decision, re.ReasonCode, re.Source, re.ActorUserID, re.MaterialHash = DecisionReopened, "", SourceAuto, "", h2
	mustRecord(t, s, re)
	st, err = s.Lookup(in.OpKind, rec.SubjectKey, h2)
	require.NoError(t, err)
	require.Equal(t, StateReopened, st.State)
	require.False(t, st.Blocked)
	st, err = s.Lookup(in.OpKind, rec.SubjectKey, h1)
	require.NoError(t, err)
	require.True(t, st.Blocked, "a persisted reopen does not lift the block on the rejected value")

	none, err := s.Lookup(in.OpKind, strings.Repeat("0", 64), h1)
	require.NoError(t, err)
	require.Equal(t, Status{}, none)
}

func TestRecord_SetsLargerThanTheEdgeCapUseMembershipRows(t *testing.T) {
	s, kv := newStore(t)
	// Exactly MaxEdgeSet is still pairwise: 50*49/2 edges.
	mustRecord(t, s, humanReject(ids("e", MaxEdgeSet)...))
	require.Equal(t, MaxEdgeSet*(MaxEdgeSet-1)/2, countPrefix(t, kv, prefixEdge))
	require.Equal(t, 0, countPrefix(t, kv, prefixMember))

	big := ids("m", MaxEdgeSet+10)
	rec := mustRecord(t, s, humanReject(big...))
	require.Equal(t, MaxEdgeSet*(MaxEdgeSet-1)/2, countPrefix(t, kv, prefixEdge), "no edge rows for the big set")
	require.Equal(t, len(big), countPrefix(t, kv, prefixMember), "one membership row per member")

	// Every pair inside the big set is vetoed (spot-check first/last/middle),
	// a pair with an outsider is not, and the family still scopes it.
	for _, p := range [][2]string{{big[0], big[1]}, {big[0], big[len(big)-1]}, {big[17], big[42]}, {big[len(big)-1], big[3]}} {
		require.True(t, mustVetoed(t, s, testType, p[0], p[1], FamilySameWork), "%v", p)
	}
	require.False(t, mustVetoed(t, s, testType, big[0], "outsider", FamilySameWork))
	require.False(t, mustVetoed(t, s, testType, big[0], big[1], FamilySameCopy))
	snap := mustLoad(t, s, nil)
	require.True(t, snapVetoed(t, snap, testType, big[5], big[55], FamilySameWork))
	require.False(t, snapVetoed(t, snap, testType, big[5], "outsider", FamilySameWork))

	mustRecord(t, s, labelInput(DecisionUnreject, big...))
	require.Equal(t, 0, countPrefix(t, kv, prefixMember))
	require.False(t, mustVetoed(t, s, testType, big[0], big[1], FamilySameWork))
	_ = rec
}

func TestRecord_SingleEntityRejectHasNoPairRows(t *testing.T) {
	s, kv := newStore(t)
	in := humanReject("only")
	in.Family = ""
	rec := mustRecord(t, s, in)
	require.Equal(t, 0, countPrefix(t, kv, prefixEdge))
	require.Equal(t, 0, countPrefix(t, kv, prefixMember))
	st, err := s.Lookup(testKind, rec.SubjectKey, "")
	require.NoError(t, err)
	require.True(t, st.Blocked)
}

func TestRecord_CommitFailureWritesNothing(t *testing.T) {
	kv := newKV(t)
	f := &failingKV{KV: kv, failApply: true}
	s := New(f)
	_, err := s.Record(humanReject("b1", "b2"))
	require.Error(t, err)
	require.Equal(t, 0, countPrefix(t, kv, "decision:"), "all-or-nothing: no record without its verdict and edges")
}

func TestRecord_VerdictNamingAMissingRejectIsCorrupt(t *testing.T) {
	s, kv := newStore(t)
	rec := mustRecord(t, s, humanReject("b1", "b2"))
	require.NoError(t, kv.DeleteRaw(recordKey(rec.ID))) // simulate loss
	_, err := s.Record(labelInput(DecisionUnreject, "b1", "b2"))
	require.ErrorIs(t, err, ErrCorrupt)
}

func TestKeys_OpKindsWithColonsDoNotShareAPrefix(t *testing.T) {
	s, _ := newStore(t)
	a := humanReject("b1")
	a.OpKind, a.Family, a.ReasonCode = "repairs", "", ReasonOther
	b := a
	b.OpKind = "repairs:dedup.split-books"
	c := a
	c.OpKind = "repairs:dedup"
	mustRecord(t, s, a)
	mustRecord(t, s, b)
	mustRecord(t, s, c)
	for kind, want := range map[string]int{"repairs": 1, "repairs:dedup.split-books": 1, "repairs:dedup": 1, "repair": 0} {
		n := 0
		require.NoError(t, s.Iterate(context.Background(), Filter{OpKind: kind}, func(r Record) error {
			require.Equal(t, kind, r.OpKind)
			n++
			return nil
		}))
		require.Equal(t, want, n, kind)
	}
}

func TestEscapeComponent_IsInjectiveAndRoundTrips(t *testing.T) {
	in := []string{"", "a", "a:b", "a%3Ab", "%", "%%", ":", "::", "a\tb", "x%zz", "100%"}
	seen := map[string]string{}
	for _, s := range in {
		e := escapeComponent(s)
		require.NotContains(t, e, ":", "%q escapes to %q", s, e)
		if prev, dup := seen[e]; dup {
			t.Fatalf("%q and %q both escape to %q", s, prev, e)
		}
		seen[e] = s
		require.Equal(t, s, unescapeComponent(e), "round trip of %q", s)
	}
}

func TestIterateAndByEntity(t *testing.T) {
	s, _ := newStore(t)
	r1 := mustRecord(t, s, humanReject("b1", "b2"))
	other := humanReject("b2", "b3")
	other.OpKind, other.ReasonCode = "dedup:book-candidate", "different_books_same_title"
	r2 := mustRecord(t, s, other)
	r3 := mustRecord(t, s, labelInput(DecisionApplied, "b1", "b2"))

	collect := func(f Filter) []string {
		var out []string
		require.NoError(t, s.Iterate(context.Background(), f, func(r Record) error { out = append(out, r.ID); return nil }))
		return out
	}
	require.Equal(t, []string{r1.ID, r2.ID, r3.ID}, collect(Filter{}), "decision (ULID) order")
	require.Equal(t, []string{r1.ID, r3.ID}, collect(Filter{OpKind: testKind}))
	require.Equal(t, []string{r2.ID}, collect(Filter{OpKind: "dedup:book-candidate"}))
	require.Empty(t, collect(Filter{OpKind: "nothing"}))

	var byEnt []string
	require.NoError(t, s.ByEntity(context.Background(), testType, "b2", func(r Record) error { byEnt = append(byEnt, r.ID); return nil }))
	require.Equal(t, []string{r1.ID, r2.ID, r3.ID}, byEnt, "b2 is named by all three")
	byEnt = nil
	require.NoError(t, s.ByEntity(context.Background(), testType, "b3", func(r Record) error { byEnt = append(byEnt, r.ID); return nil }))
	require.Equal(t, []string{r2.ID}, byEnt)

	stop := errors.New("stop")
	calls := 0
	err := s.Iterate(context.Background(), Filter{}, func(Record) error { calls++; return stop })
	require.ErrorIs(t, err, stop)
	require.Equal(t, 1, calls)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, s.Iterate(ctx, Filter{}, func(Record) error { return nil }), context.Canceled)
}

func TestIterate_PagesPastOnePage(t *testing.T) {
	s, _ := newStore(t)
	n := scanPage*2 + 37
	for i := range n {
		in := humanReject(fmt.Sprintf("solo%05d", i))
		in.Family = ""
		mustRecord(t, s, in)
	}
	seen, prev := 0, ""
	require.NoError(t, s.Iterate(context.Background(), Filter{}, func(r Record) error {
		require.Greater(t, r.ID, prev, "strictly increasing ids")
		prev = r.ID
		seen++
		return nil
	}))
	require.Equal(t, n, seen)
	v := mustLoad(t, s, nil)
	subjects, _, _ := v.Counts()
	require.Equal(t, n, subjects, "Load pages through every verdict")
}

func TestNewID_StrictlyIncreasingEvenWhenTheClockStandsStillOrGoesBack(t *testing.T) {
	kv := newKV(t)
	base := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	times := []time.Time{base, base, base, base.Add(-time.Hour), base.Add(time.Second)}
	i := 0
	s := New(kv, WithClock(func() time.Time { t := times[i%len(times)]; i++; return t }))
	prev := ""
	for range 12 {
		id, err := s.newID(s.now())
		require.NoError(t, err)
		require.Greater(t, id, prev)
		prev = id
	}
}

// N goroutines record on one subject: the supersedes chain must be one line
// with no fork and no lost record.
func TestRecord_ConcurrentOnOneSubjectKeepsOneLinearChain(t *testing.T) {
	kv := newKV(t)
	s := New(kv)
	const n = 40
	var wg sync.WaitGroup
	recs := make([]*Record, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := []Decision{DecisionApplied, DecisionSkip, DecisionApprove, DecisionUndone}[i%4]
			recs[i], errs[i] = s.Record(labelInput(d, "b1", "b2"))
		}()
	}
	wg.Wait()
	require.NoError(t, errors.Join(errs...)) // asserted here: FailNow is not allowed off the test goroutine

	hist, err := s.History(testKind, recs[0].SubjectKey)
	require.NoError(t, err)
	require.Len(t, hist, n, "no record lost")
	parents := map[string]int{}
	roots := 0
	for _, r := range hist {
		if r.SupersedesID == "" {
			roots++
		}
		parents[r.SupersedesID]++
	}
	require.Equal(t, 1, roots, "exactly one chain root")
	for p, c := range parents {
		require.Equal(t, 1, c, "record %q has more than one successor: the chain forked", p)
	}
}

func TestRecord_ConcurrentRejectAndUnrejectOnDifferentSubjectsLeaveNoEdges(t *testing.T) {
	kv := newKV(t)
	s := New(kv)
	var wg sync.WaitGroup
	errs := make([]error, 16)
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, b, c := fmt.Sprintf("a%02d", i), fmt.Sprintf("b%02d", i), "shared"
			if _, err := s.Record(humanReject(a, b, c)); err != nil {
				errs[i] = err
				return
			}
			_, errs[i] = s.Record(labelInput(DecisionUnreject, a, b, c))
		}()
	}
	wg.Wait()
	require.NoError(t, errors.Join(errs...))
	require.Equal(t, 0, countPrefix(t, kv, prefixEdge), "every edge row was deleted by its own owner")
	require.Equal(t, 32, countPrefix(t, kv, prefixRecord))
}
