// file: internal/decisions/retired_versions_reject_test.go
// version: 1.0.0
// guid: 7fa2a966-3957-4be5-91a0-e1f4c7cd90b6
// last-edited: 2026-10-30

package decisions

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// retiredReject is the owner's reject of the retired-versions proposal for the
// shell of retiredEvent: sticky (no material hash), one entity, no scope.
func retiredReject(shell string) Input {
	return Input{
		OpKind: OpKindRetiredVersions, EntityType: retiredEntityBook, EntityIDs: []string{shell},
		Decision: DecisionReject, Source: SourceHuman, ActorUserID: "u1", ActorName: "Owner",
		ReasonCode: "shell_is_distinct_edition",
	}
}

func reasonCodes(rs []Reason) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Code
	}
	return out
}

// The retired-versions kind has its own reasons on top of the common and
// repairs: ones, with no code listed twice and none of the other families'.
func TestReasons_RetiredVersionsHasItsOwnTaxonomy(t *testing.T) {
	codes := reasonCodes(Reasons(OpKindRetiredVersions))
	require.Subset(t, codes, reasonCodes(commonReasons), "the common codes stay")
	require.Subset(t, codes, reasonCodes(rewriteReasons), "so do the repairs: codes")
	require.Subset(t, codes, []string{"shell_is_distinct_edition", "keep_both", "wrong_holder", ReasonOther})
	require.Len(t, codes, len(commonReasons)+len(rewriteReasons)+len(retiredVersionsReasons), "no duplicates")
	seen := map[string]bool{}
	for _, c := range codes {
		require.False(t, seen[c], "%s listed twice", c)
		seen[c] = true
	}
	require.NotContains(t, codes, "distinct_titles", "not the split-books taxonomy")

	// The new codes belong to this kind only.
	for _, other := range []string{"repairs:repair-junk-titles", "repairs:dedup.split-books", "dedup:x", "unknown"} {
		require.False(t, ReasonKnown(other, "shell_is_distinct_edition"), other)
		require.False(t, ReasonKnown(other, "wrong_holder"), other)
	}
	// Asking for the list leaves the package's own slices alone.
	got := Reasons(OpKindRetiredVersions)
	got[0].Code = "mutated"
	require.NotEqual(t, "mutated", Reasons(OpKindRetiredVersions)[0].Code)
	require.Equal(t, "not_same_entity", commonReasons[0].Code)
	require.Equal(t, "current_value_correct", rewriteReasons[0].Code)
}

// Record takes every code of the kind and refuses another family's.
func TestRecordRetiredReject_TakesTheKindsReasonCodes(t *testing.T) {
	for _, code := range reasonCodes(Reasons(OpKindRetiredVersions)) {
		s, _ := newStore(t)
		in := retiredReject("SHELL000000000000000000001")
		in.ReasonCode = code
		rec, err := s.Record(in)
		require.NoError(t, err, code)
		require.Equal(t, code, rec.ReasonCode)
	}
	s, _ := newStore(t)
	in := retiredReject("SHELL000000000000000000001")
	in.ReasonCode = "distinct_titles"
	_, err := s.Record(in)
	require.ErrorIs(t, err, ErrInvalid)
}

// A reject of the shell is written under the shell's subject key and blocks the
// fixer's sticky ask (material hash ""). A reopen is refused (the reject
// recorded no hash, so there is no new suggestion to allow) and the shell stays
// blocked; only an unreject releases it.
func TestRecordRetiredReject_RejectBlocksReopenDoesNotReleaseUnrejectDoes(t *testing.T) {
	s, kv := newStore(t)
	ev := retiredEvent("v1")
	key, err := RetiredVersionsSubjectKey(ev.ShellID)
	require.NoError(t, err)

	rec := mustRecord(t, s, retiredReject(ev.ShellID))
	require.Equal(t, key, rec.SubjectKey, "the reject is under the shell's subject key")
	require.Equal(t, []string{ev.ShellID}, rec.EntityIDs)
	require.Equal(t, 1, countPrefix(t, kv, prefixActive))
	require.True(t, snapRejected(t, mustLoad(t, s, nil), OpKindRetiredVersions, key, ""))

	reopen := retiredReject(ev.ShellID)
	reopen.Decision, reopen.ReasonCode, reopen.Source, reopen.ActorUserID, reopen.MaterialHash = DecisionReopened, "", SourceAuto, "", "h-new"
	_, err = s.Record(reopen)
	require.ErrorIs(t, err, ErrNotReopenable, "a sticky reject cannot be reopened")
	require.True(t, snapRejected(t, mustLoad(t, s, nil), OpKindRetiredVersions, key, ""), "still blocked")

	un := retiredReject(ev.ShellID)
	un.Decision, un.ReasonCode = DecisionUnreject, ""
	mustRecord(t, s, un)
	require.False(t, snapRejected(t, mustLoad(t, s, nil), OpKindRetiredVersions, key, ""), "the shell is plannable again")
	require.Equal(t, 0, countPrefix(t, kv, prefixActive))
	st, err := s.Lookup(OpKindRetiredVersions, key, "")
	require.NoError(t, err)
	require.False(t, st.Blocked)
}

