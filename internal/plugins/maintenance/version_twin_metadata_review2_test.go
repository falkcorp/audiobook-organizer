// file: internal/plugins/maintenance/version_twin_metadata_review2_test.go
// version: 1.3.0
// guid: d63428b5-a8b1-4678-86f0-28e71e7b90a9
// last-edited: 2026-10-06

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// The regression tests for the second review of #3797 (S1-S3, N2, N4). All
// fixture rows are synthetic (public repo).

// vtDetailOf is the apply detail of a fresh row.
func vtDetailOf(t *testing.T, r repairs.Row) *vtDetail {
	t.Helper()
	d, ok := r.Detail.(*vtDetail)
	require.True(t, ok, "row %s has no detail", r.RowID)
	return d
}

// S1: the under-lock guard never runs the full-scan hash lookup. With memdb
// not serving, it refuses retry_later (fail closed) and the scan is
// never called; with memdb serving, it answers from memdb, still without the
// scan.
func TestVersionTwinFixer_Review2_S1_GuardNeverScans(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "gs1", true, nil)
	l.appliedTwin("t", "gs1", vtSaga(), nil)
	d := vtDetailOf(t, l.vtFresh("gs1"))
	require.NotEmpty(t, d.hash)

	rd, err := l.fixer.readers(repairs.NewPathResolver())
	require.NoError(t, err)
	cleared, err := vtPreWrite(rd, d)
	require.NoError(t, err)

	var scans atomic.Int64
	rd.hash = func(string) ([]database.Book, error) {
		scans.Add(1)
		return nil, errors.New("the full scan must not run under the write lock")
	}
	realMem := rd.memHash
	rd.memHash = func(string) ([]database.Book, error) {
		return nil, fmt.Errorf("%w: warmup still running", database.ErrMemDBNotReady)
	}
	b := l.get("p")
	err = vtWriteGuard(rd, d, cleared, b)
	require.Error(t, err)
	require.True(t, errors.Is(err, repairs.ErrRetryLater), err.Error())
	require.Contains(t, err.Error(), "memdb")
	require.Zero(t, scans.Load(), "the guard called the full-scan lookup")

	rd.memHash = realMem
	require.NoError(t, vtWriteGuard(rd, d, cleared, b))
	require.Zero(t, scans.Load(), "the guard called the full-scan lookup")
}

// S1: a path the pre-write check did not resolve and clear is refused under
// the lock instead of being resolved there (no disk I/O under a stripe).
func TestVersionTwinFixer_Review2_S1_GuardRefusesUnclearedPath(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "gs1b", true, nil)
	l.appliedTwin("t", "gs1b", vtSaga(), nil)
	d := vtDetailOf(t, l.vtFresh("gs1b"))
	rd, err := l.fixer.readers(repairs.NewPathResolver())
	require.NoError(t, err)
	cleared, err := vtPreWrite(rd, d)
	require.NoError(t, err)

	require.NoError(t, l.st.CreateBookFile(&database.BookFile{BookID: l.ids["p"],
		FilePath: "/lib/Synthetic Author A/p/02.m4b", Format: "m4b"}))
	err = vtWriteGuard(rd, d, cleared, l.get("p"))
	require.Error(t, err)
	require.True(t, errors.Is(err, repairs.ErrChangedSincePlan), err.Error())
	require.Contains(t, err.Error(), "02.m4b")
}

// S1 end to end, and N4: on a store whose memdb is off, the apply is refused
// retry_later (not changed_since_plan: the row did not change), the primary is not written, and the refusal comes
// before the apply body, so the record's series is not created.
func TestVersionTwinFixer_Review2_S1_MemdbOffRefusesWithoutSeriesRow(t *testing.T) {
	l := newVTLib(t)
	cand := vtSagaASIN("B0SYNTH021")
	cand.Series = "Synthetic Series Twenty-One"
	l.book("p", "gs1c", true, nil)
	l.appliedTwin("t", "gs1c", cand, nil)
	plan, rows := l.plan()
	require.True(t, rows["gs1c"].Applicable(), rows["gs1c"].SkipReason)
	before, err := l.st.GetAllSeries()
	require.NoError(t, err)

	l.st.UseMemDB = false
	res := l.apply(plan, false, "gs1c")
	require.Equal(t, 1, res.RetryLater, "outcomes %v rows %+v", res.ByOutcome, res.Rows)
	require.Zero(t, res.ChangedSincePlan, "memdb not serving is not a change to the row")
	require.Equal(t, repairs.OutcomeRetryLater, res.Rows[0].Outcome)
	require.Contains(t, res.Rows[0].Error, "memdb")
	p := l.get("p")
	require.Nil(t, p.MetadataReviewStatus)
	require.Empty(t, dcStr(p.Narrator))
	require.Nil(t, p.SeriesID)
	after, err := l.st.GetAllSeries()
	require.NoError(t, err)
	require.Len(t, after, len(before), "a refused apply created a series row")

	// memdb back: the same plan applies, series included.
	l.st.UseMemDB = true
	res = l.apply(plan, false, "gs1c")
	require.Equal(t, 1, res.Applied, "outcomes %v rows %+v", res.ByOutcome, res.Rows)
	require.NotNil(t, l.get("p").SeriesID)
}

