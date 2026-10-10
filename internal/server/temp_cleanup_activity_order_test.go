// file: internal/server/temp_cleanup_activity_order_test.go
// version: 1.0.0
// guid: 7d3b9e52-4c1a-4f68-9a0e-2b5c8d1f6a37
// last-edited: 2026-10-10

package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// drainActivity returns every entry currently queued on w, in queue order.
func drainActivity(w *activity.Writer) []database.ActivityEntry {
	var out []database.ActivityEntry
	for {
		select {
		case e := <-w.Chan():
			out = append(out, e)
		default:
			return out
		}
	}
}

// TestCleanupOrphanedTempFiles_SummaryFollowsBatchedEntries pins the feed order
// of the temp-file sweep: the per-file batch entry first, the "Removed N"
// summary after it. Both travel the writer's single channel, so queue order is
// feed order. The summary was once emitted before the batch was flushed.
func TestCleanupOrphanedTempFiles_SummaryFollowsBatchedEntries(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a.tmp.m4b", "b.tmp.m4a"} {
		if err := os.WriteFile(filepath.Join(root, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w := activity.NewWriter(nil, 16)
	s := &Server{activityWriter: w}

	const opID = "op-temp-order"
	if got := s.CleanupOrphanedTempFiles(root, opID); got != 2 {
		t.Fatalf("removed = %d, want 2", got)
	}

	entries := drainActivity(w)
	batchAt, summaryAt := -1, -1
	for i, e := range entries {
		if e.OperationID != opID {
			continue
		}
		switch {
		case e.Tier == "batch":
			batchAt = i
		case e.Summary == "Removed 2 orphaned temp files":
			summaryAt = i
		}
	}
	if summaryAt < 0 {
		t.Fatalf("summary entry missing; entries = %+v", entries)
	}
	if batchAt < 0 {
		t.Fatalf("batched per-file entry missing (not flushed); entries = %+v", entries)
	}
	if summaryAt < batchAt {
		t.Errorf("summary queued at %d before batch entry at %d", summaryAt, batchAt)
	}
}

// TestCleanupOrphanedTempFiles_NoOpSummaryTagged covers the zero-removed run:
// the summary still exists and carries the no-op tag.
func TestCleanupOrphanedTempFiles_NoOpSummaryTagged(t *testing.T) {
	w := activity.NewWriter(nil, 16)
	s := &Server{activityWriter: w}
	if got := s.CleanupOrphanedTempFiles(t.TempDir(), "op-noop"); got != 0 {
		t.Fatalf("removed = %d, want 0", got)
	}
	for _, e := range drainActivity(w) {
		if strings.HasPrefix(e.Summary, "Removed 0 orphaned") {
			for _, tg := range e.Tags {
				if tg == activity.NoOpTag {
					return
				}
			}
			t.Fatalf("summary lacks %q tag: %+v", activity.NoOpTag, e)
		}
	}
	t.Fatal("zero-removed summary entry missing")
}
