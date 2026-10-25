// file: internal/undo/master_list_elsewhere_preflight_test.go
// version: 1.1.0
// guid: 0f4b7a62-93d1-4c58-b2e6-5a8d1c70e394
// last-edited: 2026-10-25

package undo

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The preflight predicts the revert of a read-elsewhere create row where the
// revert decides it: Safe while the record is still what the import created,
// Safe and already restored when it is gone, refused (changed since) when a
// later apply recreated it as something else, refused when the row does not
// decode. Every record and user here is synthetic.
func TestPreflightUndoConflicts_MasterListElsewhereRows(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	ms := database.NewMasterListStoreFromStore(store)
	if ms == nil {
		t.Fatal("no master list store over the pebble store")
	}

	rec := func(key, listID string) database.MasterListElsewhere {
		return database.MasterListElsewhere{ID: database.ElsewhereID("user-1", key), UserID: "user-1", ListID: listID,
			Lines: []int{2}, Title: "Synthetic " + key, ASIN: "B0SYNTH" + key}
	}
	intact, gone, replaced := rec("1", "list-a"), rec("2", "list-a"), rec("3", "list-a")
	// The record a later apply of another list left where the import's was.
	later := replaced
	later.ListID = "list-b"
	later.Lines = []int{9}
	for _, r := range []database.MasterListElsewhere{intact, later} {
		if err := ms.CreateElsewhere(r); err != nil {
			t.Fatalf("create record %s: %v", r.ID, err)
		}
	}

	row := func(id string, r database.MasterListElsewhere) *database.OperationChange {
		js, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		return &database.OperationChange{ID: id, OperationID: "op1", ChangeType: ChangeTypeMasterListElsewhereCreate,
			FieldName: ElsewhereField(r.ID), OldValue: ElsewhereAbsent, NewValue: string(js)}
	}
	for _, c := range []*database.OperationChange{
		row("c1", intact),
		row("c2", gone),
		row("c3", replaced),
	} {
		if err := store.CreateOperationChange(c); err != nil {
			t.Fatalf("create change %s: %v", c.ID, err)
		}
	}

	report, err := preflightUndoConflicts(store, "op1")
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if report.Safe != 2 || report.AlreadyRestored != 1 {
		t.Errorf("safe = %d (already restored %d), want 2 (c1 intact, c2 gone) and 1", report.Safe, report.AlreadyRestored)
	}
	if len(report.CheckFailed) != 1 || report.CheckFailed[0].ChangeID != "c3" {
		t.Fatalf("check_failed = %+v, want [c3], the record a later apply recreated", report.CheckFailed)
	}
	if got := report.CheckFailed[0].Reason; got != ReasonChangedSince {
		t.Errorf("c3 refusal reason = %q, want %q", got, ReasonChangedSince)
	}
	if report.NotRestorable != 0 {
		t.Errorf("not_restorable = %d %v, want 0", report.NotRestorable, report.NotRestorableTypes)
	}
}