// S2: the reviewer's case. Primary 73,300 s; twin and record 75,600 s (about
// 3% apart: under the 5% edition hold, over the 1% evidence threshold); the
// primary already carries the record's narrator. The narrator does not
// override the disagreeing runtimes: the row is held and no ASIN is copied.
func TestVersionTwinFixer_Review2_S2_NarratorDoesNotOverrideRuntimeGap(t *testing.T) {
	l := newVTLib(t)
	cand := vtSagaASIN("B0SYNTH022")
	cand.DurationSec = 75600
	l.book("p", "gs2", true, func(b *database.Book) {
		b.Duration = vtPtr(73300)
		b.Narrator = vtPtr(cand.Narrator)
	})
	l.appliedTwin("t", "gs2", cand, nil)
	plan, rows := l.plan()
	row := rows["gs2"]
	require.Equal(t, vtHoldEvidenceConflict, row.Skipped, row.SkipReason)
	require.Contains(t, row.SkipReason, "73300s")
	require.Contains(t, row.SkipReason, "75600s")
	require.Empty(t, row.Proposed["primary_asin"])

	res := l.apply(plan, false, "gs2")
	require.Zero(t, res.Applied, "outcomes %v", res.ByOutcome)
	p := l.get("p")
	require.Empty(t, dcStr(p.ASIN), "no ASIN copied")
	require.Nil(t, p.MetadataReviewStatus)
}

// S2, the other side: with no runtime known anywhere, the primary carrying
// the record's narrator is the evidence, and the ASIN is copied.
func TestVersionTwinFixer_Review2_S2_NarratorEvidenceWhenRuntimesUnknown(t *testing.T) {
	l := newVTLib(t)
	cand := vtSagaASIN("B0SYNTH023")
	l.book("p", "gs2b", true, func(b *database.Book) {
		b.Duration = nil
		b.Narrator = vtPtr(cand.Narrator)
	})
	l.appliedTwin("t", "gs2b", cand, func(b *database.Book) { b.Duration = nil })
	plan, rows := l.plan()
	row := rows["gs2b"]
	require.True(t, row.Applicable(), "%s: %s", row.Skipped, row.SkipReason)
	require.Equal(t, "B0SYNTH023", row.Proposed["primary_asin"])
	require.Contains(t, row.Evidence, "same edition: the primary already carries the record's narrator \""+
		cand.Narrator+"\" (no two known runtimes more than max(1%, 60 s) apart)")

	res := l.apply(plan, false, "gs2b")
	require.Equal(t, 1, res.Applied, "outcomes %v rows %+v", res.ByOutcome, res.Rows)
	require.Equal(t, "B0SYNTH023", dcStr(l.get("p").ASIN))
}

