// file: internal/audiobooks/revert_settle_scoped_test.go
// version: 1.0.0
// guid: cc0f61e4-02a1-4f7d-b22f-ec410f2ddc4a
// last-edited: 2026-10-24

package audiobooks

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// A scoped run that dies after restoring a book's primary flag and before it
// settles leaves its intent under its own record. A later whole-operation
// revert must find it: it counts the row it finds already restored as
// evidence and settles the group, instead of leaving it with two primaries.
func TestRevertOperation_SettlesAGroupADeadScopedRunLeftPending(t *testing.T) {
	s := newRevertPebble(t)
	rs := NewRevertService(s)
	gid, yes := "vg-scoped-pending", true
	// The state the dead scoped run left: its demote row was reverted (o is
	// explicit true again) beside x, which the operation's hand-off crowned.
	o, err := s.CreateBook(&database.Book{Title: "O", FilePath: "/x/o", VersionGroupID: &gid, IsPrimaryVersion: &yes})
	require.NoError(t, err)
	x, err := s.CreateBook(&database.Book{Title: "X", FilePath: "/x/x", VersionGroupID: &gid, IsPrimaryVersion: &yes})
	require.NoError(t, err)
	require.NoError(t, s.CreateOperationChange(&database.OperationChange{
		OperationID: "op-scoped-pending", BookID: o.ID, ChangeType: undo.ChangeTypeBookPrimaryDemote,
		FieldName: "is_primary_version", OldValue: "true", NewValue: "false",
	}))
	scoped := newRevertScope([]string{o.ID}).recordID("op-scoped-pending")
	rs.markSettlePending(scoped, gid)
	require.True(t, rs.settlePendingGroups(scoped)[gid], "the dead scoped run's intent is recorded under its own key")

	res, err := rs.RevertOperation("op-scoped-pending")
	require.NoError(t, err, "%+v", res)
	assert.Empty(t, res.HandOffFailed)

	primaries := 0
	for _, id := range []string{o.ID, x.ID} {
		b, err := s.GetBookByID(id)
		require.NoError(t, err)
		if database.EffectiveIsPrimaryVersion(b.IsPrimaryVersion) {
			primaries++
		}
	}
	assert.Equal(t, 1, primaries, "the group is settled to one primary, not left with two")
	assert.Empty(t, rs.settlePendingGroups(scoped), "the scoped record is folded in and cleared")
	assert.Empty(t, rs.settlePendingGroups("op-scoped-pending"), "and the operation's record is cleared once settled")
}

// The same for a group a scoped run owed because its settle FAILED: the whole
// operation's revert retries it, with the later-pick check.
func TestRevertOperation_RetriesAGroupADeadScopedRunOwed(t *testing.T) {
	s := newRevertPebble(t)
	rs := NewRevertService(s)
	gid, no := "vg-scoped-owed", false
	o, err := s.CreateBook(&database.Book{Title: "O", FilePath: "/x/o", VersionGroupID: &gid, IsPrimaryVersion: &no})
	require.NoError(t, err)
	_, err = s.CreateBook(&database.Book{Title: "X", FilePath: "/x/x", VersionGroupID: &gid, IsPrimaryVersion: &no})
	require.NoError(t, err)
	// No member is primary (o was restored to explicit false by a run that
	// failed to crown it).
	scoped := newRevertScope([]string{o.ID}).recordID("op-scoped-owed")
	require.NoError(t, rs.storeSettleOwed(s, scoped, settleOwedRecord{Groups: map[string]settleOwedGroup{
		gid: {Originals: []string{o.ID}},
	}}))
	// The operation's rows are gone (purged): only the owed record is left.
	res, err := rs.RevertOperation("op-scoped-owed")
	require.NoError(t, err, "%+v", res)
	b, err := s.GetBookByID(o.ID)
	require.NoError(t, err)
	assert.True(t, database.EffectiveIsPrimaryVersion(b.IsPrimaryVersion), "the owed original is crowned")
	assert.False(t, rs.hasSettleOwed(scoped), "the scoped record is cleared")
	assert.False(t, rs.hasSettleOwed("op-scoped-owed"))
}

func TestMergeSettleOwedGroup(t *testing.T) {
	pend := settleOwedGroup{Pending: true}
	owedA := settleOwedGroup{Originals: []string{"a", "b"}, ExplicitTrue: []string{"x", "y"}}
	owedB := settleOwedGroup{Originals: []string{"b", "c"}, ExplicitTrue: []string{"y", "z"}}

	assert.Equal(t, pend, mergeSettleOwedGroup(pend, pend))
	assert.Equal(t, owedA, mergeSettleOwedGroup(pend, owedA), "an owed settle beats a pending intent")
	assert.Equal(t, owedA, mergeSettleOwedGroup(owedA, pend))
	merged := mergeSettleOwedGroup(owedA, owedB)
	assert.Equal(t, []string{"a", "b", "c"}, merged.Originals)
	assert.Equal(t, []string{"y"}, merged.ExplicitTrue, "only a member explicit true in both snapshots is not a later pick")
	assert.False(t, merged.Pending)
}
