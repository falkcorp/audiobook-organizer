// file: internal/plugins/maintenance/relink_stale_series_fixer_test.go
// version: 1.0.0
// guid: edaf6525-dcbd-426d-b672-c3293aff05f5
// last-edited: 2026-10-03

package maintenance

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// relinkLib is a real Pebble library with one stale book per decision.
type relinkLib struct {
	store  *database.PebbleStore
	ids    map[string]string
	series map[string]*database.Series
}

func newRelinkLib(t *testing.T) *relinkLib {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	lib := &relinkLib{store: st, ids: map[string]string{}, series: map[string]*database.Series{}}

	authorA, err := st.CreateAuthor("Author A")
	require.NoError(t, err)
	authorB, err := st.CreateAuthor("Author B")
	require.NoError(t, err)
	mk := func(key, name string, author *int) *database.Series {
		s, err := st.CreateSeries(name, author)
		require.NoError(t, err)
		lib.series[key] = s
		return s
	}
	live := mk("live", "The Expanse", &authorA.ID)
	mk("named", "Discworld", &authorA.ID)
	mk("twin1", "Foundation", &authorA.ID)
	mk("twin2", "Foundation", nil)
	require.NotEqual(t, lib.series["twin1"].ID, lib.series["twin2"].ID, "two distinct same-name series")
	mk("other-author", "Dune", &authorB.ID)
	mk("linked", "Linked Series", nil)
	mk("locked", "Locked Series", nil)
	mk("dw", "Doctor Who: The Monthly Adventures", nil)

	// add creates a book; emb (when set) is written as the stored series
	// object with SeriesID nil, the state older bugs left.
	add := func(key, title, path string, author *int, seriesID *int, emb *database.Series) {
		t.Helper()
		b, err := st.CreateBook(&database.Book{Title: title, FilePath: path, AuthorID: author, SeriesID: seriesID, Format: "mp3"})
		require.NoError(t, err)
		require.NoError(t, st.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: path + "/01.mp3", Format: "mp3"}))
		if emb != nil {
			e := *emb
			_, err = st.ModifyBook(b.ID, func(row *database.Book) error {
				row.SeriesID = nil
				row.Series = &e
				return nil
			})
			require.NoError(t, err)
		}
		lib.ids[key] = b.ID
	}
	a, bb := &authorA.ID, &authorB.ID
	add("relink", "Leviathan Wakes", "/lib/A/Leviathan", a, nil, live)
	// Renamed since: the id still matches, so it is a relink.
	add("renamed", "Calibans War", "/lib/A/Caliban", a, nil, &database.Series{ID: live.ID, Name: "Expanse (old name)"})
	// The row is gone; one same-name series with a compatible author.
	add("name-match", "Guards Guards", "/lib/A/Guards", a, nil, &database.Series{ID: 9001, Name: "  discworld "})
	// The row is gone; two same-name series: orphan.
	add("twins", "Foundation", "/lib/A/Foundation", a, nil, &database.Series{ID: 9002, Name: "Foundation"})
	// The row is gone; the only same-name series belongs to another author.
	add("author-clash", "Dune", "/lib/A/Dune", a, nil, &database.Series{ID: 9003, Name: "Dune"})
	_ = bb
	// Nothing matches at all.
	add("orphan", "Lost Book", "/lib/A/Lost", a, nil, &database.Series{ID: 9004, Name: "Vanished Series"})
	// Doctor Who by the stored object's name: the engine guard cannot see it.
	add("dw", "Spare Parts", "/lib/A/Spare", nil, nil, &database.Series{ID: lib.series["dw"].ID, Name: "Doctor Who: The Monthly Adventures"})
	// The user locked the series to empty: held.
	add("user-cleared", "Standalone", "/lib/A/Standalone", a, nil, &database.Series{ID: lib.series["locked"].ID, Name: "Locked Series"})
	empty := `""`
	require.NoError(t, st.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: lib.ids["user-cleared"],
		Field: database.FieldKeySeriesName, OverrideValue: &empty, OverrideLocked: true, UpdatedAt: time.Now()}))
	// Not rows: a linked book, a series-less book with no object, a deleted one.
	linkedID := lib.series["linked"].ID
	add("linked", "Linked", "/lib/A/Linked", a, &linkedID, nil)
	add("plain", "Plain", "/lib/A/Plain", a, nil, nil)
	add("deleted", "Deleted", "/lib/A/Deleted", a, nil, live)
	_, err = st.ModifyBook(lib.ids["deleted"], func(row *database.Book) error {
		yes := true
		row.MarkedForDeletion = &yes
		return nil
	})
	require.NoError(t, err)
	return lib
}

