// file: internal/plugins/maintenance/retired_versions_decision_records_test.go
// version: 1.3.0
// guid: 4e7d92b1-5a38-4c06-b2f9-8d1a63c0e745
// last-edited: 2026-10-30

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/decisions"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
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

// cutAfterRetireRows commits the retire batch and then dies, before the
// caller's recordApplied: the cut-off between RetireVersion and the record.
type cutAfterRetireRows struct{ retireWriter }

func (c cutAfterRetireRows) RetireVersion(spec repairs.RetireSpec) error {
	if err := c.retireWriter.RetireVersion(spec); err != nil {
		return err
	}
	return errors.New("injected: the process died after the retire batch committed")
}

// A retire cut off after RetireVersion and before its record committed its rows
// but never wrote `applied`, and nothing visits the shell again except the
// fresh plan that finishes its tail. That finish writes the record, from the
// version's own id and retire op (op-cut, not the finishing op), and exactly one.
func TestRetiredApply_TailFinishWritesTheAppliedRecordACutOffRetireMissed(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.seedApplyLibrary()
	res := rf.planAll()
	orig := rf.f.retireWriterFor
	rf.f.retireWriterFor = func(w *repairs.Writer) (retireWriter, error) {
		inner, err := orig(w)
		return cutAfterRetireRows{inner}, err
	}
	shell := rf.ids["would-retire"]
	out := rf.applyRows(rf.f, res, []string{shell}, false, "op-cut")
	require.Zero(t, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
	require.Empty(t, rf.retiredHistory("would-retire"), "the cut-off came before the record")
	files, err := rf.st.GetBookFiles(shell)
	require.NoError(t, err)
	require.Empty(t, files, "the retire batch did commit: the shell owns no row")

	rf.f.retireWriterFor = orig
	fresh := rf.planAll()
	require.Equal(t, retCatTailPending, rf.retiredPlanRow(fresh, "would-retire").Class)
	out = rf.applyRows(rf.f, fresh, []string{shell}, false, "op-fresh")
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)

	hist := rf.retiredHistory("would-retire")
	require.Len(t, hist, 1, "exactly one applied record")
	require.Equal(t, decisions.DecisionApplied, hist[0].Decision)
	require.Equal(t, "op-cut", hist[0].Snapshot.Current["op_id"])
	require.Equal(t, retiredVersionID("op-cut", shell), hist[0].Snapshot.Current["version_id"])
	require.Equal(t, rf.ids["primary"], hist[0].Snapshot.Current["target_id"])
}

// storeApplyOp stores the row of a finished repairs.apply op: the plan it
// applied (in its params) and the user it was enqueued for ("": none).
func (rf *retiredFixture) storeApplyOp(id, planOpID, actorUserID string) {
	rf.t.Helper()
	params, err := json.Marshal(repairs.ApplyParams{FixerID: rf.f.ID(), PlanOpID: planOpID})
	require.NoError(rf.t, err)
	now := time.Now()
	row := database.OperationV2Row{ID: id, DefID: repairs.ApplyOpID, Plugin: "maintenance", Status: "completed",
		Params: string(params), QueuedAt: now, CompletedAt: &now}
	if actorUserID != "" {
		row.ActorUserID = &actorUserID
	}
	require.NoError(rf.t, rf.st.InsertOperationV2(row))
}

// An apply enqueued by a user records that user and the plan op it applied, so
// the `applied` record reads as the user's decision.
func TestRetiredApply_AppliedRecordNamesTheUserAndThePlanOp(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.seedApplyLibrary()
	res := rf.planAll()
	shell := rf.ids["would-retire"]
	require.Equal(t, 1, rf.applyRowsAs(rf.f, res, []string{shell}, false, "op-by-user", "plan-batman", "user-alfred").Applied)

	hist := rf.retiredHistory("would-retire")
	require.Len(t, hist, 1)
	require.Equal(t, decisions.DecisionApplied, hist[0].Decision)
	require.Equal(t, decisions.SourceHuman, hist[0].Source)
	require.Equal(t, "user-alfred", hist[0].ActorUserID)
	require.Equal(t, "plan-batman", hist[0].PlanOpID)
	require.Equal(t, shell, hist[0].PlanRowID)
}

