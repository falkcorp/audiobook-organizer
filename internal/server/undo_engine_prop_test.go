// file: internal/server/undo_engine_prop_test.go
// version: 1.4.0
// last-edited: 2026-09-12
// guid: 1ff3d071-4c60-4bb0-92ed-d197fe8ad9d0
//
// Property-based tests for the undo engine (plan 4.5 task 8).
//
// One invariant is exercised here: conflict detection is conservative. If the
// file at NewValue is modified after the change's CreatedAt,
// PreflightUndoConflicts must classify it as a content-change conflict, never
// silently clobber. (The double-undo and undo+redo properties exercised
// RunUndoOperation, deleted 2026-09-12 with no production caller.)

package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"pgregory.net/rapid"
)

// newPropStore opens ONE PebbleStore per test function, closed via t.Cleanup.
// Call once outside rapid.Check; individual iterations use unique opIDs so
// they never collide with each other's changes.
func newPropStore(t *testing.T) database.Store {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// TestProp_UndoConflictConservative verifies that PreflightUndoConflicts flags
// any file_move whose new-location file was modified after CreatedAt as a
// content-change conflict. The undo engine must refuse to silently clobber
// user edits made between the original operation and the undo.
func TestProp_UndoConflictConservative(t *testing.T) {
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	store := newPropStore(t)
	rapid.Check(t, func(rt *rapid.T) {
		// Unique opID per iteration so each preflight sees only this
		// iteration's single change (not accumulated ones from prior runs).
		opID := "op-conf-" + rapid.StringMatching(`[a-z0-9]{6,12}`).Draw(rt, "op_id")

		root := filepath.Join(t.TempDir(), "conf-"+rapid.StringMatching(`[a-z0-9]{6,12}`).Draw(rt, "root"))
		oldSeg := rapid.StringMatching(`[a-z0-9_-]{3,10}`).Draw(rt, "old_seg")
		newSeg := rapid.StringMatching(`[a-z0-9_-]{3,10}`).Draw(rt, "new_seg")
		if oldSeg == newSeg {
			t.Skip("degenerate same-path draw")
		}
		fileName := rapid.StringMatching(`[a-z0-9]{3,8}\.m4b`).Draw(rt, "file")
		oldPath := filepath.Join(root, oldSeg, fileName)
		newPath := filepath.Join(root, newSeg, fileName)

		if err := os.MkdirAll(filepath.Dir(newPath), 0o775); err != nil {
			t.Fatalf("mkdir new: %v", err)
		}
		if err := os.WriteFile(newPath, []byte("initial"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		change := &database.OperationChange{
			OperationID: opID,
			BookID:      "b-conf",
			ChangeType:  "file_move",
			OldValue:    oldPath,
			NewValue:    newPath,
		}
		if err := store.CreateOperationChange(change); err != nil {
			t.Fatalf("create change: %v", err)
		}

		// Touch the file's mtime forward so it's strictly after the
		// change's CreatedAt (which PebbleStore stamps to time.Now at
		// insertion). This simulates a user editing the file after the
		// original operation but before the undo.
		ahead := time.Now().Add(1 * time.Hour)
		if err := os.Chtimes(newPath, ahead, ahead); err != nil {
			t.Fatalf("chtimes: %v", err)
		}

		report, err := PreflightUndoConflicts(store, opID)
		if err != nil {
			t.Fatalf("preflight: %v", err)
		}

		// Conservative reporting: this change must not be classified as
		// Safe. It must appear in one of the conflict buckets (content
		// changed is the expected one here; we accept any conflict
		// bucket to keep the property robust to future refinement).
		totalConflicts := len(report.ContentChanged) + len(report.BookDeleted) + len(report.ReOrganized)
		if report.Safe != 0 {
			t.Errorf("Safe = %d, want 0 (mtime-bumped change must be a conflict)", report.Safe)
		}
		if totalConflicts != 1 {
			t.Errorf("total conflicts = %d, want 1 (report=%+v)", totalConflicts, report)
		}
		if len(report.ContentChanged) != 1 {
			t.Errorf("ContentChanged = %d, want 1 (mtime bump should land here)",
				len(report.ContentChanged))
		}
	})
}