func (lib *relinkLib) fixer() *relinkSeriesFixer {
	return newRelinkSeriesFixer(&Plugin{deps: fakeDeps{store: lib.store}})
}

func (lib *relinkLib) plan(t *testing.T) (*relinkSeriesFixer, *repairs.PlanResult, map[string]repairs.Row) {
	t.Helper()
	f := lib.fixer()
	series, err := lib.store.GetAllSeries()
	require.NoError(t, err)
	res, err := repairs.RunPlan(context.Background(), f, nil,
		repairs.PlanDeps{Guard: lib.store, Series: repairs.SeriesNamesFrom(series)}, &fakeReporter{})
	require.NoError(t, err)
	rows := map[string]repairs.Row{}
	for key, id := range lib.ids {
		for _, r := range res.Rows {
			if r.RowID == id {
				rows[key] = r
			}
		}
	}
	return f, res, rows
}

func (lib *relinkLib) applyDeps(t *testing.T, f *relinkSeriesFixer) repairs.ApplyDeps {
	t.Helper()
	series, err := lib.store.GetAllSeries()
	require.NoError(t, err)
	return repairs.ApplyDeps{Guard: lib.store, Series: repairs.SeriesNamesFrom(series),
		Writer: repairs.NewWriter(lib.store, lib.store, f.ID(), "bulk_update", "repairs-")}
}

func TestRelinkStaleSeries_PlanClasses(t *testing.T) {
	lib := newRelinkLib(t)
	_, res, rows := lib.plan(t)

	for _, key := range []string{"linked", "plain", "deleted"} {
		_, ok := rows[key]
		require.False(t, ok, "%s is not a stale book and must not be a row", key)
	}
	require.Equal(t, 8, res.Total)
	require.Equal(t, map[string]int{relinkClassRelink: 4, relinkClassNameMatch: 1, relinkClassOrphan: 3}, res.ByClass,
		"the census: rows per class")

	r := rows["relink"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, relinkClassRelink, r.Class)
	require.Equal(t, repairs.RiskReview, r.Risk)
	require.Equal(t, itoa(lib.series["live"].ID), r.Proposed["series_id"])

	require.True(t, rows["renamed"].Applicable())
	require.Equal(t, relinkClassRelink, rows["renamed"].Class)
	require.Contains(t, rows["renamed"].Evidence[len(rows["renamed"].Evidence)-1], "differs")

	nm := rows["name-match"]
	require.Equal(t, relinkClassNameMatch, nm.Class)
	require.False(t, nm.Applicable(), "a name match is the owner's call")
	require.Equal(t, junkSkipNeedsManual, nm.Skipped)
	require.Equal(t, itoa(lib.series["named"].ID), nm.Proposed["candidate_series_id"])
	require.Contains(t, nm.Evidence[len(nm.Evidence)-1], itoa(lib.series["named"].ID))

	for _, key := range []string{"twins", "author-clash", "orphan"} {
		require.Equal(t, relinkClassOrphan, rows[key].Class, key)
		require.Equal(t, junkSkipNeedsManual, rows[key].Skipped, key)
	}

	require.Equal(t, relinkClassRelink, rows["dw"].Class, "the class still counts in the census")
	require.Equal(t, repairs.SkipOwnerManual, rows["dw"].Skipped)

	require.Equal(t, relinkClassRelink, rows["user-cleared"].Class)
	require.Equal(t, relinkSkipUserCleared, rows["user-cleared"].Skipped)

	require.Equal(t, 2, res.Applicable)
	require.Equal(t, 4, res.SkippedByKind[junkSkipNeedsManual])
	require.Equal(t, 1, res.SkippedByKind[repairs.SkipOwnerManual])
	require.Equal(t, 1, res.SkippedByKind[relinkSkipUserCleared])
}

