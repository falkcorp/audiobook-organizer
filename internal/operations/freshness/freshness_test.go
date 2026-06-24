// file: internal/operations/freshness/freshness_test.go
// version: 1.0.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef0123456789
// last-edited: 2026-06-24

package freshness_test

import (
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/operations/freshness"
)

func openTestDB(t *testing.T) *pebble.DB {
	t.Helper()
	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestShouldProcess_NoStamp(t *testing.T) {
	f := freshness.NewPebbleFreshness(openTestDB(t))
	if !f.ShouldProcess("acoustid_backfill", "book-1", time.Hour, false) {
		t.Error("expected true (no stamp), got false")
	}
}

func TestShouldProcess_Fresh(t *testing.T) {
	f := freshness.NewPebbleFreshness(openTestDB(t))
	if err := f.Stamp("acoustid_backfill", "book-1"); err != nil {
		t.Fatal(err)
	}
	if f.ShouldProcess("acoustid_backfill", "book-1", time.Hour, false) {
		t.Error("expected false (just stamped, within maxAge), got true")
	}
}

func TestShouldProcess_Stale(t *testing.T) {
	f := freshness.NewPebbleFreshness(openTestDB(t))
	if err := f.Stamp("acoustid_backfill", "book-1"); err != nil {
		t.Fatal(err)
	}
	// maxAge of 0 means anything is stale.
	if !f.ShouldProcess("acoustid_backfill", "book-1", 0, false) {
		t.Error("expected true (stamp older than maxAge=0), got false")
	}
}

func TestShouldProcess_Force(t *testing.T) {
	f := freshness.NewPebbleFreshness(openTestDB(t))
	if err := f.Stamp("acoustid_backfill", "book-1"); err != nil {
		t.Fatal(err)
	}
	if !f.ShouldProcess("acoustid_backfill", "book-1", time.Hour, true) {
		t.Error("expected true (force=true always returns true), got false")
	}
}

func TestStampBatch(t *testing.T) {
	f := freshness.NewPebbleFreshness(openTestDB(t))
	ids := []string{"a", "b", "c"}
	if err := f.StampBatch("lsh_backfill", ids); err != nil {
		t.Fatalf("StampBatch: %v", err)
	}
	for _, id := range ids {
		if f.ShouldProcess("lsh_backfill", id, time.Hour, false) {
			t.Errorf("id %q: expected false (just batch-stamped), got true", id)
		}
	}
	// Different op — stamps should be independent.
	if !f.ShouldProcess("other_op", "a", time.Hour, false) {
		t.Error("different op: expected true (not stamped), got false")
	}
}

func TestClearStamps(t *testing.T) {
	f := freshness.NewPebbleFreshness(openTestDB(t))
	ids := []string{"x", "y", "z"}
	if err := f.StampBatch("op", ids); err != nil {
		t.Fatalf("StampBatch: %v", err)
	}
	if err := f.ClearStamps("op"); err != nil {
		t.Fatalf("ClearStamps: %v", err)
	}
	for _, id := range ids {
		if !f.ShouldProcess("op", id, time.Hour, false) {
			t.Errorf("id %q: expected true after ClearStamps, got false", id)
		}
	}
}

func TestStampBatch_Empty(t *testing.T) {
	f := freshness.NewPebbleFreshness(openTestDB(t))
	if err := f.StampBatch("op", nil); err != nil {
		t.Fatalf("StampBatch with nil: %v", err)
	}
	if err := f.StampBatch("op", []string{}); err != nil {
		t.Fatalf("StampBatch with empty: %v", err)
	}
}

func TestClearStamps_NoOp(t *testing.T) {
	f := freshness.NewPebbleFreshness(openTestDB(t))
	// ClearStamps on an op with no stamps should not error.
	if err := f.ClearStamps("nonexistent_op"); err != nil {
		t.Fatalf("ClearStamps on empty op: %v", err)
	}
}
