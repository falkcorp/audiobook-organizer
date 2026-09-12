// file: internal/undo/engine_test.go
// version: 1.2.0
// guid: 3f8b0e2d-4c5e-4f9g-b2d6-8e0f3g5c9d4b
// last-edited: 2026-09-12

package undo

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestPreflightUndoConflicts_ReportsContentChanged(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	dir := t.TempDir()
	oldPath := filepath.Join(dir, "original", "Book.m4b")
	newPath := filepath.Join(dir, "organized", "Book.m4b")

	writeTestFile(t, newPath, "content")

	// Create a change and wait so ModTime is definitely after CreatedAt
	changeTime := time.Now().Add(-2 * time.Second)
	_ = store.CreateOperationChange(&database.OperationChange{
		ID: "c1", OperationID: "op1", BookID: "b1",
		ChangeType: "file_move",
		OldValue:   oldPath,
		NewValue:   newPath,
		CreatedAt:  changeTime,
	})

	// Update file after the operation to simulate content change
	time.Sleep(100 * time.Millisecond)
	writeTestFile(t, newPath, "modified-content")

	report, err := PreflightUndoConflicts(store, "op1")
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}

	if len(report.ContentChanged) == 0 {
		t.Error("expected content_changed conflict")
	}
	if len(report.ContentChanged) > 0 && report.ContentChanged[0].BookID != "b1" {
		t.Errorf("conflict book_id = %q, want 'b1'", report.ContentChanged[0].BookID)
	}
}

func TestPreflightUndoConflicts_ReportsSafeChanges(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	dir := t.TempDir()
	oldPath := filepath.Join(dir, "original", "Book.m4b")
	newPath := filepath.Join(dir, "organized", "Book.m4b")

	// Create the book in the database
	book, err := store.CreateBook(&database.Book{
		Title:    "Test Book",
		FilePath: newPath,
		Format:   "m4b",
	})
	if err != nil {
		t.Fatalf("create book: %v", err)
	}

	// Create operation record first with a timestamp
	changeTime := time.Now().Add(-1 * time.Hour)
	_ = store.CreateOperationChange(&database.OperationChange{
		ID: "c1", OperationID: "op1", BookID: book.ID,
		ChangeType: "file_move",
		OldValue:   oldPath,
		NewValue:   newPath,
		CreatedAt:  changeTime,
	})

	// Write the file after recording the operation, but with an older ModTime
	// to simulate a file that hasn't changed since the operation
	writeTestFile(t, newPath, "content")
	// Set the ModTime to be before the operation
	if err := os.Chtimes(newPath, changeTime.Add(-1*time.Minute), changeTime.Add(-1*time.Minute)); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	report, err := PreflightUndoConflicts(store, "op1")
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}

	if report.Safe != 1 {
		t.Errorf("safe = %d, want 1", report.Safe)
	}
	if len(report.ContentChanged) > 0 {
		t.Errorf("expected no conflicts, got %d content_changed", len(report.ContentChanged))
	}
}

// Helper functions
func writeTestFile(t *testing.T, path, content string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func readTestFile(t *testing.T, path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	return string(data)
}

// Preflight and revert use one classifier: record-only rows (author_delete,
// narrator_delete, a metadata_update on a field the revert engine cannot
// restore) are counted as not restorable, never as safe. Before, every one of
// them landed in Safe, so the UI asked "Undo 5 change(s)?" for an op whose
// revert then refused all but one row.
func TestPreflightUndoConflicts_RecordOnlyRowsAreNotSafe(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/b.m4b", Format: "m4b"})
	if err != nil {
		t.Fatalf("create book: %v", err)
	}
	rows := []*database.OperationChange{
		{ID: "c1", OperationID: "op1", ChangeType: "author_delete", FieldName: "author", OldValue: "7:A"},
		{ID: "c2", OperationID: "op1", ChangeType: "author_delete", FieldName: "author", OldValue: "8:B"},
		{ID: "c3", OperationID: "op1", ChangeType: "narrator_delete", FieldName: "narrator", OldValue: "9:C"},
		{ID: "c4", OperationID: "op1", BookID: book.ID, ChangeType: "metadata_update", FieldName: "author_id", OldValue: "7", NewValue: "9"},
		{ID: "c5", OperationID: "op1", BookID: book.ID, ChangeType: "metadata_update", FieldName: "title", OldValue: "Old", NewValue: "T"},
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
	if report.Safe != 1 {
		t.Errorf("safe = %d, want 1 (only the title row is restorable)", report.Safe)
	}
	if report.NotRestorable != 4 {
		t.Errorf("not_restorable = %d, want 4", report.NotRestorable)
	}
	want := map[string]int{"author_delete": 2, "narrator_delete": 1, "metadata_update:author_id": 1}
	if len(report.NotRestorableTypes) != len(want) {
		t.Errorf("not_restorable_types = %v, want %v", report.NotRestorableTypes, want)
	}
	for k, v := range want {
		if report.NotRestorableTypes[k] != v {
			t.Errorf("not_restorable_types[%s] = %d, want %d", k, report.NotRestorableTypes[k], v)
		}
	}
}
