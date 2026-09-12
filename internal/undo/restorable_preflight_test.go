// file: internal/undo/restorable_preflight_test.go
// version: 1.3.0
// guid: e41b8d2a-6c07-4f95-a3e8-1d9c5b7f2a60
// last-edited: 2026-09-12

package undo

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The preflight must predict the revert for series_id rows: a row whose old
// series still exists (or whose old value is "no series") is Safe, a row whose
// old series was deleted is a series_deleted conflict, and version_group_id /
// series_name rows are record-only.
func TestPreflightUndoConflicts_SeriesIDRows(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	live, err := store.CreateSeries("Live", nil)
	if err != nil {
		t.Fatalf("create series: %v", err)
	}
	gone, err := store.CreateSeries("Gone", nil)
	if err != nil {
		t.Fatalf("create series: %v", err)
	}
	if err := store.DeleteSeries(gone.ID); err != nil {
		t.Fatalf("delete series: %v", err)
	}
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/b.m4b", Format: "m4b", SeriesID: &live.ID})
	if err != nil {
		t.Fatalf("create book: %v", err)
	}
	keep := strconv.Itoa(live.ID)
	rows := []*database.OperationChange{
		{ID: "c1", OperationID: "op1", BookID: book.ID, ChangeType: "metadata_update", FieldName: "series_id", OldValue: keep, NewValue: keep},
		{ID: "c2", OperationID: "op1", BookID: book.ID, ChangeType: "metadata_update", FieldName: "series_id", OldValue: "", NewValue: keep},
		{ID: "c3", OperationID: "op1", BookID: book.ID, ChangeType: "metadata_update", FieldName: "series_id", OldValue: strconv.Itoa(gone.ID), NewValue: keep},
		{ID: "c4", OperationID: "op1", BookID: book.ID, ChangeType: "metadata_update", FieldName: "version_group_id", NewValue: "g1"},
		{ID: "c5", OperationID: "op1", ChangeType: "metadata_update", FieldName: "series_name", OldValue: "Old", NewValue: "Live"},
	}
	for _, r := range rows {
		if err := store.CreateOperationChange(r); err != nil {
			t.Fatalf("create change %s: %v", r.ID, err)
		}
	}

	report, err := PreflightUndoConflicts(store, "op1")
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if report.Safe != 2 {
		t.Errorf("safe = %d, want 2 (c1 live series, c2 no series)", report.Safe)
	}
	if len(report.SeriesDeleted) != 1 || report.SeriesDeleted[0].ChangeID != "c3" {
		t.Errorf("series_deleted = %+v, want [c3]", report.SeriesDeleted)
	}
	if report.NotRestorable != 2 ||
		report.NotRestorableTypes["metadata_update:version_group_id"] != 1 ||
		report.NotRestorableTypes["metadata_update:series_name"] != 1 {
		t.Errorf("not_restorable = %d %v, want version_group_id:1 series_name:1",
			report.NotRestorable, report.NotRestorableTypes)
	}
}

// seriesErrStore fails every series read.
type seriesErrStore struct{ *database.PebbleStore }

func (seriesErrStore) GetSeriesByID(int) (*database.Series, error) {
	return nil, errors.New("pebble: closed")
}

// A series lookup that errors fails closed in the preflight too: the row is
// never Safe, it is filed under check_failed with the lookup reason.
func TestPreflightUndoConflicts_SeriesLookupErrorFailsClosed(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/b.m4b", Format: "m4b"})
	if err != nil {
		t.Fatalf("create book: %v", err)
	}
	ten := 10
	for _, r := range []*database.OperationChange{
		{ID: "c1", OperationID: "op1", BookID: book.ID, ChangeType: "metadata_update", FieldName: "series_id", OldValue: "4", NewValue: "9"},
		{ID: "c2", OperationID: "op1", ChangeType: ChangeTypeSeriesRename, FieldName: "series_name", SeriesID: &ten, OldValue: "A", NewValue: "B"},
		{ID: "c3", OperationID: "op1", BookID: book.ID, ChangeType: "metadata_update", FieldName: "series_id", OldValue: "12 (Foo)", NewValue: "9"},
	} {
		if err := store.CreateOperationChange(r); err != nil {
			t.Fatalf("create change %s: %v", r.ID, err)
		}
	}

	report, err := PreflightUndoConflicts(seriesErrStore{store}, "op1")
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if report.Safe != 0 || len(report.CheckFailed) != 3 {
		t.Fatalf("safe = %d, check_failed = %+v, want 0 and 3 rows", report.Safe, report.CheckFailed)
	}
	want := map[string]string{"c1": ReasonSeriesLookupFailed, "c2": ReasonSeriesLookupFailed, "c3": ReasonOldValueUnparsable}
	for _, item := range report.CheckFailed {
		if item.Reason != want[item.ChangeID] {
			t.Errorf("%s reason = %q, want %q", item.ChangeID, item.Reason, want[item.ChangeID])
		}
	}
}

