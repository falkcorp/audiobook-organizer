// file: internal/audiobooks/revert_series_id_test.go
// version: 1.2.0
// guid: 5a0e7c3d-9b41-4f62-8d17-c2e4a6f19b08
// last-edited: 2026-09-12

package audiobooks

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

func intp(n int) *int { return &n }

func seriesIDRow(id, old, new string) *database.OperationChange {
	return &database.OperationChange{ID: id, OperationID: "op", BookID: "b1", ChangeType: "metadata_update",
		FieldName: "series_id", OldValue: old, NewValue: new}
}

// A series_id row written by series dedup is restored: the book points at its
// old series again and the row is marked. On origin/main series_id was outside
// the classifier, so this returned a NotRestorableError.
func TestRevertOperation_SeriesID_RestoresOldSeries(t *testing.T) {
	s := &ledgerStub{
		book:    &database.Book{ID: "b1", Title: "T", SeriesID: intp(9)},
		changes: []*database.OperationChange{seriesIDRow("c1", "4", "9")},
		series:  map[int]*database.Series{4: {ID: 4, Name: "Old"}, 9: {ID: 9, Name: "Kept"}},
	}
	result, err := NewRevertService(s).RevertOperation("op")
	if err != nil {
		t.Fatalf("RevertOperation: %v (result %+v)", err, result)
	}
	if result.Restored != 1 || result.Failed != 0 || result.NotRestorable != 0 {
		t.Errorf("result = %+v, want restored 1, failed 0, not_restorable 0", result)
	}
	if s.book.SeriesID == nil || *s.book.SeriesID != 4 {
		t.Errorf("SeriesID = %v, want 4", s.book.SeriesID)
	}
	if !slices.Equal(s.marked, []string{"c1"}) {
		t.Errorf("marked = %v, want [c1]", s.marked)
	}
}

// OldValue "" means the book had no series: the revert clears SeriesID to nil.
// Writing *0 would point the book at a series id that never exists.
func TestRevertOperation_SeriesID_EmptyOldValueClearsToNil(t *testing.T) {
	s := &ledgerStub{
		book:    &database.Book{ID: "b1", Title: "T", SeriesID: intp(9)},
		changes: []*database.OperationChange{seriesIDRow("c1", "", "9")},
	}
	result, err := NewRevertService(s).RevertOperation("op")
	if err != nil {
		t.Fatalf("RevertOperation: %v (result %+v)", err, result)
	}
	if s.book.SeriesID != nil {
		t.Errorf("SeriesID = %d, want nil", *s.book.SeriesID)
	}
	if result.Restored != 1 || !slices.Equal(s.marked, []string{"c1"}) {
		t.Errorf("restored = %d, marked = %v, want 1 and [c1]", result.Restored, s.marked)
	}
}

// Series dedup deletes the merged-from series in the same operation. Writing
// its id back would leave the book pointing at a deleted series, so the row is
// refused: counted Failed, left unmarked, book untouched.
func TestRevertOperation_SeriesID_DeletedSeriesIsRefused(t *testing.T) {
	s := &ledgerStub{
		book:    &database.Book{ID: "b1", Title: "T", SeriesID: intp(9)},
		changes: []*database.OperationChange{seriesIDRow("c1", "4", "9")},
		series:  map[int]*database.Series{9: {ID: 9, Name: "Kept"}},
	}
	result, err := NewRevertService(s).RevertOperation("op")
	if err == nil {
		t.Fatalf("RevertOperation succeeded (result %+v), want a partial error", result)
	}
	if result == nil || result.Failed != 1 || result.Restored != 0 {
		t.Fatalf("result = %+v, want failed 1, restored 0", result)
	}
	if s.book.SeriesID == nil || *s.book.SeriesID != 9 {
		t.Errorf("SeriesID = %v, want 9 (unchanged)", s.book.SeriesID)
	}
	if s.markCalls != 0 {
		t.Errorf("mark called %d times, want 0", s.markCalls)
	}
}

