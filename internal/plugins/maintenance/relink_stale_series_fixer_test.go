// file: internal/plugins/maintenance/relink_stale_series_fixer_test.go
// version: 1.3.1
// guid: edaf6525-dcbd-426d-b672-c3293aff05f5
// last-edited: 2026-10-03

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/metastate"
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
	// object with SeriesID nil, the state older builds left. This build's
	// write path drops such an object (pebble_store.go holds Series to
	// SeriesID), so the legacy row is seeded the way an older binary wrote it.
	add := func(key, title, path string, author *int, seriesID *int, emb *database.Series) {
		t.Helper()
		b, err := st.CreateBook(&database.Book{Title: title, FilePath: path, AuthorID: author, SeriesID: seriesID, Format: "mp3"})
		require.NoError(t, err)
		require.NoError(t, st.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: path + "/01.mp3", Format: "mp3"}))
		if emb != nil {
			e := *emb
			_, err = st.SeedLegacyBookRowForTest(b.ID, func(row *database.Book) error {
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
	add("relink2", "Nemesis Games", "/lib/A/Nemesis", a, nil, live)
	// Renamed since: the id matches but the names differ, so it is held.
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
	// Seeded, not ModifyBook: an ordinary write would drop the legacy object
	// and "deleted is not a row" would pass for the wrong reason.
	_, err = st.SeedLegacyBookRowForTest(lib.ids["deleted"], func(row *database.Book) error {
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
	require.Equal(t, 9, res.Total)
	require.Equal(t, map[string]int{relinkClassRelink: 3, relinkClassMismatch: 1, relinkClassCleared: 1,
		relinkClassNameMatch: 1, relinkClassOrphan: 3}, res.ByClass, "the census: rows per class")

	r := rows["relink"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, relinkClassRelink, r.Class)
	require.Equal(t, repairs.RiskReview, r.Risk)
	require.Equal(t, itoa(lib.series["live"].ID), r.Proposed["series_id"])

	ren := rows["renamed"]
	require.False(t, ren.Applicable(), "a series row whose name differs from the stored object is held")
	require.Equal(t, relinkClassMismatch, ren.Class)
	require.Equal(t, relinkSkipMismatch, ren.Skipped)

	nm := rows["name-match"]
	require.Equal(t, relinkClassNameMatch, nm.Class)
	require.False(t, nm.Applicable(), "a name match is the owner's call")
	require.Equal(t, junkSkipNeedsManual, nm.Skipped)
	require.Equal(t, itoa(lib.series["named"].ID), nm.Proposed["candidate_series_id"])
	require.Contains(t, strings.Join(nm.Evidence, "\n"), "candidate: series "+itoa(lib.series["named"].ID))

	for _, key := range []string{"twins", "author-clash", "orphan"} {
		require.Equal(t, relinkClassOrphan, rows[key].Class, key)
		require.Equal(t, junkSkipNeedsManual, rows[key].Skipped, key)
	}

	require.Equal(t, relinkClassRelink, rows["dw"].Class, "the class still counts in the census")
	require.Equal(t, repairs.SkipOwnerManual, rows["dw"].Skipped)

	require.Equal(t, relinkClassCleared, rows["user-cleared"].Class)
	require.Equal(t, relinkSkipClearedPrefix+relinkClearFieldLock, rows["user-cleared"].Skipped)

	require.Equal(t, 2, res.Applicable)
	require.Equal(t, 4, res.SkippedByKind[junkSkipNeedsManual])
	require.Equal(t, 1, res.SkippedByKind[repairs.SkipOwnerManual])
	require.Equal(t, 1, res.SkippedByKind[relinkSkipClearedPrefix+relinkClearFieldLock])
	require.Equal(t, 1, res.SkippedByKind[relinkSkipMismatch])
}

func TestRelinkStaleSeries_HistoryClearHoldsTheRow(t *testing.T) {
	lib := newRelinkLib(t)
	prev, empty := `"The Expanse"`, `""`
	require.NoError(t, lib.store.RecordMetadataChange(&database.MetadataChangeRecord{BookID: lib.ids["relink"],
		Field: "series", PreviousValue: &prev, NewValue: &empty, ChangeType: "override", Source: "user_edit", ChangedAt: time.Now()}))
	_, _, rows := lib.plan(t)
	require.Equal(t, relinkClassCleared, rows["relink"].Class)
	require.Equal(t, relinkSkipClearedPrefix+relinkClearManual, rows["relink"].Skipped, rows["relink"].SkipReason)
	require.Contains(t, strings.Join(rows["relink"].Evidence, "\n"), "user_edit", "the clearing source is in the evidence")
}

// TestRelinkStaleSeries_LegacyBlobLockHoldsTheRow: a series_name lock that
// still lives in the pre-migration preference blob holds the row too.
func TestRelinkStaleSeries_LegacyBlobLockHoldsTheRow(t *testing.T) {
	lib := newRelinkLib(t)
	require.NoError(t, lib.store.SetUserPreference(metastate.Key(lib.ids["relink"]),
		`{"series_name":{"override_value":"","override_locked":true}}`))
	_, _, rows := lib.plan(t)
	require.Equal(t, relinkSkipClearedPrefix+relinkClearFieldLock, rows["relink"].Skipped, rows["relink"].SkipReason)
	require.Equal(t, relinkClassCleared, rows["relink"].Class)
}

// TestRelinkStaleSeries_LockAgreeingWithTheSeriesAllowsRelink: a lock whose
// override names the same series does not hold the row.
func TestRelinkStaleSeries_LockAgreeingWithTheSeriesAllowsRelink(t *testing.T) {
	lib := newRelinkLib(t)
	v := `"the  expanse"`
	require.NoError(t, lib.store.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: lib.ids["relink"],
		Field: database.FieldKeySeriesName, OverrideValue: &v, OverrideLocked: true, UpdatedAt: time.Now()}))
	_, _, rows := lib.plan(t)
	require.True(t, rows["relink"].Applicable(), "%s: %s", rows["relink"].Skipped, rows["relink"].SkipReason)
}

// TestRelinkStaleSeries_ApplyRefusesALockAddedAfterReplan: the lock is read
// again inside the write.
func TestRelinkStaleSeries_ApplyRefusesALockAddedAfterReplan(t *testing.T) {
	lib := newRelinkLib(t)
	f, _, rows := lib.plan(t)
	fresh, err := f.Replan(context.Background(), nil, rows["relink"], &fakeReporter{})
	require.NoError(t, err)
	require.True(t, fresh.Applicable())
	empty := `""`
	require.NoError(t, lib.store.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: lib.ids["relink"],
		Field: database.FieldKeySeriesName, OverrideValue: &empty, OverrideLocked: true, UpdatedAt: time.Now()}))
	w := repairs.NewWriter(lib.store, lib.store, f.ID(), "bulk_update", "repairs-")
	require.ErrorIs(t, f.Apply(context.Background(), w, fresh), repairs.ErrChangedSincePlan)
	b, err := lib.store.GetBookByID(lib.ids["relink"])
	require.NoError(t, err)
	require.Nil(t, b.SeriesID)
}