// bookErrStore fails every book read.
type bookErrStore struct{ *database.PebbleStore }

func (bookErrStore) GetBookByID(string) (*database.Book, error) {
	return nil, errors.New("pebble: closed")
}

// Rows whose book is gone are refused by the revert every time it runs
// (RevertService.loadBook is CheckRestoreBook), so the preflight files them
// under book_missing, never as Safe or as book_deleted, which the web counts
// as restorable. A book read that errors fails closed under check_failed.
func TestPreflightUndoConflicts_BookRows(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	live, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/b.m4b", Format: "m4b"})
	if err != nil {
		t.Fatalf("create book: %v", err)
	}
	// The file is still at the move's new location, so the revert would read
	// the book before moving it back.
	moved := filepath.Join(t.TempDir(), "moved.m4b")
	if err := os.WriteFile(moved, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, r := range []*database.OperationChange{
		{ID: "c1", OperationID: "op1", BookID: "gone", ChangeType: "metadata_update", FieldName: "title", OldValue: "Old", NewValue: "New"},
		{ID: "c2", OperationID: "op1", BookID: "gone", ChangeType: "tag_write", FieldName: "TITLE", OldValue: "Old", NewValue: "New"},
		{ID: "c3", OperationID: "op1", BookID: "gone", ChangeType: "file_move", OldValue: "/library/old.m4b", NewValue: moved},
		{ID: "c4", OperationID: "op1", BookID: live.ID, ChangeType: "metadata_update", FieldName: "title", OldValue: "Old", NewValue: "T"},
	} {
		if err := store.CreateOperationChange(r); err != nil {
			t.Fatalf("create change %s: %v", r.ID, err)
		}
	}

	report, err := PreflightUndoConflicts(store, "op1")
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if report.Safe != 1 || len(report.BookMissing) != 3 || len(report.BookDeleted) != 0 || len(report.ContentChanged) != 0 {
		t.Fatalf("report = %+v, want safe 1 (c4) and c1-c3 in book_missing only", report)
	}
	for _, item := range report.BookMissing {
		if item.Reason != ReasonBookMissing {
			t.Errorf("%s reason = %q, want %q", item.ChangeID, item.Reason, ReasonBookMissing)
		}
	}

	report, err = PreflightUndoConflicts(bookErrStore{store}, "op1")
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if report.Safe != 0 || len(report.CheckFailed) != 4 {
		t.Fatalf("safe = %d, check_failed = %+v, want 0 and all 4 rows", report.Safe, report.CheckFailed)
	}
	for _, item := range report.CheckFailed {
		if item.Reason != ReasonBookLookupFailed {
			t.Errorf("%s reason = %q, want %q", item.ChangeID, item.Reason, ReasonBookLookupFailed)
		}
	}
}

// The fs-regroup-xml rows are checked the way the revert reads them: a
// reassign needs both its books, the others need their book, and a created row
// is record-only.
func TestPreflightUndoConflicts_FsRegroupRows(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	live, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/t"})
	if err != nil {
		t.Fatalf("create book: %v", err)
	}
	for _, r := range []*database.OperationChange{
		{ID: "r1", OperationID: "op1", BookID: live.ID, ChangeType: ChangeTypeBookFileReassign, FieldName: "book_file:f1", OldValue: "gone", NewValue: live.ID},
		{ID: "r2", OperationID: "op1", BookID: live.ID, ChangeType: ChangeTypeBookSoftDelete, FieldName: "marked_for_deletion"},
		{ID: "r3", OperationID: "op1", BookID: "gone", ChangeType: ChangeTypeBookPathUpdate, FieldName: "file_path", OldValue: "/a", NewValue: "/b"},
		{ID: "r4", OperationID: "op1", BookID: live.ID, ChangeType: ChangeTypeBookFileCreate, FieldName: "book_file:f2", NewValue: "/b/1.mp3"},
	} {
		if err := store.CreateOperationChange(r); err != nil {
			t.Fatalf("create change %s: %v", r.ID, err)
		}
	}
	report, err := PreflightUndoConflicts(store, "op1")
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if report.Safe != 1 || len(report.BookMissing) != 2 || report.NotRestorable != 1 {
		t.Fatalf("report = %+v, want safe 1 (r2), book_missing 2 (r1, r3), not_restorable 1 (r4)", report)
	}
}
