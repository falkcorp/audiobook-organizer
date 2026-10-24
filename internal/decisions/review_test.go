// file: internal/decisions/review_test.go
// version: 1.2.0
// guid: c18d5f62-0e47-4a93-b6d1-7f3a92e4058c
// last-edited: 2026-10-24

package decisions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// slowKV widens the verdict read-modify-write window so a missing lock shows
// up on every run, not one in a thousand.
type slowKV struct{ KV }

func (s *slowKV) GetRaw(key string) ([]byte, error) {
	v, err := s.KV.GetRaw(key)
	time.Sleep(200 * time.Microsecond)
	return v, err
}

// Two Stores over ONE KV (two handlers, a handler and a job) share the subject
// lock: it is keyed by the KV, not held per Store. Before it was, the stores
// interleaved the read-modify-write and forked the SupersedesID chain.
func TestRecord_TwoStoresOnOneKVKeepOneLinearChain(t *testing.T) {
	// Both Stores are built over the very same KV value; the slow reads make
	// an unprotected interleaving certain.
	slow := &slowKV{newKV(t)}
	requireLinearChain(t, New(slow), New(slow))
}

// Two Stores over two DIFFERENT wrappers of one engine (a metrics decorator
// in one handler, a bare store in a job) still share the subject lock: the
// lock is keyed by KV.LockIdentity, which a wrapper inherits from the store it
// embeds, not by the wrapper's own address. Keyed by address, each wrapper got
// its own lock and the stores forked the SupersedesID chain.
func TestRecord_TwoWrappersOfOneStoreShareTheLock(t *testing.T) {
	p := newKV(t)
	requireLinearChain(t, New(&slowKV{p}), New(&slowKV{p}))
}

func requireLinearChain(t *testing.T, a, b *Store) {
	t.Helper()
	const n = 40
	var wg sync.WaitGroup
	errs := make([]error, n)
	var first *Record
	var mu sync.Mutex
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := a
			if i%2 == 1 {
				s = b
			}
			rec, err := s.Record(labelInput([]Decision{DecisionApplied, DecisionSkip, DecisionApprove, DecisionUndone}[i%4], "b1", "b2"))
			errs[i] = err
			mu.Lock()
			first = rec
			mu.Unlock()
		}()
	}
	wg.Wait()
	require.NoError(t, errors.Join(errs...))

	hist, err := a.History(testKind, first.SubjectKey)
	require.NoError(t, err)
	require.Len(t, hist, n, "no record lost")
	parents := map[string]int{}
	for _, r := range hist {
		parents[r.SupersedesID]++
	}
	require.Equal(t, 1, parents[""], "exactly one chain root")
	for p, c := range parents {
		require.Equal(t, 1, c, "record %q has more than one successor: the chain forked", p)
	}
}

// hookKV runs onScan the first time a snapshot scans prefix, so a test can
// commit a record exactly between Load's scans.
type hookKV struct {
	KV
	prefix string
	onScan func()
	once   sync.Once
}

func (h *hookKV) NewRawSnapshot() (database.RawSnapshot, error) {
	snap, err := h.KV.NewRawSnapshot()
	if err != nil {
		return nil, err
	}
	return &hookSnap{RawSnapshot: snap, h: h}, nil
}

type hookSnap struct {
	database.RawSnapshot
	h *hookKV
}

func (s *hookSnap) ScanPrefixPage(prefix, after string, limit int) ([]database.KVPair, string, error) {
	if prefix == s.h.prefix {
		s.h.once.Do(s.h.onScan)
	}
	return s.RawSnapshot.ScanPrefixPage(prefix, after, limit)
}