func TestRelinkStaleSeries_ApplyRelinksAndUndoUnlinks(t *testing.T) {
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

	// Undo puts SeriesID back to nil. The store holds Series to SeriesID on
	// every write (PR #3698), so the undo write drops the object too: the
	// legacy "object without a link" state is one this build refuses to
	// write, and undo does not get an exception. The book ends with no
	// series rather than the stale display it had before the relink.
	undo, err := metafetch.NewService(lib.store).UndoLastApply(lib.ids["relink"])
	require.NoError(t, err)
	require.Equal(t, []string{"series_id"}, undo.Reverted, "%+v", undo)
	restored, err := lib.store.GetBookByID(lib.ids["relink"])
	require.NoError(t, err)
	require.Nil(t, restored.SeriesID)
	require.Nil(t, restored.Series, "undo writes no series, never a legacy object without its link")
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
	// "relink2": the stored object changed (an older build's write).
	_, err = lib.store.SeedLegacyBookRowForTest(lib.ids["relink2"], func(b *database.Book) error {
		b.Series = &database.Series{ID: lib.series["live"].ID, Name: "Something Else"}
		return nil
	})
	require.NoError(t, err)

	out, err := repairs.RunApply(context.Background(), f, res, "plan-1",
		[]string{rows["relink"].RowID, rows["relink2"].RowID}, false, deps, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 0, out.Applied)
	require.Equal(t, 2, out.ChangedSincePlan, "%v", out.Rows)
	b, err := lib.store.GetBookByID(lib.ids["relink"])
	require.NoError(t, err)
	require.Equal(t, other, *b.SeriesID, "the newer link is never overwritten")
	b, err = lib.store.GetBookByID(lib.ids["relink2"])
	require.NoError(t, err)
	require.Nil(t, b.SeriesID)
}