// S3: a crash after the apply committed (history written under this op's
// batch) and before its journal row. The resumed run of the same op
// recognises its own write, records the journal row exactly once (a second
// run adds nothing), and the op revert restores the primary.
func TestVersionTwinFixer_Review2_S3_CommitThenCrashIsJournaledOnResume(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "gs3", true, nil)
	l.appliedTwin("t", "gs3", vtSaga(), nil)
	flags := l.primaryFlags()
	plan, rows := l.plan()
	require.True(t, rows["gs3"].Applicable(), rows["gs3"].SkipReason)

	// The cut-off run: the apply commits with this op's batch, the journal
	// row is never written.
	d := vtDetailOf(t, l.vtFresh("gs3"))
	_, err := metafetch.NewService(l.st).ApplyMetadataCandidateWithOptions(d.primaryID, *d.cand, d.fields,
		vtApplyOptions(d, vtBatchID(vtTestOpID, d.primaryID), nil))
	require.NoError(t, err)
	require.True(t, database.MetadataApplied(l.get("p").MetadataReviewStatus))
	changes, err := l.st.GetOperationChanges(vtTestOpID)
	require.NoError(t, err)
	require.Empty(t, changes)

	for run := 1; run <= 2; run++ {
		res := l.apply(plan, false, "gs3")
		require.Equal(t, 1, res.Applied, "run %d: outcomes %v rows %+v", run, res.ByOutcome, res.Rows)
		changes, err = l.st.GetOperationChanges(vtTestOpID)
		require.NoError(t, err)
		require.Len(t, changes, 1, "run %d: exactly one journal row", run)
		require.Equal(t, undo.ChangeTypeMetadataApply, changes[0].ChangeType)
		require.Equal(t, vtBatchID(vtTestOpID, d.primaryID), changes[0].NewValue)
	}

	res, err := audiobooks.NewRevertService(l.st).RevertOperation(vtTestOpID)
	require.NoError(t, err)
	require.Equal(t, 1, res.Restored, "result %+v", res)
	p := l.get("p")
	require.Empty(t, dcStr(p.MetadataReviewStatus))
	require.Empty(t, dcStr(p.Narrator))
	require.Empty(t, dcStr(p.ASIN))
	require.Empty(t, dcStr(p.MetadataSourceHash))
	require.Equal(t, flags, l.primaryFlags())
}

// S3: an apply of the same record by someone else (no batch of this op in
// the history) is not taken for this op's: the row stays changed_since_plan
// and nothing is journaled.
func TestVersionTwinFixer_Review2_S3_OtherApplyIsNotResumed(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "gs3b", true, nil)
	l.appliedTwin("t", "gs3b", vtSaga(), nil)
	plan, _ := l.plan()
	d := vtDetailOf(t, l.vtFresh("gs3b"))
	_, err := metafetch.NewService(l.st).ApplyMetadataCandidateWithOptions(d.primaryID, *d.cand, d.fields,
		vtApplyOptions(d, vtBatchID("op-someone-else", d.primaryID), nil))
	require.NoError(t, err)

	res := l.apply(plan, false, "gs3b")
	require.Equal(t, 1, res.ChangedSincePlan, "outcomes %v rows %+v", res.ByOutcome, res.Rows)
	changes, err := l.st.GetOperationChanges(vtTestOpID)
	require.NoError(t, err)
	require.Empty(t, changes)
}

// S3, candidate copy: a crash after the copy and before its journal row.
// The resumed run journals the copy with the primary's prior cache state
// from the plan (none), once, and the op revert removes the copy.
func TestVersionTwinFixer_Review2_S3_CopyThenCrashIsJournaledOnResume(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "gs3c", true, nil)
	tid := l.book("t", "gs3c", false, nil)
	l.putCache(tid, []metafetch.MetadataCandidate{vtSaga()}, metafetch.BatchSourceHash(tid, "Synthetic Saga", "Synthetic Author A"))
	plan, rows := l.plan()
	require.True(t, rows["gs3c"].Applicable(), rows["gs3c"].SkipReason)

	_, err := metafetch.NewService(l.st).CopyCandidateCache(tid, l.ids["p"], nil)
	require.NoError(t, err)

	for run := 1; run <= 2; run++ {
		res := l.apply(plan, false, "gs3c")
		require.Equal(t, 1, res.Applied, "run %d: outcomes %v rows %+v", run, res.ByOutcome, res.Rows)
		changes, err := l.st.GetOperationChanges(vtTestOpID)
		require.NoError(t, err)
		require.Len(t, changes, 1, "run %d: exactly one journal row", run)
		require.Equal(t, undo.ChangeTypeMetadataCacheCopy, changes[0].ChangeType)
		require.Equal(t, undo.MetadataCacheAbsent, changes[0].OldValue)
	}

	res, err := audiobooks.NewRevertService(l.st).RevertOperation(vtTestOpID)
	require.NoError(t, err)
	require.Equal(t, 1, res.Restored, "result %+v", res)
	entry, err := l.st.GetMetadataCache(l.ids["p"])
	require.NoError(t, err)
	require.Nil(t, entry, "the op revert removes the copy")
}