// An old value that is not a bare integer (e.g. the "12 (Foo)" shape of a
// series_merge row) is an error, never a silent no-op that gets marked.
func TestRevertOperation_SeriesID_UnparsableOldValueFails(t *testing.T) {
	s := &ledgerStub{
		book:    &database.Book{ID: "b1", Title: "T", SeriesID: intp(9)},
		changes: []*database.OperationChange{seriesIDRow("c1", "12 (Foo)", "9 (Bar)")},
		series:  map[int]*database.Series{12: {ID: 12}},
	}
	result, err := NewRevertService(s).RevertOperation("op")
	if err == nil || result == nil || result.Failed != 1 {
		t.Fatalf("err = %v, result = %+v, want failed 1", err, result)
	}
	if s.book.SeriesID == nil || *s.book.SeriesID != 9 {
		t.Errorf("SeriesID = %v, want 9 (unchanged)", s.book.SeriesID)
	}
	if s.markCalls != 0 {
		t.Errorf("mark called %d times, want 0", s.markCalls)
	}
}

// A series lookup that errors fails closed: Failed, unmarked, book untouched.
func TestRevertOperation_SeriesID_LookupErrorFailsClosed(t *testing.T) {
	s := &ledgerStub{
		book:      &database.Book{ID: "b1", Title: "T", SeriesID: intp(9)},
		changes:   []*database.OperationChange{seriesIDRow("c1", "4", "9")},
		series:    map[int]*database.Series{4: {ID: 4}},
		seriesErr: errors.New("pebble: closed"),
	}
	result, err := NewRevertService(s).RevertOperation("op")
	if err == nil || result == nil || result.Failed != 1 || result.Restored != 0 {
		t.Fatalf("err = %v, result = %+v, want failed 1", err, result)
	}
	if s.book.SeriesID == nil || *s.book.SeriesID != 9 {
		t.Errorf("SeriesID = %v, want 9 (unchanged)", s.book.SeriesID)
	}
	if s.markCalls != 0 {
		t.Errorf("mark called %d times, want 0", s.markCalls)
	}
}

// A book that no longer exists (the Pebble store returns nil, nil) fails its
// rows instead of panicking the revert endpoint.
func TestRevertOperation_DeletedBookFailsInsteadOfPanicking(t *testing.T) {
	s := &ledgerStub{
		changes: []*database.OperationChange{
			titleRow("c1", "title"),
			seriesIDRow("c2", "", "9"),
			{ID: "c3", OperationID: "op", BookID: "b1", ChangeType: "tag_write", FieldName: "TITLE", OldValue: "Old"},
		},
	}
	result, err := NewRevertService(s).RevertOperation("op")
	if err == nil || result == nil || result.Failed != 3 || result.Restored != 0 {
		t.Fatalf("err = %v, result = %+v, want failed 3", err, result)
	}
	if !strings.Contains(err.Error(), undo.ReasonBookMissing) {
		t.Errorf("err = %v, want reason %q", err, undo.ReasonBookMissing)
	}
	if s.markCalls != 0 {
		t.Errorf("mark called %d times, want 0", s.markCalls)
	}
}

// version_group_id and series_name stay record-only (see undo/restorable.go),
// as does the duplicates handler's series_merge row.
func TestRevertOperation_VersionGroupAndSeriesName_StayRecordOnly(t *testing.T) {
	s := &ledgerStub{
		book: &database.Book{ID: "b1", Title: "T"},
		changes: []*database.OperationChange{
			{ID: "c1", OperationID: "op", BookID: "b1", ChangeType: "metadata_update", FieldName: "version_group_id", NewValue: "g1"},
			{ID: "c2", OperationID: "op", ChangeType: "metadata_update", FieldName: "series_name", OldValue: "Old", NewValue: "New"},
			{ID: "c3", OperationID: "op", BookID: "b1", ChangeType: "series_merge", FieldName: "series_id", OldValue: "4 (Old)", NewValue: "9 (Kept)"},
		},
	}
	_, err := NewRevertService(s).RevertOperation("op")
	var nr *NotRestorableError
	if !errors.As(err, &nr) {
		t.Fatalf("err = %v, want *NotRestorableError", err)
	}
	for _, label := range []string{"metadata_update:version_group_id", "metadata_update:series_name", "series_merge"} {
		if nr.Types[label] != 1 {
			t.Errorf("Types[%s] = %d, want 1 (types %v)", label, nr.Types[label], nr.Types)
		}
	}
	if s.markCalls != 0 {
		t.Errorf("mark called %d times, want 0", s.markCalls)
	}
}