// Load is a point-in-time snapshot. A re-reject that moves a pair from one
// family to another commits between Load's scan of the live-reject rows and
// its scan of the pair edges: the snapshot must hold the OLD state whole (the
// pair vetoed in same_work, not same_copy), never the active row of one state
// with the edges of the other.
func TestLoad_IsPointInTimeAcrossARecordCommittingMidLoad(t *testing.T) {
	kv := newKV(t)
	s := New(kv)
	mustRecord(t, s, humanReject("b1", "b2")) // same_work

	h := &hookKV{KV: kv, prefix: prefixEdge}
	h.onScan = func() {
		re := humanReject("b1", "b2")
		re.Family, re.ReasonCode = FamilySameCopy, "different_authors"
		_, err := s.Record(re)
		if err != nil {
			t.Errorf("re-reject during Load: %v", err)
		}
	}
	loader := New(h)
	v, err := loader.Load(context.Background(), nil)
	require.NoError(t, err)

	require.True(t, snapVetoed(t, v, testType, "b1", "b2", FamilySameWork), "the snapshot holds the state as of the instant Load began")
	require.False(t, snapVetoed(t, v, testType, "b1", "b2", FamilySameCopy), "and none of the later commit")

	// A fresh Load sees the commit whole.
	v2 := mustLoad(t, s, nil)
	require.False(t, snapVetoed(t, v2, testType, "b1", "b2", FamilySameWork))
	require.True(t, snapVetoed(t, v2, testType, "b1", "b2", FamilySameCopy))
}

// The index shape a reject was written in is what its undo deletes, whatever
// MaxEdgeSet says. Hand-build a small reject in the membership shape (as one
// written under a smaller cap would be) and undo it.
func TestUnreject_DeletesByThePersistedIndexShape(t *testing.T) {
	s, kv := newStore(t)
	rec := mustRecord(t, s, humanReject("b1", "b2", "b3"))
	require.Equal(t, IndexEdges, rec.IndexShape)

	// Rewrite it as a members-shaped reject: swap the pair edges for
	// membership rows and persist the shape on the record.
	keys, err := indexKeys(rec)
	require.NoError(t, err)
	var writes []database.RawWrite
	for _, k := range keys {
		writes = append(writes, database.RawWrite{Key: k, Delete: true})
	}
	rec.IndexShape = IndexMembers
	raw, err := json.Marshal(rec)
	require.NoError(t, err)
	writes = append(writes, database.RawWrite{Key: recordKey(rec.ID), Value: raw})
	mkeys, err := indexKeys(rec)
	require.NoError(t, err)
	for _, k := range mkeys {
		writes = append(writes, database.RawWrite{Key: k})
	}
	require.NoError(t, kv.ApplyRawBatch(writes))
	require.Equal(t, 0, countPrefix(t, kv, prefixEdge))
	require.Equal(t, 3, countPrefix(t, kv, prefixMember))
	require.True(t, mustVetoed(t, s, testType, "b1", "b3", FamilySameWork))

	mustRecord(t, s, labelInput(DecisionUnreject, "b1", "b2", "b3"))
	require.Equal(t, 0, countPrefix(t, kv, prefixMember), "the members the reject wrote are gone")
	require.Equal(t, 0, countPrefix(t, kv, prefixEdge))
	require.False(t, mustVetoed(t, s, testType, "b1", "b3", FamilySameWork))
}

func TestIndexShape_PersistedAndCorruptShapeFailsClosed(t *testing.T) {
	s, kv := newStore(t)
	small := mustRecord(t, s, humanReject("b1", "b2"))
	big := mustRecord(t, s, humanReject(ids("m", MaxEdgeSet+1)...))
	solo := humanReject("only")
	solo.Family = ""
	one := mustRecord(t, s, solo)
	require.Equal(t, IndexEdges, small.IndexShape)
	require.Equal(t, IndexMembers, big.IndexShape)
	require.Equal(t, "", one.IndexShape)

	// A multi-entity reject with an unknown shape cannot be undone by guess.
	small.IndexShape = ""
	raw, err := json.Marshal(small)
	require.NoError(t, err)
	require.NoError(t, kv.SetRaw(recordKey(small.ID), raw))
	_, err = s.Record(labelInput(DecisionUnreject, "b1", "b2"))
	require.ErrorIs(t, err, ErrCorrupt)
}

