// file: internal/undo/engine_test.go
// version: 1.0.0
// guid: 3f8b0e2d-4c5e-4f9g-b2d6-8e0f3g5c9d4b

package undo

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestRunUndoOperation_FileMove(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	dir := t.TempDir()
	oldPath := filepath.Join(dir, "original", "Book.m4b")
	newPath := filepath.Join(dir, "organized", "Book.m4b")

	writeTestFile(t, newPath, "audio-content")

	if cerr := store.CreateOperationChange(&database.OperationChange{
		ID: "c1", OperationID: "op1", BookID: "b1",
		ChangeType: "file_move",
		OldValue:   oldPath,
		NewValue:   newPath,
		CreatedAt:  time.Now().Add(-1 * time.Hour),
	}); cerr != nil {
		t.Fatalf("create change: %v", cerr)
	}

	result, err := RunUndoOperation(store, "op1", nil, nil)
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if result.Reverted != 1 {
		t.Errorf("reverted = %d, want 1", result.Reverted)
	}

	if _, err := os.Stat(oldPath); err != nil {
		t.Errorf("old path should exist after undo: %v", err)
	}
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Errorf("new path should not exist after undo")
	}
	if got := readTestFile(t, oldPath); got != "audio-content" {
		t.Errorf("content = %q", got)
	}
}

func TestRunUndoOperation_ConflictDetected(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	dir := t.TempDir()
	oldPath := filepath.Join(dir, "original", "Book.m4b")
	newPath := filepath.Join(dir, "organized", "Book.m4b")

	writeTestFile(t, oldPath, "something-new-at-old-location")
	writeTestFile(t, newPath, "audio-content")

	_ = store.CreateOperationChange(&database.OperationChange{
		ID: "c1", OperationID: "op1", BookID: "b1",
		ChangeType: "file_move",
		OldValue:   oldPath,
		NewValue:   newPath,
		CreatedAt:  time.Now().Add(-1 * time.Hour),
	})

	result, err := RunUndoOperation(store, "op1", nil, nil)
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if result.Failed != 1 {
		t.Errorf("failed = %d, want 1 (conflict)", result.Failed)
	}
	if len(result.Errors) == 0 {
		t.Error("expected error message about conflict")
	}
}

func TestRunUndoOperation_SkipsAlreadyReverted(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	dir := t.TempDir()
	oldPath := filepath.Join(dir, "original", "Book.m4b")
	newPath := filepath.Join(dir, "organized", "Book.m4b")
	writeTestFile(t, newPath, "content")

	now := time.Now()
	_ = store.CreateOperationChange(&database.OperationChange{
		ID: "c1", OperationID: "op1", BookID: "b1",
		ChangeType: "file_move",
		OldValue:   oldPath,
		NewValue:   newPath,
		CreatedAt:  now.Add(-2 * time.Hour),
		RevertedAt: &now,
	})

	result, err := RunUndoOperation(store, "op1", nil, nil)
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if result.SkippedReverted != 1 {
		t.Errorf("skipped_reverted = %d, want 1", result.SkippedReverted)
	}
	if result.Reverted != 0 {
		t.Errorf("reverted = %d, want 0", result.Reverted)
	}
}

func TestRunUndoOperation_CallsOnFileMovedCallback(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	dir := t.TempDir()
	oldPath := filepath.Join(dir, "original", "Book.m4b")
	newPath := filepath.Join(dir, "organized", "Book.m4b")

	writeTestFile(t, newPath, "audio-content")

	if cerr := store.CreateOperationChange(&database.OperationChange{
		ID: "c1", OperationID: "op1", BookID: "book123",
		ChangeType: "file_move",
		OldValue:   oldPath,
		NewValue:   newPath,
		CreatedAt:  time.Now().Add(-1 * time.Hour),
	}); cerr != nil {
		t.Fatalf("create change: %v", cerr)
	}

	var callbackCalled bool
	var callbackBookID string
	var callbackPath string

	callback := func(bookID, oldFilePath string) {
		callbackCalled = true
		callbackBookID = bookID
		callbackPath = oldFilePath
	}

	result, err := RunUndoOperation(store, "op1", nil, callback)
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if result.Reverted != 1 {
		t.Errorf("reverted = %d, want 1", result.Reverted)
	}

	if !callbackCalled {
		t.Error("callback was not called")
	}
	if callbackBookID != "book123" {
		t.Errorf("callback bookID = %q, want 'book123'", callbackBookID)
	}
	if callbackPath != oldPath {
		t.Errorf("callback path = %q, want %q", callbackPath, oldPath)
	}
}

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
