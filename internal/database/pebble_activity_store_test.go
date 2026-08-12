// file: internal/database/pebble_activity_store_test.go
// version: 1.2.0
// guid: c9d0e1f2-a3b4-0010-3456-000000000010
// last-edited: 2026-08-11

// Package database — parity test suite for PebbleActivityStore.
//
// WHY a separate test file:
//   - The existing activity_store_test.go and activity_compact_test.go test the SQLite
//     ActivityStore.  The NutsDB variant is tested in activity_compact_test.go (see
//     TestNutsActivityStore_RecompactDigests).
//   - This file runs the SAME behavioral scenarios over PebbleActivityStore so any
//     regression in the new backend is caught at the same granularity as the others.
//   - "Parity gate" = every test here must pass, matching the Nuts/SQL test names.
package database

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestPebbleActivityStore creates a temp PebbleDB directory and a PebbleActivityStore.
func newTestPebbleActivityStore(t *testing.T) *PebbleActivityStore {
	t.Helper()
	dir := t.TempDir()
	db, err := pebble.Open(filepath.Join(dir, "test.pebble"), &pebble.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return NewPebbleActivityStore(db)
}

// ── parity: basic record + query ─────────────────────────────────────────────

func TestPebbleActivityStore_RecordAndQuery(t *testing.T) {
	s := newTestPebbleActivityStore(t)

	opID := "op-abc-123"
	bookID := "book-xyz-456"
	ts := time.Now().UTC().Truncate(time.Second)

	entry := ActivityEntry{
		Timestamp:   ts,
		Tier:        "change",
		Type:        "metadata_apply",
		Level:       "info",
		Source:      "apply_pipeline",
		OperationID: opID,
		BookID:      bookID,
		Summary:     "Applied metadata",
		Details:     map[string]any{"field": "title", "old": "foo", "new": "bar"},
		Tags:        []string{"enrichment", "isbn"},
	}

	id, err := s.Record(entry)
	require.NoError(t, err)
	assert.Greater(t, id, int64(0))

	entries, total, err := s.Query(context.Background(), ActivityFilter{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, entries, 1)

	got := entries[0]
	assert.Equal(t, id, got.ID)
	assert.Equal(t, "change", got.Tier)
	assert.Equal(t, "metadata_apply", got.Type)
	assert.Equal(t, "info", got.Level)
	assert.Equal(t, "apply_pipeline", got.Source)
	assert.Equal(t, opID, got.OperationID)
	assert.Equal(t, bookID, got.BookID)
	assert.Equal(t, "Applied metadata", got.Summary)
	assert.Nil(t, got.PrunedAt)

	// Details round-trip
	require.NotNil(t, got.Details)
	assert.Equal(t, "title", got.Details["field"])

	// Tags round-trip
	assert.ElementsMatch(t, []string{"enrichment", "isbn"}, got.Tags)
}

// ── parity: query filters ─────────────────────────────────────────────────────

func TestPebbleActivityStore_QueryFilters(t *testing.T) {
	s := newTestPebbleActivityStore(t)

	now := time.Now().UTC()

	entries := []ActivityEntry{
		{Tier: "change", Type: "metadata_apply", Level: "info", Source: "s1",
			OperationID: "op-1", BookID: "book-1", Summary: "A",
			Tags: []string{"alpha", "beta"}, Timestamp: now.Add(-4 * time.Minute)},
		{Tier: "change", Type: "tag_write", Level: "warn", Source: "s2",
			OperationID: "op-1", BookID: "book-2", Summary: "B",
			Tags: []string{"beta", "gamma"}, Timestamp: now.Add(-3 * time.Minute)},
		{Tier: "debug", Type: "isbn_lookup", Level: "info", Source: "s3",
			OperationID: "op-2", BookID: "book-1", Summary: "C",
			Tags: []string{"gamma"}, Timestamp: now.Add(-2 * time.Minute)},
		{Tier: "debug", Type: "isbn_lookup", Level: "error", Source: "s4",
			OperationID: "op-3", BookID: "book-3", Summary: "D",
			Tags: []string{"alpha"}, Timestamp: now.Add(-1 * time.Minute)},
	}
	for _, e := range entries {
		_, err := s.Record(e)
		require.NoError(t, err)
	}

	t.Run("filter_by_tier", func(t *testing.T) {
		res, total, err := s.Query(context.Background(), ActivityFilter{Tier: "debug", Limit: 50})
		require.NoError(t, err)
		assert.Equal(t, 2, total)
		assert.Len(t, res, 2)
		for _, r := range res {
			assert.Equal(t, "debug", r.Tier)
		}
	})

	t.Run("filter_by_type", func(t *testing.T) {
		res, total, err := s.Query(context.Background(), ActivityFilter{Type: "isbn_lookup", Limit: 50})
		require.NoError(t, err)
		assert.Equal(t, 2, total)
		assert.Len(t, res, 2)
	})

	t.Run("filter_by_operation_id", func(t *testing.T) {
		res, total, err := s.Query(context.Background(), ActivityFilter{OperationID: "op-1", Limit: 50})
		require.NoError(t, err)
		assert.Equal(t, 2, total)
		assert.Len(t, res, 2)
	})

	t.Run("filter_by_book_id", func(t *testing.T) {
		res, total, err := s.Query(context.Background(), ActivityFilter{BookID: "book-1", Limit: 50})
		require.NoError(t, err)
		assert.Equal(t, 2, total)
		assert.Len(t, res, 2)
	})

	t.Run("filter_single_tag_alpha", func(t *testing.T) {
		res, total, err := s.Query(context.Background(), ActivityFilter{Tags: []string{"alpha"}, Limit: 50})
		require.NoError(t, err)
		assert.Equal(t, 2, total)
		assert.Len(t, res, 2)
	})

	t.Run("filter_two_tags_requires_both", func(t *testing.T) {
		// Only entry[0] has both alpha AND beta
		res, total, err := s.Query(context.Background(), ActivityFilter{Tags: []string{"alpha", "beta"}, Limit: 50})
		require.NoError(t, err)
		assert.Equal(t, 1, total)
		assert.Len(t, res, 1)
		assert.Equal(t, "A", res[0].Summary)
	})

	t.Run("limit", func(t *testing.T) {
		res, total, err := s.Query(context.Background(), ActivityFilter{Limit: 2})
		require.NoError(t, err)
		assert.Len(t, res, 2)
		// CONTRACT CHANGE (bounded query): total is exact only when the walk
		// exhausts the log. A paged query stops at offset+limit+1 matches so it
		// never materializes the whole log, so total here is that probe value
		// (3), not the exact match count (4). Asserting the exact 3 — rather
		// than a >= range — is deliberate: a range would also pass against the
		// old full-scan implementation that returned 4.
		assert.Equal(t, 3, total)
		// The probe row is what keeps pagination working: total must stay
		// strictly greater than this page so the caller knows more exist.
		assert.Greater(t, total, len(res))
	})

	t.Run("offset", func(t *testing.T) {
		res, total, err := s.Query(context.Background(), ActivityFilter{Limit: 50, Offset: 3})
		require.NoError(t, err)
		assert.Equal(t, 4, total)
		assert.Len(t, res, 1)
	})
}

// ── parity: prune ─────────────────────────────────────────────────────────────

func TestPebbleActivityStore_Prune(t *testing.T) {
	s := newTestPebbleActivityStore(t)

	cutoff := time.Now().UTC().Add(-1 * time.Hour)

	for i := 0; i < 3; i++ {
		_, err := s.Record(ActivityEntry{
			Tier: "debug", Type: "tag_write", Level: "debug",
			Source: "writer", Summary: "old debug",
			Timestamp: cutoff.Add(-time.Duration(i+1) * time.Minute),
		})
		require.NoError(t, err)
	}
	_, err := s.Record(ActivityEntry{
		Tier: "audit", Type: "tag_write", Level: "info",
		Source: "writer", Summary: "old audit",
		Timestamp: cutoff.Add(-5 * time.Minute),
	})
	require.NoError(t, err)
	_, err = s.Record(ActivityEntry{
		Tier: "debug", Type: "tag_write", Level: "debug",
		Source: "writer", Summary: "recent debug",
		Timestamp: time.Now().UTC(),
	})
	require.NoError(t, err)

	deleted, err := s.Prune(cutoff, "debug")
	require.NoError(t, err)
	assert.Equal(t, 3, deleted)

	all, total, err := s.Query(context.Background(), ActivityFilter{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, all, 2)

	for _, e := range all {
		assert.NotEqual(t, "old debug", e.Summary)
	}
}

// ── parity: wipe all activity ─────────────────────────────────────────────────

func TestPebbleActivityStore_WipeAllActivity(t *testing.T) {
	s := newTestPebbleActivityStore(t)

	for _, tier := range actTiers[:4] { // change/debug/audit/info
		_, err := s.Record(ActivityEntry{
			Tier: tier, Type: "test", Level: "info",
			Source: "test", Summary: "entry in " + tier,
		})
		require.NoError(t, err)
	}

	n, err := s.WipeAllActivity()
	require.NoError(t, err)
	assert.Equal(t, int64(4), n)

	_, total, err := s.Query(context.Background(), ActivityFilter{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 0, total)
}

// ── parity: GetDistinctSources ────────────────────────────────────────────────

func TestPebbleActivityStore_GetDistinctSources(t *testing.T) {
	s := newTestPebbleActivityStore(t)

	entries := []ActivityEntry{
		{Tier: "change", Type: "request", Level: "info", Source: "gin", Summary: "req 1"},
		{Tier: "debug", Type: "request", Level: "debug", Source: "gin", Summary: "req 2"},
		{Tier: "info", Type: "scan", Level: "info", Source: "scanner", Summary: "scan 1"},
		{Tier: "change", Type: "metadata_apply", Level: "info", Source: "metadata", Summary: "apply 1"},
	}
	for _, e := range entries {
		_, err := s.Record(e)
		require.NoError(t, err)
	}

	t.Run("unfiltered_returns_all_sources", func(t *testing.T) {
		sources, err := s.GetDistinctSources(context.Background(), ActivityFilter{})
		require.NoError(t, err)
		assert.Len(t, sources, 3, "expected gin, scanner, metadata")
		assert.Equal(t, "gin", sources[0].Source)
		assert.Equal(t, 2, sources[0].Count)
	})

	t.Run("filtered_by_tier_debug", func(t *testing.T) {
		sources, err := s.GetDistinctSources(context.Background(), ActivityFilter{Tier: "debug"})
		require.NoError(t, err)
		assert.Len(t, sources, 1)
		assert.Equal(t, "gin", sources[0].Source)
		assert.Equal(t, 1, sources[0].Count)
	})
}

// ── parity: CompactByDay — basic ──────────────────────────────────────────────

func TestPebbleActivityStore_CompactByDay_Basic(t *testing.T) {
	s := newTestPebbleActivityStore(t)

	day1 := time.Date(2025, 6, 10, 12, 0, 0, 0, time.UTC)
	day2 := time.Date(2025, 6, 11, 14, 0, 0, 0, time.UTC)
	olderThan := time.Date(2025, 6, 12, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		_, err := s.Record(ActivityEntry{
			Tier:      "change",
			Type:      "metadata_applied",
			Level:     "info",
			Source:    "pipeline",
			BookID:    "book-1",
			Summary:   "applied metadata",
			Timestamp: day1.Add(time.Duration(i) * time.Minute),
			Details:   map[string]any{"book_title": "Test Book"},
		})
		require.NoError(t, err)
	}

	for i := 0; i < 2; i++ {
		_, err := s.Record(ActivityEntry{
			Tier:      "change",
			Type:      "tag_written",
			Level:     "info",
			Source:    "writer",
			BookID:    "book-2",
			Summary:   "wrote tags",
			Timestamp: day2.Add(time.Duration(i) * time.Minute),
			Details:   map[string]any{"title": "Other Book"},
		})
		require.NoError(t, err)
	}

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
			if dd.Date == "2025-06-10" {
				day1Digest = dd
			}
		case "audit":
			auditSurvived++
		}
	}
	assert.Equal(t, 2, digestCount, "expected 2 digest rows")
	assert.Equal(t, 0, auditSurvived, "audit entry must be folded")

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

// ── parity: CompactByDay — idempotent ────────────────────────────────────────

func TestPebbleActivityStore_CompactByDay_Idempotent(t *testing.T) {
	s := newTestPebbleActivityStore(t)

	ts := time.Date(2025, 5, 1, 10, 0, 0, 0, time.UTC)
	olderThan := time.Date(2025, 5, 2, 0, 0, 0, 0, time.UTC)

	_, err := s.Record(ActivityEntry{
		Tier:      "change",
		Type:      "config_changed",
		Level:     "info",
		Source:    "settings",
		Summary:   "changed setting",
		Timestamp: ts,
	})
	require.NoError(t, err)

	r1, err := s.CompactByDay(context.Background(), olderThan)
	require.NoError(t, err)
	assert.Equal(t, 1, r1.DaysCompacted)
	assert.Equal(t, 1, r1.EntriesDeleted)

	r2, err := s.CompactByDay(context.Background(), olderThan)
	require.NoError(t, err)
	assert.Equal(t, 0, r2.DaysCompacted)
	assert.Equal(t, 0, r2.EntriesDeleted)
}

// ── parity: CompactByDay — truncates large days ───────────────────────────────

func TestPebbleActivityStore_CompactByDay_Truncates(t *testing.T) {
	s := newTestPebbleActivityStore(t)

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
		})
		require.NoError(t, err)
	}

	result, err := s.CompactByDay(context.Background(), olderThan)
	require.NoError(t, err)
	assert.Equal(t, 1, result.DaysCompacted)
	assert.Equal(t, 600, result.EntriesDeleted)

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

	assert.Len(t, dd.Items, 500, "items capped at 500")
	assert.True(t, dd.Truncated)
	assert.Equal(t, 100, dd.TruncatedCount)
	assert.Equal(t, 600, dd.OriginalCount)
}