// An apply no user enqueued (a scheduled or system one) still names its plan op
// and is recorded as automatic: no actor is invented for it.
func TestRetiredApply_SystemApplyIsRecordedAsAutomatic(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.seedApplyLibrary()
	res := rf.planAll()
	shell := rf.ids["would-retire"]
	require.Equal(t, 1, rf.applyRowsAs(rf.f, res, []string{shell}, false, "op-by-system", "plan-joker", "").Applied)

	hist := rf.retiredHistory("would-retire")
	require.Len(t, hist, 1)
	require.Equal(t, decisions.SourceAuto, hist[0].Source)
	require.Empty(t, hist[0].ActorUserID)
	require.Equal(t, "plan-joker", hist[0].PlanOpID)
}

// The Writer's provenance is the run's, and only the run's: it is set for the
// rows RunApply applies and gone after, so a Writer reused outside a run
// records nothing about the last one.
func TestRetiredApply_ProvenanceIsScopedToTheRun(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.seedApplyLibrary()
	res := rf.planAll()
	w := repairs.NewWriter(rf.st, rf.st, rf.f.ID(), "bulk_update", "repairs-").WithJournal(rf.st, rf.st, "op-scoped").WithFieldStates(rf.st)
	require.Equal(t, repairs.ApplyProvenance{}, w.Provenance())
	_, err := repairs.RunApply(context.Background(), rf.f, res, "plan-penguin", []string{rf.ids["would-retire"]}, false,
		repairs.ApplyDeps{Guard: rf.st, Writer: w, OpID: "op-scoped", StandDown: &countingStandDown{}, ActorUserID: "user-selina", ActorName: "selina"},
		&fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, repairs.ApplyProvenance{}, w.Provenance(), "cleared once the run ended")
	hist := rf.retiredHistory("would-retire")
	require.Len(t, hist, 1)
	require.Equal(t, "selina", hist[0].ActorName)
}

// A fresh plan finishes the tail of a retire an earlier op (op-cut) cut off,
// and writes the record that op missed. The record is the retire op's, so it
// names the retire op's user and plan, read from its row, and not the user who
// ran the op that finished it.
func TestRetiredApply_TailFinishNamesTheRetireOpsUserNotTheFinishers(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.seedApplyLibrary()
	res := rf.planAll()
	orig := rf.f.retireWriterFor
	rf.f.retireWriterFor = func(w *repairs.Writer) (retireWriter, error) {
		inner, err := orig(w)
		return cutAfterRetireRows{inner}, err
	}
	shell := rf.ids["would-retire"]
	rf.applyRowsAs(rf.f, res, []string{shell}, false, "op-cut", "plan-cut", "user-alfred")
	require.Empty(t, rf.retiredHistory("would-retire"), "the cut-off came before the record")
	rf.storeApplyOp("op-cut", "plan-cut", "user-alfred")

	rf.f.retireWriterFor = orig
	fresh := rf.planAll()
	require.Equal(t, retCatTailPending, rf.retiredPlanRow(fresh, "would-retire").Class)
	out := rf.applyRowsAs(rf.f, fresh, []string{shell}, false, "op-fresh", "plan-fresh", "user-selina")
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)

	hist := rf.retiredHistory("would-retire")
	require.Len(t, hist, 1)
	require.Equal(t, "op-cut", hist[0].Snapshot.Current["op_id"])
	require.Equal(t, decisions.SourceHuman, hist[0].Source)
	require.Equal(t, "user-alfred", hist[0].ActorUserID)
	require.Equal(t, "plan-cut", hist[0].PlanOpID)
}

