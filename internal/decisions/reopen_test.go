// file: internal/decisions/reopen_test.go
// version: 1.0.0
// guid: a47e3c19-5d82-4b06-93f1-e2c80d6b7a54
// last-edited: 2026-10-21

package decisions

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const rewriteKind = "repairs:repair-junk-titles"

var (
	hashOld   = MaterialHash(map[string]string{"title": "Old"})
	hashNew   = MaterialHash(map[string]string{"title": "New"})
	hashThird = MaterialHash(map[string]string{"title": "Third"})
)

// rewriteReject is a field-rewrite reject: one entity, a material hash.
func rewriteReject(hash string) Input {
	return Input{
		OpKind: rewriteKind, EntityType: testType, EntityIDs: []string{"b1"}, Scope: "row-1",
		Decision: DecisionReject, Source: SourceHuman, ActorUserID: "u1",
		ReasonCode: "proposed_value_wrong", MaterialHash: hash,
	}
}

func reopenOf(in Input, newHash string) Input {
	in.Decision, in.ReasonCode, in.Source, in.ActorUserID, in.MaterialHash = DecisionReopened, "", SourceAuto, "", newHash
	return in
}

func lookup(t testing.TB, s *Store, in Input, hash string) Status {
	t.Helper()
	st, err := s.Lookup(in.OpKind, subjectOf(t, in), hash)
	require.NoError(t, err)
	return st
}

// Owner decision B (2026-10-21): a persisted reopen allows NEW suggestions
// only. The value that was rejected stays blocked, including when the data
// flips back to it.
func TestReopen_AllowsNewSuggestionsButKeepsTheRejectedValueBlocked(t *testing.T) {
	s, kv := newStore(t)
	in := rewriteReject(hashOld)
	mustRecord(t, s, in)
	mustRecord(t, s, reopenOf(in, hashNew))

	v, err := s.Verdict(in.OpKind, subjectOf(t, in))
	require.NoError(t, err)
	require.Equal(t, StateReopened, v.State)
	require.True(t, v.Live())
	require.Equal(t, []string{hashOld}, v.RejectedHashes, "the reopen keeps the rejected hash")
	require.NotEmpty(t, v.RejectID, "the reject stays the live one")
	require.Equal(t, 1, countPrefix(t, kv, prefixActive), "the active-index row survives a reopen")

	st := lookup(t, s, in, hashNew)
	require.False(t, st.Blocked, "the changed value is a new suggestion")
	require.Equal(t, StateReopened, st.State)

	st = lookup(t, s, in, hashOld)
	require.True(t, st.Blocked, "the data flipped back to the rejected value: still blocked")
	require.Equal(t, StateActive, st.State)
	require.True(t, lookup(t, s, in, "").Blocked, "a caller with no hash cannot tell, so it is blocked")

	snap := mustLoad(t, s, nil)
	require.True(t, snapRejected(t, snap, in.OpKind, subjectOf(t, in), hashOld))
	require.False(t, snapRejected(t, snap, in.OpKind, subjectOf(t, in), hashNew))
	require.True(t, snapRejected(t, snap, in.OpKind, subjectOf(t, in), ""))
}

// A reopen must not run dropLiveEdges: the pair stays vetoed.
func TestReopen_DoesNotDropPairEdges(t *testing.T) {
	s, kv := newStore(t)
	in := humanReject("b1", "b2", "b3")
	in.OpKind, in.ReasonCode, in.MaterialHash = rewriteKind, "proposed_value_wrong", hashOld
	mustRecord(t, s, in)
	require.Equal(t, 3, countPrefix(t, kv, prefixEdge))

	mustRecord(t, s, reopenOf(in, hashNew))
	require.Equal(t, 3, countPrefix(t, kv, prefixEdge), "a reopen leaves the edges")
	require.True(t, mustVetoed(t, s, testType, "b1", "b2", FamilySameWork))
	require.True(t, snapVetoed(t, mustLoad(t, s, nil), testType, "b2", "b3", FamilySameWork))
}

