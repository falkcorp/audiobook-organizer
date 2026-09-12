// file: internal/audiobooks/revert_unrestorable_test.go
// version: 1.0.0
// guid: 28cae8c7-2875-491c-bd27-d45740fef9c3
// last-edited: 2026-09-12

package audiobooks

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ledgerStub is a revertServiceStore that records exactly which change IDs the
// engine asked to mark reverted.
type ledgerStub struct {
	book      *database.Book
	changes   []*database.OperationChange
	marked    []string
	markCalls int
}

func (s *ledgerStub) GetBookByID(string) (*database.Book, error) { return s.book, nil }
func (s *ledgerStub) UpdateBook(_ string, b *database.Book) (*database.Book, error) {
	s.book = b
	return b, nil
}
func (s *ledgerStub) GetOperationChanges(string) ([]*database.OperationChange, error) {
	return s.changes, nil
}
func (s *ledgerStub) MarkOperationChangesReverted(_ string, ids []string) error {
	s.markCalls++
	s.marked = append(s.marked, ids...)
	return nil
}
func (s *ledgerStub) GetAllImportPaths() ([]database.ImportPath, error) { return nil, nil }

func deleteRow(id, changeType string) *database.OperationChange {
	return &database.OperationChange{ID: id, OperationID: "op", ChangeType: changeType,
		FieldName: strings.TrimSuffix(changeType, "_delete"), OldValue: "7:Someone", NewValue: "purged_empty"}
}

func titleRow(id, field string) *database.OperationChange {
	return &database.OperationChange{ID: id, OperationID: "op", BookID: "b1", ChangeType: "metadata_update",
		FieldName: field, OldValue: "Old", NewValue: "New"}
}

// A purge op's ledger is all record-only rows: revert must refuse with a
// NotRestorableError and mark nothing. On origin/main it marked every row
// reverted and returned "partially reverted".
func TestRevertOperation_OnlyRecordOnlyRows_ErrorsAndMarksNothing(t *testing.T) {
	for _, ct := range []string{"author_delete", "narrator_delete"} {
		t.Run(ct, func(t *testing.T) {
			s := &ledgerStub{changes: []*database.OperationChange{deleteRow("c1", ct), deleteRow("c2", ct), deleteRow("c3", ct)}}
			result, err := NewRevertService(s).RevertOperation("op")
			var nr *NotRestorableError
			if !errors.As(err, &nr) {
				t.Fatalf("err = %v, want *NotRestorableError", err)
			}
			want := "cannot be undone automatically: 3 " + ct + " rows"
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %q, want it to contain %q", err, want)
			}
			if result != nil {
				t.Errorf("result = %+v, want nil", result)
			}
			if s.markCalls != 0 || len(s.marked) != 0 {
				t.Errorf("marked %v (%d calls), want nothing marked", s.marked, s.markCalls)
			}
		})
	}
}

// A mix of one restorable row and one record-only row: the restorable row is
// restored and marked; the other is reported and left unmarked.
func TestRevertOperation_MixedRows_MarksOnlyRestored(t *testing.T) {
	for _, ct := range []string{"author_delete", "narrator_delete"} {
		t.Run(ct, func(t *testing.T) {
			s := &ledgerStub{
				book:    &database.Book{ID: "b1", Title: "New"},
				changes: []*database.OperationChange{titleRow("c1", "title"), deleteRow("c2", ct)},
			}
			result, err := NewRevertService(s).RevertOperation("op")
			if err != nil {
				t.Fatalf("RevertOperation: %v", err)
			}
			if s.book.Title != "Old" {
				t.Errorf("title = %q, want restored to %q", s.book.Title, "Old")
			}
			if !slices.Equal(s.marked, []string{"c1"}) {
				t.Errorf("marked = %v, want [c1] only", s.marked)
			}
			if result.Total != 2 || result.Restored != 1 || result.NotRestorable != 1 || result.Failed != 0 {
				t.Errorf("result = %+v, want total 2 / restored 1 / not_restorable 1 / failed 0", result)
			}
			if result.NotRestorableTypes[ct] != 1 {
				t.Errorf("NotRestorableTypes = %v, want %s:1", result.NotRestorableTypes, ct)
			}
			if !result.Partial() {
				t.Error("Partial() = false, want true")
			}
			if !strings.Contains(result.Summary(), "1 "+ct+" row") {
				t.Errorf("Summary() = %q, want it to name the %s row", result.Summary(), ct)
			}
		})
	}
}

// Any change type the engine has no reversal for is treated like the
// record-only types, not silently dropped.
func TestRevertOperation_UnknownTypeIsNotRestorable(t *testing.T) {
	s := &ledgerStub{changes: []*database.OperationChange{deleteRow("c1", "series_delete")}}
	_, err := NewRevertService(s).RevertOperation("op")
	var nr *NotRestorableError
	if !errors.As(err, &nr) || nr.Types["series_delete"] != 1 {
		t.Fatalf("err = %v, want *NotRestorableError counting series_delete", err)
	}
	if s.markCalls != 0 {
		t.Errorf("mark called %d times, want 0", s.markCalls)
	}
}

// A restorable row whose reversal fails is counted as Failed, is not marked,
// and the call returns an error alongside the result.
func TestRevertOperation_FailedRestoreIsNotMarked(t *testing.T) {
	s := &ledgerStub{
		book:    &database.Book{ID: "b1", Title: "New"},
		changes: []*database.OperationChange{titleRow("c1", "title"), titleRow("c2", "no_such_field")},
	}
	result, err := NewRevertService(s).RevertOperation("op")
	if err == nil {
		t.Fatal("err = nil, want a partial-failure error")
	}
	if result == nil || result.Restored != 1 || result.Failed != 1 {
		t.Fatalf("result = %+v, want restored 1 / failed 1", result)
	}
	if !slices.Equal(s.marked, []string{"c1"}) {
		t.Errorf("marked = %v, want [c1] only", s.marked)
	}
}

func TestRevertOperation_AllRestored_NotPartial(t *testing.T) {
	s := &ledgerStub{
		book:    &database.Book{ID: "b1", Title: "New"},
		changes: []*database.OperationChange{titleRow("c1", "title"), {ID: "c2", OperationID: "op", ChangeType: "organize_summary"}},
	}
	result, err := NewRevertService(s).RevertOperation("op")
	if err != nil {
		t.Fatalf("RevertOperation: %v", err)
	}
	if result.Partial() || result.Restored != 2 {
		t.Errorf("result = %+v, want 2 restored and not partial", result)
	}
	if len(s.marked) != 2 {
		t.Errorf("marked = %v, want both rows", s.marked)
	}
}
