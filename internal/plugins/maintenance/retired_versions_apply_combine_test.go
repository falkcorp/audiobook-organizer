// file: internal/plugins/maintenance/retired_versions_apply_combine_test.go
// version: 1.0.0
// guid: ecf4b780-bffc-475e-b1db-93a3bebb717b
// last-edited: 2026-10-30

package maintenance

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// combineJournal writes an unreversed combine journal that names the fixture
// books as absorbed into survivor.
func (rf *retiredFixture) combineJournal(id, survivor string, absorbed ...string) {
	rf.t.Helper()
	j := &merge.CombineJournal{ID: id, Status: merge.CombineJournalApplied, SurvivorID: rf.ids[survivor]}
	for _, n := range absorbed {
		j.Absorbed = append(j.Absorbed, merge.CombineAbsorbed{BookID: rf.ids[n]})
	}
	require.NoError(rf.t, merge.WriteCombineJournal(rf.st, j))
}

// countingBookFileStore counts the data writes that reach the store a Writer
// was wired to. It embeds the real store, so database.AsCapability finds these
// overrides by a direct assertion and a retire cannot reach the store any other
// way.
type countingBookFileStore struct {
	*database.PebbleStore
	retireBatches, versionsCreated, rehomes atomic.Int32
}

func (c *countingBookFileStore) RetireBookFiles(moves []database.BookFileMove, holderID, versionID string) error {
	c.retireBatches.Add(1)
	return c.PebbleStore.RetireBookFiles(moves, holderID, versionID)
}

func (c *countingBookFileStore) CreateBookVersion(v *database.BookVersion) (*database.BookVersion, error) {
	c.versionsCreated.Add(1)
	return c.PebbleStore.CreateBookVersion(v)
}

func (c *countingBookFileStore) RehomeRetiredVersion(versionID, newHolderID string) error {
	c.rehomes.Add(1)
	return c.PebbleStore.RehomeRetiredVersion(versionID, newHolderID)
}

// An Apply writes the retired version, its rows and its journal through
// repairs.Writer (RetireVersion), asserted on the store the Writer itself was
// given: the version create and the one retire batch reach it, and the journal
// rows are the undo package's, with the provenance the fixer builds.
func TestRetiredApply_WritesThroughTheRepairsWriter(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.live("Year One", "g1", rf.file("g1/year-one.mp3"))
	rf.trashed("The Long Halloween", "g1", 30, rf.file("g1/halloween-1.mp3"), rf.file("g1/halloween-2.mp3"))
	res := rf.planAll()
	shell, holder := rf.ids["The Long Halloween"], rf.ids["Year One"]

	const opID = "op-writer"
	counting := &countingBookFileStore{PebbleStore: rf.st}
	w := repairs.NewWriter(rf.st, rf.st, rf.f.ID(), "bulk_update", "repairs-").WithJournal(counting, rf.st, opID).WithFieldStates(rf.st)
	out, err := repairs.RunApply(t.Context(), rf.f, res, "plan-1", []string{shell}, false,
		repairs.ApplyDeps{Guard: rf.st, Writer: w, OpID: opID, StandDown: &countingStandDown{}}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)

	require.EqualValues(t, 1, counting.versionsCreated.Load(), "the version was created through the Writer's store")
	require.EqualValues(t, 1, counting.retireBatches.Load(), "both rows went in one batch through the Writer's store")
	require.Zero(t, counting.rehomes.Load())
	j := rf.journalByType(opID)
	require.Equal(t, 1, j[undo.ChangeTypeBookVersionCreate], "%v", j)
	require.Equal(t, 2, j[undo.ChangeTypeBookFileRetire], "%v", j)

	vs, err := rf.st.GetRetiredVersionsFromBook(shell)
	require.NoError(t, err)
	require.Len(t, vs, 1)
	require.Equal(t, retiredVersionSource, vs[0].Source)
	require.Equal(t, "mp3", vs[0].Format)
	require.Equal(t, "/lib/The Long Halloween", vs[0].SourceOriginalPath, "provenance survives the move to the Writer")
	require.Contains(t, vs[0].MetadataJSON, `"shell_title":"The Long Halloween"`)
	require.Equal(t, rf.now.UTC().Truncate(1000), vs[0].RetiredAt.UTC(), "the run's clock stamps the version once")
	held, err := rf.st.GetRetiredBookFiles(holder)
	require.NoError(t, err)
	require.Len(t, held, 2)
	for _, r := range held {
		require.Equal(t, vs[0].ID, r.VersionID)
	}
}