// Even a reject that did carry a hash, reopened onto new data, blocks the
// fixer: its ask is sticky, so ANY live reject holds the shell, a reopened one
// included. Only an unreject releases it, which is why the route for this kind
// must offer one.
func TestRecordRetiredReject_ReopenedHashedRejectStillBlocksTheStickyAsk(t *testing.T) {
	s, _ := newStore(t)
	ev := retiredEvent("v1")
	key, err := RetiredVersionsSubjectKey(ev.ShellID)
	require.NoError(t, err)
	in := retiredReject(ev.ShellID)
	in.MaterialHash = "h-old"
	mustRecord(t, s, in)
	reopen := in
	reopen.Decision, reopen.ReasonCode, reopen.Source, reopen.ActorUserID, reopen.MaterialHash = DecisionReopened, "", SourceAuto, "", "h-new"
	mustRecord(t, s, reopen)

	v, err := s.Verdict(OpKindRetiredVersions, key)
	require.NoError(t, err)
	require.Equal(t, StateReopened, v.State)
	require.True(t, snapRejected(t, mustLoad(t, s, nil), OpKindRetiredVersions, key, ""), "the sticky ask is blocked by a reopened reject")

	un := in
	un.Decision, un.ReasonCode, un.MaterialHash = DecisionUnreject, "", ""
	mustRecord(t, s, un)
	require.False(t, snapRejected(t, mustLoad(t, s, nil), OpKindRetiredVersions, key, ""))
}

// A revert leaves an undone record and puts the rows back: the record is a
// label, never a veto, whether or not the shell was ever rejected. A reject made
// before the apply survives the label, and a reject lifted afterwards stays
// lifted.
func TestRecordRetiredUndone_IsALabelNeverAVeto(t *testing.T) {
	s, _ := newStore(t)
	ev := retiredEvent("v1")
	key, err := RetiredVersionsSubjectKey(ev.ShellID)
	require.NoError(t, err)
	blocked := func() bool { return snapRejected(t, mustLoad(t, s, nil), OpKindRetiredVersions, key, "") }

	_, err = RecordRetiredApplied(s, ev)
	require.NoError(t, err)
	ev.ActorUserID, ev.ActorName, ev.OpID = "u1", "Owner", "op-undo"
	_, err = RecordRetiredUndone(s, ev)
	require.NoError(t, err)
	require.False(t, blocked(), "applied then undone: plannable")
	st, err := s.Lookup(OpKindRetiredVersions, key, "")
	require.NoError(t, err)
	require.False(t, st.Blocked)

	mustRecord(t, s, retiredReject(ev.ShellID))
	require.True(t, blocked())
	again := retiredEvent("v2")
	again.ActorUserID, again.OpID = "u1", "op-undo-2"
	_, err = RecordRetiredUndone(s, again)
	require.NoError(t, err)
	require.True(t, blocked(), "an undone label lifts no reject")

	un := retiredReject(ev.ShellID)
	un.Decision, un.ReasonCode = DecisionUnreject, ""
	mustRecord(t, s, un)
	third := retiredEvent("v3")
	third.ActorUserID, third.OpID = "u1", "op-undo-3"
	_, err = RecordRetiredUndone(s, third)
	require.NoError(t, err)
	require.False(t, blocked(), "and a later undone label puts none back")
}
