// file: internal/plugins/maintenance/scan_title_revert_fixer_test.go
// version: 1.0.0
// guid: 63020fe7-ecfe-4a9a-8450-fccba4c32c64
// last-edited: 2026-10-06

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// strLibrary is a synthetic library (no real titles or ids: public repo) with
// one book per row kind. Every book is written before since (the "pre-scan"
// state), by the "scan" after since, and in some cases again after that.
type strLibrary struct {
	st    *database.PebbleStore
	since time.Time

	reverted, lastWrite, same, empty, changed, noSnap string
	otherFieldFirst                                   string
	locked, itunesBook, itunesFile, owner, merged     string
	seriesID, authorID                                int
}

const strMissingID = "01SYNTHETICMISSINGBOOK0000"

func newSTRLibrary(t *testing.T) *strLibrary {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	l := &strLibrary{st: st}
	a, err := st.CreateAuthor("Synthetic Author")
	require.NoError(t, err)
	l.authorID = a.ID
	s, err := st.CreateSeries("Synthetic Series", &l.authorID)
	require.NoError(t, err)
	l.seriesID = s.ID

	n := 0
	book := func(title string, pid *string) string {
		n++
		seq := n
		b, err := st.CreateBook(&database.Book{Title: title, AuthorID: &l.authorID, SeriesID: &l.seriesID,
			SeriesSequence: &seq, FilePath: "/srv/library/synthetic" + string(rune('a'+n)) + ".m4b", Format: "m4b",
			ITunesPersistentID: pid})
		require.NoError(t, err)
		return b.ID
	}
	setTitle := func(id, title string) {
		_, err := st.ModifyBook(id, func(b *database.Book) error { b.Title = title; return nil })
		require.NoError(t, err)
	}
	touch := func(id, desc string) {
		_, err := st.ModifyBook(id, func(b *database.Book) error { b.Description = &desc; return nil })
		require.NoError(t, err)
	}
	pid := "SYNTHPID0000001"

	// Before the scan.
	l.reverted = book("Seed Title A", nil)
	for _, v := range []string{"Draft A1", "Draft A2"} {
		setTitle(l.reverted, v)
	}
	setTitle(l.reverted, "Volume 7 of 12")
	l.lastWrite = book("Volume 2 of 9", nil)
	l.same = book("Unchanged Title", nil)
	l.empty = book("Seed Title D", nil)
	setTitle(l.empty, "")
	l.changed = book("Volume 4 of 6", nil)
	l.noSnap = book("Volume 5 of 5", nil)
	setTitle(l.noSnap, "Volume 5 of 5 (pre)")
	l.locked = book("Volume 3 of 8", nil)
	l.itunesBook = book("Volume 1 of 4", &pid)
	l.itunesFile = book("Volume 6 of 7", nil)
	require.NoError(t, st.CreateBookFile(&database.BookFile{BookID: l.itunesFile, FilePath: "/srv/library/synthetic-file.m4b",
		Format: "m4b", ITunesPersistentID: "SYNTHPID0000002"}))
	l.owner = book("Doctor Who: The Synthetic Paradox", nil)
	l.merged = book("Volume 8 of 8", nil)
	l.otherFieldFirst = book("Volume 3 of 10", nil)

	// The scan starts. Snapshot keys are wall-clock nanoseconds; the sleeps
	// keep every write strictly on its side of since.
	time.Sleep(5 * time.Millisecond)
	l.since = time.Now()
	time.Sleep(5 * time.Millisecond)

	setTitle(l.reverted, "of 12")
	setTitle(l.lastWrite, "of 9")
	touch(l.same, "a scan wrote the description only")
	setTitle(l.empty, "of 3")
	setTitle(l.changed, "of 6")
	setTitle(l.locked, "of 8")
	setTitle(l.itunesBook, "of 4")
	setTitle(l.itunesFile, "of 7")
	setTitle(l.owner, "of 3 (synthetic)")
	setTitle(l.merged, "of 8")
	// Another field first, the title in a later write.
	touch(l.otherFieldFirst, "written before the title")
	setTitle(l.otherFieldFirst, "of 10")

	// After the scan: many later writes on one book (the snapshot at the
	// scan's start must not be cut off by a limit), an owner's edit on
	// another, a merge on a third.
	for i := 0; i < 30; i++ {
		touch(l.reverted, "later write "+itoa(i))
	}
	setTitle(l.changed, "Owner Edited Title")
	survivor := l.lastWrite
	_, err = st.ModifyBook(l.merged, func(b *database.Book) error { b.MergedIntoBookID = &survivor; return nil })
	require.NoError(t, err)

	require.NoError(t, st.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: l.locked,
		Field: database.FieldKeyTitle, OverrideLocked: true, UpdatedAt: time.Now()}))
	return l
}

