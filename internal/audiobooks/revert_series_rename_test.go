// file: internal/audiobooks/revert_series_rename_test.go
// version: 1.1.0
// guid: 0c7d2e91-5f3a-4b86-9e14-a8b6d3f5c227
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

func seriesRenameRow(id string, seriesID *int, old, new string) *database.OperationChange {
	return &database.OperationChange{ID: id, OperationID: "op", ChangeType: undo.ChangeTypeSeriesRename,
		FieldName: "series_name", SeriesID: seriesID, OldValue: old, NewValue: new}
}

func TestRevertOperation_SeriesRename_RenamesBack(t *testing.T) {
	s := &ledgerStub{
		changes: []*database.OperationChange{seriesRenameRow("c1", intp(10), "Old Name", "New Name")},
		series:  map[int]*database.Series{10: {ID: 10, Name: "New Name"}},
	}
	result, err := NewRevertService(s).RevertOperation("op")
	if err != nil {
		t.Fatalf("RevertOperation: %v (result %+v)", err, result)
	}
	if !slices.Equal(s.renames, []string{"10:Old Name"}) {
		t.Errorf("renames = %v, want [10:Old Name]", s.renames)
	}
	if result.Restored != 1 || !slices.Equal(s.marked, []string{"c1"}) {
		t.Errorf("restored = %d, marked = %v, want 1 and [c1]", result.Restored, s.marked)
	}
}

// Every refusal: counted Failed, no rename issued, row left unmarked.
func TestRevertOperation_SeriesRename_Refusals(t *testing.T) {
	cases := []struct {
		name   string
		series map[int]*database.Series
		err    error
		reason string
	}{
		{"series deleted", map[int]*database.Series{}, nil, undo.ReasonSeriesDeleted},
		{"renamed since", map[int]*database.Series{10: {ID: 10, Name: "Someone Else's Name"}}, nil, undo.ReasonSeriesRenamedSince},
		{"lookup error fails closed", nil, errors.New("pebble: closed"), undo.ReasonSeriesLookupFailed},
		{"old name taken by another series", map[int]*database.Series{
			10: {ID: 10, Name: "New Name"},
			11: {ID: 11, Name: "old name"}, // the name index is case-insensitive
		}, nil, undo.ReasonSeriesNameTaken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &ledgerStub{
				changes:   []*database.OperationChange{seriesRenameRow("c1", intp(10), "Old Name", "New Name")},
				series:    tc.series,
				seriesErr: tc.err,
			}
			result, err := NewRevertService(s).RevertOperation("op")
			if err == nil || result == nil || result.Failed != 1 || result.Restored != 0 {
				t.Fatalf("err = %v, result = %+v, want failed 1, restored 0", err, result)
			}
			if !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("err = %v, want reason %q", err, tc.reason)
			}
			if len(s.renames) != 0 {
				t.Errorf("renames = %v, want none", s.renames)
			}
			if s.markCalls != 0 {
				t.Errorf("mark called %d times, want 0", s.markCalls)
			}
		})
	}
}

// The collision lookup erroring fails closed: without an answer to "does
// another series hold the old name?" the rename back is not attempted.
func TestRevertOperation_SeriesRename_NameLookupErrorFailsClosed(t *testing.T) {
	s := &ledgerStub{
		changes: []*database.OperationChange{seriesRenameRow("c1", intp(10), "Old Name", "New Name")},
		series:  map[int]*database.Series{10: {ID: 10, Name: "New Name"}},
		nameErr: errors.New("pebble: closed"),
	}
	result, err := NewRevertService(s).RevertOperation("op")
	if err == nil || result == nil || result.Failed != 1 {
		t.Fatalf("err = %v, result = %+v, want failed 1", err, result)
	}
	if !strings.Contains(err.Error(), undo.ReasonSeriesLookupFailed) {
		t.Errorf("err = %v, want reason %q", err, undo.ReasonSeriesLookupFailed)
	}
	if len(s.renames) != 0 || s.markCalls != 0 {
		t.Errorf("renames = %v, mark calls = %d, want none", s.renames, s.markCalls)
	}
}

// A different author's series holding the old name is not a collision: the
// store's name index is keyed by author.
func TestRevertOperation_SeriesRename_SameNameOtherAuthorIsNoCollision(t *testing.T) {
	s := &ledgerStub{
		changes: []*database.OperationChange{seriesRenameRow("c1", intp(10), "Old Name", "New Name")},
		series: map[int]*database.Series{
			10: {ID: 10, Name: "New Name"},
			11: {ID: 11, Name: "Old Name", AuthorID: intp(3)},
		},
	}
	if _, err := NewRevertService(s).RevertOperation("op"); err != nil {
		t.Fatalf("RevertOperation: %v", err)
	}
	if !slices.Equal(s.renames, []string{"10:Old Name"}) {
		t.Errorf("renames = %v, want [10:Old Name]", s.renames)
	}
}

// Rows without a series id stay record-only: the malformed series_rename and
// the book-scoped metadata_update/series_name rows written before
// series_rename existed. Nothing guesses a series for them.
func TestRevertOperation_SeriesRename_WithoutSeriesIDIsRecordOnly(t *testing.T) {
	s := &ledgerStub{
		changes: []*database.OperationChange{
			seriesRenameRow("c1", nil, "Old Name", "New Name"),
			{ID: "c2", OperationID: "op", ChangeType: "metadata_update", FieldName: "series_name", OldValue: "Old Name", NewValue: "New Name"},
		},
		series: map[int]*database.Series{10: {ID: 10, Name: "New Name"}},
	}
	_, err := NewRevertService(s).RevertOperation("op")
	var nr *NotRestorableError
	if !errors.As(err, &nr) {
		t.Fatalf("err = %v, want *NotRestorableError", err)
	}
	if nr.Types["series_rename:(no series id)"] != 1 || nr.Types["metadata_update:series_name"] != 1 {
		t.Errorf("Types = %v", nr.Types)
	}
	if len(s.renames) != 0 {
		t.Errorf("renames = %v, want none", s.renames)
	}
}
