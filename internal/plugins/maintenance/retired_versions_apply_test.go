// file: internal/plugins/maintenance/retired_versions_apply_test.go
// version: 1.0.0
// guid: 0c4f7b92-6d18-4a35-b8e1-3f95a27d4c60
// last-edited: 2026-10-23

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// Every book, title and path here is invented: the repo is public.

// enableApply turns retire_versions_apply_enabled on for the fixture's fixer.
func (rf *retiredFixture) enableApply() {
	rf.f.applyEnabled = func() bool { return true }
}

// planAll runs the engine's plan (guards included) and returns the result.
func (rf *retiredFixture) planAll() *repairs.PlanResult {
	rf.t.Helper()
	res, err := repairs.RunPlan(context.Background(), rf.f, nil, repairs.PlanDeps{Guard: rf.st}, &fakeReporter{})
	require.NoError(rf.t, err)
	return res
}

// applyRows runs the engine's apply of ids over res under opID.
func (rf *retiredFixture) applyRows(f repairs.Fixer, res *repairs.PlanResult, ids []string, dry bool, opID string) *repairs.ApplyResult {
	rf.t.Helper()
	w := repairs.NewWriter(rf.st, rf.st, f.ID(), "bulk_update", "repairs-").WithJournal(rf.st, rf.st, opID).WithFieldStates(rf.st)
	out, err := repairs.RunApply(context.Background(), f, res, "plan-1", ids, dry,
		repairs.ApplyDeps{Guard: rf.st, Writer: w, OpID: opID, StandDown: &countingStandDown{}}, &fakeReporter{})
	require.NoError(rf.t, err)
	return out
}

func retiredOutcomeOf(out *repairs.ApplyResult, id string) repairs.RowResult {
	for _, r := range out.Rows {
		if r.RowID == id {
			return r
		}
	}
	return repairs.RowResult{}
}

func (rf *retiredFixture) journalByType(opID string) map[string]int {
	rf.t.Helper()
	changes, err := rf.st.GetOperationChanges(opID)
	require.NoError(rf.t, err)
	out := map[string]int{}
	for _, c := range changes {
		out[c.ChangeType]++
	}
	return out
}

func (rf *retiredFixture) addExternalID(shell, id string) {
	rf.t.Helper()
	require.NoError(rf.t, rf.st.CreateExternalIDMapping(&database.ExternalIDMapping{
		Source: "itunes", ExternalID: id, BookID: rf.ids[shell], Provenance: "itunes"}))
}

// seedApplyLibrary builds the fixture the apply tests share: one row per
// category (would_retire with an external id, same_path, kept = too recent,
// unresolved = no group) and a would_retire row that is changed after the plan
// is taken. It returns the planned rows' ids.
func (rf *retiredFixture) seedApplyLibrary() {
	shared := rf.file("g1/shared.mp3")
	rf.live("primary", "g1", shared)
	rf.trashed("would-retire", "g1", 30, rf.file("g1/own-1.mp3"), rf.file("g1/own-2.mp3"))
	rf.addExternalID("would-retire", "PID-WR-1")
	rf.trashed("same-path", "g1", 30, shared)
	rf.trashed("kept", "g1", 3, rf.file("g1/recent.mp3"))
	rf.trashed("unresolved", "", 30, rf.file("loose/own.mp3"))
	rf.trashed("late", "g1", 30, rf.file("g1/late-1.mp3"))
}

func (rf *retiredFixture) applicableIDs(res *repairs.PlanResult) []string {
	var ids []string
	for _, r := range res.Rows {
		if r.Applicable() {
			ids = append(ids, r.RowID)
		}
	}
	return ids
}

// With the flag off the fixer is plan-only and Apply refuses, naming the flag.
func TestRetiredApply_FlagOffRefuses(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.seedApplyLibrary()
	before := snapshotLibrary(t, rf.st)
	res := rf.planAll()

	require.True(t, repairs.IsPlanOnly(rf.f))
	require.Contains(t, rf.f.Description(), "retire_versions_apply_enabled")
	w := repairs.NewWriter(rf.st, rf.st, rf.f.ID(), "bulk_update", "repairs-").WithJournal(rf.st, rf.st, "op-off")
	_, err := repairs.RunApply(context.Background(), rf.f, res, "plan-1", rf.applicableIDs(res), false,
		repairs.ApplyDeps{Guard: rf.st, Writer: w, OpID: "op-off", StandDown: &countingStandDown{}}, &fakeReporter{})
	require.ErrorIs(t, err, repairs.ErrPlanOnly)

	// Past the engine's refusal the fixer refuses itself.
	err = rf.f.Apply(context.Background(), w, res.Rows[0])
	require.ErrorIs(t, err, errRetiredApplyDisabled)
	require.ErrorContains(t, err, "retire_versions_apply_enabled")
	require.Equal(t, before, snapshotLibrary(t, rf.st))
}