func TestReopen_RefusedWhenItCannotApply(t *testing.T) {
	s, kv := newStore(t)

	// No reject at all.
	in := rewriteReject(hashOld)
	_, err := s.Record(reopenOf(in, hashNew))
	require.ErrorIs(t, err, ErrNotRejected)

	// A sticky reject (merge-shaped: no material hash) cannot be reopened.
	sticky := rewriteReject("")
	sticky.Scope = "sticky"
	mustRecord(t, s, sticky)
	_, err = s.Record(reopenOf(sticky, hashNew))
	require.ErrorIs(t, err, ErrNotReopenable)

	mustRecord(t, s, in)
	before := countPrefix(t, kv, prefixRecord)

	// The new hash must differ from the rejected one.
	_, err = s.Record(reopenOf(in, hashOld))
	require.ErrorIs(t, err, ErrNotReopenable)
	// ...and must be given.
	_, err = s.Record(reopenOf(in, ""))
	require.ErrorIs(t, err, ErrInvalid)
	// A reopen needs a valid source and, for a human, an actor.
	bad := reopenOf(in, hashNew)
	bad.Source = "robot"
	_, err = s.Record(bad)
	require.ErrorIs(t, err, ErrInvalid)
	bad = reopenOf(in, hashNew)
	bad.Source, bad.ActorUserID = SourceHuman, ""
	_, err = s.Record(bad)
	require.ErrorIs(t, err, ErrInvalid)

	require.Equal(t, before, countPrefix(t, kv, prefixRecord), "a refused reopen leaves no record")
	require.True(t, lookup(t, s, in, hashOld).Blocked)
}

// Rejecting the new suggestion adds its hash: both stay blocked, a third is
// new again. An unreject clears the whole set.
func TestReopen_RejectAgainAddsHashAndUnrejectClearsAll(t *testing.T) {
	s, kv := newStore(t)
	in := rewriteReject(hashOld)
	mustRecord(t, s, in)
	mustRecord(t, s, reopenOf(in, hashNew))
	mustRecord(t, s, rewriteReject(hashNew))

	v, err := s.Verdict(in.OpKind, subjectOf(t, in))
	require.NoError(t, err)
	require.Equal(t, StateActive, v.State)
	require.ElementsMatch(t, []string{hashOld, hashNew}, v.RejectedHashes)
	require.True(t, lookup(t, s, in, hashOld).Blocked)
	require.True(t, lookup(t, s, in, hashNew).Blocked)
	require.False(t, lookup(t, s, in, hashThird).Blocked)
	_, err = s.Record(reopenOf(in, hashNew))
	require.ErrorIs(t, err, ErrNotReopenable, "hashNew is rejected now")
	mustRecord(t, s, reopenOf(in, hashThird))

	un := in
	un.Decision, un.ReasonCode = DecisionUnreject, ""
	mustRecord(t, s, un)
	v, err = s.Verdict(in.OpKind, subjectOf(t, in))
	require.NoError(t, err)
	require.Equal(t, StateUnrejected, v.State)
	require.Empty(t, v.RejectedHashes)
	require.False(t, lookup(t, s, in, hashOld).Blocked, "only an unreject lifts the block")
	require.Equal(t, 0, countPrefix(t, kv, prefixActive))
}

// A sticky re-reject stays sticky; a hashed reject on a sticky verdict does not
// narrow it.
func TestReject_StickyAbsorbs(t *testing.T) {
	s, _ := newStore(t)
	in := rewriteReject(hashOld)
	mustRecord(t, s, in)
	mustRecord(t, s, rewriteReject("")) // now sticky: blocks whatever the data
	require.True(t, lookup(t, s, in, hashNew).Blocked)
	mustRecord(t, s, rewriteReject(hashThird)) // must not narrow it back
	require.True(t, lookup(t, s, in, hashNew).Blocked)
}

func TestUnreject_FromReopenedLiftsEverythingAndDropsEdges(t *testing.T) {
	s, kv := newStore(t)
	in := humanReject("b1", "b2")
	in.OpKind, in.ReasonCode, in.MaterialHash = rewriteKind, "proposed_value_wrong", hashOld
	mustRecord(t, s, in)
	mustRecord(t, s, reopenOf(in, hashNew))
	un := in
	un.Decision, un.ReasonCode, un.MaterialHash = DecisionUnreject, "", ""
	mustRecord(t, s, un)
	require.Equal(t, 0, countPrefix(t, kv, prefixEdge))
	require.Equal(t, 0, countPrefix(t, kv, prefixActive))
	require.False(t, mustVetoed(t, s, testType, "b1", "b2", FamilySameWork))
}