func TestRelinkStaleSeries_HistoryClearHoldsTheRow(t *testing.T) {
	lib := newRelinkLib(t)
	prev, empty := `"The Expanse"`, `""`
	require.NoError(t, lib.store.RecordMetadataChange(&database.MetadataChangeRecord{BookID: lib.ids["relink"],
		Field: "series", PreviousValue: &prev, NewValue: &empty, ChangeType: "override", Source: "user_edit", ChangedAt: time.Now()}))
	_, _, rows := lib.plan(t)
	require.Equal(t, relinkSkipUserCleared, rows["relink"].Skipped, rows["relink"].SkipReason)
}

func TestRelinkStaleSeries_ApplyRelinksAndUndoRestores(t *testing.T) {
	lib := newRelinkLib(t)
	f, res, rows := lib.plan(t)
	raw, err := json.Marshal(res)
	require.NoError(t, err)
	var plan repairs.PlanResult
	require.NoError(t, json.Unmarshal(raw, &plan)) // apply reads the stored plan

	before, err := lib.store.GetBookByID(lib.ids["relink"])
	require.NoError(t, err)
	deps := lib.applyDeps(t, f)
	sel := []string{rows["relink"].RowID, rows["orphan"].RowID, rows["name-match"].RowID}
	out, err := repairs.RunApply(context.Background(), f, &plan, "plan-1", sel, false, deps, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, out.Applied, "%v", out.ByOutcome)
	require.Equal(t, 2, out.ByOutcome[repairs.OutcomeNotApplicable])
	require.Equal(t, 1, out.HistoryRows, "exactly one history row: series_id")

	after, err := lib.store.GetBookByID(lib.ids["relink"])
	require.NoError(t, err)
	require.NotNil(t, after.SeriesID)
	require.Equal(t, lib.series["live"].ID, *after.SeriesID)
	require.NotNil(t, after.Series, "the series object is kept")
	require.Equal(t, *before.Series, *after.Series)
	require.Equal(t, before.SeriesSequence, after.SeriesSequence)
	require.Equal(t, before.FilePath, after.FilePath)

	// A resumed run writes nothing twice.
	again, err := repairs.RunApply(context.Background(), f, &plan, "plan-1", sel[:1], false, deps, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 0, again.Applied)
	require.Equal(t, 1, again.ChangedSincePlan)

	// Undo puts SeriesID back to nil, object intact.
	undo, err := metafetch.NewService(lib.store).UndoLastApply(lib.ids["relink"])
	require.NoError(t, err)
	require.Equal(t, []string{"series_id"}, undo.Reverted, "%+v", undo)
	restored, err := lib.store.GetBookByID(lib.ids["relink"])
	require.NoError(t, err)
	require.Nil(t, restored.SeriesID)
	require.NotNil(t, restored.Series)
	require.Equal(t, *before.Series, *restored.Series)
}

func TestRelinkStaleSeries_ReplanRefusesChangedSincePlan(t *testing.T) {
	lib := newRelinkLib(t)
	f, res, rows := lib.plan(t)
	deps := lib.applyDeps(t, f)

	// "relink": someone linked a series meanwhile.
	other := lib.series["linked"].ID
	_, err := lib.store.ModifyBook(lib.ids["relink"], func(b *database.Book) error {
		b.SeriesID = &other
		return nil
	})
	require.NoError(t, err)
	// "renamed": the stored object changed.
	_, err = lib.store.ModifyBook(lib.ids["renamed"], func(b *database.Book) error {
		b.Series = &database.Series{ID: lib.series["live"].ID, Name: "Something Else"}
		return nil
	})
	require.NoError(t, err)

	out, err := repairs.RunApply(context.Background(), f, res, "plan-1",
		[]string{rows["relink"].RowID, rows["renamed"].RowID}, false, deps, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 0, out.Applied)
	require.Equal(t, 2, out.ChangedSincePlan, "%v", out.Rows)
	b, err := lib.store.GetBookByID(lib.ids["relink"])
	require.NoError(t, err)
	require.Equal(t, other, *b.SeriesID, "the newer link is never overwritten")
	b, err = lib.store.GetBookByID(lib.ids["renamed"])
	require.NoError(t, err)
	require.Nil(t, b.SeriesID)
}