// N2: an ASIN the apply will not copy (no edition evidence) is not checked
// against books outside the group, so a book carrying it does not hold the
// row, and neither does an unbuilt ISBN/ASIN index.
func TestVersionTwinFixer_Review2_N2_UncopiedASINDoesNotHold(t *testing.T) {
	l := newVTLib(t)
	l.book("out", "g-out", true, func(b *database.Book) { b.ASIN = vtPtr("B0SYNTH024"); b.Title = "Synthetic Other" })
	// 2% off the twin and no narrator: no evidence, so no ASIN copy.
	l.book("p", "gn2", true, func(b *database.Book) { b.Duration = vtPtr(74000) })
	l.appliedTwin("t", "gn2", vtSagaASIN("B0SYNTH024"), nil)
	_, rows := l.plan()
	row := rows["gn2"]
	require.True(t, row.Applicable(), "%s: %s", row.Skipped, row.SkipReason)
	require.Empty(t, row.Proposed["primary_asin"])

	require.NoError(t, l.st.SetSetting("book_isbn_index_v1_done", "false", "bool", false))
	_, rows = l.plan()
	require.True(t, rows["gn2"].Applicable(), "an unbuilt index holds only a row that copies an identifier: %s",
		rows["gn2"].SkipReason)
}

// N2: the ISBN the apply would copy is checked too, and the ASIN lookup
// finds a book storing the ASIN in another case.
func TestVersionTwinFixer_Review2_N2_ISBNAndASINCaseHold(t *testing.T) {
	l := newVTLib(t)
	l.book("out-isbn", "g-out1", true, func(b *database.Book) { b.ISBN13 = vtPtr("9790000000251"); b.Title = "Synthetic Other" })
	isbnCand := vtSagaASIN("B0SYNTH025")
	isbnCand.ISBN13 = "9790000000251"
	l.book("p1", "gn2a", true, nil)
	l.appliedTwin("t1", "gn2a", isbnCand, nil)

	l.book("out-asin", "g-out2", true, func(b *database.Book) { b.ASIN = vtPtr("b0synth026"); b.Title = "Synthetic Other Two" })
	l.book("p2", "gn2b", true, nil)
	l.appliedTwin("t2", "gn2b", vtSagaASIN("B0SYNTH026"), nil)

	_, rows := l.plan()
	require.Equal(t, vtHoldIDElsewhere, rows["gn2a"].Skipped, rows["gn2a"].SkipReason)
	require.Contains(t, rows["gn2a"].SkipReason, l.ids["out-isbn"])
	require.Contains(t, rows["gn2a"].SkipReason, "ISBN-13 9790000000251")
	require.Equal(t, vtHoldIDElsewhere, rows["gn2b"].Skipped, rows["gn2b"].SkipReason)
	require.Contains(t, rows["gn2b"].SkipReason, l.ids["out-asin"])
}

// S3, two apply ops of one plan: op A copies the candidates and journals
// them; op B, run on the same plan row afterwards, finds an identical copy
// but must not take it for its own: no journal row under B, and the row is
// not reported applied.
func TestVersionTwinFixer_Review2_S3_OtherOpsCopyIsNotResumed(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "gs3d", true, nil)
	tid := l.book("t", "gs3d", false, nil)
	l.putCache(tid, []metafetch.MetadataCandidate{vtSaga()}, metafetch.BatchSourceHash(tid, "Synthetic Saga", "Synthetic Author A"))
	plan, _ := l.plan()
	res := l.apply(plan, false, "gs3d")
	require.Equal(t, 1, res.Applied, "op A: outcomes %v rows %+v", res.ByOutcome, res.Rows)

	const opB = "op-version-twin-apply-b"
	series, err := l.st.GetAllSeries()
	require.NoError(t, err)
	deps := repairs.ApplyDeps{Guard: l.st, Tags: l.p.repairsGuardTags(), Series: repairs.SeriesNamesFrom(series), OpID: opB,
		Writer: repairs.NewWriter(l.st, l.st, l.fixer.ID(), "bulk_update", "repairs-").WithJournal(l.st, l.st, opB)}
	resB, err := repairs.RunApply(context.Background(), l.fixer, plan, "op-version-twin-plan", []string{"gs3d"}, false, deps,
		&repairsOpReporter{id: opB})
	require.NoError(t, err)
	require.Zero(t, resB.Applied, "op B: outcomes %v rows %+v", resB.ByOutcome, resB.Rows)
	changes, err := l.st.GetOperationChanges(opB)
	require.NoError(t, err)
	require.Empty(t, changes, "op B journaled op A's copy")
}