func TestHumanRejectIsReplacedOnlyByAHuman(t *testing.T) {
	s, kv := newStore(t)
	mustRecord(t, s, humanReject("b1", "b2")) // same_work, human

	auto := humanReject("b1", "b2")
	auto.Source, auto.ActorUserID, auto.Family, auto.ReasonCode = SourceLLM, "", FamilySameCopy, "different_authors"
	_, err := s.Record(auto)
	require.ErrorIs(t, err, ErrHumanRejectLive, "a different family")
	auto.Family, auto.ReasonCode = FamilySameWork, "distinct_titles"
	_, err = s.Record(auto)
	require.ErrorIs(t, err, ErrHumanRejectLive, "even the same family: the record would pass to a non-human")
	require.True(t, mustVetoed(t, s, testType, "b1", "b2", FamilySameWork))
	require.False(t, mustVetoed(t, s, testType, "b1", "b2", FamilySameCopy))
	require.Equal(t, 1, countPrefix(t, kv, prefixRecord), "refused writes leave nothing")

	// A human may change the family, and non-humans may still label.
	h2 := humanReject("b1", "b2")
	h2.Family, h2.ReasonCode = FamilySameCopy, "different_authors"
	mustRecord(t, s, h2)
	require.True(t, mustVetoed(t, s, testType, "b1", "b2", FamilySameCopy))
	label := labelInput(DecisionApplied, "b1", "b2")
	label.Source, label.ActorUserID = SourceAuto, ""
	mustRecord(t, s, label)

	// An auto reject may replace an auto reject, and starts one on a fresh subject.
	fresh := humanReject("c1", "c2")
	fresh.Source, fresh.ActorUserID = SourceAuto, ""
	mustRecord(t, s, fresh)
	mustRecord(t, s, fresh)
}

func TestUnrejectByNonHumanIsRefusedAndLeavesTheVeto(t *testing.T) {
	s, _ := newStore(t)
	mustRecord(t, s, humanReject("b1", "b2"))
	for _, src := range []Source{SourceAuto, SourceLLM} {
		un := labelInput(DecisionUnreject, "b1", "b2")
		un.Source, un.ActorUserID = src, ""
		_, err := s.Record(un)
		require.ErrorIs(t, err, ErrInvalid, string(src))
	}
	require.True(t, mustVetoed(t, s, testType, "b1", "b2", FamilySameWork))
}

func TestPairVetoed_UnknownFamilyIsAnErrorNotFalse(t *testing.T) {
	s, kv := newStore(t)
	mustRecord(t, s, humanReject("b1", "b2"))
	forceEdge(t, kv, testType, "b8", "b9", FamilyAny)
	v := mustLoad(t, s, nil)

	for _, fam := range []string{"", "same-work", "SAME_WORK", "anything_new"} {
		_, err := s.PairVetoed(testType, "b1", "b2", fam)
		require.ErrorIs(t, err, ErrInvalid, "direct, family %q", fam)
		_, err = v.PairVetoed(testType, "b1", "b2", fam)
		require.ErrorIs(t, err, ErrInvalid, "snapshot, family %q", fam)
	}
	_, err := s.PairVetoed("", "b1", "b2", FamilySameWork)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = v.PairVetoed("", "b1", "b2", FamilySameWork)
	require.ErrorIs(t, err, ErrInvalid)

	// Queried FamilyAny: "is this pair vetoed in ANY family?".
	require.True(t, mustVetoed(t, s, testType, "b1", "b2", FamilyAny))
	require.True(t, snapVetoed(t, v, testType, "b2", "b1", FamilyAny))
	require.True(t, mustVetoed(t, s, testType, "b8", "b9", FamilyAny))
	require.False(t, mustVetoed(t, s, testType, "b1", "b9", FamilyAny), "no edge in any family")
	require.False(t, snapVetoed(t, v, testType, "b1", "b9", FamilyAny))
	// And it reaches set memberships too.
	big := ids("m", MaxEdgeSet+2)
	mustRecord(t, s, humanReject(big...))
	require.True(t, mustVetoed(t, s, testType, big[0], big[1], FamilyAny))
	require.True(t, snapVetoed(t, mustLoad(t, s, nil), testType, big[0], big[1], FamilyAny))
}

