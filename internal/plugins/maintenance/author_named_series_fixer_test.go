// file: internal/plugins/maintenance/author_named_series_fixer_test.go
// version: 1.1.0
// guid: aa736089-b4ed-43cf-b00e-426629a38cb7
// last-edited: 2026-10-06

package maintenance

import (
	"context"
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

// ansEnqueueDeps records the operations the fixer enqueues.
type ansEnqueueDeps struct {
	fakeDeps
	defIDs []string
	params []any
	err    error
}

func (d *ansEnqueueDeps) EnqueueOp(_ context.Context, defID string, params any) (string, error) {
	if d.err != nil {
		return "", d.err
	}
	d.defIDs = append(d.defIDs, defID)
	d.params = append(d.params, params)
	return "op-fetch-1", nil
}

// ansLibrary is a small library covering every verdict and exclusion.
type ansLibrary struct {
	st *database.PebbleStore
	// junk series "Brandon Sanderson" and its books
	junkSeries                                   int
	elantris, warbreaker, coauthored, applied    string
	locked, doctorWho, noMatch                   string
	mistborn                                     string // by Sanderson, no series
	realSeriesJunkAuthorBook, thrawn             string
	rogueBook                                    string
	discworldBook                                string
	sandersonID, starWarsAuthorID, rogueAuthorID int
}

func newANSLibrary(t *testing.T) *ansLibrary {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	l := &ansLibrary{st: st}
	author := func(name string) int {
		a, err := st.CreateAuthor(name)
		require.NoError(t, err)
		return a.ID
	}
	series := func(name string, authorID *int) int {
		s, err := st.CreateSeries(name, authorID)
		require.NoError(t, err)
		return s.ID
	}
	n := 0
	book := func(title string, authorID int, seriesID *int, seq *int) string {
		n++
		b, err := st.CreateBook(&database.Book{Title: title, AuthorID: &authorID, SeriesID: seriesID, SeriesSequence: seq,
			FilePath: "/srv/library/book" + string(rune('a'+n)) + ".m4b", Format: "m4b"})
		require.NoError(t, err)
		return b.ID
	}
	pos := func(v int) *int { return &v }

	l.sandersonID = author("Brandon Sanderson")
	sid := series("Brandon Sanderson", &l.sandersonID)
	l.junkSeries = sid
	l.elantris = book("Elantris", l.sandersonID, &sid, pos(1))
	l.warbreaker = book("Warbreaker", l.sandersonID, &sid, nil)
	l.mistborn = book("Mistborn", l.sandersonID, nil, nil)
	l.coauthored = book("Dreamer", l.sandersonID, &sid, nil)
	coauthor := author("Mary Robinette Kowal")
	require.NoError(t, st.SetBookAuthors(l.coauthored, []database.BookAuthor{
		{BookID: l.coauthored, AuthorID: l.sandersonID, Role: "author", Position: 0},
		{BookID: l.coauthored, AuthorID: coauthor, Role: "author", Position: 1}}))
	l.applied = book("Arcanum", l.sandersonID, &sid, nil)
	l.noMatch = book("Firstborn", l.sandersonID, &sid, nil)
	for id, status := range map[string]string{l.applied: "matched", l.noMatch: "no_match"} {
		s := status
		_, err := st.ModifyBook(id, func(b *database.Book) error { b.MetadataReviewStatus = &s; return nil })
		require.NoError(t, err)
	}
	l.locked = book("Legion", l.sandersonID, &sid, nil)
	require.NoError(t, st.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: l.locked,
		Field: database.FieldKeySeriesName, OverrideLocked: true, UpdatedAt: time.Now()}))
	l.doctorWho = book("Doctor Who: The Sanderson Paradox", l.sandersonID, &sid, nil)

	// A real series sharing an author row's name: "Star Wars" holds Zahn's
	// book, so the junk "Star Wars" author's book in it is held.
	l.starWarsAuthorID = author("Star Wars")
	zahn := author("Timothy Zahn")
	sw := series("Star Wars", nil)
	l.thrawn = book("Thrawn", zahn, &sw, nil)
	l.realSeriesJunkAuthorBook = book("Heir to the Empire", l.starWarsAuthorID, &sw, nil)

	// A real series under a junk AUTHOR row: the author has no books outside it.
	l.rogueAuthorID = author("Rogue Merchant")
	rm := series("Rogue Merchant", &l.rogueAuthorID)
	l.rogueBook = book("Rogue Merchant 1", l.rogueAuthorID, &rm, pos(1))

	// No author shares this series' name: never a row.
	pratchett := author("Terry Pratchett")
	dw := series("Discworld", nil)
	l.discworldBook = book("Mort", pratchett, &dw, pos(4))
	return l
}

