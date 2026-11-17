// file: internal/audiobooks/revert_series_denumber_test.go
// version: 1.0.0
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
