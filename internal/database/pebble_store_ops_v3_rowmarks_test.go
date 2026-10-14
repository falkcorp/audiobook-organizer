// file: internal/database/pebble_store_ops_v3_rowmarks_test.go
// version: 1.0.0
// guid: b8e3d0f1-a530-4206-b9a9-20bfd85c4ec5
// last-edited: 2026-10-14

package database

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

// TestRowMarksV3: a row's apply marker round-trips by (run, row), with the
// history it names; another run's or row's key finds nothing. Like an
// intent, a marker is refused for a missing run (writing nothing), a
// discarded one or a terminal one, and a discard deletes the run's markers
// with the rest of its keys and leaves other runs' alone.
func TestRowMarksV3(t *testing.T) {
	p := newTimelineStore(t)
	id := insertV3(t, p, "test.rowmarks", "")
	other := insertV3(t, p, "test.rowmarks", "")
	m := RowMarkV3{RowID: "row:1", Batches: []RowMarkBatch{{BookID: "book-001", BatchID: "op-x-01"}},
		Changes: []string{"c-1", "c-2"}}
	if err := p.PutRowMarkV3(id, m); err != nil {
		t.Fatal(err)
	}
	if err := p.PutRowMarkV3(other, RowMarkV3{RowID: "row:2"}); err != nil {
		t.Fatal(err)
	}
	got, err := p.GetRowMarkV3(id, "row:1")
	if err != nil || got == nil || got.RowID != "row:1" || got.MarkedAt.IsZero() || len(got.Batches) != 1 ||
		got.Batches[0] != m.Batches[0] || len(got.Changes) != 2 || got.Changes[1] != "c-2" {
		t.Fatalf("GetRowMarkV3 = %+v, %v", got, err)
	}
	for _, k := range [][2]string{{id, "row:2"}, {other, "row:1"}, {id, "row"}} {
		if got, err := p.GetRowMarkV3(k[0], k[1]); err != nil || got != nil {
			t.Fatalf("GetRowMarkV3(%s, %s) = %+v, %v; want nil", k[0], k[1], got, err)
		}
	}

	before := v3Digest(t, p, "opv3:")
	if err := p.PutRowMarkV3(newRunID(), RowMarkV3{RowID: "row:1"}); !errors.Is(err, ErrRunNotFoundV3) {
		t.Fatalf("missing run: err = %v, want ErrRunNotFoundV3", err)
	}
	if v3Digest(t, p, "opv3:") != before {
		t.Fatal("a refused marker changed opv3:")
	}

	if deleted, err := p.DeleteRunV3(id, reachableV3States()); err != nil || !deleted {
		t.Fatalf("DeleteRunV3: %v %v", deleted, err)
	}
	if n := countPrefix(t, p, opv3RowMarkPrefix+id+":", ""); n != 0 {
		t.Fatalf("%d marker keys left after discard", n)
	}
	if got, err := p.GetRowMarkV3(other, "row:2"); err != nil || got == nil {
		t.Fatalf("the other run's marker went with the discard: %+v, %v", got, err)
	}
	if err := p.PutRowMarkV3(id, RowMarkV3{RowID: "late"}); !errors.Is(err, ErrRunNotFoundV3) {
		t.Fatalf("discarded run: err = %v, want ErrRunNotFoundV3", err)
	}
	transition(t, p, other, state.EvDispatch, nil)
	transition(t, p, other, state.EvReturnNil, nil)
	if err := p.PutRowMarkV3(other, RowMarkV3{RowID: "after-terminal"}); err == nil {
		t.Fatal("a terminal run took a marker")
	}
}

// TestGetOperationChange: one change row by (operation, change id); another
// operation's id, or an unknown one, finds nothing.
func TestGetOperationChange(t *testing.T) {
	p := newTimelineStore(t)
	for _, c := range []*OperationChange{
		{ID: "c-1", OperationID: "op-1", BookID: "book-001", ChangeType: "metadata_update", FieldName: "title"},
		{ID: "c-2", OperationID: "op-2", BookID: "book-002", ChangeType: "metadata_update", FieldName: "title"},
	} {
		if err := p.CreateOperationChange(c); err != nil {
			t.Fatal(err)
		}
	}
	got, err := p.GetOperationChange("op-1", "c-1")
	if err != nil || got == nil || got.BookID != "book-001" || got.OperationID != "op-1" {
		t.Fatalf("GetOperationChange = %+v, %v", got, err)
	}
	for _, k := range [][2]string{{"op-1", "c-2"}, {"op-2", "c-1"}, {"op-1", "c"}} {
		if got, err := p.GetOperationChange(k[0], k[1]); err != nil || got != nil {
			t.Fatalf("GetOperationChange(%s, %s) = %+v, %v; want nil", k[0], k[1], got, err)
		}
	}
	if err := p.MarkOperationChangesReverted("op-1", []string{"c-1"}); err != nil {
		t.Fatal(err)
	}
	if got, err := p.GetOperationChange("op-1", "c-1"); err != nil || got == nil || got.RevertedAt == nil {
		t.Fatalf("after revert: %+v, %v; want RevertedAt set", got, err)
	}
}