func (l *strLibrary) params(t *testing.T, ids ...string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"book_ids": ids, "since": l.since.Format(time.RFC3339Nano),
		"source_op_id": "op-synthetic-scan"})
	require.NoError(t, err)
	return raw
}

func (l *strLibrary) all() []string {
	return []string{l.reverted, l.lastWrite, l.same, l.empty, l.changed, l.noSnap, l.locked,
		l.itunesBook, l.itunesFile, l.owner, l.merged, l.otherFieldFirst, strMissingID}
}

func (l *strLibrary) plan(t *testing.T, f *scanTitleRevertFixer, ids ...string) *repairs.PlanResult {
	t.Helper()
	res, err := repairs.RunPlan(context.Background(), f, l.params(t, ids...), repairs.PlanDeps{Guard: l.st}, &fakeReporter{})
	require.NoError(t, err)
	return res
}

func strRowsByID(res *repairs.PlanResult) map[string]repairs.Row {
	out := map[string]repairs.Row{}
	for _, r := range res.Rows {
		out[r.RowID] = r
	}
	return out
}

// Every skip kind is detected and reported; only listed books are rows.
func TestScanTitleRevert_Classification(t *testing.T) {
	l := newSTRLibrary(t)
	f := newScanTitleRevertFixer(&Plugin{deps: fakeDeps{store: l.st}})
	res := l.plan(t, f, l.all()...)
	rows := strRowsByID(res)
	require.Len(t, rows, len(l.all()), "one row per listed book")

	for id, want := range map[string]string{l.reverted: "Volume 7 of 12", l.lastWrite: "Volume 2 of 9",
		l.otherFieldFirst: "Volume 3 of 10"} {
		r := rows[id]
		assert.Empty(t, r.Skipped, "%s: %s", id, r.SkipReason)
		assert.Equal(t, want, r.Proposed["title"])
		assert.Equal(t, "Synthetic Author", r.Author)
		assert.Contains(t, r.Evidence, "source operation op-synthetic-scan")
	}
	assert.Equal(t, "of 12", rows[l.reverted].Current["title"])

	held := map[string]string{
		l.same:       strSkipSame,
		l.empty:      strSkipOldTitleEmpty,
		l.changed:    strSkipChangedSince,
		l.noSnap:     strSkipNoSnapshot,
		l.locked:     strSkipLocked,
		l.itunesBook: strSkipITunes,
		l.itunesFile: strSkipITunes,
		l.owner:      repairs.SkipOwnerManual,
		l.merged:     strSkipGone,
		strMissingID: strSkipGone,
	}
	for id, want := range held {
		assert.Equal(t, want, rows[id].Skipped, "book %s: %s", id, rows[id].SkipReason)
		assert.NotEmpty(t, rows[id].SkipReason, "book %s", id)
	}
	assert.Equal(t, 3, res.Applicable)
}

