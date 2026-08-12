// file: internal/database/activity_compact_test.go
// version: 2.1.0
// guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-08-11

package database

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestNutsActivityStore creates a temp NutsActivityStore and registers cleanup.
func newTestNutsActivityStore(t *testing.T) *NutsActivityStore {
	t.Helper()
	dir := t.TempDir()
	store, err := NewNutsActivityStore(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestCompactByDay_BasicCompaction inserts 5 change-tier entries across 2
// days plus 1 audit entry on day 1, compacts, and verifies 2 digests
// created, 6 entries deleted (audit folds into day 1's digest), and the
// audit entry is reflected in that digest.
func TestCompactByDay_BasicCompaction(t *testing.T) {
	s := newTestNutsActivityStore(t)

	day1 := time.Date(2025, 6, 10, 12, 0, 0, 0, time.UTC)
	day2 := time.Date(2025, 6, 11, 14, 0, 0, 0, time.UTC)
	olderThan := time.Date(2025, 6, 12, 0, 0, 0, 0, time.UTC)

	// 3 change-tier entries on day 1
	for i := 0; i < 3; i++ {
		_, err := s.Record(ActivityEntry{
			Tier:      "change",
			Type:      "metadata_applied",
			Level:     "info",
			Source:    "pipeline",
			BookID:    "book-1",
			Summary:   "applied metadata",
			Timestamp: day1.Add(time.Duration(i) * time.Minute),
			Details:   map[string]any{"book_title": "Test Book", "fields": "title,author", "source": "openai"},
		})
		require.NoError(t, err)
	}

	// 2 change-tier entries on day 2
	for i := 0; i < 2; i++ {
		_, err := s.Record(ActivityEntry{
			Tier:      "change",
			Type:      "tag_written",
			Level:     "info",
			Source:    "writer",
			BookID:    "book-2",
			Summary:   "wrote tags",
			Timestamp: day2.Add(time.Duration(i) * time.Minute),
			Details:   map[string]any{"title": "Other Book", "tag_count": float64(5), "file_count": float64(2)},
		})
		require.NoError(t, err)
	}

	// 1 audit entry on day 1 — folds into day 1's digest with forensic
	// fields (operation_id, tier='audit') preserved as DigestItem.
	_, err := s.Record(ActivityEntry{
		Tier:        "audit",
		Type:        "user_login",
		Level:       "info",
		Source:      "auth",
		OperationID: "op-login-42",
		Summary:     "user logged in",
		Timestamp:   day1.Add(30 * time.Minute),
	})
	require.NoError(t, err)

	result, err := s.CompactByDay(context.Background(), olderThan)
	require.NoError(t, err)
	assert.Equal(t, 2, result.DaysCompacted)
	assert.Equal(t, 6, result.EntriesDeleted, "5 change + 1 audit folded")

	// Should now have: 2 digest rows total — audit folded into day 1.
	all, total, err := s.Query(context.Background(), ActivityFilter{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 2, total)

	var digestCount, auditSurvived int
	var day1Digest DigestDetails
	for _, e := range all {
		switch e.Tier {
		case "digest":
			digestCount++
			assert.Equal(t, "daily_digest", e.Type)
			assert.Equal(t, "compaction", e.Source)

			require.NotNil(t, e.Details)
			detailsJSON, err := json.Marshal(e.Details)
			require.NoError(t, err)
			var dd DigestDetails
			err = json.Unmarshal(detailsJSON, &dd)
			require.NoError(t, err)
			assert.NotEmpty(t, dd.Date)
			assert.Greater(t, dd.OriginalCount, 0)
			assert.NotEmpty(t, dd.Counts)
			assert.NotEmpty(t, dd.Items)

			if dd.Date == "2025-06-10" {
				day1Digest = dd
			}
		case "audit":
			auditSurvived++
		}
	}
	assert.Equal(t, 2, digestCount, "expected 2 digest rows")
	assert.Equal(t, 0, auditSurvived, "audit entry must be folded, not survive raw")

	// Day 1 digest must reflect 4 entries (3 change + 1 audit), keep the
	// audit's operation_id, and place audit first in items.
	require.Equal(t, "2025-06-10", day1Digest.Date)
	assert.Equal(t, 4, day1Digest.OriginalCount)
	assert.Equal(t, 1, day1Digest.Counts["user_login"])
	assert.Equal(t, 3, day1Digest.Counts["metadata_applied"])
	require.NotEmpty(t, day1Digest.Items)
	first := day1Digest.Items[0]
	assert.Equal(t, "audit", first.Tier, "audit items must sort first")
	assert.Equal(t, "user_login", first.Type)
	assert.Equal(t, "op-login-42", first.OperationID, "operation_id preserved")
}

// TestCompactByDay_Idempotent verifies that compacting twice is a no-op the
// second time.
func TestCompactByDay_Idempotent(t *testing.T) {
	s := newTestNutsActivityStore(t)

	ts := time.Date(2025, 5, 1, 10, 0, 0, 0, time.UTC)
	olderThan := time.Date(2025, 5, 2, 0, 0, 0, 0, time.UTC)

	_, err := s.Record(ActivityEntry{
		Tier:      "change",
		Type:      "config_changed",
		Level:     "info",
		Source:    "settings",
		Summary:   "changed setting",
		Timestamp: ts,
		Details:   map[string]any{"key": "scan_interval"},
	})
	require.NoError(t, err)

	// First compaction
	r1, err := s.CompactByDay(context.Background(), olderThan)
	require.NoError(t, err)
	assert.Equal(t, 1, r1.DaysCompacted)
	assert.Equal(t, 1, r1.EntriesDeleted)

	// Second compaction — should be no-op
	r2, err := s.CompactByDay(context.Background(), olderThan)
	require.NoError(t, err)
	assert.Equal(t, 0, r2.DaysCompacted)
	assert.Equal(t, 0, r2.EntriesDeleted)
}

// TestCompactByDay_FoldsAuditTier verifies that audit-tier entries are
// folded into the daily digest (preserving forensic fields) rather than
// left as raw rows. This is the regression test for the "Compact →
// Everything (now)" button leaving pages of audit entries behind.
func TestCompactByDay_FoldsAuditTier(t *testing.T) {
	s := newTestNutsActivityStore(t)

	ts := time.Date(2025, 4, 15, 8, 0, 0, 0, time.UTC)
	olderThan := time.Date(2025, 4, 16, 0, 0, 0, 0, time.UTC)

	_, err := s.Record(ActivityEntry{
		Tier:        "audit",
		Type:        "permission_change",
		Level:       "info",
		Source:      "admin",
		OperationID: "op-perm-7",
		Summary:     "permissions updated",
		Timestamp:   ts,
	})
	require.NoError(t, err)

	result, err := s.CompactByDay(context.Background(), olderThan)
	require.NoError(t, err)
	assert.Equal(t, 1, result.DaysCompacted)
	assert.Equal(t, 1, result.EntriesDeleted)

	// Original audit row gone, replaced by a single digest row.
	all, total, err := s.Query(context.Background(), ActivityFilter{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, all, 1)
	assert.Equal(t, "digest", all[0].Tier)

	// Digest must carry the audit item with its operation_id and tier.
	detailsJSON, err := json.Marshal(all[0].Details)
	require.NoError(t, err)
	var dd DigestDetails
	require.NoError(t, json.Unmarshal(detailsJSON, &dd))
	require.Len(t, dd.Items, 1)
	item := dd.Items[0]
	assert.Equal(t, "audit", item.Tier)
	assert.Equal(t, "permission_change", item.Type)
	assert.Equal(t, "op-perm-7", item.OperationID)
	assert.Equal(t, 1, dd.Counts["permission_change"])
}

// TestCompactByDay_TruncatesLargeDays inserts 600 entries on one day and
// verifies items are capped at 500 with truncation metadata.
func TestCompactByDay_TruncatesLargeDays(t *testing.T) {
	s := newTestNutsActivityStore(t)

	day := time.Date(2025, 3, 20, 6, 0, 0, 0, time.UTC)
	olderThan := time.Date(2025, 3, 21, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 600; i++ {
		_, err := s.Record(ActivityEntry{
			Tier:      "debug",
			Type:      "tag_written",
			Level:     "debug",
			Source:    "writer",
			Summary:   "wrote tags",
			Timestamp: day.Add(time.Duration(i) * time.Second),
			Details:   map[string]any{"tag_count": float64(3), "file_count": float64(1)},
		})
		require.NoError(t, err)
	}

	result, err := s.CompactByDay(context.Background(), olderThan)
	require.NoError(t, err)
	assert.Equal(t, 1, result.DaysCompacted)
	assert.Equal(t, 600, result.EntriesDeleted)

	// Verify digest details
	all, total, err := s.Query(context.Background(), ActivityFilter{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, all, 1)

	digest := all[0]
	assert.Equal(t, "digest", digest.Tier)
	require.NotNil(t, digest.Details)

	detailsJSON, err := json.Marshal(digest.Details)
	require.NoError(t, err)
	var dd DigestDetails
	err = json.Unmarshal(detailsJSON, &dd)
	require.NoError(t, err)

	assert.Len(t, dd.Items, 500, "items should be capped at 500")
	assert.True(t, dd.Truncated, "Truncated should be true")
	assert.Equal(t, 100, dd.TruncatedCount, "100 items should have been truncated")
	assert.Equal(t, 600, dd.OriginalCount)
}

// TestCompactByDay_MergesIntoExistingDigest is the regression test for the
// "compact Everything (now) returns 0" bug. When a daily digest already
// exists for a date AND more uncompacted change/debug entries have been
// written for that same date (late imports, background tasks, etc.), a
// second compact run used to skip past the day and leave the new entries
// permanently uncompacted. This test proves that's fixed: the second run
// merges new entries into the existing digest and deletes the originals.
func TestCompactByDay_MergesIntoExistingDigest(t *testing.T) {
	s := newTestNutsActivityStore(t)

	// Day 1: three initial entries at 08:00 on 2025-05-15.
	day := time.Date(2025, 5, 15, 8, 0, 0, 0, time.UTC)
	// olderThan is set so every run compacts everything written so far.
	initialCutoff := time.Date(2025, 5, 15, 12, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		_, err := s.Record(ActivityEntry{
			Tier:      "change",
			Type:      "metadata_applied",
			Level:     "info",
			Source:    "test",
			Summary:   "initial entry",
			Timestamp: day,
			Details:   map[string]any{"book_title": "Initial Book"},
		})
		require.NoError(t, err)
	}

	// First compaction — 3 entries compacted into 1 digest.
	r1, err := s.CompactByDay(context.Background(), initialCutoff)
	require.NoError(t, err)
	assert.Equal(t, 1, r1.DaysCompacted, "first run should create 1 digest")
	assert.Equal(t, 3, r1.EntriesDeleted)

	// Late-arriving entries: 5 more entries for the SAME day, written
	// AFTER the first compact ran (e.g. background imports, crash recovery).
	lateDay := time.Date(2025, 5, 15, 11, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		_, err := s.Record(ActivityEntry{
			Tier:      "change",
			Type:      "tag_written",
			Level:     "info",
			Source:    "test",
			Summary:   "late entry",
			Timestamp: lateDay,
			Details:   map[string]any{"book_title": "Late Book", "tag_count": float64(4), "file_count": float64(1)},
		})
		require.NoError(t, err)
	}

	// Second compaction — must MERGE the 5 late entries into the
	// existing digest, not skip them.
	r2, err := s.CompactByDay(context.Background(), initialCutoff)
	require.NoError(t, err)
	assert.Equal(t, 1, r2.DaysCompacted, "second run should merge into existing digest (counted as 1 day compacted)")
	assert.Equal(t, 5, r2.EntriesDeleted, "all 5 late entries must be deleted")

	// Verify via public API: query digest entries for 2025-05-15.
	all, total, err := s.Query(context.Background(), ActivityFilter{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 1, total, "must be exactly one digest row after merge")
	require.Len(t, all, 1)
	assert.Equal(t, "digest", all[0].Tier)

	// Unmarshal the merged digest and verify it contains both old and new counts.
	detailsJSON, err := json.Marshal(all[0].Details)
	require.NoError(t, err)
	var dd DigestDetails
	err = json.Unmarshal(detailsJSON, &dd)
	require.NoError(t, err)

	assert.Equal(t, 8, dd.OriginalCount, "merged digest should cover all 8 entries (3 old + 5 new)")
	assert.Equal(t, 3, dd.Counts["metadata_applied"], "old counts preserved")
	assert.Equal(t, 5, dd.Counts["tag_written"], "new counts added")
	assert.Len(t, dd.Items, 8, "all 8 items present")
}

// TestCompactByDay_DigestItemTimestampAndTags verifies that each DigestItem
// carries the source row's timestamp (non-zero) and any tags from that row.
// This regression test covers the 2026-05-20 addition of these fields.
func TestCompactByDay_DigestItemTimestampAndTags(t *testing.T) {
	s := newTestNutsActivityStore(t)

	base := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	olderThan := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)

	// Insert 3 entries with explicit timestamps and tags, 1 minute apart.
	for i := 0; i < 3; i++ {
		_, err := s.Record(ActivityEntry{
			Tier:      "change",
			Type:      "metadata_applied",
			Level:     "info",
			Source:    "pipeline",
			Summary:   "applied metadata",
			Timestamp: base.Add(time.Duration(i) * time.Minute),
			Tags:      []string{"action:metadata-apply", "outcome:ok", "source:pipeline"},
		})
		require.NoError(t, err)
	}

	result, err := s.CompactByDay(context.Background(), olderThan)
	require.NoError(t, err)
	assert.Equal(t, 1, result.DaysCompacted)
	assert.Equal(t, 3, result.EntriesDeleted)

	all, _, err := s.Query(context.Background(), ActivityFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, all, 1)

	detailsJSON, err := json.Marshal(all[0].Details)
	require.NoError(t, err)
	var dd DigestDetails
	require.NoError(t, json.Unmarshal(detailsJSON, &dd))
	require.Len(t, dd.Items, 3)

	// Items are sorted audit→error→normal; all 3 here are normal.
	// Verify timestamps are ascending (as inserted) and non-zero.
	for i, item := range dd.Items {
		assert.False(t, item.Timestamp.IsZero(), "item %d: Timestamp must be non-zero", i)
		assert.NotEmpty(t, item.Tags, "item %d: Tags must be populated", i)
		assert.Contains(t, item.Tags, "action:metadata-apply", "item %d: action tag preserved", i)
		assert.Contains(t, item.Tags, "outcome:ok", "item %d: outcome tag preserved", i)
	}

	// Items should be in ascending timestamp order (all are "normal" tier).
	for i := 1; i < len(dd.Items); i++ {
		assert.True(t, !dd.Items[i].Timestamp.Before(dd.Items[i-1].Timestamp),
			"items should be in non-decreasing timestamp order")
	}
}

// TestNutsActivityStore_RecompactDigests verifies that RecompactDigests:
//  1. Re-derives type+tags on legacy digest items (type=system_log, empty tags).
//  2. Returns the correct touched count.
//  3. Is idempotent: a second run returns 0 touched.
func TestNutsActivityStore_RecompactDigests(t *testing.T) {
	s := newTestNutsActivityStore(t)
	ctx := context.Background()

	day := time.Date(2025, 3, 10, 0, 0, 0, 0, time.UTC)

	// Insert 3 entries with legacy-style types that should be re-derived.
	for i := 0; i < 3; i++ {
		_, err := s.Record(ActivityEntry{
			Tier:      "change",
			Type:      "system_log", // legacy type — should be re-derived
			Level:     "info",
			Source:    "compaction",
			Summary:   "applied metadata to book",
			Timestamp: day.Add(time.Duration(i) * time.Minute),
			Tags:      []string{}, // empty tags — triggers isLegacyItem
		})
		require.NoError(t, err)
	}

	// Compact to create a digest.
	olderThan := day.Add(24 * time.Hour)
	_, err := s.CompactByDay(ctx, olderThan)
	require.NoError(t, err)

	// Run RecompactDigests — should touch exactly 1 digest.
	res, err := s.RecompactDigests(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Touched, "first run: should touch the one digest")
	assert.Equal(t, 0, res.Skipped, "first run: nothing should be skipped")

	// Read back the digest and verify items got proper types and tags.
	dd, _, err := s.findExistingDigest(day.Format("2006-01-02"))
	require.NoError(t, err)
	require.Len(t, dd.Items, 3, "digest should still have 3 items")
	for i, item := range dd.Items {
		assert.NotEqual(t, "system_log", item.Type, "item %d: type should be re-derived away from system_log", i)
		assert.NotEmpty(t, item.Tags, "item %d: tags should be populated after recompact", i)
	}

	// Idempotency: second run should touch 0 (all items now have proper types/tags).
	res2, err := s.RecompactDigests(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, res2.Touched, "second run: should be idempotent (0 touched)")
	assert.Equal(t, 1, res2.Skipped, "second run: digest should be skipped as already clean")
}