// Turning the flag on makes the fixer applicable and ends its opt-out of the
// scan stand-down (it writes book_file rows now).
func TestRetiredApply_FlagOnOffersApply(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	require.False(t, repairs.IsPlanOnly(rf.f))
	require.False(t, repairs.SkipsScanStandDown(rf.f))
	require.NotContains(t, rf.f.Description(), "no Apply control")
}

// A dry run reports would_apply for every applicable row and writes nothing.
func TestRetiredApply_DryRunWritesNothing(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.seedApplyLibrary()
	res := rf.planAll()
	ids := rf.applicableIDs(res)
	require.Len(t, ids, 3, "would-retire, same-path and late")
	before := snapshotLibrary(t, rf.st)

	out := rf.applyRows(rf.f, res, ids, true, "op-dry")
	require.True(t, out.DryRun)
	require.Equal(t, 3, out.ByOutcome[repairs.OutcomeWouldApply], "%v", out.ByOutcome)
	require.Zero(t, out.Applied)
	require.Zero(t, out.BookWrites)
	require.Zero(t, out.JournalRows)
	require.Empty(t, rf.journalByType("op-dry"))
	require.Equal(t, before, snapshotLibrary(t, rf.st))
	vs, err := rf.st.GetRetiredVersionsFromBook(rf.ids["would-retire"])
	require.NoError(t, err)
	require.Empty(t, vs)
}