// The restored title comes from the earliest snapshot at or after since:
// not a snapshot before it ("Draft A2"), not the latest one ("of 12"), and
// not cut off by the 30 later snapshots.
func TestScanTitleRevert_PicksEarliestSnapshotAtOrAfterSince(t *testing.T) {
	l := newSTRLibrary(t)
	snaps, err := l.st.GetBookSnapshots(l.reverted, 0)
	require.NoError(t, err)
	require.Greater(t, len(snaps), 30)

	found, later := strSnapshotsSince(snaps, l.since)
	require.NotNil(t, found)
	require.Len(t, later, 30, "every snapshot after the found one, none cut off")
	title, err := strSnapshotTitle(found)
	require.NoError(t, err)
	assert.Equal(t, "Volume 7 of 12", title)
	wrote, _, err := strScanWrote(later, title, "live title")
	require.NoError(t, err)
	assert.Equal(t, "of 12", wrote, "the next snapshot holds what the scan wrote")

	// Order-independent: the same answer from a reversed (oldest-first) list.
	rev := make([]database.BookSnapshot, len(snaps))
	for i := range snaps {
		rev[len(snaps)-1-i] = snaps[i]
	}
	found2, _ := strSnapshotsSince(rev, l.since)
	require.NotNil(t, found2)
	assert.Equal(t, found.Timestamp, found2.Timestamp)

	// A snapshot stamped exactly at since counts.
	at := []database.BookSnapshot{{BookID: "x", Timestamp: l.since.Add(-time.Second)}, {BookID: "x", Timestamp: l.since}}
	f3, n3 := strSnapshotsSince(at, l.since)
	require.NotNil(t, f3)
	assert.Equal(t, l.since, f3.Timestamp)
	assert.Empty(t, n3)
	f4, _ := strSnapshotsSince(at[:1], l.since)
	assert.Nil(t, f4, "a snapshot before since is never the one")
}

// The scan's title is the first later title that differs from the restored
// one: a write of another field in between is passed over, and with no
// differing snapshot it is the live title.
func TestScanTitleRevert_ScanWrotePassesOverOtherFieldWrites(t *testing.T) {
	l := newSTRLibrary(t)
	snaps, err := l.st.GetBookSnapshots(l.otherFieldFirst, 0)
	require.NoError(t, err)
	found, later := strSnapshotsSince(snaps, l.since)
	require.NotNil(t, found)
	require.Len(t, later, 1)
	restore, err := strSnapshotTitle(found)
	require.NoError(t, err)
	assert.Equal(t, "Volume 3 of 10", restore)
	next, err := strSnapshotTitle(&later[0])
	require.NoError(t, err)
	assert.Equal(t, "Volume 3 of 10", next, "the scan's first write was not the title")
	wrote, at, err := strScanWrote(later, restore, "of 10")
	require.NoError(t, err)
	assert.Equal(t, "of 10", wrote)
	assert.Equal(t, "the live row", at)
}

// A write of another field between plan and apply (the ASIN backfill, say)
// adds a snapshot but does not refuse the row: its title never moved.
func TestScanTitleRevert_UnrelatedWriteAfterPlanStillApplies(t *testing.T) {
	l := newSTRLibrary(t)
	f := newScanTitleRevertFixer(&Plugin{deps: &ansEnqueueDeps{fakeDeps: fakeDeps{store: l.st}}})
	res := l.plan(t, f, l.lastWrite, l.reverted)
	for _, id := range []string{l.lastWrite, l.reverted} {
		asin := "B0SYNTH" + id[len(id)-3:]
		_, err := l.st.ModifyBook(id, func(b *database.Book) error { b.ASIN = &asin; return nil })
		require.NoError(t, err)
	}
	w := repairs.NewWriter(l.st, l.st, f.ID(), "bulk_update", "repairs-").WithJournal(l.st, l.st, "op-late")
	out, err := repairs.RunApply(context.Background(), f, res, "plan-1", []string{l.lastWrite, l.reverted}, false,
		repairs.ApplyDeps{Guard: l.st, Writer: w, OpID: "op-late"}, &fakeReporter{})
	require.NoError(t, err)
	assert.Equal(t, 2, out.Applied, "%v", out.Rows)
	b, err := l.st.GetBookByID(l.lastWrite)
	require.NoError(t, err)
	assert.Equal(t, "Volume 2 of 9", b.Title)
}