// Owner decision A: a reject applies per question family, not per op kind. A
// split-books same_work reject blocks fragment-consolidation and folder-books
// from proposing the same pair, and does not touch a same_copy question.
func TestFamilyScope_SplitBooksRejectBlocksTheOtherSameWorkFixers(t *testing.T) {
	s, _ := newStore(t)
	mustRecord(t, s, humanReject("b1", "b2")) // repairs:dedup.split-books, same_work
	v := mustLoad(t, s, nil)

	for _, kind := range []string{"repairs:dedup.split-books", "repairs:fragment-consolidation", "repairs:folder-books"} {
		// What a producer of `kind` asks before proposing joining b1 and b2.
		require.True(t, mustVetoed(t, s, testType, "b1", "b2", FamilySameWork), kind)
		require.True(t, snapVetoed(t, v, testType, "b2", "b1", FamilySameWork), kind)
	}
	for _, kind := range []string{"repairs:duplicate-copies", "repairs:version-twin-metadata"} {
		require.False(t, mustVetoed(t, s, testType, "b1", "b2", FamilySameCopy), kind)
		require.False(t, snapVetoed(t, v, testType, "b1", "b2", FamilySameCopy), kind)
	}
	// The verdict itself stays per op_kind: the other fixers' own proposals
	// are different subjects.
	other := humanReject("b1", "b2")
	other.OpKind = "repairs:folder-books"
	st, err := s.Lookup(other.OpKind, subjectOf(t, other), "")
	require.NoError(t, err)
	require.False(t, st.Blocked, "only the PAIR veto crosses op kinds")
}

func TestCanonicalisation_ULIDCaseAndWhitespace(t *testing.T) {
	const idA = "01JB8Y3G5XK0S1M2N3P4Q5R6T7"
	const idB = "01JB8Y3G5XK0S1M2N3P4Q5R6T8"
	loA, loB := strings.ToLower(idA), strings.ToLower(idB)

	s, kv := newStore(t)
	rec := mustRecord(t, s, humanReject(loB, loA)) // recorded in lowercase
	require.Equal(t, []string{idA, idB}, rec.EntityIDs, "stored upper-case")
	want := subjectOf(t, humanReject(idA, idB))
	require.Equal(t, want, rec.SubjectKey, "one subject however the ids are cased")

	// A second spelling is the same proposal: same verdict, chain continues.
	again := mustRecord(t, s, labelInput(DecisionSkip, idA, loB))
	require.Equal(t, rec.ID, again.SupersedesID)
	require.Equal(t, 1, countPrefix(t, kv, prefixEdge))

	// Every lookup entry point finds the veto under either spelling.
	for _, p := range [][2]string{{idA, idB}, {loA, loB}, {loA, idB}, {idB, loA}} {
		require.True(t, mustVetoed(t, s, testType, p[0], p[1], FamilySameWork), "%v", p)
	}
	v := mustLoad(t, s, nil)
	require.True(t, snapVetoed(t, v, testType, loA, loB, FamilySameWork))
	require.True(t, snapVetoed(t, v, testType, loB, idA, FamilySameWork))
	require.False(t, mustVetoed(t, s, testType, loA, loA, FamilySameWork), "one entity under two spellings is not a pair")
	require.False(t, snapVetoed(t, v, testType, loA, idA, FamilySameWork))

	// Lookup, Verdict, History and Get take a lowercase/uppercase key and id.
	for _, key := range []string{want, strings.ToUpper(want)} {
		st, err := s.Lookup(testKind, key, "")
		require.NoError(t, err)
		require.True(t, st.Blocked)
		h, err := s.History(testKind, key)
		require.NoError(t, err)
		require.Len(t, h, 2)
	}
	got, err := s.Get(strings.ToLower(rec.ID))
	require.NoError(t, err)
	require.Equal(t, rec.ID, got.ID)
	require.True(t, snapRejected(t, v, testKind, strings.ToUpper(want), ""))

	var n int
	require.NoError(t, s.ByEntity(context.Background(), testType, loA, func(Record) error { n++; return nil }))
	require.Equal(t, 2, n, "ByEntity canonicalises its id")
}