// A live apply: one row per category, plus a row that changed after the plan.
func TestRetiredApply_LiveApplyEveryCategory(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.seedApplyLibrary()
	res := rf.planAll()
	ids := append(rf.applicableIDs(res), rf.ids["kept"], rf.ids["unresolved"])
	holderBefore, err := rf.st.GetBookFiles(rf.ids["primary"])
	require.NoError(t, err)
	require.Len(t, holderBefore, 1)

	// The late row changes after the plan was taken.
	require.NoError(t, rf.st.CreateBookFile(&database.BookFile{BookID: rf.ids["late"], FilePath: rf.file("g1/late-2.mp3"), Format: "mp3"}))

	const opID = "op-live"
	out := rf.applyRows(rf.f, res, ids, false, opID)
	require.Equal(t, 2, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
	require.Equal(t, 1, out.ChangedSincePlan, "%v", out.ByOutcome)
	require.Equal(t, 2, out.ByOutcome[repairs.OutcomeNotApplicable], "kept and unresolved: %v", out.ByOutcome)
	require.Zero(t, out.Failed)
	require.Equal(t, repairs.OutcomeChangedSincePlan, retiredOutcomeOf(out, rf.ids["late"]).Outcome)
	require.Equal(t, repairs.OutcomeNotApplicable, retiredOutcomeOf(out, rf.ids["kept"]).Outcome)
	require.Equal(t, repairs.OutcomeNotApplicable, retiredOutcomeOf(out, rf.ids["unresolved"]).Outcome)

	holder := rf.ids["primary"]
	wr := rf.ids["would-retire"]
	// The shell owns no live row; its rows are retired under the holder.
	live, err := rf.st.GetBookFiles(wr)
	require.NoError(t, err)
	require.Empty(t, live)
	retired, err := rf.st.GetRetiredBookFiles(holder)
	require.NoError(t, err)
	require.Len(t, retired, 3, "two rows of would-retire and one of same-path")
	for _, r := range retired {
		require.True(t, r.Retired)
		require.NotEmpty(t, r.VersionID)
	}
	// The holder's own live rows are untouched.
	holderAfter, err := rf.st.GetBookFiles(holder)
	require.NoError(t, err)
	require.Equal(t, holderBefore, holderAfter)

	// The retired version row names the shell and carries provenance.
	vs, err := rf.st.GetRetiredVersionsFromBook(wr)
	require.NoError(t, err)
	require.Len(t, vs, 1)
	require.Equal(t, database.BookVersionStatusRetired, vs[0].Status)
	require.Equal(t, holder, vs[0].BookID)
	require.Equal(t, opID, vs[0].RetireOpID)
	require.Equal(t, "/lib/would-retire", vs[0].SourceOriginalPath)
	require.Contains(t, vs[0].MetadataJSON, `"shell_title":"would-retire"`)
	require.Equal(t, retiredVersionID(opID, wr), vs[0].ID)

	// The external id moved to the holder; the shell points at the holder.
	onShell, err := rf.st.GetExternalIDsForBook(wr)
	require.NoError(t, err)
	for _, e := range onShell {
		require.True(t, e.Tombstoned, "a live mapping stayed on the shell: %+v", e)
	}
	onHolder, err := rf.st.GetExternalIDsForBook(holder)
	require.NoError(t, err)
	require.Len(t, onHolder, 1)
	require.Equal(t, "PID-WR-1", onHolder[0].ExternalID)
	b, err := rf.st.GetBookByID(wr)
	require.NoError(t, err)
	require.NotNil(t, b.MergedIntoBookID)
	require.Equal(t, holder, *b.MergedIntoBookID)

	// The same-path shell retired its row too and kept the holder's.
	sp, err := rf.st.GetBookFiles(rf.ids["same-path"])
	require.NoError(t, err)
	require.Empty(t, sp)

	// The skipped and changed rows are untouched.
	for _, n := range []string{"kept", "unresolved", "late"} {
		files, err := rf.st.GetBookFiles(rf.ids[n])
		require.NoError(t, err)
		require.NotEmpty(t, files, n)
	}

	// Every write was journaled first.
	j := rf.journalByType(opID)
	require.Equal(t, 2, j[undo.ChangeTypeRepairPlanRecord], "%v", j)
	require.Equal(t, 2, j[retChangeBookVersionCreate], "%v", j)
	require.Equal(t, 3, j[retChangeBookFileRetire], "%v", j)
	require.Equal(t, 1, j[undo.ChangeTypeExternalIDReassign], "%v", j)
	require.Equal(t, 2, j[undo.ChangeTypeBookMergedInto], "%v", j)
}

// A second apply of the same plan applies nothing: the rows are retired.
func TestRetiredApply_SecondRunAppliesNothing(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.seedApplyLibrary()
	res := rf.planAll()
	ids := rf.applicableIDs(res)
	first := rf.applyRows(rf.f, res, ids, false, "op-first")
	require.Equal(t, 3, first.Applied, "%v", first.ByOutcome)
	after := snapshotLibrary(t, rf.st)

	again := rf.applyRows(rf.f, res, ids, false, "op-again")
	require.Zero(t, again.Applied, "%v", again.ByOutcome)
	require.Zero(t, again.BookWrites)
	require.Zero(t, again.JournalRows)
	require.Empty(t, rf.journalByType("op-again"))
	require.Equal(t, after, snapshotLibrary(t, rf.st))

	// A fresh plan reports the retired books as already_retired and skips them.
	rows := rf.plan()
	for _, n := range []string{"would-retire", "same-path", "late"} {
		require.Equal(t, retCatAlreadyRetired, rows[rf.ids[n]].Class, n)
		require.False(t, rows[rf.ids[n]].Applicable(), n)
	}
}

// failingExternalIDs fails the external-id step while armed, to cut a retire
// off after its commit point.
type failingExternalIDs struct {
	retireWriter
	armed *atomic.Bool
}

func (f failingExternalIDs) ReassignExternalID(shellID, holderID string, m database.ExternalIDMapping) error {
	if f.armed.Load() {
		return errors.New("injected: store went away")
	}
	return f.retireWriter.ReassignExternalID(shellID, holderID, m)
}

// A retire cut off after the commit point is a partial apply, and the next
// apply of the same plan finishes the unfinished steps.
func TestRetiredApply_ResumeFinishesTheTail(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.seedApplyLibrary()
	res := rf.planAll()
	ids := []string{rf.ids["would-retire"]}
	var armed atomic.Bool
	armed.Store(true)
	rf.f.retireWriterFor = func(w *repairs.Writer) (retireWriter, error) {
		inner, err := rf.f.newJournaledRetireWriter(w)
		return failingExternalIDs{inner, &armed}, err
	}
	wr, holder := rf.ids["would-retire"], rf.ids["primary"]

	out := rf.applyRows(rf.f, res, ids, false, "op-cut")
	require.Equal(t, 1, out.Partial, "%v %+v", out.ByOutcome, out.Rows)
	require.Zero(t, out.Applied)
	live, err := rf.st.GetBookFiles(wr)
	require.NoError(t, err)
	require.Empty(t, live, "the rows were retired before the cut")
	b, err := rf.st.GetBookByID(wr)
	require.NoError(t, err)
	require.Nil(t, b.MergedIntoBookID, "the tail did not finish")

	armed.Store(false)
	out = rf.applyRows(rf.f, res, ids, false, "op-resume")
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
	j := rf.journalByType("op-resume")
	require.Zero(t, j[retChangeBookVersionCreate], "a resume mints no second version: %v", j)
	require.Zero(t, j[retChangeBookFileRetire], "a resume retires no row again: %v", j)
	require.Equal(t, 1, j[undo.ChangeTypeExternalIDReassign], "%v", j)
	b, err = rf.st.GetBookByID(wr)
	require.NoError(t, err)
	require.NotNil(t, b.MergedIntoBookID)
	require.Equal(t, holder, *b.MergedIntoBookID)
	onHolder, err := rf.st.GetExternalIDsForBook(holder)
	require.NoError(t, err)
	require.Len(t, onHolder, 1)
	vs, err := rf.st.GetRetiredVersionsFromBook(wr)
	require.NoError(t, err)
	require.Len(t, vs, 1, "one version, never two")

	out = rf.applyRows(rf.f, res, ids, false, "op-done")
	require.Zero(t, out.Applied, "%v", out.ByOutcome)
}

// Two shells with a row at the same path as the holder's: after the first
// retires, the second's plan must still hold (a retired row is not a live
// owner of the path), so both apply in one run.
func TestRetiredApply_SiblingShellsAtOnePath(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	shared := rf.file("g1/shared.mp3")
	rf.live("primary", "g1", shared)
	rf.trashed("sib-a", "g1", 30, shared)
	rf.trashed("sib-b", "g1", 30, shared)
	res := rf.planAll()
	ids := rf.applicableIDs(res)
	require.Len(t, ids, 2)

	out := rf.applyRows(rf.f, res, ids, false, "op-sib")
	require.Equal(t, 2, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
	retired, err := rf.st.GetRetiredBookFiles(rf.ids["primary"])
	require.NoError(t, err)
	require.Len(t, retired, 2)
	live, err := rf.st.GetBookFiles(rf.ids["primary"])
	require.NoError(t, err)
	require.Len(t, live, 1)
}

// A shell an unreversed combine names as absorbed is held (retry_later),
// nothing written, until the combine-undo link exists.
func TestRetiredApply_CombineUndoAwareRowIsHeld(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	rf.trashed("absorbed", "g", 30, rf.file("g/a.mp3"))
	rf.trashed("plain", "g", 30, rf.file("g/b.mp3"))
	require.NoError(t, merge.WriteCombineJournal(rf.st, &merge.CombineJournal{ID: "J-held", Status: merge.CombineJournalApplied,
		SurvivorID: rf.ids["primary"], Absorbed: []merge.CombineAbsorbed{{BookID: rf.ids["absorbed"]}}}))
	res := rf.planAll()
	ids := rf.applicableIDs(res)
	require.Len(t, ids, 2)

	out := rf.applyRows(rf.f, res, ids, false, "op-held")
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
	require.Equal(t, 1, out.RetryLater, "%v", out.ByOutcome)
	require.Equal(t, repairs.OutcomeRetryLater, retiredOutcomeOf(out, rf.ids["absorbed"]).Outcome)
	files, err := rf.st.GetBookFiles(rf.ids["absorbed"])
	require.NoError(t, err)
	require.NotEmpty(t, files, "the held shell keeps its rows")
}

// A shell that itself holds a retired version (the chain case) hands that
// version to the new holder, so the shell is left owning nothing.
func TestRetiredApply_ChainRehomesTheShellsRetiredVersions(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	rf.trashed("middle", "g", 30, rf.file("g/middle.mp3"))
	rf.trashed("inner", "g", 30, rf.file("g/inner.mp3"))
	// inner was retired into middle while middle was still live (the store
	// refuses a soft-deleted holder), and middle was trashed afterwards.
	mb, err := rf.st.GetBookByID(rf.ids["middle"])
	require.NoError(t, err)
	trashedAt, trashedFlag := mb.MarkedForDeletionAt, mb.MarkedForDeletion
	mb.MarkedForDeletion, mb.MarkedForDeletionAt = nil, nil
	_, err = rf.st.UpdateBook(mb.ID, mb)
	require.NoError(t, err)
	inner, err := rf.st.GetBookFiles(rf.ids["inner"])
	require.NoError(t, err)
	require.Len(t, inner, 1)
	retiredAt := rf.now.Add(-time.Hour).UTC().Truncate(time.Microsecond)
	_, err = rf.st.CreateBookVersion(&database.BookVersion{ID: "rv-chain-inner", BookID: rf.ids["middle"],
		Status: database.BookVersionStatusRetired, Source: retiredVersionSource,
		RetiredFromBookID: rf.ids["inner"], RetireOpID: "op-earlier", RetiredAt: &retiredAt})
	require.NoError(t, err)
	rw, ok := database.AsCapability[database.RetiredBookFileWriter](rf.st)
	require.True(t, ok)
	require.NoError(t, rw.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{inner[0].ID}, SourceBookID: rf.ids["inner"]}},
		rf.ids["middle"], "rv-chain-inner"))
	mb, err = rf.st.GetBookByID(rf.ids["middle"])
	require.NoError(t, err)
	mb.MarkedForDeletion, mb.MarkedForDeletionAt = trashedFlag, trashedAt
	_, err = rf.st.UpdateBook(mb.ID, mb)
	require.NoError(t, err)
	held, err := rf.st.GetRetiredBookFiles(rf.ids["middle"])
	require.NoError(t, err)
	require.Len(t, held, 1)

	res := rf.planAll()
	out := rf.applyRows(rf.f, res, []string{rf.ids["middle"]}, false, "op-chain-2")
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
	held, err = rf.st.GetRetiredBookFiles(rf.ids["middle"])
	require.NoError(t, err)
	require.Empty(t, held, "the middle shell holds nothing any more")
	atHolder, err := rf.st.GetRetiredBookFiles(rf.ids["primary"])
	require.NoError(t, err)
	require.Len(t, atHolder, 2, "inner's version and middle's own")
	require.Equal(t, 1, rf.journalByType("op-chain-2")[retChangeVersionRehome])
}