func TestRelinkStaleSeries_ApplyCompareAndSetInsideTheWrite(t *testing.T) {
	lib := newRelinkLib(t)
	f, _, rows := lib.plan(t)
	fresh, err := f.Replan(context.Background(), nil, rows["relink"], &fakeReporter{})
	require.NoError(t, err)
	require.True(t, fresh.Applicable())
	// The book changes between Replan and the write.
	_, err = lib.store.ModifyBook(lib.ids["relink"], func(b *database.Book) error {
		b.Series = &database.Series{ID: 1, Name: "Changed"}
		return nil
	})
	require.NoError(t, err)
	w := repairs.NewWriter(lib.store, lib.store, f.ID(), "bulk_update", "repairs-")
	err = f.Apply(context.Background(), w, fresh)
	require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
	require.Equal(t, 0, w.Writes())

	// The target series row vanished between Replan and the write.
	fresh, err = f.Replan(context.Background(), nil, rows["renamed"], &fakeReporter{})
	require.NoError(t, err)
	require.NoError(t, lib.store.DeleteSeries(lib.series["live"].ID))
	err = f.Apply(context.Background(), w, fresh)
	require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
	b, err := lib.store.GetBookByID(lib.ids["renamed"])
	require.NoError(t, err)
	require.Nil(t, b.SeriesID)
}

func TestRelinkStaleSeries_GuardExclusion(t *testing.T) {
	lib := newRelinkLib(t)
	// A relinkable book whose files sit under books/itunes/**: the engine
	// guard skips it at plan time and again at apply time.
	b, err := lib.store.CreateBook(&database.Book{Title: "iTunes Book", FilePath: "/mnt/books/itunes/Author/Book",
		Format: "m4b"})
	require.NoError(t, err)
	require.NoError(t, lib.store.CreateBookFile(&database.BookFile{BookID: b.ID,
		FilePath: "/mnt/books/itunes/Author/Book/book.m4b", Format: "m4b"}))
	live := *lib.series["live"]
	pid := "ABCDEF0123456789"
	_, err = lib.store.ModifyBook(b.ID, func(row *database.Book) error {
		row.Series = &live
		row.ITunesPersistentID = &pid
		return nil
	})
	require.NoError(t, err)
	// A relinkable book whose TITLE names Doctor Who (series is neutral).
	dwt, err := lib.store.CreateBook(&database.Book{Title: "Doctor Who: Placebo Effect", FilePath: "/lib/X/Placebo", Format: "mp3"})
	require.NoError(t, err)
	_, err = lib.store.ModifyBook(dwt.ID, func(row *database.Book) error {
		row.Series = &live
		return nil
	})
	require.NoError(t, err)

	f, res, _ := lib.plan(t)
	byID := map[string]repairs.Row{}
	for _, r := range res.Rows {
		byID[r.RowID] = r
	}
	require.Equal(t, repairs.SkipITunes, byID[b.ID].Skipped, byID[b.ID].SkipReason)
	require.Equal(t, relinkClassRelink, byID[b.ID].Class)
	require.Contains(t, byID[b.ID].Evidence[1], pid)
	require.Equal(t, repairs.SkipOwnerManual, byID[dwt.ID].Skipped, byID[dwt.ID].SkipReason)

	out, err := repairs.RunApply(context.Background(), f, res, "plan-1", []string{b.ID, dwt.ID, lib.ids["dw"]}, false,
		lib.applyDeps(t, f), &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 0, out.Applied)
	require.Equal(t, 3, out.ByOutcome[repairs.OutcomeNotApplicable])
	got, err := lib.store.GetBookByID(b.ID)
	require.NoError(t, err)
	require.Nil(t, got.SeriesID)
}
