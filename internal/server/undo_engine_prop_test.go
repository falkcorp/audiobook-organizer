// file: internal/server/undo_engine_prop_test.go
// version: 1.3.0
// last-edited: 2026-08-15
// guid: 1ff3d071-4c60-4bb0-92ed-d197fe8ad9d0
//
// Property-based tests for the undo engine (plan 4.5 task 8).
//
// Three invariants are exercised here:
//
//  1. Double-undo is idempotent. Running RunUndoOperation twice against the
//     same operation_id must leave the second call with Reverted == 0, and the
//     count of already-reverted skips must equal the Reverted count of the
//     first call. Failures (e.g. missing book on a metadata_update) must
//     reproduce deterministically because RevertedAt is only stamped on
//     success.
//
//  2. Undo + redo preserves on-disk state. For a file_move change, undo moves
//     new → old; re-running the original operation (modeled as the same
//     os.Rename in the forward direction) moves old → new and must leave the
//     file content byte-identical to what was there before the undo.
//
//  3. Conflict detection is conservative. If the file at NewValue is modified
//     after the change's CreatedAt, PreflightUndoConflicts must classify it as
//     a content-change conflict — never silently clobber.

package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/testutil/rapidgen"
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

// TestProp_UndoIdempotent verifies that running RunUndoOperation twice on the
// same operation leaves the second run with Reverted == 0, and that
// SkippedReverted on the second run matches the Reverted count from the first
// run. Changes drawn from rapidgen.OperationChange cover all three supported
// change types. file_move and tag_write revert as no-ops (because their
// randomly generated paths don't exist on disk), and metadata_update fails
// (because the random BookID doesn't resolve) — both outcomes preserve the
// idempotency invariant.
func TestProp_UndoIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	store := newPropStore(t)
	rapid.Check(t, func(rt *rapid.T) {
		opID := "op-" + rapid.StringMatching(`[a-z0-9]{8,16}`).Draw(rt, "op_id")
		n := rapid.IntRange(1, 6).Draw(rt, "n_changes")

		for i := 0; i < n; i++ {
			change := rapidgen.OperationChange(rt, opID, "book-"+rapid.StringMatching(`[a-z0-9]{4,10}`).Draw(rt, "book_id"))
			if err := store.CreateOperationChange(change); err != nil {
				t.Fatalf("create change: %v", err)
			}
		}

		first, err := RunUndoOperation(store, opID, nil)
		if err != nil {
			t.Fatalf("first undo: %v", err)
		}

		second, err := RunUndoOperation(store, opID, nil)
		if err != nil {
			t.Fatalf("second undo: %v", err)
		}

		// The second run must not revert anything new — every change is
		// either already-reverted (if the first run succeeded) or still
		// failing the same way (if the first run failed).
		if second.Reverted != 0 {
			t.Errorf("second undo reverted = %d, want 0 (first=%+v second=%+v)",
				second.Reverted, first, second)
		}
		if second.SkippedReverted != first.Reverted {
			t.Errorf("second.SkippedReverted = %d, want %d (first.Reverted)",
				second.SkippedReverted, first.Reverted)
		}
		if second.Failed != first.Failed {
			t.Errorf("second.Failed = %d, want %d (first.Failed) — failures should reproduce",
				second.Failed, first.Failed)
		}
		if first.Reverted+first.Failed != n {
			t.Errorf("first run accounted for %d changes, want %d",
				first.Reverted+first.Failed, n)
		}
	})
}

// TestProp_UndoRedoRoundTrip verifies that for a file_move change, undo moves
// new → old, and re-running the original move (forward) leaves the file
// byte-for-byte identical at NewValue. This is the "redo" side of the
// reversibility story: the undo engine must cleanly release its hold on the
// new path so the caller can re-execute the original operation.
func TestProp_UndoRedoRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("slow property test; run without -short")
	}
	store := newPropStore(t)
	rapid.Check(t, func(rt *rapid.T) {
		// Unique opID per iteration so accumulated state from prior iterations
		// doesn't affect this one's Reverted == 1 assertion.
		opID := "op-rt-" + rapid.StringMatching(`[a-z0-9]{6,12}`).Draw(rt, "op_id")

		// t.TempDir in a rapid.Check closure returns the *outer* test's
		// temp dir (rapid.T doesn't expose TempDir), so we scope each
		// draw under a unique subdirectory to avoid path collisions
		// across shrinks.
		root := filepath.Join(t.TempDir(), "rt-"+rapid.StringMatching(`[a-z0-9]{6,12}`).Draw(rt, "root"))

		oldSeg := rapid.StringMatching(`[a-z0-9_-]{3,10}`).Draw(rt, "old_seg")
		newSeg := rapid.StringMatching(`[a-z0-9_-]{3,10}`).Draw(rt, "new_seg")
		if oldSeg == newSeg {
			// Same-path moves are degenerate and would fail the "target
			// already exists" check in revertFileMove. Skip to keep the
			// property focused on the real reversibility invariant.
			t.Skip("degenerate same-path draw")
		}
		fileName := rapid.StringMatching(`[a-z0-9]{3,8}\.m4b`).Draw(rt, "file")
		oldPath := filepath.Join(root, oldSeg, fileName)
		newPath := filepath.Join(root, newSeg, fileName)

		content := rapid.StringMatching(`[A-Za-z0-9 ]{4,64}`).Draw(rt, "content")
		if err := os.MkdirAll(filepath.Dir(newPath), 0o775); err != nil {
			t.Fatalf("mkdir new: %v", err)
		}
		if err := os.WriteFile(newPath, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		change := &database.OperationChange{
			OperationID: opID,
			BookID:      "b-rt",
			ChangeType:  "file_move",
			OldValue:    oldPath,
			NewValue:    newPath,
		}
		if err := store.CreateOperationChange(change); err != nil {
			t.Fatalf("create change: %v", err)
		}

		// Undo: the file should travel new → old.
		res, err := RunUndoOperation(store, opID, nil)
		if err != nil {
			t.Fatalf("undo: %v", err)
		}
		if res.Reverted != 1 {
			t.Fatalf("undo reverted = %d, want 1 (res=%+v)", res.Reverted, res)
		}
		if _, err := os.Stat(oldPath); err != nil {
			t.Fatalf("old path missing after undo: %v", err)
		}
		if _, err := os.Stat(newPath); !os.IsNotExist(err) {
			t.Fatalf("new path should not exist after undo, stat err=%v", err)
		}

		// Redo: re-execute the original move forward. Re-creating the
		// parent dir is part of the original operation.
		if err := os.MkdirAll(filepath.Dir(newPath), 0o775); err != nil {
			t.Fatalf("mkdir new (redo): %v", err)
		}
		if err := os.Rename(oldPath, newPath); err != nil {
			t.Fatalf("redo rename: %v", err)
		}

		// Content must survive the round-trip unchanged.
		got, err := os.ReadFile(newPath)
		if err != nil {
			t.Fatalf("read after redo: %v", err)
		}
		if string(got) != content {
			t.Errorf("content after redo = %q, want %q", string(got), content)
		}
		if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
			t.Errorf("old path should be empty after redo, stat err=%v", err)
		}
	})
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