// holderProbe measures how many rows of one holder are in flight at once
// (Replan through Apply), the proof that the partition keeps one holder's rows
// from running together.
type holderProbe struct {
	repairs.Fixer
	mu       sync.Mutex
	inFlight map[string]int
	peak     int
}

func (p *holderProbe) enter(holder string) {
	p.mu.Lock()
	p.inFlight[holder]++
	if p.inFlight[holder] > p.peak {
		p.peak = p.inFlight[holder]
	}
	p.mu.Unlock()
	time.Sleep(40 * time.Millisecond)
}

func (p *holderProbe) leave(holder string) {
	p.mu.Lock()
	p.inFlight[holder]--
	p.mu.Unlock()
}

func (p *holderProbe) Replan(ctx context.Context, params json.RawMessage, planned repairs.Row, rep registry.Reporter) (repairs.Row, error) {
	p.enter(planned.Current["target_book_id"])
	return p.Fixer.Replan(ctx, params, planned, rep)
}

func (p *holderProbe) Apply(ctx context.Context, w *repairs.Writer, fresh repairs.Row) error {
	defer p.leave(fresh.Current["target_book_id"])
	return p.Fixer.Apply(ctx, w, fresh)
}

// Rows retiring into one holder run in order in one partition, so no two
// workers ever touch the same holder; the control (the holder stripped from
// the rows' BookIDs, as if a plan forgot to name it) shows the probe can fail.
func TestRetiredApply_OneHolderIsOnePartition(t *testing.T) {
	run := func(stripHolder bool) (peak int, applied int) {
		rf := newRetiredFixture(t)
		rf.enableApply()
		rf.live("primary", "g", rf.file("g/primary.mp3"))
		for i := 0; i < 6; i++ {
			rf.trashed("shell-"+string(rune('a'+i)), "g", 30, rf.file("g/"+string(rune('a'+i))+".mp3"))
		}
		res := rf.planAll()
		ids := rf.applicableIDs(res)
		require.Len(t, ids, 6)
		if stripHolder {
			for i := range res.Rows {
				res.Rows[i].BookIDs = []string{res.Rows[i].RowID}
			}
		}
		probe := &holderProbe{Fixer: rf.f, inFlight: map[string]int{}}
		out := rf.applyRows(probe, res, ids, false, "op-part")
		return probe.peak, out.Applied
	}
	peak, applied := run(false)
	require.Equal(t, 6, applied)
	require.Equal(t, 1, peak, "rows of one holder must never be in flight together")

	peak, _ = run(true)
	require.Greater(t, peak, 1, "control: without the shared holder the rows do overlap")
}
