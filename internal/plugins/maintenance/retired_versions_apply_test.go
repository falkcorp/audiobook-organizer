// file: internal/plugins/maintenance/retired_versions_apply_test.go
// version: 1.2.0
// guid: 0c4f7b92-6d18-4a35-b8e1-3f95a27d4c60
// last-edited: 2026-10-24

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// Work that is done is skipped, not "changed since the plan": the book did
	// not change under the plan, the plan's work was finished.
	require.Equal(t, 3, again.ByOutcome[repairs.OutcomeNotApplicable], "%v %+v", again.ByOutcome, again.Rows)
	require.Zero(t, again.ByOutcome[repairs.OutcomeChangedSincePlan], "%v", again.ByOutcome)
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

// Two shells with a row at the same path P, where the holder has NO live row at
// P: after the first retires, the holder holds a RETIRED row at P. That row is a
// hidden version, not a live owner of the path, so the second shell's plan must
// not move (it would otherwise flip to would_retire_same_path and go
// changed_since_plan), and both rows apply in one run. A holder that already
// had a live row at P would not exercise this: the retired row would dedupe to
// the same book.
func TestRetiredApply_SiblingShellsAtOnePath(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	shared := rf.file("g1/shared.mp3")
	rf.live("primary", "g1", rf.file("g1/primary-own.mp3"))
	rf.trashed("sib-a", "g1", 30, shared)
	rf.trashed("sib-b", "g1", 30, shared)
	res := rf.planAll()
	ids := rf.applicableIDs(res)
	require.Len(t, ids, 2)
	for _, n := range []string{"sib-a", "sib-b"} {
		require.Equal(t, retCatWouldRetire, rf.retiredPlanRow(res, n).Class, "%s: the shells share a path only with each other", n)
	}

	out := rf.applyRows(rf.f, res, ids, false, "op-sib")
	require.Equal(t, 2, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
	require.Zero(t, out.ByOutcome[repairs.OutcomeChangedSincePlan], "%v", out.ByOutcome)
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

// retiredPlanRow is the row of a plan result for one named book.
func (rf *retiredFixture) retiredPlanRow(res *repairs.PlanResult, name string) repairs.Row {
	rf.t.Helper()
	for _, r := range res.Rows {
		if r.RowID == rf.ids[name] {
			return r
		}
	}
	rf.t.Fatalf("no row for %s", name)
	return repairs.Row{}
}

// A retire cut off after its commit point is finishable from a FRESH plan, not
// only from the plan the cut-off run started from: the book owns no live row
// and a retired version holding rows names it, so the fresh plan lists it
// applicable (retire_tail_pending) and Apply writes just the unfinished steps.
func TestRetiredApply_FreshPlanFinishesACutOffRetire(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.seedApplyLibrary()
	res := rf.planAll()
	var armed atomic.Bool
	armed.Store(true)
	rf.f.retireWriterFor = func(w *repairs.Writer) (retireWriter, error) {
		inner, err := rf.f.newJournaledRetireWriter(w)
		return failingExternalIDs{inner, &armed}, err
	}
	wr, holder := rf.ids["would-retire"], rf.ids["primary"]
	out := rf.applyRows(rf.f, res, []string{wr}, false, "op-cut")
	require.Equal(t, 1, out.Partial, "%v %+v", out.ByOutcome, out.Rows)
	armed.Store(false)

	fresh := rf.planAll()
	row := rf.retiredPlanRow(fresh, "would-retire")
	require.Equal(t, retCatTailPending, row.Class)
	require.True(t, row.Applicable(), "a cut-off retire must be finishable from a fresh plan: %+v", row)
	require.Contains(t, row.Reason, "external id")
	require.Contains(t, rf.applicableIDs(fresh), wr)

	out = rf.applyRows(rf.f, fresh, []string{wr}, false, "op-fresh")
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
	j := rf.journalByType("op-fresh")
	require.Zero(t, j[retChangeBookVersionCreate], "%v", j)
	require.Zero(t, j[retChangeBookFileRetire], "%v", j)
	require.Zero(t, j[undo.ChangeTypeRepairPlanRecord], "the tail needs no plan record: %v", j)
	require.Equal(t, 1, j[undo.ChangeTypeExternalIDReassign], "%v", j)
	require.Equal(t, 1, j[undo.ChangeTypeBookMergedInto], "%v", j)
	onHolder, err := rf.st.GetExternalIDsForBook(holder)
	require.NoError(t, err)
	require.Len(t, onHolder, 1)
	vs, err := rf.st.GetRetiredVersionsFromBook(wr)
	require.NoError(t, err)
	require.Len(t, vs, 1)

	// Done now: the next plan says so, and the plan that listed it applies nothing.
	require.Equal(t, retCatAlreadyRetired, rf.retiredPlanRow(rf.planAll(), "would-retire").Class)
	again := rf.applyRows(rf.f, fresh, []string{wr}, false, "op-fresh-again")
	require.Zero(t, again.Applied, "%v", again.ByOutcome)
	require.Equal(t, 1, again.ByOutcome[repairs.OutcomeNotApplicable], "%v", again.ByOutcome)
}

// A retired version that holds no rows (what a retire cut off before its commit
// point, or a purge, leaves) does not make its shell "retired": nothing moved
// off the shell, so its external ids must stay with it, not follow it onto a
// holder that never got the audio.
func TestRetiredApply_EmptyVersionIsNotAFinishedRetire(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.live("primary", "g1", rf.file("g1/primary.mp3"))
	rf.trashed("ghost", "g1", 30)
	rf.addExternalID("ghost", "PID-GHOST")
	retiredAt := rf.now.Add(-time.Hour).UTC().Truncate(time.Microsecond)
	_, err := rf.st.CreateBookVersion(&database.BookVersion{ID: "rv-empty-ghost", BookID: rf.ids["primary"],
		Status: database.BookVersionStatusRetired, Source: retiredVersionSource,
		RetiredFromBookID: rf.ids["ghost"], RetireOpID: "op-failed", RetiredAt: &retiredAt})
	require.NoError(t, err)

	res := rf.planAll()
	row := rf.retiredPlanRow(res, "ghost")
	require.NotEqual(t, retCatAlreadyRetired, row.Class, "%+v", row)
	require.NotEqual(t, retCatTailPending, row.Class, "%+v", row)
	require.False(t, row.Applicable(), "%+v", row)
	require.Empty(t, rf.applicableIDs(res))
	onGhost, err := rf.st.GetExternalIDsForBook(rf.ids["ghost"])
	require.NoError(t, err)
	require.Len(t, onGhost, 1, "the external id stays on the book the audio never left")
	onHolder, err := rf.st.GetExternalIDsForBook(rf.ids["primary"])
	require.NoError(t, err)
	require.Empty(t, onHolder)
}

// A retire of a shell that already has an empty retired version onto the same
// holder adopts it instead of leaving a second empty version, and journals no
// create (another op made it).
func TestRetiredApply_AdoptsAnEmptyVersionLeftByAFailedRetire(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.live("primary", "g1", rf.file("g1/primary.mp3"))
	rf.trashed("adopt", "g1", 30, rf.file("g1/adopt-1.mp3"))
	retiredAt := rf.now.Add(-time.Hour).UTC().Truncate(time.Microsecond)
	_, err := rf.st.CreateBookVersion(&database.BookVersion{ID: "rv-left-over", BookID: rf.ids["primary"],
		Status: database.BookVersionStatusRetired, Source: retiredVersionSource,
		RetiredFromBookID: rf.ids["adopt"], RetireOpID: "op-failed", RetiredAt: &retiredAt})
	require.NoError(t, err)

	res := rf.planAll()
	require.Equal(t, retCatWouldRetire, rf.retiredPlanRow(res, "adopt").Class)
	out := rf.applyRows(rf.f, res, []string{rf.ids["adopt"]}, false, "op-adopt")
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
	vs, err := rf.st.GetRetiredVersionsFromBook(rf.ids["adopt"])
	require.NoError(t, err)
	require.Len(t, vs, 1, "the empty version was adopted, not joined by a second")
	require.Equal(t, "rv-left-over", vs[0].ID)
	held, err := rf.st.GetRetiredBookFiles(rf.ids["primary"])
	require.NoError(t, err)
	require.Len(t, held, 1)
	require.Equal(t, "rv-left-over", held[0].VersionID)
	j := rf.journalByType("op-adopt")
	require.Zero(t, j[retChangeBookVersionCreate], "%v", j)
	require.Equal(t, 1, j[retChangeBookFileRetire], "%v", j)
}

// staleMergedIntoReader reports the shell with no merged-into link whatever the
// store holds: the window between SetMergedInto's read and its write.
type staleMergedIntoReader struct{ real retiredBookGetter }

func (s staleMergedIntoReader) GetBookByID(id string) (*database.Book, error) {
	b, err := s.real.GetBookByID(id)
	if b != nil {
		c := *b
		c.MergedIntoBookID = nil
		return &c, err
	}
	return b, err
}

// A merged-into write that finds the link already set (another writer won
// between the read and the write) writes nothing, so it is not reported as a
// write and leaves no standing journal row.
func TestRetiredApply_SetMergedIntoThatSkipsIsNotAWrite(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.live("primary", "g1", rf.file("g1/primary.mp3"))
	rf.trashed("shell", "g1", 30, rf.file("g1/shell.mp3"))
	rf.mergedInto("shell", "primary")
	opID := "op-merged-skip"
	w := repairs.NewWriter(rf.st, rf.st, rf.f.ID(), "bulk_update", "repairs-").WithJournal(rf.st, rf.st, opID).WithFieldStates(rf.st)
	rw, err := rf.f.newJournaledRetireWriter(w)
	require.NoError(t, err)
	jw := rw.(*journaledRetireWriter)
	jw.books = staleMergedIntoReader{real: jw.books}

	wrote, err := jw.SetMergedInto(rf.ids["shell"], rf.ids["primary"])
	require.NoError(t, err)
	require.False(t, wrote, "the skipped write must not be reported as made")
	changes, err := rf.st.GetOperationChanges(opID)
	require.NoError(t, err)
	for _, c := range changes {
		if c.ChangeType == undo.ChangeTypeBookMergedInto {
			require.True(t, c.Voided, "a skipped write leaves no standing journal row: %+v", c)
		}
	}
}

// runReads counts the whole-library reads one apply run makes.
type runReads struct{ series, users, journals atomic.Int32 }

type countingOpsStore struct {
	*database.PebbleStore
	c *runReads
}

func (o countingOpsStore) GetAllSeries() ([]database.Series, error) {
	o.c.series.Add(1)
	return o.PebbleStore.GetAllSeries()
}

type countingUserStore struct {
	merge.UserStateRepairStore
	c *runReads
}

func (u countingUserStore) ListUsers() ([]database.User, error) {
	u.c.users.Add(1)
	return u.UserStateRepairStore.ListUsers()
}

type countingJournals struct {
	CombineJournalScanner
	c *runReads
}

func (j countingJournals) ScanPrefix(prefix string) ([]database.KVPair, error) {
	j.c.journals.Add(1)
	return j.CombineJournalScanner.ScanPrefix(prefix)
}

type runReadsDeps struct {
	fakeDeps
	ops   countingOpsStore
	users countingUserStore
	jr    countingJournals
}

func (d runReadsDeps) OpsStore() OpsStore                              { return d.ops }
func (d runReadsDeps) MergeUserStateStore() merge.UserStateRepairStore { return d.users }
func (d runReadsDeps) CombineJournals() CombineJournalScanner          { return d.jr }

// An apply run reads every series, every user and every combine journal ONCE,
// before its first row, not once per row's re-plan and again per row under the
// global merge lock.
func TestRetiredApply_ReadsTheLibraryWideStateOncePerRun(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.live("primary", "g1", rf.file("g1/primary.mp3"))
	for i := range 4 {
		rf.trashed(fmt.Sprintf("shell-%d", i), "g1", 30, rf.file(fmt.Sprintf("g1/%d.mp3", i)))
	}
	res := rf.planAll()
	ids := rf.applicableIDs(res)
	require.Len(t, ids, 4)

	c := &runReads{}
	base := fakeDeps{store: rf.st}
	deps := runReadsDeps{fakeDeps: base,
		ops:   countingOpsStore{PebbleStore: rf.st, c: c},
		users: countingUserStore{UserStateRepairStore: base.MergeUserStateStore(), c: c},
		jr:    countingJournals{CombineJournalScanner: base.CombineJournals(), c: c}}
	f := newRetiredVersionsFixer(&Plugin{deps: deps})
	f.now = rf.f.now
	f.applyEnabled = func() bool { return true }

	out := rf.applyRows(f, res, ids, false, "op-once")
	require.Equal(t, 4, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
	require.EqualValues(t, 1, c.series.Load(), "series are listed once per run, not per row")
	require.EqualValues(t, 1, c.users.Load(), "users are listed once per run, not per row")
	require.EqualValues(t, 1, c.journals.Load(), "the combine journals are scanned once per run, not per row")
}

// flagSequence answers the apply flag from a script, one read at a time, then
// repeats the last answer.
func flagSequence(answers ...bool) func() bool {
	var n atomic.Int32
	return func() bool {
		i := int(n.Add(1)) - 1
		if i >= len(answers) {
			i = len(answers) - 1
		}
		return answers[i]
	}
}

// The flag is read by the engine (PlanOnly, then NoScanStandDown) before the
// run starts and by the fixer once when it begins. A flip between those reads
// is not a failed row: nothing was written and the same plan applies again.
func TestRetiredApply_FlagFlippingAsTheRunStartsIsRetryLaterNotFailed(t *testing.T) {
	cases := []struct {
		name    string
		answers []bool
	}{
		// The engine saw it on; it is off when the run begins.
		{"off_at_begin", []bool{true, true, false}},
		// The engine saw it off for the stand-down (so its Writer refuses every
		// write) and on for everything else.
		{"off_for_the_stand_down_decision", []bool{true, false, true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rf := newRetiredFixture(t)
			rf.enableApply()
			rf.live("primary", "g1", rf.file("g1/primary.mp3"))
			rf.trashed("shell", "g1", 30, rf.file("g1/shell.mp3"))
			res := rf.planAll()
			ids := rf.applicableIDs(res)
			require.Len(t, ids, 1)

			rf.f.applyEnabled = flagSequence(tc.answers...)
			out := rf.applyRows(rf.f, res, ids, false, "op-flip")
			require.Zero(t, out.Failed, "%v %+v", out.ByOutcome, out.Rows)
			require.Equal(t, 1, out.RetryLater, "%v %+v", out.ByOutcome, out.Rows)
			files, err := rf.st.GetBookFiles(rf.ids["shell"])
			require.NoError(t, err)
			require.NotEmpty(t, files, "nothing was written")

			rf.enableApply()
			out = rf.applyRows(rf.f, res, ids, false, "op-flip-again")
			require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
		})
	}
}
