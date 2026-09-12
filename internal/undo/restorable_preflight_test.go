// file: internal/undo/restorable_preflight_test.go
// version: 1.1.0
// guid: e41b8d2a-6c07-4f95-a3e8-1d9c5b7f2a60
// last-edited: 2026-09-12

package undo

import (
	"errors"
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
// never Safe, it is filed under series_check_failed with the lookup reason.
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
	if report.Safe != 0 || len(report.SeriesCheckFailed) != 3 {
		t.Fatalf("safe = %d, series_check_failed = %+v, want 0 and 3 rows", report.Safe, report.SeriesCheckFailed)
	}
	want := map[string]string{"c1": ReasonSeriesLookupFailed, "c2": ReasonSeriesLookupFailed, "c3": ReasonOldValueUnparsable}
	for _, item := range report.SeriesCheckFailed {
		if item.Reason != want[item.ChangeID] {
			t.Errorf("%s reason = %q, want %q", item.ChangeID, item.Reason, want[item.ChangeID])
		}
	}
}
