// file: internal/database/activity_summary_clamp_test.go
// version: 1.0.0
// guid: 21adeec8-3e0a-48f2-9a07-6a196e44026f
// last-edited: 2026-09-08
//
// Guards the activity summary clamp. Prod's activity database reached 5.3 GB on
// 2026-09-07 with 5.48 GB of it in the summary column alone; the largest single
// summary was 9,558,930 bytes.

package database

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClampActivitySummary_LeavesNormalSummariesAlone(t *testing.T) {
	const s = "Scanning folders: 7960/121940 (The Land - Founding - Chaos Seeds, Book 1)"
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
	// Record and recordBatch both clamp, and a dual-write store hands the same
	// entry to two backends. Clamping twice must not keep appending markers.
	once := clampActivitySummary(strings.Repeat("y", 5_000_000))
	twice := clampActivitySummary(once)

	if once != twice {
		t.Fatalf("clamp is not idempotent: second pass changed the value (%d -> %d bytes)", len(once), len(twice))
	}
}