func TestRelinkStaleSeries_ApplyCompareAndSetInsideTheWrite(t *testing.T) {
	lib := newRelinkLib(t)
	f, _, rows := lib.plan(t)
	fresh, err := f.Replan(context.Background(), nil, rows["relink"], &fakeReporter{})
	require.NoError(t, err)
	require.True(t, fresh.Applicable())
	// The book changes between Replan and the write (an older build's write).
	_, err = lib.store.SeedLegacyBookRowForTest(lib.ids["relink"], func(b *database.Book) error {
		b.Series = &database.Series{ID: 1, Name: "Changed"}
		return nil
	})
	require.NoError(t, err)
	w := repairs.NewWriter(lib.store, lib.store, f.ID(), "bulk_update", "repairs-")
	err = f.Apply(context.Background(), w, fresh)
	require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
	require.Equal(t, 0, w.Writes())

	// The target series row vanished between Replan and the write.
	fresh, err = f.Replan(context.Background(), nil, rows["relink2"], &fakeReporter{})
	require.NoError(t, err)
	require.NoError(t, lib.store.DeleteSeries(lib.series["live"].ID))
	err = f.Apply(context.Background(), w, fresh)
	require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
	b, err := lib.store.GetBookByID(lib.ids["relink2"])
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
	_, err = lib.store.SeedLegacyBookRowForTest(b.ID, func(row *database.Book) error {
		row.Series = &live
		row.ITunesPersistentID = &pid
		return nil
	})
	require.NoError(t, err)
	// A relinkable book whose TITLE names Doctor Who (series is neutral).
	dwt, err := lib.store.CreateBook(&database.Book{Title: "Doctor Who: Placebo Effect", FilePath: "/lib/X/Placebo", Format: "mp3"})
	require.NoError(t, err)
	_, err = lib.store.SeedLegacyBookRowForTest(dwt.ID, func(row *database.Book) error {
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

// The history tests below are the review probes of PR #3703. History is keyed
// by book, FIELD and time, so the whole-book read is ordered field by field;
// the fixer must find the newest series row by time across fields.

// TestRelinkStaleSeries_UserClearAfterTheFixersRelinkHolds: the fixer
// relinks, then the user clears the series in the editor. The next plan must
// hold the row, not relink it again.
func TestRelinkStaleSeries_UserClearAfterTheFixersRelinkHolds(t *testing.T) {
	lib := newRelinkLib(t)
	f, res, rows := lib.plan(t)
	raw, err := json.Marshal(res)
	require.NoError(t, err)
	var plan repairs.PlanResult
	require.NoError(t, json.Unmarshal(raw, &plan))
	out, err := repairs.RunApply(context.Background(), f, &plan, "plan-1", []string{rows["relink"].RowID}, false,
		lib.applyDeps(t, f), &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, out.Applied)
	id := lib.ids["relink"]
	time.Sleep(2 * time.Millisecond)
	// Editor clear: SeriesID nil plus the history row the editor writes.
	var pre *database.Book
	post, err := lib.store.ModifyBook(id, func(b *database.Book) error {
		pre, _ = database.SnapshotBook(b)
		b.SeriesID = nil
		return nil
	})
	require.NoError(t, err)
	_, err = database.RecordBookEditHistory(lib.store, pre, post, database.ChangeTypeManual, "manual", time.Now(), nil)
	require.NoError(t, err)
	b, err := lib.store.GetBookByID(id)
	require.NoError(t, err)
	require.Nil(t, b.SeriesID)
	require.Nil(t, b.Series, "this build's clear drops the object with the link")
	_, _, rowsNow := lib.plan(t)
	_, listed := rowsNow["relink"]
	require.False(t, listed, "a series this build cleared leaves nothing to relink")

	// An older build's clear left the object behind (preserve-on-nil). With
	// that row and the same history, the clear must still win over the
	// fixer's own series_id row.
	_, err = lib.store.SeedLegacyBookRowForTest(id, func(b *database.Book) error {
		b.Series = pre.Series
		return nil
	})
	require.NoError(t, err)
	_, _, rows2 := lib.plan(t)
	r := rows2["relink"]
	require.False(t, r.Applicable(), "the user's later clear must win over the fixer's own series_id row")
	require.Equal(t, relinkClassCleared, r.Class)
	require.Equal(t, relinkSkipClearedPrefix+relinkClearManual, r.Skipped)
}

// TestRelinkStaleSeries_StaleObjectNamingThePreviousSeriesHolds: the book
// moved from series A to B by a SeriesID-only writer (the object still names
// A), then B was unlinked and deleted with no history. Relinking to A would
// put the book back in a series the user moved it off.
func TestRelinkStaleSeries_StaleObjectNamingThePreviousSeriesHolds(t *testing.T) {
	lib := newRelinkLib(t)
	st := lib.store
	a := lib.series["named"]
	bser, err := st.CreateSeries("Temp Series B", nil)
	require.NoError(t, err)
	bk, err := st.CreateBook(&database.Book{Title: "Mover", FilePath: "/lib/A/Mover", SeriesID: &a.ID, Format: "mp3"})
	require.NoError(t, err)
	_, err = st.ModifyBook(bk.ID, func(row *database.Book) error {
		row.Series = &database.Series{ID: a.ID, Name: a.Name, AuthorID: a.AuthorID}
		return nil
	})
	require.NoError(t, err)
	// Both writes below are an older build's: the move to B by SeriesID
	// alone and the unlink each left the object naming A. This build's
	// write path would drop that object, so they are seeded.
	var pre *database.Book
	post, err := st.SeedLegacyBookRowForTest(bk.ID, func(row *database.Book) error {
		pre, _ = database.SnapshotBook(row)
		id := bser.ID
		row.SeriesID = &id
		return nil
	})
	require.NoError(t, err)
	_, err = database.RecordBookEditHistory(st, pre, post, database.ChangeTypeManual, "manual", time.Now(), nil)
	require.NoError(t, err)
	_, err = st.SeedLegacyBookRowForTest(bk.ID, func(row *database.Book) error {
		row.SeriesID, row.SeriesSequence = nil, nil
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, st.DeleteSeries(bser.ID))

	_, res, _ := lib.plan(t)
	var r repairs.Row
	for _, row := range res.Rows {
		if row.RowID == bk.ID {
			r = row
		}
	}
	require.False(t, r.Applicable(), "relinks to series A, which the user moved the book off")
	require.Equal(t, relinkClassMismatch, r.Class)
	require.Equal(t, relinkSkipMismatch, r.Skipped)
}

// TestRelinkStaleSeries_ManyOtherHistoryRowsCannotHideAClear: rows of other
// fields never push a series clear out of a window.
func TestRelinkStaleSeries_ManyOtherHistoryRowsCannotHideAClear(t *testing.T) {
	lib := newRelinkLib(t)
	id := lib.ids["relink"]
	prev, empty := `"The Expanse"`, `""`
	require.NoError(t, lib.store.RecordMetadataChange(&database.MetadataChangeRecord{BookID: id,
		Field: "series", PreviousValue: &prev, NewValue: &empty, ChangeType: "manual", Source: "manual", ChangedAt: time.Now().Add(-time.Hour)}))
	for i := 0; i < 201; i++ {
		v := fmt.Sprintf(`"t%d"`, i)
		require.NoError(t, lib.store.RecordMetadataChange(&database.MetadataChangeRecord{BookID: id,
			Field: "title", NewValue: &v, Source: "x", ChangedAt: time.Now().Add(-time.Duration(i) * time.Second)}))
	}
	_, _, rows := lib.plan(t)
	require.False(t, rows["relink"].Applicable(), "201 title rows must not hide the series clear")
	require.Equal(t, relinkClassCleared, rows["relink"].Class)
}

// TestRelinkStaleSeries_OlderSeriesIDRowCannotShadowANewerClear: an older
// Writer series_id row sorts before the newer editor "series" clear by field
// name; time decides.
func TestRelinkStaleSeries_OlderSeriesIDRowCannotShadowANewerClear(t *testing.T) {
	lib := newRelinkLib(t)
	id := lib.ids["relink"]
	old, nw := `""`, `"5"`
	require.NoError(t, lib.store.RecordMetadataChange(&database.MetadataChangeRecord{BookID: id,
		Field: "series_id", PreviousValue: &old, NewValue: &nw, Source: "maintenance.junk-author", ChangedAt: time.Now().Add(-48 * time.Hour)}))
	prev, empty := `"The Expanse"`, `""`
	require.NoError(t, lib.store.RecordMetadataChange(&database.MetadataChangeRecord{BookID: id,
		Field: "series", PreviousValue: &prev, NewValue: &empty, ChangeType: "manual", Source: "manual", ChangedAt: time.Now()}))
	_, _, rows := lib.plan(t)
	require.False(t, rows["relink"].Applicable())
	require.Equal(t, relinkSkipClearedPrefix+relinkClearManual, rows["relink"].Skipped)
}

// TestRelinkStaleSeries_EditorSetThenClearHolds: T1 the user sets the series
// (series_name override row, lock, "series" row); T2 the user clears it (a
// "series" -> "" row only; the old lock stays and agrees with the object).
func TestRelinkStaleSeries_EditorSetThenClearHolds(t *testing.T) {
	lib := newRelinkLib(t)
	id := lib.ids["relink"]
	t1 := time.Now().Add(-24 * time.Hour)
	v := `"The Expanse"`
	require.NoError(t, lib.store.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: id,
		Field: database.FieldKeySeriesName, OverrideValue: &v, OverrideLocked: true, UpdatedAt: t1}))
	empty := `""`
	require.NoError(t, lib.store.RecordMetadataChange(&database.MetadataChangeRecord{BookID: id,
		Field: "series_name", PreviousValue: &empty, NewValue: &v, ChangeType: "override", Source: "user_edit", ChangedAt: t1}))
	require.NoError(t, lib.store.RecordMetadataChange(&database.MetadataChangeRecord{BookID: id,
		Field: "series", PreviousValue: &empty, NewValue: &v, ChangeType: "manual", Source: "manual", ChangedAt: t1}))
	require.NoError(t, lib.store.RecordMetadataChange(&database.MetadataChangeRecord{BookID: id,
		Field: "series", PreviousValue: &v, NewValue: &empty, ChangeType: "manual", Source: "manual", ChangedAt: time.Now()}))
	_, _, rows := lib.plan(t)
	require.False(t, rows["relink"].Applicable(), "the T1 series_name row must not shadow the T2 clear")
	require.Equal(t, relinkSkipClearedPrefix+relinkClearManual, rows["relink"].Skipped)
}

// TestRelinkStaleSeries_NullHistoryValueHolds: a JSON null new value is a
// clear, not a reason to stop looking.
func TestRelinkStaleSeries_NullHistoryValueHolds(t *testing.T) {
	lib := newRelinkLib(t)
	prev, null := `"The Expanse"`, `null`
	require.NoError(t, lib.store.RecordMetadataChange(&database.MetadataChangeRecord{BookID: lib.ids["relink"],
		Field: "series_name", PreviousValue: &prev, NewValue: &null, ChangeType: "batch", Source: "batch", ChangedAt: time.Now()}))
	_, _, rows := lib.plan(t)
	require.Equal(t, relinkSkipClearedPrefix+relinkClearBatch, rows["relink"].Skipped)
}

// TestRelinkStaleSeries_HistoryAgreeingWithTheObjectStaysApplicable: a newest
// series row naming the same series is not a clear.
func TestRelinkStaleSeries_HistoryAgreeingWithTheObjectStaysApplicable(t *testing.T) {
	lib := newRelinkLib(t)
	empty, v := `""`, `"The Expanse"`
	sid := lib.series["live"].ID
	require.NoError(t, lib.store.RecordMetadataChange(&database.MetadataChangeRecord{BookID: lib.ids["relink"],
		Field: "series", PreviousValue: &empty, NewValue: &v, NewRef: &database.MetadataChangeRef{SeriesID: &sid},
		ChangeType: "fetched", Source: "Audible", ChangedAt: time.Now()}))
	_, _, rows := lib.plan(t)
	require.True(t, rows["relink"].Applicable(), "%s: %s", rows["relink"].Skipped, rows["relink"].SkipReason)
	require.Equal(t, relinkClassRelink, rows["relink"].Class)
}

// TestRelinkStaleSeries_IncompatibleSeriesAuthorHolds: the series row by id
// belongs to another author.
func TestRelinkStaleSeries_IncompatibleSeriesAuthorHolds(t *testing.T) {
	lib := newRelinkLib(t)
	dune := lib.series["other-author"] // author B; the book is author A's
	_, err := lib.store.SeedLegacyBookRowForTest(lib.ids["relink"], func(b *database.Book) error {
		b.Series = &database.Series{ID: dune.ID, Name: dune.Name, AuthorID: dune.AuthorID}
		return nil
	})
	require.NoError(t, err)
	_, _, rows := lib.plan(t)
	require.Equal(t, relinkClassMismatch, rows["relink"].Class)
	require.Equal(t, relinkSkipMismatch, rows["relink"].Skipped)
}

// TestRelinkStaleSeries_CensusGroupsClearsBySource: the plan summary counts
// the cleared rows by who cleared them.
func TestRelinkStaleSeries_CensusGroupsClearsBySource(t *testing.T) {
	lib := newRelinkLib(t)
	prev, empty := `"The Expanse"`, `""`
	for key, src := range map[string]string{"relink": "manual", "relink2": "maintenance.junk-author"} {
		require.NoError(t, lib.store.RecordMetadataChange(&database.MetadataChangeRecord{BookID: lib.ids[key],
			Field: "series_id", PreviousValue: &prev, NewValue: &empty, ChangeType: "bulk_update", Source: src, ChangedAt: time.Now()}))
	}
	_, res, rows := lib.plan(t)
	require.Equal(t, 3, res.ByClass[relinkClassCleared], "two by history, one by field lock")
	require.Equal(t, 1, res.SkippedByKind[relinkSkipClearedPrefix+relinkClearManual])
	require.Equal(t, 1, res.SkippedByKind[relinkSkipClearedPrefix+relinkClearFixer])
	require.Equal(t, 1, res.SkippedByKind[relinkSkipClearedPrefix+relinkClearFieldLock])
	require.Contains(t, strings.Join(rows["relink2"].Evidence, "\n"), "maintenance.junk-author")
	require.Equal(t, 0, res.Applicable)
}

// TestRelinkStaleSeries_ApplyRefusesAClearRecordedAfterReplan: history is
// read again inside the write.
func TestRelinkStaleSeries_ApplyRefusesAClearRecordedAfterReplan(t *testing.T) {
	lib := newRelinkLib(t)
	f, _, rows := lib.plan(t)
	fresh, err := f.Replan(context.Background(), nil, rows["relink"], &fakeReporter{})
	require.NoError(t, err)
	require.True(t, fresh.Applicable())
	prev, empty := `"The Expanse"`, `""`
	require.NoError(t, lib.store.RecordMetadataChange(&database.MetadataChangeRecord{BookID: lib.ids["relink"],
		Field: "series", PreviousValue: &prev, NewValue: &empty, ChangeType: "manual", Source: "manual", ChangedAt: time.Now()}))
	w := repairs.NewWriter(lib.store, lib.store, f.ID(), "bulk_update", "repairs-")
	require.ErrorIs(t, f.Apply(context.Background(), w, fresh), repairs.ErrChangedSincePlan)
	require.Equal(t, 0, w.Writes())
}

// TestRelinkStaleSeries_OperationRevertOfAWriterLinkHolds: a series link made
// through the Writer (junk-author linkSeries journals series_id with an empty
// old value) is reverted by the operation revert. The revert must leave a
// history row, so the fixer holds the book instead of re-proposing the link
// the owner just reverted.
func TestRelinkStaleSeries_OperationRevertOfAWriterLinkHolds(t *testing.T) {
	lib := newRelinkLib(t)
	sid := lib.series["live"].ID
	id := lib.ids["relink"]
	w := repairs.NewWriter(lib.store, lib.store, "maintenance.junk-author", "bulk_update", "repairs-")
	_, err := w.Modify(id, func(b *database.Book) error {
		x := sid
		b.SeriesID = &x
		return nil
	})
	require.NoError(t, err)
	const opID = "op-relink-revert"
	require.NoError(t, lib.store.CreateOperationChange(&database.OperationChange{ID: "c1", OperationID: opID, BookID: id,
		ChangeType: "metadata_update", FieldName: "series_id", OldValue: "", NewValue: itoa(sid)}))
	rev, err := audiobooks.NewRevertService(lib.store).RevertOperation(opID)
	require.NoError(t, err)
	require.Equal(t, 1, rev.Restored, "%+v", rev)
	b, err := lib.store.GetBookByID(id)
	require.NoError(t, err)
	require.Nil(t, b.SeriesID)
	require.Nil(t, b.Series, "this build's revert drops the object with the link")
	_, _, rowsNow := lib.plan(t)
	_, listed := rowsNow["relink"]
	require.False(t, listed, "a link this build reverted leaves nothing to relink")

	// An older build's revert left the object behind (preserve-on-nil). The
	// revert's history row must still hold that book.
	live := *lib.series["live"]
	_, err = lib.store.SeedLegacyBookRowForTest(id, func(b *database.Book) error {
		b.Series = &live
		return nil
	})
	require.NoError(t, err)
	_, _, rows := lib.plan(t)
	r := rows["relink"]
	require.False(t, r.Applicable(), "the reverted link must not be re-proposed: %v", r.Evidence)
	require.Equal(t, relinkClassCleared, r.Class)
	require.Equal(t, relinkSkipClearedPrefix+relinkClearOpRevert, r.Skipped)
	require.Contains(t, strings.Join(r.Evidence, "\n"), "operation_revert")
}

// TestRelinkStaleSeries_RenameAfterAWarmReplanIsRefused: Replan reads the
// target series row fresh, so a rename after an earlier Replan warmed the
// index changes the fingerprint, and Apply refuses a row whose series no
// longer matches the object even when handed it.
func TestRelinkStaleSeries_RenameAfterAWarmReplanIsRefused(t *testing.T) {
	lib := newRelinkLib(t)
	f, _, rows := lib.plan(t)
	planned := rows["relink"]
	warm, err := f.Replan(context.Background(), nil, planned, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, planned.Fingerprint, warm.Fingerprint)
	require.NoError(t, lib.store.UpdateSeriesName(lib.series["live"].ID, "Totally Different"))
	fresh, err := f.Replan(context.Background(), nil, planned, &fakeReporter{})
	require.NoError(t, err)
	require.NotEqual(t, planned.Fingerprint, fresh.Fingerprint, "the rename must change the fingerprint")
	require.False(t, fresh.Applicable())
	require.Equal(t, relinkClassMismatch, fresh.Class)

	// Apply re-checks the fresh row too: the decision Replan made before the
	// rename is refused.
	w := lib.applyDeps(t, f).Writer
	require.ErrorIs(t, f.Apply(context.Background(), w, warm), repairs.ErrChangedSincePlan)
	b, err := lib.store.GetBookByID(lib.ids["relink"])
	require.NoError(t, err)
	require.Nil(t, b.SeriesID)
}

// TestRelinkStaleSeries_OverrideRemovedIsNotAClear: a series_name override
// row with no value means the override was removed, not that the series was
// cleared.
func TestRelinkStaleSeries_OverrideRemovedIsNotAClear(t *testing.T) {
	lib := newRelinkLib(t)
	prev := `"The Expanse"`
	require.NoError(t, lib.store.RecordMetadataChange(&database.MetadataChangeRecord{BookID: lib.ids["relink"],
		Field: database.FieldKeySeriesName, PreviousValue: &prev, NewValue: nil,
		ChangeType: "override", Source: "user_edit", ChangedAt: time.Now()}))
	_, _, rows := lib.plan(t)
	require.True(t, rows["relink"].Applicable(), "%s: %s", rows["relink"].Skipped, rows["relink"].SkipReason)
}

func TestRelinkClearBucket(t *testing.T) {
	for _, tc := range []struct{ source, changeType, want string }{
		{"operation_revert", "undo", relinkClearOpRevert},
		{"undo-last-apply", "undo", relinkClearUndo},
		{"manual", "manual", relinkClearManual},
		{"user_edit", "override", relinkClearManual},
		{"batch", "batch", relinkClearBatch},
		{"maintenance.junk-author", "bulk_update", relinkClearFixer},
		{"Audible", "fetched", relinkClearMetadataApply},
		{"something", "else", relinkClearOther},
	} {
		require.Equal(t, tc.want, relinkClearBucket(tc.source, tc.changeType), "%+v", tc)
	}
}