// ── parity: CompactByDay — merges into existing digest ───────────────────────

func TestPebbleActivityStore_CompactByDay_MergesExisting(t *testing.T) {
	s := newTestPebbleActivityStore(t)

	day := time.Date(2025, 5, 15, 8, 0, 0, 0, time.UTC)
	cutoff := time.Date(2025, 5, 15, 12, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		_, err := s.Record(ActivityEntry{
			Tier: "change", Type: "metadata_applied", Level: "info",
			Source: "test", Summary: "initial entry", Timestamp: day,
		})
		require.NoError(t, err)
	}

	r1, err := s.CompactByDay(context.Background(), cutoff)
	require.NoError(t, err)
	assert.Equal(t, 1, r1.DaysCompacted)
	assert.Equal(t, 3, r1.EntriesDeleted)

	lateDay := time.Date(2025, 5, 15, 11, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		_, err := s.Record(ActivityEntry{
			Tier: "change", Type: "tag_written", Level: "info",
			Source: "test", Summary: "late entry", Timestamp: lateDay,
		})
		require.NoError(t, err)
	}

	r2, err := s.CompactByDay(context.Background(), cutoff)
	require.NoError(t, err)
	assert.Equal(t, 1, r2.DaysCompacted)
	assert.Equal(t, 5, r2.EntriesDeleted)

	// Exactly one digest row for the day.
	all, total, err := s.Query(context.Background(), ActivityFilter{Tier: "digest", Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 1, total, "must be exactly one digest per day")

	detailsJSON, err := json.Marshal(all[0].Details)
	require.NoError(t, err)
	var dd DigestDetails
	require.NoError(t, json.Unmarshal(detailsJSON, &dd))

	assert.Equal(t, 8, dd.OriginalCount, "merged digest: 3 old + 5 new")
	assert.Equal(t, 3, dd.Counts["metadata_applied"])
	assert.Equal(t, 5, dd.Counts["tag_written"])
}

// ── parity: RecompactDigests ──────────────────────────────────────────────────

func TestPebbleActivityStore_RecompactDigests(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	ctx := context.Background()

	day := time.Date(2025, 3, 10, 0, 0, 0, 0, time.UTC)

	// Insert 3 legacy-style entries (type=system_log, no tags).
	for i := 0; i < 3; i++ {
		_, err := s.Record(ActivityEntry{
			Tier:      "change",
			Type:      "system_log",
			Level:     "info",
			Source:    "compaction",
			Summary:   "applied metadata to book",
			Timestamp: day.Add(time.Duration(i) * time.Minute),
			Tags:      []string{},
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
	assert.Equal(t, 0, res.Skipped)

	// Read back the digest and verify items got proper types and tags.
	dd, _, err := s.findExistingDigest(day.Format("2006-01-02"))
	require.NoError(t, err)
	require.Len(t, dd.Items, 3, "digest should still have 3 items")
	for i, item := range dd.Items {
		assert.NotEqual(t, "system_log", item.Type, "item %d: type should be re-derived", i)
		assert.NotEmpty(t, item.Tags, "item %d: tags should be populated after recompact", i)
	}

	// Idempotency: second run should skip all.
	res2, err := s.RecompactDigests(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, res2.Touched, "second run: should be idempotent")
	assert.Equal(t, 1, res2.Skipped)
}

// ── parity: MigrateSystemActivityLogs — no-op ────────────────────────────────

func TestPebbleActivityStore_MigrateSystemActivityLogs_Noop(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	n, err := s.MigrateSystemActivityLogs()
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

// ── parity: Close — no-op ────────────────────────────────────────────────────

func TestPebbleActivityStore_Close_Noop(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	require.NoError(t, s.Close())
	// Close must be idempotent.
	require.NoError(t, s.Close())
}

// ── dual-write: entry lands in both backends ──────────────────────────────────

// TestDualWriteActivityStore_WritesReplicated verifies that Record writes to both
// backends (NutsDB and Pebble) so a query on either returns the entry.
func TestDualWriteActivityStore_WritesReplicated(t *testing.T) {
	nutsStore := newTestNutsActivityStore(t)
	pebbleStore := newTestPebbleActivityStore(t)

	// Start in read-from-nuts mode.
	dual := NewDualWriteActivityStore(nutsStore, pebbleStore, false)

	entry := ActivityEntry{
		Tier:    "change",
		Type:    "tag_write",
		Level:   "info",
		Source:  "dual-write-test",
		Summary: "dual write test entry",
	}
	id, err := dual.Record(entry)
	require.NoError(t, err)
	assert.Greater(t, id, int64(0))

	// Query from NutsDB (primary when ReadFromPebble=false).
	resNuts, totalNuts, err := dual.Query(context.Background(), ActivityFilter{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, totalNuts)
	assert.Equal(t, "dual-write-test", resNuts[0].Source)

	// Query Pebble directly to confirm replication.
	resPebble, totalPebble, err := pebbleStore.Query(context.Background(), ActivityFilter{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, totalPebble, "entry must also land in Pebble")
	assert.Equal(t, "dual-write-test", resPebble[0].Source)

	// Flip to read-from-pebble and verify reads come from Pebble.
	dual.ReadFromPebble = true
	resAfterFlip, totalAfterFlip, err := dual.Query(context.Background(), ActivityFilter{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, totalAfterFlip)
	assert.Equal(t, "dual-write-test", resAfterFlip[0].Source)
}

// ── backfill: sentinel check ──────────────────────────────────────────────────

func TestIsActivityPebbleBackfillDone_FalseBeforeFlag(t *testing.T) {
	dir := t.TempDir()
	db, err := pebble.Open(filepath.Join(dir, "test.pebble"), &pebble.Options{})
	require.NoError(t, err)
	defer db.Close()

	assert.False(t, IsActivityPebbleBackfillDone(db))
}

func TestIsActivityPebbleBackfillDone_TrueAfterFlag(t *testing.T) {
	dir := t.TempDir()
	db, err := pebble.Open(filepath.Join(dir, "test.pebble"), &pebble.Options{})
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, db.Set([]byte(ActivityPebbleBackfillKey), []byte("2026-01-01"), pebble.Sync))
	assert.True(t, IsActivityPebbleBackfillDone(db))
}

// ── backfill: dry-run counts ──────────────────────────────────────────────────

func TestBackfillNutsActivityToPebble_DryRun(t *testing.T) {
	nutsStore := newTestNutsActivityStore(t)
	pebbleDir := t.TempDir()
	db, err := pebble.Open(filepath.Join(pebbleDir, "test.pebble"), &pebble.Options{})
	require.NoError(t, err)
	defer db.Close()
	pebbleStore := NewPebbleActivityStore(db)

	// Write 5 entries across 3 tiers to NutsDB.
	for _, tier := range []string{"change", "debug", "audit"} {
		for i := 0; i < 1; i++ {
			_, err := nutsStore.Record(ActivityEntry{
				Tier:    tier,
				Type:    "test",
				Level:   "info",
				Source:  "backfill-test",
				Summary: "test " + tier,
			})
			require.NoError(t, err)
		}
	}
	// Also add 2 more to change.
	for i := 0; i < 2; i++ {
		_, err := nutsStore.Record(ActivityEntry{
			Tier: "change", Type: "extra", Level: "info",
			Source: "backfill-test", Summary: "extra",
		})
		require.NoError(t, err)
	}

	res, err := BackfillNutsActivityToPebble(context.Background(), nutsStore, pebbleStore, true)
	require.NoError(t, err)
	assert.True(t, res.DryRun)
	assert.False(t, res.AlreadyDone)
	assert.Equal(t, 5, res.EntriesCopied, "dry-run should count 5 entries")

	// Confirm Pebble is still empty (dry-run).
	_, total, err := pebbleStore.Query(context.Background(), ActivityFilter{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 0, total, "dry-run must not write to Pebble")

	// Confirm sentinel NOT written.
	assert.False(t, IsActivityPebbleBackfillDone(db))
}

// ── backfill: apply — copies all entries and writes sentinel ─────────────────

func TestBackfillNutsActivityToPebble_Apply(t *testing.T) {
	nutsStore := newTestNutsActivityStore(t)
	pebbleDir := t.TempDir()
	db, err := pebble.Open(filepath.Join(pebbleDir, "test.pebble"), &pebble.Options{})
	require.NoError(t, err)
	defer db.Close()
	pebbleStore := NewPebbleActivityStore(db)

	// Write 3 entries to NutsDB.
	for i := 0; i < 3; i++ {
		_, err := nutsStore.Record(ActivityEntry{
			Tier:    "change",
			Type:    "test",
			Level:   "info",
			Source:  "backfill-apply-test",
			Summary: "entry",
			BookID:  "book-99",
		})
		require.NoError(t, err)
	}

	res, err := BackfillNutsActivityToPebble(context.Background(), nutsStore, pebbleStore, false)
	require.NoError(t, err)
	assert.False(t, res.DryRun)
	assert.False(t, res.AlreadyDone)
	assert.Equal(t, 3, res.EntriesCopied)

	// Sentinel must be set.
	assert.True(t, IsActivityPebbleBackfillDone(db))

	// Pebble must have the entries.
	_, total, err := pebbleStore.Query(context.Background(), ActivityFilter{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 3, total, "Pebble must have all 3 copied entries")
}

// ── backfill: idempotent after sentinel ───────────────────────────────────────

func TestBackfillNutsActivityToPebble_Idempotent(t *testing.T) {
	nutsStore := newTestNutsActivityStore(t)
	pebbleDir := t.TempDir()
	db, err := pebble.Open(filepath.Join(pebbleDir, "test.pebble"), &pebble.Options{})
	require.NoError(t, err)
	defer db.Close()
	pebbleStore := NewPebbleActivityStore(db)

	_, err = nutsStore.Record(ActivityEntry{
		Tier: "change", Type: "test", Level: "info",
		Source: "sentinel-test", Summary: "one entry",
	})
	require.NoError(t, err)

	// First run.
	r1, err := BackfillNutsActivityToPebble(context.Background(), nutsStore, pebbleStore, false)
	require.NoError(t, err)
	assert.Equal(t, 1, r1.EntriesCopied)
	assert.True(t, IsActivityPebbleBackfillDone(db))

	// Second run — should return AlreadyDone=true with 0 copies.
	r2, err := BackfillNutsActivityToPebble(context.Background(), nutsStore, pebbleStore, false)
	require.NoError(t, err)
	assert.True(t, r2.AlreadyDone, "second run must short-circuit on sentinel")
	assert.Equal(t, 0, r2.EntriesCopied)
}