// A combine_undo_aware row is no longer held: Apply retires it with the id of
// the combine journal that names it, the Writer writes the combine-retire link,
// and every retire row carries the id so the combine's undo finds it.
func TestRetiredApply_CombineUndoAwareRowIsRetiredLinkedToItsCombine(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.live("Year One", "g1", rf.file("g1/year-one.mp3"))
	rf.live("The Dark Knight Returns", "")
	rf.trashed("Hush", "g1", 30, rf.file("g1/hush-1.mp3"), rf.file("g1/hush-2.mp3"))
	rf.combineJournal("J-Penguin", "The Dark Knight Returns", "Hush")
	res := rf.planAll()
	shell := rf.ids["Hush"]
	row := rf.retiredPlanRow(res, "Hush")
	require.Equal(t, []string{retFlagCombineUndoAware}, row.Flags)
	require.True(t, row.Applicable(), row.Reason)

	// A re-plan is no longer a hold.
	replanned, err := rf.f.Replan(t.Context(), nil, row, &fakeReporter{})
	require.NoError(t, err)
	require.True(t, replanned.Applicable(), "%s: %s", replanned.Class, replanned.SkipReason)
	require.False(t, replanned.RetryLater)

	const opID = "op-linked"
	out := rf.applyRows(rf.f, res, []string{shell}, false, opID)
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
	require.Zero(t, out.ByOutcome[repairs.OutcomeRetryLater])

	links, err := merge.ListCombineRetireLinks(rf.st, "J-Penguin")
	require.NoError(t, err)
	require.Equal(t, []merge.CombineRetireLink{{ShellID: shell, OpID: opID}}, links)
	changes, err := rf.st.GetOperationChanges(opID)
	require.NoError(t, err)
	retires := 0
	for _, c := range changes {
		if c.ChangeType != undo.ChangeTypeBookFileRetire {
			continue
		}
		retires++
		r, err := undo.DecodeBookFileRetire(c)
		require.NoError(t, err)
		require.Equal(t, "J-Penguin", r.CombineJournalID, "the combine rides inside the row")
	}
	require.Equal(t, 2, retires)
}

// A shell named by two unreversed combine journals cannot be linked to both, so
// it stays held (retry_later) at Replan and at Apply, with nothing written.
func TestRetiredApply_ShellNamedByTwoCombinesIsHeld(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.live("Year One", "g1", rf.file("g1/year-one.mp3"))
	rf.live("The Dark Knight Returns", "")
	rf.trashed("Hush", "g1", 30, rf.file("g1/hush-1.mp3"))
	rf.combineJournal("J-Joker", "The Dark Knight Returns", "Hush")
	rf.combineJournal("J-Riddler", "The Dark Knight Returns", "Hush")
	res := rf.planAll()
	shell := rf.ids["Hush"]
	require.True(t, rf.retiredPlanRow(res, "Hush").Applicable())

	out := rf.applyRows(rf.f, res, []string{shell}, false, "op-two")
	require.Equal(t, 1, out.ByOutcome[repairs.OutcomeRetryLater], "%v %+v", out.ByOutcome, out.Rows)
	require.Zero(t, out.Applied)
	r := retiredOutcomeOf(out, shell)
	require.Equal(t, retCatCombineMultiJournal, r.Skipped, "%+v", r)
	require.Empty(t, rf.journalByType("op-two"), "nothing was journaled for a held row")
	live, err := rf.st.GetBookFiles(shell)
	require.NoError(t, err)
	require.Len(t, live, 1, "the shell keeps its row")

	// Undoing one of the combines releases the hold.
	j, err := merge.NewService(rf.st).GetCombineJournal("J-Riddler")
	require.NoError(t, err)
	j.Status = merge.CombineJournalUndone
	require.NoError(t, merge.WriteCombineJournal(rf.st, j))
	fresh := rf.planAll()
	out = rf.applyRows(rf.f, fresh, []string{shell}, false, "op-one")
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
	links, err := merge.ListCombineRetireLinks(rf.st, "J-Joker")
	require.NoError(t, err)
	require.Len(t, links, 1)
}

