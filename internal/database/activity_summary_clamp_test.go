// file: internal/database/activity_summary_clamp_test.go
// version: 1.1.0
// guid: 21adeec8-3e0a-48f2-9a07-6a196e44026f
// last-edited: 2026-10-19
//
// Guards the activity summary clamp. Prod's SQLite activity database (since
// removed) reached 5.3 GB on 2026-09-07 with 5.48 GB of it in the summary
// column alone; the largest single summary was 9,558,930 bytes. The Pebble
// store applies the same clamp on every write.

package database

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClampActivitySummary_LeavesNormalSummariesAlone(t *testing.T) {
	const s = "Scanning folders: 7960/121940 (Example Title, Book 1)"
	if got := clampActivitySummary(s); got != s {
		t.Fatalf("a %d-byte summary was modified; only oversized ones should be touched", len(s))
	}
}

func TestClampActivitySummary_BoundsTheProdWorstCase(t *testing.T) {
	// The real one was 9,558,930 bytes.
	huge := strings.Repeat("x", 9_558_930)

	got := clampActivitySummary(huge)

	if len(got) >= len(huge) {
		t.Fatalf("clamped length %d is not smaller than the input %d", len(got), len(huge))
	}
	// The cap is on the RESULT, marker included — that is what makes the clamp
	// idempotent (see TestClampActivitySummary_IsIdempotent).
	if len(got) > activitySummaryMax {
		t.Fatalf("clamped length %d exceeds the %d cap; the marker must fit inside the budget", len(got), activitySummaryMax)
	}
	if !strings.Contains(got, "summary truncated") {
		t.Fatalf("truncated summary must say so, got tail %q", got[max(0, len(got)-80):])
	}
	if !strings.Contains(got, "9558930") {
		t.Fatal("truncation marker must report the original size so a reader knows what was lost")
	}
}

func TestClampActivitySummary_DoesNotSplitARune(t *testing.T) {
	// Multi-byte runes straddling the cut point: a naive s[:max] would slice one
	// in half and produce invalid UTF-8, which then has to survive a JSON
	// round-trip and a TEXT column.
	got := clampActivitySummary(strings.Repeat("é", activitySummaryMax))

	if !utf8.ValidString(got) {
		t.Fatal("clamped summary is not valid UTF-8; the cut split a rune")
	}
}

func TestClampActivitySummary_IsIdempotent(t *testing.T) {
	// An entry that passes through the clamp twice must not keep appending
	// markers.
	once := clampActivitySummary(strings.Repeat("y", 5_000_000))
	twice := clampActivitySummary(once)

	if once != twice {
		t.Fatalf("clamp is not idempotent: second pass changed the value (%d -> %d bytes)", len(once), len(twice))
	}
}

// TestPebbleRecord_ClampsOversizedSummaries pins that the Pebble store bounds
// the summary on both write paths. Until 2026-10-19 only the SQLite store
// clamped; Pebble stored whatever it was handed.
func TestPebbleRecord_ClampsOversizedSummaries(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	huge := strings.Repeat("z", 3*activitySummaryMax)

	_, err := s.Record(ActivityEntry{Tier: "change", Type: "test_event", Source: "clamp_test", Summary: huge})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := s.RecordBatch([]ActivityEntry{{Tier: "debug", Type: "test_event", Source: "clamp_test", Summary: huge}}); err != nil {
		t.Fatalf("RecordBatch: %v", err)
	}

	for _, tier := range []string{"change", "debug"} {
		rows, _, err := s.Query(context.Background(), ActivityFilter{Tier: tier, Limit: 10})
		if err != nil {
			t.Fatalf("Query %s: %v", tier, err)
		}
		if len(rows) != 1 {
			t.Fatalf("tier %s: got %d rows, want 1", tier, len(rows))
		}
		got := rows[0].Summary
		if len(got) > activitySummaryMax || !strings.Contains(got, "summary truncated") {
			t.Errorf("tier %s: stored summary is %d bytes (cap %d) and marked=%t; the write path did not clamp",
				tier, len(got), activitySummaryMax, strings.Contains(got, "summary truncated"))
		}
	}
}
