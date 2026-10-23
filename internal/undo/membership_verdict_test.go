// file: internal/undo/membership_verdict_test.go
// version: 1.1.0
// guid: c3a71e08-5d94-4f26-b8e0-19f6d2a4b573
// last-edited: 2026-10-22

package undo

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// verdictStore is a ConflictChecker that also reads a list's journal
// (ListJournalReader), failing when asked to.
type verdictStore struct {
	ConflictChecker
	rows []*database.OperationChange
	fail bool
}

func (s verdictStore) GetListChanges(kind, listID string) ([]*database.OperationChange, error) {
	if s.fail {
		return nil, errors.New("pebble: closed")
	}
	return s.rows, nil
}

// noJournalStore cannot read a list's journal at all.
type noJournalStore struct{ ConflictChecker }

func verdictCarry(t *testing.T) (row, marker *database.OperationChange) {
	t.Helper()
	_, rec, ok := CarryMembership([]string{"a", "B", "c"}, "B", "S")
	require.True(t, ok)
	val, err := EncodeMembership(rec)
	require.NoError(t, err)
	now := time.Unix(1_700_000_000, 0)
	marker = &database.OperationChange{ID: "1-op1-S", OperationID: "op1", BookID: "S", ChangeType: ChangeTypeUserContentFollow,
		FieldName: UserContentField(TookKind(UserContentCollection), "L"), OldValue: val, NewValue: "B", CreatedAt: now}
	row = &database.OperationChange{ID: "2-op1-B", OperationID: "op1", BookID: "B", ChangeType: ChangeTypeUserContentFollow,
		FieldName: UserContentField(UserContentCollection, "L"), OldValue: val, NewValue: "S", CreatedAt: now.Add(time.Microsecond)}
	return row, marker
}

// A list journal that cannot be read refuses the row as unreadable: the
// preflight must not offer an Undo the revert (which reads the same journal)
// cannot perform, and must not guess a verdict from the list alone.
func TestMembershipVerdict_AJournalReadErrorIsFieldUnreadable(t *testing.T) {
	r, m := verdictCarry(t)
	v := membershipVerdict(verdictStore{fail: true, rows: []*database.OperationChange{m, r}}, []string{"a", "S", "c"}, r, UserContentCollection, "L")
	require.Error(t, v.refusal)
	require.Equal(t, ReasonFieldUnreadable, RefusalReason(v.refusal), "%v", v.refusal)

	ok := membershipVerdict(verdictStore{rows: []*database.OperationChange{m, r}}, []string{"a", "S", "c"}, r, UserContentCollection, "L")
	require.NoError(t, ok.refusal)
	require.False(t, ok.already)
}

// A store without the journal capability refuses the row as unreadable, as the
// revert does (revertListFollow): the revert decides every list row from the
// list's journal and refuses without it, so the preflight must not offer an Undo
// the revert cannot perform, whether the list looks fine or not.
func TestMembershipVerdict_WithoutJournalTheRowIsRefusedAsUnreadable(t *testing.T) {
	r, _ := verdictCarry(t)
	for name, cur := range map[string][]string{"survivor removed by hand": {"a", "c"}, "list as the retire left it": {"a", "S", "c"}} {
		v := membershipVerdict(noJournalStore{}, cur, r, UserContentCollection, "L")
		require.Error(t, v.refusal, name)
		require.Equal(t, ReasonFieldUnreadable, RefusalReason(v.refusal), "%s: %v", name, v.refusal)
		require.False(t, v.already, name)
	}
}

// S's own retire out of the list (into X) stands; B is then retired into S,
// which is not listed, so S takes B's slot. The owner removes S by hand.
// Reverting B's row alone must refuse (changed since): S is not where the
// retire put it. An older standing retire of S must not count as "S retired out
// since", which would send B back beside its neighbours as if S had never been
// the slot's holder.
func TestMembershipRevert_AnOlderRetireOfTheSurvivorIsNotRetiredOutSince(t *testing.T) {
	s := newHandSim("a", "B", "c", "S", "d")
	require.NotEmpty(t, s.retire("op1", "S", "X"))
	require.Equal(t, []string{"a", "B", "c", "X", "d"}, s.list)
	id := s.retire("op2", "B", "S")
	require.NotEmpty(t, id)
	require.Equal(t, []string{"a", "S", "c", "X", "d"}, s.list)
	s.list = slices.DeleteFunc(slices.Clone(s.list), func(x string) bool { return x == "S" }) // by hand

	got, err := s.revertRow(id)
	require.Error(t, err, "outcome %d list %v notes %v", got, s.list, s.notes)
	require.Equal(t, ReasonChangedSince, RefusalReason(err))
}