// The whole path with a real combine, as far as UndoCombine goes today: a combine
// absorbs a shell, the shell is given a row afterwards, and the fixer retires it
// linked to the combine. UndoCombine finds that retire through the link (it
// names the row as "taken by a retire since, still held by it") and refuses the
// undo, leaving the retire, the shell and the journal exactly as they were: a
// combine undo never restores a shell around rows it cannot account for. The
// owner then reverts the retire op itself and the row is back on the shell.
//
// UndoCombine does not yet reverse a retire that took a row the shell gained
// after the combine (merge/combine_journal.go counts every such row as a file
// the book "has since been given"); the reversal of a linked retire that took
// none is TestRevertCombine_EndToEndReversesALinkedRetireUnderTheMergeLock.
func TestRetiredApply_UndoCombineSeesTheLinkedRetire(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	// The combine stamps the shell's trash time from the real clock; the fixer's
	// clock is moved past the age gate instead of rewriting that stamp (an
	// edited stamp is a "re-deleted since the combine" refusal of its own).
	rf.now = time.Now().UTC().Add(30 * 24 * time.Hour)
	rf.live("Year One", "g1", rf.file("g1/year-one.mp3"))
	keep := rf.live("The Dark Knight Returns", "", rf.file("keep/dkr.mp3"))
	hush := rf.live("Hush", "", rf.file("hush/hush-1.mp3"))
	knight := rf.live("Knightfall", "", rf.file("knight/knightfall.mp3"))

	const combineOp = "op-combine"
	cres, err := merge.NewService(rf.st).CombineBooksWithOptions([]string{keep, hush, knight}, keep, nil, merge.CombineOptions{OperationID: combineOp})
	require.NoError(t, err)
	require.NotEmpty(t, cres.JournalID)
	b, err := rf.st.GetBookByID(hush)
	require.NoError(t, err)
	require.True(t, b.IsSoftDeleted(), "the combine trashed the shell")
	left, err := rf.st.GetBookFiles(hush)
	require.NoError(t, err)
	require.Empty(t, left, "the combine moved every row off the shell")

	// The shell then collects a row and joins the holder's version group: a
	// retire candidate the combine still names.
	extra := &database.BookFile{BookID: hush, FilePath: rf.file("g1/hush-extra.mp3"), Format: "mp3"}
	require.NoError(t, rf.st.CreateBookFile(extra))
	b.VersionGroupID = retiredPtr("g1")
	_, err = rf.st.UpdateBook(b.ID, b)
	require.NoError(t, err)

	res := rf.planAll()
	row := rf.retiredPlanRow(res, "Hush")
	require.Equal(t, []string{retFlagCombineUndoAware}, row.Flags, "%s: %s", row.Class, row.Reason)
	require.True(t, row.Applicable(), "%s: %s", row.Class, row.SkipReason)
	const retireOp = "op-retire-linked"
	out := rf.applyRows(rf.f, res, []string{hush}, false, retireOp)
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
	links, err := merge.ListCombineRetireLinks(rf.st, cres.JournalID)
	require.NoError(t, err)
	require.Equal(t, []merge.CombineRetireLink{{ShellID: hush, OpID: retireOp}}, links)
	holder := rf.ids["Year One"]
	held, err := rf.st.GetRetiredBookFiles(holder)
	require.NoError(t, err)
	require.Len(t, held, 1)

	result, err := audiobooks.NewRevertService(rf.st).RevertOperation(combineOp)
	require.Error(t, err, "%+v", result)
	require.ErrorContains(t, err, "taken by a retire since, still held by it", "UndoCombine found the retire through the link")
	held, err = rf.st.GetRetiredBookFiles(holder)
	require.NoError(t, err)
	require.Len(t, held, 1, "a refused undo leaves the retire in place")
	b, err = rf.st.GetBookByID(hush)
	require.NoError(t, err)
	require.True(t, b.IsSoftDeleted(), "and the shell in the trash")
	j, err := merge.NewService(rf.st).GetCombineJournal(cres.JournalID)
	require.NoError(t, err)
	require.NotEqual(t, merge.CombineJournalUndone, j.Status)

	// Reverting the retire op itself puts the row back and removes the version.
	_, err = audiobooks.NewRevertService(rf.st).RevertOperation(retireOp)
	require.NoError(t, err)
	held, err = rf.st.GetRetiredBookFiles(holder)
	require.NoError(t, err)
	require.Empty(t, held)
	back, err := rf.st.GetBookFileByID(hush, extra.ID)
	require.NoError(t, err)
	require.NotNil(t, back)
	require.False(t, back.Retired)
	vs, err := rf.st.GetRetiredVersionsFromBook(hush)
	require.NoError(t, err)
	require.Empty(t, vs)
}
