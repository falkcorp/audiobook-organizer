// file: internal/database/sql_activity_summary_clamp_test.go
// version: 1.0.0
// guid: 8b47e0c9-2f13-45da-9e60-c4a1d5382bf7
// last-edited: 2026-09-08

package database

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// legacySrcKeySeq makes each fixture row's src_key unique.
var legacySrcKeySeq int

// insertLegacyOversizedSummary writes a row the way it existed BEFORE
// clampActivitySummary was added to the write path.
//
// It has to bypass Record on purpose: Record clamps, so it is structurally
// incapable of producing the rows this backfill exists to fix. A test that built
// its fixture through Record would be asserting against an empty set and would
// pass no matter what ClampOversizedSummaries did.
func insertLegacyOversizedSummary(t *testing.T, s *SQLActivityStore, summary string) int64 {
	t.Helper()
	// src_key is a UNIQUE index. Deriving it from the summary text would
	// collide across these fixtures, which are deliberately long runs of one
	// repeated character, so give each row its own key.
	legacySrcKeySeq++
	srcKey := fmt.Sprintf("legacy-fixture-%d", legacySrcKeySeq)
	res, err := s.writer.Exec(s.dialect.rebind(sqlActInsert),
		srcKey, time.Now().UnixMilli(), "info", "system", "error",
		"itunes", "", "", summary, nil, "[]", nil)
	if err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}
	return id
}

func readSummary(t *testing.T, s *SQLActivityStore, id int64) string {
	t.Helper()
	var got string
	if err := s.reader.QueryRow(s.dialect.rebind(`SELECT summary FROM activity WHERE id = ?`), id).
		Scan(&got); err != nil {
		t.Fatalf("read summary id=%d: %v", id, err)
	}
	return got
}

// A summary can sit under the cap in CHARACTERS while sitting far over it in
// BYTES. SQLite's length() returns characters for TEXT, so an uncast predicate
// (`length(summary) > 8192`) does not select this row — and it is exactly the
// kind of row worth clamping, because it occupies three bytes per character on
// disk.
//
// This is the oracle for the CAST(summary AS BLOB) in oversizedSummarySelect.
// Delete the cast and only this test fails; every other test here uses ASCII,
// where characters and bytes coincide and the bug is invisible.
func TestClampOversizedSummaries_SelectsByBytesNotCharacters(t *testing.T) {
	s := newTestSQLStore(t)

	// 4,000 three-byte runes: 4,000 characters (under the 8,192 cap by
	// character count) but 12,000 bytes (over it by byte count).
	const runeCount = 4000
	summary := strings.Repeat("あ", runeCount)
	if len([]rune(summary)) >= activitySummaryMax {
		t.Fatalf("fixture invalid: %d characters is not under the %d cap",
			len([]rune(summary)), activitySummaryMax)
	}
	if len(summary) <= activitySummaryMax {
		t.Fatalf("fixture invalid: %d bytes is not over the %d cap",
			len(summary), activitySummaryMax)
	}
	id := insertLegacyOversizedSummary(t, s, summary)

	res, err := s.ClampOversizedSummaries(context.Background(), ClampSummariesOptions{})
	if err != nil {
		t.Fatalf("clamp: %v", err)
	}
	if res.Clamped != 1 {
		t.Fatalf("a row over the cap in bytes but under it in characters was not clamped: "+
			"Clamped=%d (length() on TEXT counts characters — the predicate needs CAST AS BLOB)",
			res.Clamped)
	}
	if got := readSummary(t, s, id); len(got) > activitySummaryMax {
		t.Errorf("clamped summary is still %d bytes, over the %d cap", len(got), activitySummaryMax)
	}
}

// The clamp must be safe to re-run: a second pass over an already-clamped table
// rewrites nothing. Without this, an interrupted-and-resumed backfill (the
// expected way a multi-GB pass completes) would keep re-truncating rows and
// stacking a second marker onto each one.
func TestClampOversizedSummaries_IsIdempotent(t *testing.T) {
	s := newTestSQLStore(t)
	for i := range 3 {
		insertLegacyOversizedSummary(t, s, strings.Repeat("x", activitySummaryMax*2)+string(rune('a'+i)))
	}

	first, err := s.ClampOversizedSummaries(context.Background(), ClampSummariesOptions{})
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if first.Clamped != 3 {
		t.Fatalf("first pass clamped %d rows, want 3", first.Clamped)
	}

	second, err := s.ClampOversizedSummaries(context.Background(), ClampSummariesOptions{})
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if second.Scanned != 0 || second.Clamped != 0 {
		t.Errorf("second pass was not a no-op: scanned=%d clamped=%d "+
			"(the clamp result must fall at or under the cap so it stops matching the predicate)",
			second.Scanned, second.Clamped)
	}
}