// S2: the twin's runtime unknown, the primary's and the record's within 1%:
// that is runtime evidence (the narrator is not needed), and the ASIN is
// copied.
func TestVersionTwinFixer_Review2_S2_PrimaryAndRecordRuntimeEvidence(t *testing.T) {
	l := newVTLib(t)
	cand := vtSagaASIN("B0SYNTH027")
	cand.DurationSec = 75600
	l.book("p", "gs2c", true, func(b *database.Book) { b.Narrator = vtPtr(cand.Narrator) })
	l.appliedTwin("t", "gs2c", cand, func(b *database.Book) { b.Duration = nil })
	_, rows := l.plan()
	row := rows["gs2c"]
	require.True(t, row.Applicable(), "%s: %s", row.Skipped, row.SkipReason)
	require.Equal(t, "B0SYNTH027", row.Proposed["primary_asin"])
	require.Contains(t, row.Evidence, "same edition: runtimes agree within max(1%, 60 s) "+
		"(primary 75600s, twin unknown, record 75600s)")
}

// S2 (round 3): narrator evidence is not dropped because the primary's
// runtime is unknown. Primary unknown, twin and record agreeing: no two known
// runtimes disagree, so the primary carrying the record's narrator is
// evidence, and the ASIN is copied.
func TestVersionTwinFixer_Review3_S2_NarratorEvidenceWithPrimaryRuntimeUnknown(t *testing.T) {
	l := newVTLib(t)
	cand := vtSagaASIN("B0SYNTH031")
	cand.DurationSec = 3600
	l.book("p", "gr3a", true, func(b *database.Book) {
		b.Duration = nil
		b.Narrator = vtPtr(cand.Narrator)
	})
	l.appliedTwin("t", "gr3a", cand, func(b *database.Book) { b.Duration = vtPtr(3600) })
	_, rows := l.plan()
	row := rows["gr3a"]
	require.True(t, row.Applicable(), "%s: %s", row.Skipped, row.SkipReason)
	require.Equal(t, "B0SYNTH031", row.Proposed["primary_asin"])
	require.Contains(t, row.Evidence, "same edition: the primary already carries the record's narrator \""+
		cand.Narrator+"\" (no two known runtimes more than max(1%, 60 s) apart)")
}

// S2 (round 3): with the primary's runtime unknown and the twin's and the
// record's disagreeing, a matching narrator is still a conflict.
func TestVersionTwinFixer_Review3_S2_TwinRecordDisagreeStillConflicts(t *testing.T) {
	l := newVTLib(t)
	cand := vtSagaASIN("B0SYNTH032")
	cand.DurationSec = 3800
	l.book("p", "gr3b", true, func(b *database.Book) {
		b.Duration = nil
		b.Narrator = vtPtr(cand.Narrator)
	})
	l.appliedTwin("t", "gr3b", cand, func(b *database.Book) { b.Duration = vtPtr(3600) })
	_, rows := l.plan()
	require.Equal(t, vtHoldEvidenceConflict, rows["gr3b"].Skipped, rows["gr3b"].SkipReason)
}

// Item 3 (round 3): an identifier the apply copies, gained by a book outside
// the group between the re-plan and the write, refuses the write.
func TestVersionTwinFixer_Review3_IdentifierGainedAfterReplanRefuses(t *testing.T) {
	l := newVTLib(t)
	l.book("out", "g-out3", true, func(b *database.Book) { b.Title = "Synthetic Other" })
	l.book("p", "gr3c", true, nil)
	l.appliedTwin("t", "gr3c", vtSagaASIN("B0SYNTH033"), nil)
	fresh := l.vtFresh("gr3c")
	require.Equal(t, "B0SYNTH033", vtDetailOf(t, fresh).ids.asin)

	_, err := l.st.ModifyBook(l.ids["out"], func(b *database.Book) error { b.ASIN = vtPtr("B0SYNTH033"); return nil })
	require.NoError(t, err)
	err = l.fixer.Apply(context.Background(), l.writer(), fresh)
	require.Error(t, err)
	require.True(t, errors.Is(err, repairs.ErrChangedSincePlan), err.Error())
	require.Contains(t, err.Error(), l.ids["out"])
	p := l.get("p")
	require.Nil(t, p.MetadataReviewStatus)
	require.Empty(t, dcStr(p.ASIN))
}