func TestCanonicalisation_RefusesWhatCannotBeMadeCanonical(t *testing.T) {
	s, _ := newStore(t)
	mustRecord(t, s, humanReject("b1", "b2"))
	v := mustLoad(t, s, nil)

	for _, bad := range []string{" b1", "b1 ", "b1\n", "", "\tb1"} {
		_, err := s.PairVetoed(testType, bad, "b2", FamilySameWork)
		require.ErrorIs(t, err, ErrInvalid, "direct %q", bad)
		_, err = v.PairVetoed(testType, "b2", bad, FamilySameWork)
		require.ErrorIs(t, err, ErrInvalid, "snapshot %q", bad)
		require.ErrorIs(t, s.ByEntity(context.Background(), testType, bad, func(Record) error { return nil }), ErrInvalid)
	}
	// A subject key that is not 64 hex is an error, never "no such proposal".
	for _, bad := range []string{"", "abc", strings.Repeat("g", 64), strings.Repeat("0", 63)} {
		_, err := s.Lookup(testKind, bad, "")
		require.ErrorIs(t, err, ErrInvalid, "Lookup %q", bad)
		_, err = s.Verdict(testKind, bad)
		require.ErrorIs(t, err, ErrInvalid)
		_, err = s.History(testKind, bad)
		require.ErrorIs(t, err, ErrInvalid)
		_, err = v.SubjectRejected(testKind, bad, "")
		require.ErrorIs(t, err, ErrInvalid, "SubjectRejected %q", bad)
	}
	_, err := s.Get("not-a-ulid")
	require.ErrorIs(t, err, ErrInvalid)
}

// decision:va: must hold exactly the live verdicts, through any sequence.
func TestActiveIndex_MatchesLiveVerdictsThroughAnySequence(t *testing.T) {
	s, kv := newStore(t)
	mustRecord(t, s, humanReject("b1", "b2"))
	mustRecord(t, s, labelInput(DecisionApplied, "b1", "b2"))
	mustRecord(t, s, humanReject("c1"))
	mustRecord(t, s, labelInput(DecisionUnreject, "b1", "b2"))
	rw := rewriteReject(hashOld)
	mustRecord(t, s, rw)
	mustRecord(t, s, reopenOf(rw, hashNew))
	mustRecord(t, s, labelInput(DecisionSkip, "d1", "d2"))

	live := map[string]bool{}
	require.NoError(t, s.scanAll(context.Background(), prefixVerdict, func(p database.KVPair) error {
		var vd Verdict
		require.NoError(t, json.Unmarshal(p.Value, &vd))
		if vd.Live() {
			live[activeKey(vd.OpKind, vd.SubjectKey)] = true
		}
		return nil
	}))
	var active []string
	require.NoError(t, s.scanAll(context.Background(), prefixActive, func(p database.KVPair) error {
		active = append(active, p.Key)
		return nil
	}))
	require.Len(t, active, len(live))
	for _, k := range active {
		require.True(t, live[k], k)
	}
	require.Equal(t, 2, len(active), "c1 (single reject) and the rewrite reject; b1/b2 was unrejected")
	require.Equal(t, 4, countPrefix(t, kv, prefixVerdict), "label-only and lifted verdicts stay in v:, out of va:")
}

func TestAuthorTombstoneResolver_SelfRedirectIsLiveNotAnError(t *testing.T) {
	res := AuthorTombstoneResolver(selfTombstone{})
	got, err := res("author", "5")
	require.NoError(t, err)
	require.Equal(t, "5", got)
}

type selfTombstone struct{}

func (selfTombstone) GetAuthorTombstone(old int) (int, error) { return old, nil }

