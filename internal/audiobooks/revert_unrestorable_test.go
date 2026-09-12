// file: internal/audiobooks/revert_unrestorable_test.go
// version: 1.4.0
// guid: 28cae8c7-2875-491c-bd27-d45740fef9c3
// last-edited: 2026-09-12

package audiobooks

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ledgerStub is a revertServiceStore that records exactly which change IDs the
// engine asked to mark reverted.
type ledgerStub struct {
	book      *database.Book
	changes   []*database.OperationChange
	marked    []string
	markCalls int
	// failBook makes GetBookByID fail for that book ID.
	failBook string
	// series is what GetSeriesByID / GetSeriesByName can find.
	series    map[int]*database.Series
	seriesErr error
	// nameErr fails only GetSeriesByName (the collision lookup).
	nameErr error
	// renames records UpdateSeriesName calls as "id:name".
	renames []string
}

func (s *ledgerStub) GetBookByID(id string) (*database.Book, error) {
	if id != "" && id == s.failBook {
		return nil, errors.New("book lookup failed")
	}
	if s.book == nil {
		return nil, nil // the Pebble store's answer for a missing book
	}
	// A copy, as a real store returns: a restore that forgets UpdateBook must
	// leave s.book (the persisted row) unchanged, so tests asserting on
	// s.book catch it.
	cp := *s.book
	return &cp, nil
}
func (s *ledgerStub) UpdateBook(_ string, b *database.Book) (*database.Book, error) {
	cp := *b
	s.book = &cp
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

// GetSeriesByID answers from s.series; a missing id is (nil, nil), as the
// Pebble store reports ErrNotFound. seriesErr makes every series read fail.
func (s *ledgerStub) GetSeriesByID(id int) (*database.Series, error) {
	if s.seriesErr != nil {
		return nil, s.seriesErr
	}
	return s.series[id], nil
}

// GetSeriesByName matches case-insensitively under the same author, as the
// store's name index does.
func (s *ledgerStub) GetSeriesByName(name string, authorID *int) (*database.Series, error) {
	if s.seriesErr != nil {
		return nil, s.seriesErr
	}
	if s.nameErr != nil {
		return nil, s.nameErr
	}
	for _, ser := range s.series {
		sameAuthor := (ser.AuthorID == nil) == (authorID == nil) && (authorID == nil || *ser.AuthorID == *authorID)
		if sameAuthor && strings.EqualFold(ser.Name, name) {
			return ser, nil
		}
	}
	return nil, nil
}

func (s *ledgerStub) UpdateSeriesName(id int, name string) error {
	s.renames = append(s.renames, fmt.Sprintf("%d:%s", id, name))
	if ser := s.series[id]; ser != nil {
		ser.Name = name
	}
	return nil
}

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
		book:     &database.Book{ID: "b1", Title: "New"},
		failBook: "b2",
		changes: []*database.OperationChange{titleRow("c1", "title"),
			{ID: "c2", OperationID: "op", BookID: "b2", ChangeType: "metadata_update", FieldName: "title", OldValue: "Old", NewValue: "New"}},
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

// maintenance.author-duplicate-merge journals author_delete plus one
// metadata_update row with field author_id per moved book. author_id is not a
// field the engine can restore, so the whole op is record-only: it must be
// refused as NotRestorable (409), not attempted and reported as a 500 with
// every author_id row counted Failed.
func TestRevertOperation_UnrevertableMetadataField_IsNotRestorable(t *testing.T) {
	s := &ledgerStub{
		book:    &database.Book{ID: "b1", Title: "New"},
		changes: []*database.OperationChange{titleRow("c1", "author_id"), titleRow("c2", "author_id"), deleteRow("c3", "author_delete")},
	}
	result, err := NewRevertService(s).RevertOperation("op")
	var nr *NotRestorableError
	if !errors.As(err, &nr) {
		t.Fatalf("err = %v (result %+v), want *NotRestorableError", err, result)
	}
	if nr.Types["metadata_update:author_id"] != 2 || nr.Types["author_delete"] != 1 {
		t.Errorf("Types = %v, want metadata_update:author_id:2 author_delete:1", nr.Types)
	}
	if s.markCalls != 0 {
		t.Errorf("mark called %d times, want 0", s.markCalls)
	}
}

// Rows an earlier partial revert left unmarked can be retried: rows already
// marked are skipped one by one and only the rest are reversed and marked.
// The previous guard refused the whole op as "already reverted" as soon as any
// row was marked, so the leftover rows could never be retried.
func TestRevertOperation_RetriesRowsLeftUnmarked(t *testing.T) {
	now := time.Now()
	done := titleRow("c1", "title")
	done.RevertedAt = &now
	s := &ledgerStub{
		book:    &database.Book{ID: "b1", Title: "New"},
		changes: []*database.OperationChange{done, titleRow("c2", "title"), deleteRow("c3", "author_delete")},
	}
	result, err := NewRevertService(s).RevertOperation("op")
	if err != nil {
		t.Fatalf("RevertOperation: %v", err)
	}
	if !slices.Equal(s.marked, []string{"c2"}) {
		t.Errorf("marked = %v, want [c2] only", s.marked)
	}
	if result.Restored != 1 || result.AlreadyReverted != 1 || result.NotRestorable != 1 {
		t.Errorf("result = %+v, want restored 1 / already_reverted 1 / not_restorable 1", result)
	}
	if !strings.Contains(result.Summary(), "1 already reverted earlier") {
		t.Errorf("Summary() = %q, want it to count the already-reverted row", result.Summary())
	}
}

// "Already reverted" is reported only when every restorable row is marked; a
// record-only row alongside them does not turn it into a NotRestorableError.
func TestRevertOperation_AllRestorableRowsReverted_SaysAlreadyReverted(t *testing.T) {
	now := time.Now()
	done := titleRow("c1", "title")
	done.RevertedAt = &now
	s := &ledgerStub{changes: []*database.OperationChange{done, deleteRow("c2", "author_delete")}}
	_, err := NewRevertService(s).RevertOperation("op")
	if err == nil || !strings.Contains(err.Error(), "already been reverted") {
		t.Fatalf("err = %v, want an already-reverted error", err)
	}
	var nr *NotRestorableError
	if errors.As(err, &nr) {
		t.Errorf("err is a NotRestorableError, want already-reverted")
	}
	if s.markCalls != 0 {
		t.Errorf("mark called %d times, want 0", s.markCalls)
	}
}