// Params are required: book ids and an RFC3339 since.
func TestScanTitleRevert_ParamsRequired(t *testing.T) {
	l := newSTRLibrary(t)
	f := newScanTitleRevertFixer(&Plugin{deps: fakeDeps{store: l.st}})
	for name, raw := range map[string]string{
		"none":      "",
		"no ids":    `{"since":"2026-10-05T21:18:21-04:00"}`,
		"blank ids": `{"book_ids":[" "],"since":"2026-10-05T21:18:21-04:00"}`,
		"no since":  `{"book_ids":["a"]}`,
		"bad since": `{"book_ids":["a"],"since":"yesterday"}`,
	} {
		_, err := f.Plan(context.Background(), json.RawMessage(raw), &fakeReporter{})
		assert.Error(t, err, name)
	}
	p, err := parseSTRParams(json.RawMessage(`{"book_ids":["b"," a ","b"],"since":"2026-10-05T21:18:21-04:00"}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, p.ids)
	assert.Equal(t, time.Date(2026, 10, 6, 1, 18, 21, 0, time.UTC), p.since.UTC())
}

// Apply writes the title only, records history and the journal after the
// write, enqueues one forced fetch for the applied book only, and the op
// revert puts the scan's title back.
func TestScanTitleRevert_ApplyTitleOnlyJournalsFetchesAndReverts(t *testing.T) {
	l := newSTRLibrary(t)
	deps := &ansEnqueueDeps{fakeDeps: fakeDeps{store: l.st}}
	f := newScanTitleRevertFixer(&Plugin{deps: deps})
	res := l.plan(t, f, l.all()...)

	before, err := l.st.GetBookByID(l.reverted)
	require.NoError(t, err)
	filesBefore, err := l.st.GetBookFiles(l.reverted)
	require.NoError(t, err)

	const opID = "op-str"
	w := repairs.NewWriter(l.st, l.st, f.ID(), "bulk_update", "repairs-").WithJournal(l.st, l.st, opID)
	out, err := repairs.RunApply(context.Background(), f, res, "plan-1", []string{l.reverted, l.locked, l.same}, false,
		repairs.ApplyDeps{Guard: l.st, Writer: w, OpID: opID}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, out.Applied, "%v", out.Rows)
	assert.Equal(t, 2, out.ByOutcome[repairs.OutcomeNotApplicable])

	after, err := l.st.GetBookByID(l.reverted)
	require.NoError(t, err)
	assert.Equal(t, "Volume 7 of 12", after.Title)
	assert.Equal(t, before.AuthorID, after.AuthorID)
	assert.Equal(t, before.SeriesID, after.SeriesID)
	assert.Equal(t, before.SeriesSequence, after.SeriesSequence)
	assert.Equal(t, before.FilePath, after.FilePath)
	assert.Equal(t, before.Description, after.Description)
	assert.Equal(t, before.Format, after.Format)
	filesAfter, err := l.st.GetBookFiles(l.reverted)
	require.NoError(t, err)
	assert.Equal(t, filesBefore, filesAfter, "book_file rows untouched")
	assert.Equal(t, 1, out.BookWrites)
	assert.Equal(t, 1, out.HistoryRows, "one history row: the title")

	hist, err := l.st.GetMetadataChangeHistory(l.reverted, "title", 5)
	require.NoError(t, err)
	require.NotEmpty(t, hist)
	assert.Equal(t, scanTitleRevertFixerID, hist[0].Source)

	changes, err := l.st.GetOperationChanges(opID)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	assert.Equal(t, l.reverted, changes[0].BookID)
	assert.Equal(t, "title", changes[0].FieldName)
	assert.Equal(t, "of 12", changes[0].OldValue)
	assert.Equal(t, "Volume 7 of 12", changes[0].NewValue)

	// One forced fetch, for the applied book only.
	require.Equal(t, []string{metabatch.CandidateFetchDefID}, deps.defIDs)
	p, ok := deps.params[0].(metabatch.FetchOpParams)
	require.True(t, ok)
	assert.Equal(t, []string{l.reverted}, p.BookIDs)
	assert.True(t, p.Force)
	assert.Equal(t, "op-fetch-1", out.FollowUp)

	// Untouched: the locked book keeps the scan's title.
	lk, err := l.st.GetBookByID(l.locked)
	require.NoError(t, err)
	assert.Equal(t, "of 8", lk.Title)

	rev, err := audiobooks.NewRevertService(l.st).RevertOperation(opID)
	require.NoError(t, err)
	require.Zero(t, rev.Failed, "revert: %+v", rev)
	b, err := l.st.GetBookByID(l.reverted)
	require.NoError(t, err)
	assert.Equal(t, "of 12", b.Title, "the op revert restores the pre-revert title")
}

// "Undo last apply" restores the title from the history row.
func TestScanTitleRevert_UndoLastApply(t *testing.T) {
	l := newSTRLibrary(t)
	f := newScanTitleRevertFixer(&Plugin{deps: &ansEnqueueDeps{fakeDeps: fakeDeps{store: l.st}}})
	res := l.plan(t, f, l.lastWrite)
	w := repairs.NewWriter(l.st, l.st, f.ID(), "bulk_update", "repairs-").WithJournal(l.st, l.st, "op-undo")
	out, err := repairs.RunApply(context.Background(), f, res, "plan-1", []string{l.lastWrite}, false,
		repairs.ApplyDeps{Guard: l.st, Writer: w, OpID: "op-undo"}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, out.Applied, "%v", out.Rows)

	u, err := metafetch.NewService(l.st).UndoLastApply(l.lastWrite)
	require.NoError(t, err)
	assert.Equal(t, []string{"title"}, u.Reverted)
	b, err := l.st.GetBookByID(l.lastWrite)
	require.NoError(t, err)
	assert.Equal(t, "of 9", b.Title)
}

// A title that moves after the plan is refused as changed_since_plan, both by
// the engine's re-plan and by the compare-and-set inside the write; nothing
// is written, journaled or fetched. A dry run writes nothing either.
func TestScanTitleRevert_ChangedSincePlan(t *testing.T) {
	l := newSTRLibrary(t)
	deps := &ansEnqueueDeps{fakeDeps: fakeDeps{store: l.st}}
	f := newScanTitleRevertFixer(&Plugin{deps: deps})
	res := l.plan(t, f, l.reverted, l.lastWrite)

	dry, err := repairs.RunApply(context.Background(), f, res, "plan-1", []string{l.reverted}, true,
		repairs.ApplyDeps{Guard: l.st}, &fakeReporter{})
	require.NoError(t, err)
	assert.Equal(t, 1, dry.ByOutcome[repairs.OutcomeWouldApply])
	assert.Empty(t, deps.defIDs, "a dry run enqueues nothing")

	_, err = l.st.ModifyBook(l.reverted, func(b *database.Book) error { b.Title = "Owner Title After Plan"; return nil })
	require.NoError(t, err)
	w := repairs.NewWriter(l.st, l.st, f.ID(), "bulk_update", "repairs-").WithJournal(l.st, l.st, "op-csp")
	out, err := repairs.RunApply(context.Background(), f, res, "plan-1", []string{l.reverted}, false,
		repairs.ApplyDeps{Guard: l.st, Writer: w, OpID: "op-csp"}, &fakeReporter{})
	require.NoError(t, err)
	assert.Equal(t, 1, out.ChangedSincePlan, "%v", out.Rows)
	assert.Empty(t, deps.defIDs, "nothing applied, nothing fetched")
	b, err := l.st.GetBookByID(l.reverted)
	require.NoError(t, err)
	assert.Equal(t, "Owner Title After Plan", b.Title)

	// The in-write check: a decision whose "current" is stale is refused.
	w2 := repairs.NewWriter(l.st, l.st, f.ID(), "bulk_update", "repairs-").WithJournal(l.st, l.st, "op-cas")
	err = f.Apply(context.Background(), w2, repairs.Row{RowID: l.lastWrite,
		Detail: &strDecision{bookID: l.lastWrite, current: "a stale title", restore: "Volume 2 of 9"}})
	require.True(t, errors.Is(err, repairs.ErrChangedSincePlan), "err = %v", err)
	changes, err := l.st.GetOperationChanges("op-cas")
	require.NoError(t, err)
	assert.Empty(t, changes, "nothing is journaled for a write that was not made")
	lw, err := l.st.GetBookByID(l.lastWrite)
	require.NoError(t, err)
	assert.Equal(t, "of 9", lw.Title)

	// A lock landing after the re-plan is refused inside the write too.
	require.NoError(t, l.st.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: l.lastWrite,
		Field: database.FieldKeyTitle, OverrideLocked: true, UpdatedAt: time.Now()}))
	err = f.Apply(context.Background(), w2, repairs.Row{RowID: l.lastWrite,
		Detail: &strDecision{bookID: l.lastWrite, current: "of 9", restore: "Volume 2 of 9"}})
	require.True(t, errors.Is(err, repairs.ErrChangedSincePlan), "err = %v", err)
}

// The fixer is registered with the plugin's repairs registry.
func TestScanTitleRevert_Registered(t *testing.T) {
	p := &Plugin{deps: fakeDeps{}}
	_, ok := p.Repairs().Get(scanTitleRevertFixerID)
	assert.True(t, ok)
}