func (l *ansLibrary) plan(t *testing.T, f *authorNamedSeriesFixer) map[string]repairs.Row {
	t.Helper()
	res, err := repairs.RunPlan(context.Background(), f, nil, repairs.PlanDeps{Guard: l.st}, &fakeReporter{})
	require.NoError(t, err)
	byID := map[string]repairs.Row{}
	for _, r := range res.Rows {
		byID[r.RowID] = r
	}
	return byID
}

// Classification: only books of a junk series (every book by the same-named
// author, who has books elsewhere) are applicable; real series, junk author
// rows, co-authored, applied, no-match, locked and Doctor Who books are held.
func TestAuthorNamedSeriesFixer_Classification(t *testing.T) {
	l := newANSLibrary(t)
	f := newAuthorNamedSeriesFixer(&Plugin{deps: fakeDeps{store: l.st}})
	rows := l.plan(t, f)

	for _, id := range []string{l.elantris, l.warbreaker} {
		r, ok := rows[id]
		require.True(t, ok, "junk-series book %s is a row", id)
		assert.Empty(t, r.Skipped, "%s: %s", id, r.SkipReason)
		assert.Equal(t, "author_named_series", r.Class)
		assert.Equal(t, "Brandon Sanderson", r.Current["series"])
		assert.Equal(t, "", r.Proposed["series"])
	}
	assert.Equal(t, "1", rows[l.elantris].Current["series_sequence"])

	held := map[string]string{
		l.coauthored:               ansSkipCoauthored,
		l.applied:                  ansSkipApplied,
		l.noMatch:                  ansSkipNoMatch,
		l.locked:                   junkSkipUserLocked,
		l.doctorWho:                repairs.SkipOwnerManual,
		l.realSeriesJunkAuthorBook: ansSkipRealSeries,
		l.rogueBook:                ansSkipAuthorRowJunk,
	}
	for id, want := range held {
		r, ok := rows[id]
		require.True(t, ok, "held book %s is listed", id)
		assert.Equal(t, want, r.Skipped, "book %s (%s)", id, r.Title)
		assert.NotEmpty(t, r.SkipReason)
	}
	for _, id := range []string{l.thrawn, l.discworldBook, l.mistborn} {
		assert.NotContains(t, rows, id, "a book not credited to a series' same-named author is never a row")
	}
}

// Apply writes only the approved ids, removes the link and the position,
// journals after the write, enqueues one forced candidate fetch for the
// changed books, and is undoable both ways.
func TestAuthorNamedSeriesFixer_ApplyUnlinksJournalsAndIsUndoable(t *testing.T) {
	l := newANSLibrary(t)
	deps := &ansEnqueueDeps{fakeDeps: fakeDeps{store: l.st}}
	f := newAuthorNamedSeriesFixer(&Plugin{deps: deps})
	res, err := repairs.RunPlan(context.Background(), f, nil, repairs.PlanDeps{Guard: l.st}, &fakeReporter{})
	require.NoError(t, err)

	const opID = "op-ans"
	w := repairs.NewWriter(l.st, l.st, f.ID(), "bulk_update", "repairs-").WithJournal(l.st, l.st, opID)
	out, err := repairs.RunApply(context.Background(), f, res, "plan-1",
		[]string{l.elantris, l.coauthored}, false, repairs.ApplyDeps{Guard: l.st, Writer: w, OpID: opID}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, out.Applied, "%v", out.ByOutcome)
	assert.Equal(t, 1, out.ByOutcome[repairs.OutcomeNotApplicable], "the co-authored row is never written")

	b, err := l.st.GetBookByID(l.elantris)
	require.NoError(t, err)
	assert.Nil(t, b.SeriesID)
	assert.Nil(t, b.SeriesSequence)
	for _, id := range []string{l.warbreaker, l.coauthored} {
		o, err := l.st.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, o.SeriesID, "unapproved book %s keeps its series", id)
	}
	// The series row is kept (the revert needs it).
	s, err := l.st.GetSeriesByID(l.junkSeries)
	require.NoError(t, err)
	require.NotNil(t, s)

	// Journaled: series_id and series_sequence under the op.
	changes, err := l.st.GetOperationChanges(opID)
	require.NoError(t, err)
	fields := map[string]string{}
	for _, c := range changes {
		if c.BookID == l.elantris && c.ChangeType == "metadata_update" {
			fields[c.FieldName] = c.OldValue
		}
	}
	assert.Equal(t, map[string]string{"series_id": itoa(l.junkSeries), "series_sequence": "1"}, fields)

	// One forced fetch for the changed book, fetch only.
	require.Equal(t, []string{metabatch.CandidateFetchDefID}, deps.defIDs)
	p, ok := deps.params[0].(metabatch.FetchOpParams)
	require.True(t, ok)
	assert.Equal(t, []string{l.elantris}, p.BookIDs)
	assert.True(t, p.Force)
	assert.Equal(t, "op-fetch-1", out.FollowUp)

	// The op revert restores both the link and the position.
	rev, err := audiobooks.NewRevertService(l.st).RevertOperation(opID)
	require.NoError(t, err)
	require.Zero(t, rev.Failed, "revert: %+v", rev)
	b, err = l.st.GetBookByID(l.elantris)
	require.NoError(t, err)
	require.NotNil(t, b.SeriesID)
	assert.Equal(t, l.junkSeries, *b.SeriesID)
	require.NotNil(t, b.SeriesSequence)
	assert.Equal(t, 1, *b.SeriesSequence)
}

