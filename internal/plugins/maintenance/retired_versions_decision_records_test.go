// file: internal/plugins/maintenance/retired_versions_decision_records_test.go
// version: 1.0.0
// guid: 4e7d92b1-5a38-4c06-b2f9-8d1a63c0e745
// last-edited: 2026-10-28

package maintenance

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/decisions"
)

// retiredHistory is the decision chain of one shell's retired-versions
// proposal, newest first.
func (rf *retiredFixture) retiredHistory(shell string) []decisions.Record {
	rf.t.Helper()
	key, err := decisions.RetiredVersionsSubjectKey(rf.ids[shell])
	require.NoError(rf.t, err)
	kv, ok := database.AsCapability[decisions.KV](rf.st)
	require.True(rf.t, ok)
	hist, err := decisions.New(kv).History(decisions.OpKindRetiredVersions, key)
	require.NoError(rf.t, err)
	return hist
}

// An Apply writes one `applied` record per row it retires, keyed by the shell,
// naming the holder, the version it minted and its own op. A row it did not
// retire (too recent, no group, changed since the plan) gets none.
func TestRetiredApply_WritesTheAppliedDecisionRecord(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.seedApplyLibrary()
	res := rf.planAll()
	ids := append(rf.applicableIDs(res), rf.ids["kept"], rf.ids["unresolved"])
	// The late row changes after the plan was taken, so its apply is refused.
	require.NoError(t, rf.st.CreateBookFile(&database.BookFile{BookID: rf.ids["late"], FilePath: rf.file("g1/late-2.mp3"), Format: "mp3"}))

	const opID = "op-records"
	out := rf.applyRows(rf.f, res, ids, false, opID)
	require.Equal(t, 2, out.Applied, "%v %+v", out.ByOutcome, out.Rows)

	for _, name := range []string{"would-retire", "same-path"} {
		hist := rf.retiredHistory(name)
		require.Len(t, hist, 1, name)
		rec := hist[0]
		require.Equal(t, decisions.DecisionApplied, rec.Decision, name)
		require.Equal(t, decisions.OpKindRetiredVersions, rec.OpKind, name)
		require.Equal(t, []string{rf.ids[name]}, rec.EntityIDs, name)
		require.Equal(t, rf.ids[name], rec.Snapshot.Current["shell_id"], name)
		require.Equal(t, rf.ids["primary"], rec.Snapshot.Current["target_id"], name)
		require.Equal(t, opID, rec.Snapshot.Current["op_id"], name)
		require.Equal(t, retiredVersionID(opID, rf.ids[name]), rec.Snapshot.Current["version_id"], name)
		require.Equal(t, rf.ids[name], rec.PlanRowID, name)
		require.Equal(t, decisions.SourceAuto, rec.Source, name)
	}
	for _, name := range []string{"kept", "unresolved", "late", "primary"} {
		require.Empty(t, rf.retiredHistory(name), "%s was not retired", name)
	}

	// A label, not a veto: the row is still planned like any other.
	require.NotEqual(t, retCatVetoed, rf.plan()[rf.ids["late"]].Class)
}

// A second run of the same plan retires nothing and writes no second record.
func TestRetiredApply_RerunWritesNoSecondDecisionRecord(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.seedApplyLibrary()
	res := rf.planAll()
	shell := rf.ids["would-retire"]
	require.Equal(t, 1, rf.applyRows(rf.f, res, []string{shell}, false, "op-once").Applied)
	require.Len(t, rf.retiredHistory("would-retire"), 1)

	rf.applyRows(rf.f, res, []string{shell}, false, "op-twice")
	require.Len(t, rf.retiredHistory("would-retire"), 1, "the rows were already retired")
}

// A retire cut off after its commit point wrote its record there; the fresh
// plan that finishes the tail (op-fresh) is not a retire and adds none.
func TestRetiredApply_CutOffRetireIsRecordedOnceAtItsCommit(t *testing.T) {
	rf := newRetiredFixture(t)
	cutOffThenFinishedByAFreshPlan(t, rf)

	hist := rf.retiredHistory("would-retire")
	require.Len(t, hist, 1)
	require.Equal(t, decisions.DecisionApplied, hist[0].Decision)
	require.Equal(t, "op-cut", hist[0].Snapshot.Current["op_id"])
}

// The whole round trip: an Apply and the revert of its operation leave an
// `applied` then an `undone` record on the shell's chain, both naming the same
// version and retire op, and the shell is plannable again (neither is a veto).
func TestRetiredApply_RevertWritesTheUndoneRecordAfterTheApplied(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.seedApplyLibrary()
	res := rf.planAll()
	shell, holder := rf.ids["would-retire"], rf.ids["primary"]
	const opID = "op-roundtrip"
	require.Equal(t, 1, rf.applyRows(rf.f, res, []string{shell}, false, opID).Applied)
	require.Len(t, rf.retiredHistory("would-retire"), 1)

	rev, err := audiobooks.NewRevertService(rf.st).RevertOperation(opID)
	require.NoError(t, err)
	require.Zero(t, rev.Failed, "%+v", rev)

	hist := rf.retiredHistory("would-retire")
	require.Len(t, hist, 2)
	undone, applied := hist[0], hist[1] // newest first
	require.Equal(t, decisions.DecisionApplied, applied.Decision)
	require.Equal(t, decisions.DecisionUndone, undone.Decision)
	require.Equal(t, applied.ID, undone.SupersedesID)
	for _, k := range []string{"shell_id", "target_id", "version_id", "op_id"} {
		require.Equal(t, applied.Snapshot.Current[k], undone.Snapshot.Current[k], k)
	}
	require.Equal(t, shell, undone.Snapshot.Current["shell_id"])
	require.Equal(t, holder, undone.Snapshot.Current["target_id"])
	require.Equal(t, retiredVersionID(opID, shell), undone.Snapshot.Current["version_id"])

	require.Equal(t, retCatWouldRetire, rf.plan()[shell].Class, "undone is a label, the shell stays plannable")
}