// NIT (round 3): a candidate copy this op already journaled (only the
// checkpoint was lost) is recognised as journaled, whatever old value its
// journal row recorded: here the plan's recorded prior (Row.State) is made to
// differ from the old value the first run journaled, so a resume that
// journaled with the plan's prior would add a second row.
func TestVersionTwinFixer_Review3_CopyAlreadyJournaledByThisOp(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "gr3d", true, nil)
	tid := l.book("t", "gr3d", false, nil)
	l.putCache(tid, []metafetch.MetadataCandidate{vtSaga()}, metafetch.BatchSourceHash(tid, "Synthetic Saga", "Synthetic Author A"))
	plan, _ := l.plan()
	res := l.apply(plan, false, "gr3d")
	require.Equal(t, 1, res.Applied, "outcomes %v rows %+v", res.ByOutcome, res.Rows)
	changes, err := l.st.GetOperationChanges(vtTestOpID)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	require.Equal(t, undo.MetadataCacheAbsent, changes[0].OldValue)

	other, err := undo.EncodeMetadataCacheOld(&database.MetadataCandidateCache{BookID: l.ids["p"],
		FetchedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)})
	require.NoError(t, err)
	require.NotEqual(t, changes[0].OldValue, other)
	st, err := json.Marshal(vtPlanState{Prior: other})
	require.NoError(t, err)
	for i := range plan.Rows {
		if plan.Rows[i].RowID == "gr3d" {
			plan.Rows[i].State = st
		}
	}

	res = l.apply(plan, false, "gr3d")
	require.Equal(t, 1, res.Applied, "outcomes %v rows %+v", res.ByOutcome, res.Rows)
	changes, err = l.st.GetOperationChanges(vtTestOpID)
	require.NoError(t, err)
	require.Len(t, changes, 1, "the resume journaled a second row with the plan's prior")
}

// Item 2 (round 4): a re-plan that holds on an unbuilt ISBN/ASIN index is a
// transient hold: RunApply reports the row retry_later (not settled), not
// changed_since_plan, and writes nothing. Once the index is built the same
// plan applies.
func TestVersionTwinFixer_Review4_UnbuiltIndexAtReplanIsRetryLater(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "gr4a", true, nil)
	l.appliedTwin("t", "gr4a", vtSagaASIN("B0SYNTH041"), nil)
	plan, rows := l.plan()
	require.True(t, rows["gr4a"].Applicable(), rows["gr4a"].SkipReason)
	require.Equal(t, "B0SYNTH041", rows["gr4a"].Proposed["primary_asin"])

	require.NoError(t, l.st.SetSetting("book_isbn_index_v1_done", "false", "bool", false))
	res := l.apply(plan, false, "gr4a")
	require.Equal(t, 1, res.RetryLater, "outcomes %v rows %+v", res.ByOutcome, res.Rows)
	require.Zero(t, res.ChangedSincePlan)
	require.Equal(t, repairs.OutcomeRetryLater, res.Rows[0].Outcome)
	require.Equal(t, vtHoldIDElsewhere, res.Rows[0].Skipped)
	require.Contains(t, res.Rows[0].Error, "index is not built")
	require.Nil(t, l.get("p").MetadataReviewStatus)

	require.NoError(t, l.st.SetISBNIndexBuilt())
	res = l.apply(plan, false, "gr4a")
	require.Equal(t, 1, res.Applied, "outcomes %v rows %+v", res.ByOutcome, res.Rows)
	require.Equal(t, "B0SYNTH041", dcStr(l.get("p").ASIN))
}