// A resolver is caller code. What it returns is canonicalised before it keys a
// pair, so a ULID handed back in lower case still meets the stored (upper
// case) edge, and an id with stray whitespace or nothing at all makes the id
// unresolvable (held, never a silent miss).
func TestVetoes_ResolverOutputIsCanonicalised(t *testing.T) {
	const a, b = "01J0000000000000000000000A", "01J0000000000000000000000B"
	s, _ := newStore(t)
	mustRecord(t, s, humanReject(a, b))

	lower := func(_, id string) (string, error) { return strings.ToLower(id), nil }
	v := mustLoad(t, s, lower)
	require.True(t, snapVetoed(t, v, testType, a, b, FamilySameWork), "lower-case resolver output must meet the stored edge")

	for name, bad := range map[string]string{"whitespace": " " + a, "empty": "", "newline": a + "\n"} {
		t.Run(name, func(t *testing.T) {
			// At Load (stored edge ids): the ids are listed, the veto is held ...
			held, err := s.Load(context.Background(), func(_, id string) (string, error) { return bad, nil })
			require.NoError(t, err)
			require.Len(t, held.Unresolved(), 2)
			// ... and at query time.
			ok := mustLoad(t, s, nil)
			ok.resolve = func(_, id string) ([]string, error) { return []string{id, bad}, nil }
			_, err = ok.PairVetoed(testType, a, b, FamilySameWork)
			require.ErrorIs(t, err, ErrIDUnresolvable)
			require.ErrorIs(t, err, ErrResolvedIDInvalid)
		})
	}
}

// Resolving can read the database (author tombstones), so Load closes its
// snapshot BEFORE it resolves any id: those reads must not extend the lease.
func TestLoad_ClosesTheSnapshotBeforeResolving(t *testing.T) {
	kv := &closeTrackKV{KV: newKV(t)}
	s := New(kv)
	mustRecord(t, s, humanReject("b1", "b2"))
	mustRecord(t, s, humanReject("m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8", "m9", "m10", "m11", "m12", "m13", "m14", "m15", "m16", "m17", "m18", "m19", "m20",
		"m21", "m22", "m23", "m24", "m25", "m26", "m27", "m28", "m29", "m30", "m31", "m32", "m33", "m34", "m35", "m36", "m37", "m38", "m39", "m40",
		"m41", "m42", "m43", "m44", "m45", "m46", "m47", "m48", "m49", "m50", "m51"))

	calls := 0
	_, err := s.Load(context.Background(), func(_, id string) (string, error) {
		calls++
		require.Equal(t, int32(0), kv.open.Load(), "a snapshot was still open while resolving %q", id)
		return id, nil
	})
	require.NoError(t, err)
	require.Positive(t, calls)
	require.Equal(t, int32(1), kv.opened.Load(), "one snapshot for the whole Load")
}

type closeTrackKV struct {
	KV
	open, opened atomic.Int32
}

func (k *closeTrackKV) NewRawSnapshot() (database.RawSnapshot, error) {
	snap, err := k.KV.NewRawSnapshot()
	if err != nil {
		return nil, err
	}
	k.open.Add(1)
	k.opened.Add(1)
	return &closeTrackSnap{RawSnapshot: snap, k: k}, nil
}

type closeTrackSnap struct {
	database.RawSnapshot
	k    *closeTrackKV
	once sync.Once
}

func (s *closeTrackSnap) Close() error {
	s.once.Do(func() { s.k.open.Add(-1) })
	return s.RawSnapshot.Close()
}

// The read paths validate op_kind like the write path does: a malformed one is
// an error, never "nothing decided" (which reads as "not rejected").
func TestReads_RefuseAMalformedOpKind(t *testing.T) {
	s, _ := newStore(t)
	rec := mustRecord(t, s, humanReject("b1", "b2"))
	v := mustLoad(t, s, nil)
	for name, kind := range map[string]string{"empty": "", "control": "repairs:dedup\x00split"} {
		t.Run(name, func(t *testing.T) {
			_, err := s.Verdict(kind, rec.SubjectKey)
			require.ErrorIs(t, err, ErrInvalidComponent)
			_, err = s.Lookup(kind, rec.SubjectKey, "")
			require.ErrorIs(t, err, ErrInvalidComponent)
			_, err = s.History(kind, rec.SubjectKey)
			require.ErrorIs(t, err, ErrInvalidComponent)
			_, err = v.SubjectRejected(kind, rec.SubjectKey, "")
			require.ErrorIs(t, err, ErrInvalidComponent)
		})
	}
	// The valid kind still answers.
	st, err := s.Lookup(testKind, rec.SubjectKey, "")
	require.NoError(t, err)
	require.True(t, st.Blocked)
}