// DryRun must measure exactly what a real run would free, while leaving the
// stored value untouched. A dry run that under-reports is worse than none: it is
// the number someone uses to decide whether the real run is worth its downtime.
func TestClampOversizedSummaries_DryRunMeasuresWithoutWriting(t *testing.T) {
	s := newTestSQLStore(t)
	original := strings.Repeat("y", activitySummaryMax*3)
	id := insertLegacyOversizedSummary(t, s, original)

	dry, err := s.ClampOversizedSummaries(context.Background(), ClampSummariesOptions{DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if got := readSummary(t, s, id); got != original {
		t.Fatalf("dry run modified the row: stored length %d, original %d", len(got), len(original))
	}

	wet, err := s.ClampOversizedSummaries(context.Background(), ClampSummariesOptions{})
	if err != nil {
		t.Fatalf("real run: %v", err)
	}
	if dry.Reclaimed() != wet.Reclaimed() {
		t.Errorf("dry run predicted %d bytes reclaimed, real run freed %d",
			dry.Reclaimed(), wet.Reclaimed())
	}
}

// Reclaimed() must equal the bytes actually removed from the column, measured
// from the database rather than from the result struct's own arithmetic.
func TestClampOversizedSummaries_ReclaimedMatchesBytesActuallyFreed(t *testing.T) {
	s := newTestSQLStore(t)
	original := strings.Repeat("z", activitySummaryMax*5)
	id := insertLegacyOversizedSummary(t, s, original)

	res, err := s.ClampOversizedSummaries(context.Background(), ClampSummariesOptions{})
	if err != nil {
		t.Fatalf("clamp: %v", err)
	}
	actual := int64(len(original) - len(readSummary(t, s, id)))
	if res.Reclaimed() != actual {
		t.Errorf("Reclaimed()=%d but the column shrank by %d bytes", res.Reclaimed(), actual)
	}
}

// A capped run must say it stopped early. Without Truncated a caller cannot
// distinguish "clamped every oversized row" from "clamped the first Max of
// them", which is the difference between finished and half-done.
func TestClampOversizedSummaries_MaxStopsEarlyAndReportsTruncated(t *testing.T) {
	s := newTestSQLStore(t)
	for i := range 5 {
		insertLegacyOversizedSummary(t, s, strings.Repeat("q", activitySummaryMax*2)+string(rune('a'+i)))
	}

	res, err := s.ClampOversizedSummaries(context.Background(), ClampSummariesOptions{Max: 2})
	if err != nil {
		t.Fatalf("clamp: %v", err)
	}
	if res.Clamped != 2 {
		t.Errorf("Clamped=%d, want 2 (Max must bound the rewrite count)", res.Clamped)
	}
	if !res.Truncated {
		t.Error("Truncated is false after a run that stopped on Max with rows left")
	}

	rest, err := s.ClampOversizedSummaries(context.Background(), ClampSummariesOptions{})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if rest.Clamped != 3 {
		t.Errorf("resume clamped %d rows, want the remaining 3", rest.Clamped)
	}
	if rest.Truncated {
		t.Error("Truncated is true after a run that reached the end of the table")
	}
}

// Cancelling mid-pass must return committed progress rather than discarding it,
// so an interrupted multi-hour run resumes instead of restarting.
func TestClampOversizedSummaries_CancelReturnsCommittedProgress(t *testing.T) {
	s := newTestSQLStore(t)
	insertLegacyOversizedSummary(t, s, strings.Repeat("w", activitySummaryMax*2))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := s.ClampOversizedSummaries(ctx, ClampSummariesOptions{})
	if err == nil {
		t.Fatal("cancelled pass returned nil error")
	}
	if res.Clamped != 0 {
		t.Errorf("Clamped=%d on a pass cancelled before its first batch", res.Clamped)
	}
}