// Item 3 (round 4): the pre-write identifier check with the index unbuilt
// refuses retry_later, and the primary is not written.
func TestVersionTwinFixer_Review4_PreWriteUnbuiltIndexIsRetryLater(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "gr4b", true, nil)
	l.appliedTwin("t", "gr4b", vtSagaASIN("B0SYNTH042"), nil)
	fresh := l.vtFresh("gr4b")
	require.Equal(t, "B0SYNTH042", vtDetailOf(t, fresh).ids.asin)

	require.NoError(t, l.st.SetSetting("book_isbn_index_v1_done", "false", "bool", false))
	err := l.fixer.Apply(context.Background(), l.writer(), fresh)
	require.Error(t, err)
	require.True(t, errors.Is(err, repairs.ErrRetryLater), err.Error())
	require.False(t, errors.Is(err, repairs.ErrChangedSincePlan), err.Error())
	p := l.get("p")
	require.Nil(t, p.MetadataReviewStatus)
	require.Empty(t, dcStr(p.ASIN))
	require.Empty(t, dcStr(p.Narrator))
}

// Item 4 (round 4): every pair of known runtimes is compared. Primary 3060 s,
// twin 3000 s, record 3100 s: primary-twin and primary-record agree within
// max(1%, 60 s), twin-record (100 s) does not, so there is no runtime
// evidence and no ASIN copy; with the primary carrying the record's narrator
// it is a conflict.
func TestVersionTwinFixer_Review4_TwinRecordRuntimeDisagreementIsNotEvidence(t *testing.T) {
	l := newVTLib(t)
	cand := vtSagaASIN("B0SYNTH043")
	cand.DurationSec = 3100
	l.book("p", "gr4c", true, func(b *database.Book) { b.Duration = vtPtr(3060) })
	l.appliedTwin("t", "gr4c", cand, func(b *database.Book) { b.Duration = vtPtr(3000) })

	cand2 := vtSagaASIN("B0SYNTH044")
	cand2.DurationSec = 3100
	l.book("p2", "gr4d", true, func(b *database.Book) {
		b.Duration = vtPtr(3060)
		b.Narrator = vtPtr(cand2.Narrator)
	})
	l.appliedTwin("t2", "gr4d", cand2, func(b *database.Book) { b.Duration = vtPtr(3000) })

	_, rows := l.plan()
	row := rows["gr4c"]
	require.True(t, row.Applicable(), "%s: %s", row.Skipped, row.SkipReason)
	require.Empty(t, row.Proposed["primary_asin"], "no evidence, no ASIN: %v", row.Evidence)
	require.Equal(t, vtHoldEvidenceConflict, rows["gr4d"].Skipped, rows["gr4d"].SkipReason)
}

// Round 5: a re-plan held on the unbuilt index whose twin record was ALSO
// refreshed since the plan (new record, so new candidate hash and identifiers)
// is changed_since_plan, not retry_later: the transient hold does not mask the
// change, and nothing is written.
func TestVersionTwinFixer_Review5_UnbuiltIndexDoesNotMaskRefreshedRecord(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "gr5a", true, nil)
	tid := l.appliedTwin("t", "gr5a", vtSagaASIN("B0SYNTH051"), nil)
	plan, rows := l.plan()
	require.True(t, rows["gr5a"].Applicable(), rows["gr5a"].SkipReason)

	fresh := vtSagaASIN("B0SYNTH052")
	_, err := l.st.ModifyBook(tid, func(b *database.Book) error {
		b.MetadataSourceHash = vtPtr(metafetch.CandidateSourceHash(fresh))
		b.ASIN = vtPtr(fresh.ASIN)
		return nil
	})
	require.NoError(t, err)
	l.putCache(tid, []metafetch.MetadataCandidate{fresh}, "")
	require.NoError(t, l.st.SetSetting("book_isbn_index_v1_done", "false", "bool", false))

	held, err := l.fixer.Replan(context.Background(), nil, rows["gr5a"], &fakeReporter{})
	require.NoError(t, err)
	require.True(t, held.RetryLater, "the re-plan holds on the index: %s", held.SkipReason)
	require.NotEqual(t, rows["gr5a"].Fingerprint, held.RetryFingerprint)

	res := l.apply(plan, false, "gr5a")
	require.Equal(t, 1, res.ChangedSincePlan, "outcomes %v rows %+v", res.ByOutcome, res.Rows)
	require.Zero(t, res.RetryLater)
	require.Equal(t, repairs.OutcomeChangedSincePlan, res.Rows[0].Outcome)
	require.Nil(t, l.get("p").MetadataReviewStatus)
	require.Empty(t, dcStr(l.get("p").ASIN))
}
