// file: internal/audiobooks/revert_series_denumber_test.go
// version: 1.2.0
// guid: e1a47c92-5b08-4d36-a7f3-9c60d2b84e15
// last-edited: 2026-11-16

package audiobooks

import (
	"path/filepath"
	"strconv"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// denumberJournal is what maintenance.series-denumber journals for one plan: the
// base series' creation, each book's link (and position) move, and the emptied
// numbered series' deletion, written in that order.
type denumberFixture struct {
	store *database.PebbleStore
	op    string
	base  *database.Series
	src   *database.Series
}

func newDenumberFixture(t *testing.T) *denumberFixture {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.WaitForWarmup()
	store.UseMemDB = true

	author, err := store.CreateAuthor("Alfred Pennyworth")
	if err != nil {
		t.Fatal(err)
	}
	aid := author.ID
	src, err := store.CreateSeries("Court of Owls 05", &aid)
	if err != nil {
		t.Fatal(err)
	}
	f := &denumberFixture{store: store, op: "op-denumber", src: src}
	// One book in the numbered series, no position of its own.
	if _, err := store.CreateBook(&database.Book{ID: "b1", Title: "The Talon", SeriesID: &src.ID}); err != nil {
		t.Fatal(err)
	}

	// The apply: create the base, move the book, journal, delete the source.
	f.base, err = store.CreateSeries("Court of Owls", &aid)
	if err != nil {
		t.Fatal(err)
	}
	baseJSON, _ := undo.NewSeriesSnapshotJSON(undo.SeriesSnapshot{ID: f.base.ID, Name: f.base.Name, AuthorID: f.base.AuthorID})
	srcJSON, _ := undo.NewSeriesSnapshotJSON(undo.SeriesSnapshot{ID: src.ID, Name: src.Name, AuthorID: src.AuthorID})
	rec := func(c *database.OperationChange) {
		t.Helper()
		c.OperationID = f.op
		if err := database.RecordOperationChange(store, c); err != nil {
			t.Fatalf("journal %s: %v", c.ChangeType, err)
		}
	}
	rec(&database.OperationChange{ID: "r1", SeriesID: &f.base.ID, ChangeType: undo.ChangeTypeSeriesCreate, NewValue: baseJSON})
	rec(&database.OperationChange{ID: "r2", BookID: "b1", ChangeType: "metadata_update", FieldName: "series_id",
		OldValue: strconv.Itoa(src.ID), NewValue: strconv.Itoa(f.base.ID)})
	rec(&database.OperationChange{ID: "r3", BookID: "b1", ChangeType: "metadata_update", FieldName: "series_sequence",
		OldValue: "", NewValue: "5"})
	if _, err := store.ModifyBook("b1", func(b *database.Book) error {
		b.SeriesID, b.SeriesSequence = &f.base.ID, new(5)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rec(&database.OperationChange{ID: "r4", SeriesID: &src.ID, ChangeType: undo.ChangeTypeSeriesRowDelete, OldValue: srcJSON})
	if err := store.DeleteSeries(src.ID); err != nil {
		t.Fatal(err)
	}
	return f
}

// The whole apply is undone: the numbered series comes back under its own id,
// the book goes back into it with no position, and the base series made for
// the move is removed. The preflight predicts exactly that, with no refusals,
// although the numbered series does not exist when it runs.
func TestRevertSeriesDenumber_RoundTrip(t *testing.T) {
	f := newDenumberFixture(t)
	rows, err := f.store.GetOperationChanges(f.op)
	if err != nil {
		t.Fatal(err)
	}
	report, err := undo.PreflightUndoConflictsFromChanges(f.store, rows)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if len(report.SeriesDeleted)+len(report.CheckFailed)+len(report.ContentChanged) != 0 || report.Safe != 4 {
		t.Fatalf("preflight = %+v, want 4 safe rows and no conflicts", report)
	}

	res, err := NewRevertService(f.store).RevertOperation(f.op)
	if err != nil {
		t.Fatalf("RevertOperation: %v (%+v)", err, res)
	}
	if res.Restored != 4 || res.Failed != 0 || res.NotRestorable != 0 {
		t.Fatalf("result = %+v, want 4 restored", res)
	}
	if !res.RestoredSeriesRows() || res.RestoredTypes[undo.ChangeTypeSeriesRowDelete] != 1 ||
		res.RestoredTypes[undo.ChangeTypeSeriesCreate] != 1 || res.RestoredTypes[undo.RestoredSeriesLink] != 1 {
		t.Fatalf("restored types = %v, want one each of series_create, series_row_delete and a series link (the server drops the series caches on any of them)", res.RestoredTypes)
	}
	back, _ := f.store.GetSeriesByID(f.src.ID)
	if back == nil || back.Name != "Court of Owls 05" {
		t.Fatalf("numbered series %d not restored under its id: %+v", f.src.ID, back)
	}
	b, _ := f.store.GetBookByID("b1")
	if b.SeriesID == nil || *b.SeriesID != f.src.ID || b.SeriesSequence != nil {
		t.Fatalf("book = series %v seq %v, want series %d and no position", b.SeriesID, b.SeriesSequence, f.src.ID)
	}
	if gone, _ := f.store.GetSeriesByID(f.base.ID); gone != nil {
		t.Fatalf("base series %d made for the move still exists", f.base.ID)
	}
}

// Re-running a finished revert is refused as already reverted and changes nothing.
func TestRevertSeriesDenumber_RevertTwice(t *testing.T) {
	f := newDenumberFixture(t)
	svc := NewRevertService(f.store)
	if _, err := svc.RevertOperation(f.op); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RevertOperation(f.op); err == nil {
		t.Fatalf("second revert was accepted")
	}
	b, _ := f.store.GetBookByID("b1")
	if b.SeriesID == nil || *b.SeriesID != f.src.ID {
		t.Fatalf("book series = %v after the second revert, want %d", b.SeriesID, f.src.ID)
	}
}

// A series that took the numbered series' name since refuses the restore, and
// with it the book's link row (the series is not coming back); the book stays
// where it is and the preflight says so before anything is written.
func TestRevertSeriesDenumber_NameTakenRefusesTheRestoreAndTheLink(t *testing.T) {
	f := newDenumberFixture(t)
	aid := f.src.AuthorID
	if _, err := f.store.CreateSeries("Court of Owls 05", aid); err != nil {
		t.Fatal(err)
	}
	rows, _ := f.store.GetOperationChanges(f.op)
	report, err := undo.PreflightUndoConflictsFromChanges(f.store, rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.SeriesNameTaken) == 0 || len(report.SeriesDeleted) == 0 {
		t.Fatalf("preflight = %+v, want the restore refused (name taken) and the book's link refused (series deleted)", report)
	}

	res, _ := NewRevertService(f.store).RevertOperation(f.op)
	if res.Restored == 4 {
		t.Fatalf("result = %+v, want a refusal", res)
	}
	b, _ := f.store.GetBookByID("b1")
	if b.SeriesID == nil || *b.SeriesID != f.base.ID {
		t.Fatalf("book moved to series %v though its series could not come back", b.SeriesID)
	}
	if kept, _ := f.store.GetSeriesByID(f.base.ID); kept == nil {
		t.Fatalf("base series deleted while it still holds the book")
	}
}

// The apply journals a book's rows BEFORE it writes the book. A write that then
// fails leaves rows describing a move that never happened; the book is still
// where the rows' old values say. The revert must read that as already restored
// and carry on, not refuse the operation: one failed write must not make the
// whole apply irreversible.
func TestRevertSeriesDenumber_RowsOfAMoveThatWasNeverWrittenAreHarmless(t *testing.T) {
	f := newDenumberFixture(t)
	aid := f.src.AuthorID
	src2, err := f.store.CreateSeries("Dark Knight 03", aid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateBook(&database.Book{ID: "b2", Title: "Year One", SeriesID: &src2.ID}); err != nil {
		t.Fatal(err)
	}
	// Journaled, never written: b2 stays in its numbered series with no position.
	for i, c := range []*database.OperationChange{
		{ID: "r5", BookID: "b2", ChangeType: "metadata_update", FieldName: "series_id", OldValue: strconv.Itoa(src2.ID), NewValue: strconv.Itoa(f.base.ID)},
		{ID: "r6", BookID: "b2", ChangeType: "metadata_update", FieldName: "series_sequence", OldValue: "", NewValue: "3"},
	} {
		c.OperationID = f.op
		if err := database.RecordOperationChange(f.store, c); err != nil {
			t.Fatalf("journal row %d: %v", i, err)
		}
	}

	rows, _ := f.store.GetOperationChanges(f.op)
	if _, err := undo.PreflightUndoConflictsFromChanges(f.store, rows); err != nil {
		t.Fatalf("preflight refused the operation: %v", err)
	}
	res, err := NewRevertService(f.store).RevertOperation(f.op)
	if err != nil {
		t.Fatalf("RevertOperation: %v (%+v)", err, res)
	}
	if res.Failed != 0 || res.NotRestorable != 0 {
		t.Fatalf("result = %+v, want no failures", res)
	}
	b2, _ := f.store.GetBookByID("b2")
	if b2.SeriesID == nil || *b2.SeriesID != src2.ID || b2.SeriesSequence != nil {
		t.Fatalf("b2 = series %v position %v, want series %d and none", b2.SeriesID, b2.SeriesSequence, src2.ID)
	}
	b1, _ := f.store.GetBookByID("b1")
	if b1.SeriesID == nil || *b1.SeriesID != f.src.ID {
		t.Fatalf("b1 not restored beside the harmless rows: %v", b1.SeriesID)
	}
}

// Only series rows make a revert drop the series caches; a book field revert
// does not.
func TestRevertResult_RestoredSeriesRows(t *testing.T) {
	cases := []struct {
		name  string
		types map[string]int
		want  bool
	}{
		{"nothing restored", nil, false},
		{"a tag write", map[string]int{"tag_write": 3, "metadata_update": 2}, false},
		{"a series rename", map[string]int{undo.ChangeTypeSeriesRename: 1}, true},
		{"a created series removed", map[string]int{undo.ChangeTypeSeriesCreate: 1}, true},
		{"a deleted series recreated", map[string]int{undo.ChangeTypeSeriesRowDelete: 1}, true},
		{"a series link restored", map[string]int{"metadata_update": 1, undo.RestoredSeriesLink: 1}, true},
	}
	for _, tc := range cases {
		if got := (&RevertResult{RestoredTypes: tc.types}).RestoredSeriesRows(); got != tc.want {
			t.Errorf("%s: RestoredSeriesRows = %v, want %v", tc.name, got, tc.want)
		}
	}
	var none *RevertResult
	if none.RestoredSeriesRows() {
		t.Errorf("a nil result reported series rows")
	}
}