// "Undo last apply" restores a book from the history rows Modify wrote after
// the write (no op-journaled marker refuses it).
func TestAuthorNamedSeriesFixer_UndoLastApplyRestoresLink(t *testing.T) {
	l := newANSLibrary(t)
	f := newAuthorNamedSeriesFixer(&Plugin{deps: &ansEnqueueDeps{fakeDeps: fakeDeps{store: l.st}}})
	res, err := repairs.RunPlan(context.Background(), f, nil, repairs.PlanDeps{Guard: l.st}, &fakeReporter{})
	require.NoError(t, err)
	w := repairs.NewWriter(l.st, l.st, f.ID(), "bulk_update", "repairs-").WithJournal(l.st, l.st, "op-undo")
	out, err := repairs.RunApply(context.Background(), f, res, "plan-1", []string{l.elantris}, false,
		repairs.ApplyDeps{Guard: l.st, Writer: w, OpID: "op-undo"}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, out.Applied, "%v", out.ByOutcome)

	undo, err := metafetch.NewService(l.st).UndoLastApply(l.elantris)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"series_id", "series_sequence"}, undo.Reverted)
	b, err := l.st.GetBookByID(l.elantris)
	require.NoError(t, err)
	require.NotNil(t, b.SeriesID)
	assert.Equal(t, l.junkSeries, *b.SeriesID)
	require.NotNil(t, b.SeriesSequence)
	assert.Equal(t, 1, *b.SeriesSequence)
}

// A row whose link moved after the plan is refused as changed_since_plan and
// not written; a dry run writes nothing and enqueues nothing.
func TestAuthorNamedSeriesFixer_ChangedSincePlanAndDryRun(t *testing.T) {
	l := newANSLibrary(t)
	deps := &ansEnqueueDeps{fakeDeps: fakeDeps{store: l.st}}
	f := newAuthorNamedSeriesFixer(&Plugin{deps: deps})
	res, err := repairs.RunPlan(context.Background(), f, nil, repairs.PlanDeps{Guard: l.st}, &fakeReporter{})
	require.NoError(t, err)

	dry, err := repairs.RunApply(context.Background(), f, res, "plan-1", []string{l.elantris}, true,
		repairs.ApplyDeps{Guard: l.st}, &fakeReporter{})
	require.NoError(t, err)
	assert.Equal(t, 1, dry.ByOutcome[repairs.OutcomeWouldApply])
	assert.Empty(t, deps.defIDs, "a dry run enqueues nothing")

	// The owner renumbers Elantris after the plan.
	_, err = l.st.ModifyBook(l.elantris, func(b *database.Book) error { v := 7; b.SeriesSequence = &v; return nil })
	require.NoError(t, err)
	// And moves Warbreaker out of the series.
	_, err = l.st.ModifyBook(l.warbreaker, func(b *database.Book) error { b.SeriesID = nil; return nil })
	require.NoError(t, err)

	w := repairs.NewWriter(l.st, l.st, f.ID(), "bulk_update", "repairs-").WithJournal(l.st, l.st, "op-csp")
	out, err := repairs.RunApply(context.Background(), f, res, "plan-1", []string{l.elantris, l.warbreaker}, false,
		repairs.ApplyDeps{Guard: l.st, Writer: w, OpID: "op-csp"}, &fakeReporter{})
	require.NoError(t, err)
	assert.Equal(t, 2, out.ChangedSincePlan, "%v", out.ByOutcome)
	assert.Zero(t, out.Applied)
	assert.Empty(t, deps.defIDs, "nothing applied, nothing fetched")
	b, err := l.st.GetBookByID(l.elantris)
	require.NoError(t, err)
	require.NotNil(t, b.SeriesID, "a changed row is not written")
	assert.Equal(t, 7, *b.SeriesSequence)
}