// When the retire op's row is gone (a discarded op) its record is automatic
// rather than blamed on the op that finished the tail, and the tail still runs.
func TestRetiredApply_TailFinishWithNoRetireOpRowIsAutomatic(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.seedApplyLibrary()
	res := rf.planAll()
	orig := rf.f.retireWriterFor
	rf.f.retireWriterFor = func(w *repairs.Writer) (retireWriter, error) {
		inner, err := orig(w)
		return cutAfterRetireRows{inner}, err
	}
	shell := rf.ids["would-retire"]
	rf.applyRowsAs(rf.f, res, []string{shell}, false, "op-cut", "plan-cut", "user-alfred")
	rf.f.retireWriterFor = orig
	fresh := rf.planAll()
	out := rf.applyRowsAs(rf.f, fresh, []string{shell}, false, "op-fresh", "plan-fresh", "user-selina")
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)

	hist := rf.retiredHistory("would-retire")
	require.Len(t, hist, 1)
	require.Equal(t, "op-cut", hist[0].Snapshot.Current["op_id"])
	require.Equal(t, decisions.SourceAuto, hist[0].Source)
	require.Empty(t, hist[0].ActorUserID)
	require.Empty(t, hist[0].PlanOpID)
}

// A revert run for a user records that user on the `undone` record, with their
// name; the `applied` record it undoes keeps the apply's user.
func TestRetiredApply_RevertByAUserNamesThemOnTheUndoneRecord(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.seedApplyLibrary()
	res := rf.planAll()
	shell := rf.ids["would-retire"]
	require.Equal(t, 1, rf.applyRowsAs(rf.f, res, []string{shell}, false, "op-rv", "plan-1", "user-alfred").Applied)

	rev, err := audiobooks.NewRevertService(rf.st).
		WithActor(audiobooks.RevertActor{UserID: "user-selina", Name: "selina"}).RevertOperation("op-rv")
	require.NoError(t, err)
	require.Zero(t, rev.Failed, "%+v", rev)

	hist := rf.retiredHistory("would-retire")
	require.Len(t, hist, 2)
	undone, applied := hist[0], hist[1]
	require.Equal(t, decisions.DecisionUndone, undone.Decision)
	require.Equal(t, decisions.SourceHuman, undone.Source)
	require.Equal(t, "user-selina", undone.ActorUserID)
	require.Equal(t, "selina", undone.ActorName)
	require.Equal(t, "user-alfred", applied.ActorUserID, "the applied record keeps who applied")
}

// A revert nobody asked for (a system path, no WithActor) is recorded as
// automatic, as before.
func TestRetiredApply_RevertWithNoUserIsRecordedAsAutomatic(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.seedApplyLibrary()
	res := rf.planAll()
	shell := rf.ids["would-retire"]
	require.Equal(t, 1, rf.applyRowsAs(rf.f, res, []string{shell}, false, "op-rv-sys", "plan-1", "user-alfred").Applied)

	_, err := audiobooks.NewRevertService(rf.st).RevertOperation("op-rv-sys")
	require.NoError(t, err)

	hist := rf.retiredHistory("would-retire")
	require.Len(t, hist, 2)
	require.Equal(t, decisions.DecisionUndone, hist[0].Decision)
	require.Equal(t, decisions.SourceAuto, hist[0].Source)
	require.Empty(t, hist[0].ActorUserID)
}

// A cut-off retire finished by a re-run of the same op (a resume) writes the
// record from the running op's own provenance, with no op-row read: the running
// op is the retire op.
func TestRetiredApply_TailFinishByTheSameOpUsesItsOwnProvenance(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.seedApplyLibrary()
	res := rf.planAll()
	orig := rf.f.retireWriterFor
	rf.f.retireWriterFor = func(w *repairs.Writer) (retireWriter, error) {
		inner, err := orig(w)
		return cutAfterRetireRows{inner}, err
	}
	shell := rf.ids["would-retire"]
	rf.applyRowsAs(rf.f, res, []string{shell}, false, "op-cut", "plan-cut", "user-alfred")
	require.Empty(t, rf.retiredHistory("would-retire"))

	rf.f.retireWriterFor = orig
	fresh := rf.planAll()
	out := rf.applyRowsAs(rf.f, fresh, []string{shell}, false, "op-cut", "plan-cut", "user-alfred")
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)

	hist := rf.retiredHistory("would-retire")
	require.Len(t, hist, 1)
	require.Equal(t, "user-alfred", hist[0].ActorUserID)
	require.Equal(t, "plan-cut", hist[0].PlanOpID)
}
