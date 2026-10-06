// file: internal/plugins/maintenance/version_twin_metadata_review2_test.go
// version: 1.0.0
// guid: d63428b5-a8b1-4678-86f0-28e71e7b90a9
// last-edited: 2026-10-06

package maintenance

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

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
// not serving, it refuses changed_since_plan (fail closed) and the scan is
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
	require.True(t, errors.Is(err, repairs.ErrChangedSincePlan), err.Error())
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
// changed_since_plan, the primary is not written, and the refusal comes
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
	require.Equal(t, 1, res.ChangedSincePlan, "outcomes %v rows %+v", res.ByOutcome, res.Rows)
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
		cand.Narrator+"\" (runtimes not comparable)")

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