// The under-lock check in Apply refuses a link that moved between Replan and
// the write.
func TestAuthorNamedSeriesFixer_ApplyCompareAndSet(t *testing.T) {
	l := newANSLibrary(t)
	f := newAuthorNamedSeriesFixer(&Plugin{deps: fakeDeps{store: l.st}})
	w := repairs.NewWriter(l.st, l.st, f.ID(), "bulk_update", "repairs-").WithJournal(l.st, l.st, "op-cas")
	stale := 99
	err := f.Apply(context.Background(), w, repairs.Row{RowID: l.elantris,
		Detail: &ansDecision{bookID: l.elantris, seriesID: l.junkSeries, seq: &stale}})
	require.True(t, errors.Is(err, repairs.ErrChangedSincePlan), "err = %v", err)
	changes, err := l.st.GetOperationChanges("op-cas")
	require.NoError(t, err)
	assert.Empty(t, changes, "nothing is journaled for a write that was not made")
}

// A failed enqueue fails no row: the writes were made, the error is recorded.
func TestAuthorNamedSeriesFixer_EnqueueFailureIsRecorded(t *testing.T) {
	l := newANSLibrary(t)
	deps := &ansEnqueueDeps{fakeDeps: fakeDeps{store: l.st}, err: errors.New("queue down")}
	f := newAuthorNamedSeriesFixer(&Plugin{deps: deps})
	res, err := repairs.RunPlan(context.Background(), f, nil, repairs.PlanDeps{Guard: l.st}, &fakeReporter{})
	require.NoError(t, err)
	w := repairs.NewWriter(l.st, l.st, f.ID(), "bulk_update", "repairs-").WithJournal(l.st, l.st, "op-enq")
	out, err := repairs.RunApply(context.Background(), f, res, "plan-1", []string{l.elantris}, false,
		repairs.ApplyDeps{Guard: l.st, Writer: w, OpID: "op-enq"}, &fakeReporter{})
	require.NoError(t, err)
	assert.Equal(t, 1, out.Applied)
	assert.Contains(t, out.FollowUpError, "queue down")
}

// Applying two books of one series in one run applies both: unlinking one
// book does not move its sibling's fingerprint. One worker, so the second
// row is always re-planned after the first is written (with more workers
// both may re-plan first and hide a sibling-dependent fingerprint).
func TestAuthorNamedSeriesFixer_SiblingsApplyTogether(t *testing.T) {
	l := newANSLibrary(t)
	deps := &ansEnqueueDeps{fakeDeps: fakeDeps{store: l.st}}
	f := newAuthorNamedSeriesFixer(&Plugin{deps: deps})
	res, err := repairs.RunPlan(context.Background(), f, nil, repairs.PlanDeps{Guard: l.st}, &fakeReporter{})
	require.NoError(t, err)
	w := repairs.NewWriter(l.st, l.st, f.ID(), "bulk_update", "repairs-").WithJournal(l.st, l.st, "op-sib")
	out, err := repairs.RunApply(context.Background(), f, res, "plan-1", []string{l.elantris, l.warbreaker}, false,
		repairs.ApplyDeps{Guard: l.st, Writer: w, OpID: "op-sib", Concurrency: 1}, &fakeReporter{})
	require.NoError(t, err)
	assert.Equal(t, 2, out.Applied, "%v", out.ByOutcome)
	require.Len(t, deps.params, 1, "one fetch for the whole run")
	p, ok := deps.params[0].(metabatch.FetchOpParams)
	require.True(t, ok)
	assert.ElementsMatch(t, []string{l.elantris, l.warbreaker}, p.BookIDs)
}
