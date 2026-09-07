// file: internal/database/sql_activity_store_test.go
// version: 1.2.0
// guid: 9f1c4b70-3d21-4a58-b0e6-1c8a2f5d6e30
// last-edited: 2026-09-07

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestSQLStore(t *testing.T) *SQLActivityStore {
	t.Helper()
	s, err := OpenSQLiteActivityStore(filepath.Join(t.TempDir(), "activity.sqlite"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustRecord(t *testing.T, s *SQLActivityStore, e ActivityEntry) {
	t.Helper()
	if _, err := s.Record(e); err != nil {
		t.Fatalf("record: %v", err)
	}
}

func TestSQLActivity_RecordQueryFilters(t *testing.T) {
	s := newTestSQLStore(t)
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	mustRecord(t, s, ActivityEntry{Timestamp: base, Tier: "info", Type: "scan", Level: "info", Source: "scanner", Summary: "Scanned Library folder", Tags: []string{"action:scan", "legacy"}})
	mustRecord(t, s, ActivityEntry{Timestamp: base.Add(time.Minute), Tier: "change", Type: "metadata_applied", Level: "info", Source: "metafetch", OperationID: "op1", BookID: "bk1", Summary: "Applied metadata", Tags: []string{"action:metadata"}})
	mustRecord(t, s, ActivityEntry{Timestamp: base.Add(2 * time.Minute), Tier: "debug", Type: "err", Level: "error", Source: "scanner", Summary: "scanned but FAILED", Tags: []string{"action:scan"}})

	tests := []struct {
		name string
		f    ActivityFilter
		want int
	}{
		{"all", ActivityFilter{}, 3},
		{"tier", ActivityFilter{Tier: "change"}, 1},
		{"type", ActivityFilter{Type: "scan"}, 1},
		{"level", ActivityFilter{Level: "error"}, 1},
		{"source", ActivityFilter{Source: "scanner"}, 2},
		{"op", ActivityFilter{OperationID: "op1"}, 1},
		{"book", ActivityFilter{BookID: "bk1"}, 1},
		{"search-casesensitive-hit", ActivityFilter{Search: "Scanned"}, 1},
		{"search-casesensitive-miss", ActivityFilter{Search: "SCANNED"}, 0},
		{"search-substr", ActivityFilter{Search: "FAILED"}, 1},
		{"tags-and-hit", ActivityFilter{Tags: []string{"action:scan"}}, 2},
		{"tags-and-both", ActivityFilter{Tags: []string{"action:scan", "legacy"}}, 1},
		{"exclude-source", ActivityFilter{ExcludeSources: []string{"scanner"}}, 1},
		{"exclude-tier", ActivityFilter{ExcludeTiers: []string{"debug", "change"}}, 1},
		{"exclude-tags", ActivityFilter{ExcludeTags: []string{"action:scan"}}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			page, total, err := s.Query(context.Background(), tc.f)
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			if total != tc.want {
				t.Errorf("total = %d, want %d", total, tc.want)
			}
			if len(page) != tc.want {
				t.Errorf("page len = %d, want %d", len(page), tc.want)
			}
		})
	}

	// Newest-first ordering.
	page, _, err := s.Query(context.Background(), ActivityFilter{})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if page[0].Summary != "scanned but FAILED" {
		t.Errorf("expected newest-first; got %q first", page[0].Summary)
	}
}

func TestSQLActivity_PaginationTotal(t *testing.T) {
	s := newTestSQLStore(t)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i := range 25 {
		mustRecord(t, s, ActivityEntry{Timestamp: base.Add(time.Duration(i) * time.Minute), Tier: "info", Type: "t", Level: "info", Source: "s", Summary: fmt.Sprintf("row %d", i)})
	}
	page, total, err := s.Query(context.Background(), ActivityFilter{Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if total != 25 {
		t.Errorf("total = %d, want 25", total)
	}
	if len(page) != 10 {
		t.Errorf("page = %d, want 10", len(page))
	}
	// Offset page.
	page2, _, _ := s.Query(context.Background(), ActivityFilter{Limit: 10, Offset: 20})
	if len(page2) != 5 {
		t.Errorf("last page = %d, want 5", len(page2))
	}
}

func TestSQLActivity_Prune(t *testing.T) {
	s := newTestSQLStore(t)
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	mustRecord(t, s, ActivityEntry{Timestamp: old, Tier: "debug", Type: "t", Level: "info", Source: "s", Summary: "old"})
	mustRecord(t, s, ActivityEntry{Timestamp: recent, Tier: "debug", Type: "t", Level: "info", Source: "s", Summary: "recent"})

	n, err := s.Prune(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), "debug")
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned = %d, want 1", n)
	}
	_, total, _ := s.Query(context.Background(), ActivityFilter{})
	if total != 1 {
		t.Errorf("remaining = %d, want 1", total)
	}
}

func TestSQLActivity_CompactByDay_Bounded(t *testing.T) {
	s := newTestSQLStore(t)
	day := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	// More than maxDigestItems entries on one day, across tiers/levels.
	const n = maxDigestItems + 250
	for i := range n {
		tier, level := "info", "info"
		switch {
		case i%50 == 0:
			tier = "audit"
		case i%7 == 0:
			level = "error"
		}
		mustRecord(t, s, ActivityEntry{
			Timestamp: day.Add(time.Duration(i) * time.Second),
			Tier:      tier, Type: "scan_progress", Level: level, Source: "scanner",
			Summary: fmt.Sprintf("entry %d", i),
		})
	}
	// A newer day that must NOT be compacted.
	newer := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	mustRecord(t, s, ActivityEntry{Timestamp: newer, Tier: "info", Type: "t", Level: "info", Source: "s", Summary: "keep me"})

	res, err := s.CompactByDay(context.Background(), time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if res.DaysCompacted != 1 {
		t.Errorf("days = %d, want 1", res.DaysCompacted)
	}
	if res.EntriesDeleted != n {
		t.Errorf("deleted = %d, want %d", res.EntriesDeleted, n)
	}

	// Source rows for the day are gone; exactly one digest row exists; newer row survives.
	page, _, err := s.Query(context.Background(), ActivityFilter{Tier: "digest"})
	if err != nil {
		t.Fatalf("query digest: %v", err)
	}
	if len(page) != 1 {
		t.Fatalf("digest rows = %d, want 1", len(page))
	}
	dig := page[0]
	if dig.Type != "daily_digest" {
		t.Errorf("digest type = %q", dig.Type)
	}
	// Digest details: OriginalCount == n, Truncated with 250 dropped, <=500 items.
	b, _ := json.Marshal(dig.Details)
	var dd DigestDetails
	if err := json.Unmarshal(b, &dd); err != nil {
		t.Fatalf("decode digest details: %v", err)
	}
	if dd.OriginalCount != n {
		t.Errorf("OriginalCount = %d, want %d", dd.OriginalCount, n)
	}
	if len(dd.Items) != maxDigestItems {
		t.Errorf("items = %d, want %d", len(dd.Items), maxDigestItems)
	}
	if !dd.Truncated || dd.TruncatedCount != 250 {
		t.Errorf("truncated=%v count=%d, want true/250", dd.Truncated, dd.TruncatedCount)
	}
	// Audit precedence: first items are audit tier (there are n/50 ≈ 15 audit rows).
	if dd.Items[0].Tier != "audit" {
		t.Errorf("first digest item tier = %q, want audit", dd.Items[0].Tier)
	}

	// The newer day survives as a raw row.
	_, total, _ := s.Query(context.Background(), ActivityFilter{Search: "keep me"})
	if total != 1 {
		t.Errorf("newer row survived count = %d, want 1", total)
	}
}

func TestSQLActivity_WipeCancel(t *testing.T) {
	s := newTestSQLStore(t)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i := range 3 {
		mustRecord(t, s, ActivityEntry{Timestamp: base.Add(time.Duration(i) * time.Minute), Tier: "info", Type: "t", Level: "info", Source: "s", Summary: fmt.Sprintf("r%d", i)})
	}
	// Cancelled context: returns ctx.Err and a count that is a lower bound (0..3).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n, err := s.WipeAllActivity(ctx)
	if err == nil {
		t.Errorf("expected ctx error on cancelled wipe")
	}
	if n < 0 || n > 3 {
		t.Errorf("cancelled wipe count = %d, out of range", n)
	}

	// A full wipe with a live context clears everything.
	n2, err := s.WipeAllActivity(context.Background())
	if err != nil {
		t.Fatalf("wipe: %v", err)
	}
	if n2 < 0 {
		t.Errorf("wipe count negative")
	}
	_, total, _ := s.Query(context.Background(), ActivityFilter{})
	if total != 0 {
		t.Errorf("after wipe total = %d, want 0", total)
	}
}

func TestSQLActivity_BackfillIdempotent(t *testing.T) {
	s := newTestSQLStore(t)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	// Distinct entries (Summary "a" vs "b") ⇒ distinct content keys ⇒ two rows.
	entries := []ActivityEntry{
		{Timestamp: base, Tier: "info", Type: "t", Level: "info", Source: "s", Summary: "a"},
		{Timestamp: base.Add(time.Minute), Tier: "info", Type: "t", Level: "info", Source: "s", Summary: "b"},
	}

	n1, err := s.recordBatch(context.Background(), entries)
	if err != nil {
		t.Fatalf("backfill1: %v", err)
	}
	if n1 != 2 {
		t.Errorf("first backfill inserted %d, want 2", n1)
	}
	// Re-run: same content keys → zero inserted, no duplicates.
	n2, err := s.recordBatch(context.Background(), entries)
	if err != nil {
		t.Fatalf("backfill2: %v", err)
	}
	if n2 != 0 {
		t.Errorf("second backfill inserted %d, want 0 (idempotent)", n2)
	}
	_, total, _ := s.Query(context.Background(), ActivityFilter{})
	if total != 2 {
		t.Errorf("total after re-run = %d, want 2", total)
	}
}

// TestSQLActivity_ContentKeyRoundTrip is the constraint the whole cutover rests
// on: the content key derived from a live-recorded entry must equal the key
// derived from the SAME entry after it round-trips through Pebble's JSON storage
// and back. If it does not, the backfill's copy of a live-dual-written event
// would not dedup and the log would gain duplicates. It also asserts the two
// deliberately-excluded fields (ID, PrunedAt) do not perturb the key.
func TestSQLActivity_ContentKeyRoundTrip(t *testing.T) {
	live := ActivityEntry{
		Timestamp:   time.Date(2026, 9, 1, 12, 0, 0, 123, time.UTC),
		Tier:        "change",
		Type:        "metadata_applied",
		Level:       "info",
		Source:      "metafetch",
		OperationID: "op1",
		BookID:      "bk1",
		Summary:     "Applied metadata",
		Details:     map[string]any{"z": 1.0, "a": "x", "m": true},
		Tags:        []string{"action:metadata", "provider:google"},
	}
	liveKey, err := activitySrcKey(live)
	if err != nil {
		t.Fatalf("live key: %v", err)
	}

	// Simulate Pebble's round-trip: it marshals the entry (with a stamped ID)
	// and later decodes it back for the backfill.
	stamped := live
	stamped.ID = 987654321
	b, err := json.Marshal(stamped)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded ActivityEntry
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// And a lifecycle mutation the backfill might observe on an older row.
	pruned := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	decoded.PrunedAt = &pruned

	backfillKey, err := activitySrcKey(decoded)
	if err != nil {
		t.Fatalf("backfill key: %v", err)
	}
	if liveKey != backfillKey {
		t.Fatalf("content key diverged across round-trip:\n live=%s\n back=%s", liveKey, backfillKey)
	}
}

// TestSQLActivity_LiveDualWriteThenBackfillNoDup is the end-to-end proof: record
// an entry through the migration wrapper (dual-writing to a real Pebble + a real
// SQLite), then run the full backfill over that Pebble. The backfill re-copies
// the row the live path already stored, and the content key must make it a
// no-op — SQLite must hold exactly ONE row, and parity must pass.
func TestSQLActivity_LiveDualWriteThenBackfillNoDup(t *testing.T) {
	pebbleStore := newTestPebbleActivityStore(t)
	sqlStore := newTestSQLStore(t)
	mig := NewMigratingActivityStore(pebbleStore, sqlStore, false)

	// A historical-timestamp entry: exactly the shape (changelog/batcher) that
	// broke the old timestamp-cutoff design — recorded "now" but carrying an old
	// timestamp, so any cutoff would have re-copied it as a duplicate.
	old := time.Date(2026, 1, 15, 8, 30, 0, 0, time.UTC)
	if _, err := mig.Record(ActivityEntry{
		Timestamp: old, Tier: "change", Type: "metadata_changed", Level: "info",
		Source: "changelog", Summary: "historical import", Tags: []string{"action:import"},
	}); err != nil {
		t.Fatalf("live record: %v", err)
	}
	// A zero-timestamp entry: normalized once in the wrapper, so both backends
	// agree on the resolved instant (the second asymmetry the redesign closes).
	if _, err := mig.Record(ActivityEntry{
		Tier: "info", Type: "scan", Source: "scanner", Summary: "live now",
	}); err != nil {
		t.Fatalf("live record zero-ts: %v", err)
	}

	if _, total, _ := sqlStore.Query(context.Background(), ActivityFilter{}); total != 2 {
		t.Fatalf("after dual-write SQLite total = %d, want 2", total)
	}

	res, err := BackfillPebbleActivityToSQL(context.Background(), pebbleStore, sqlStore, false)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if !res.ParityOK {
		t.Fatalf("parity failed: %+v", res)
	}
	// The backfill scanned both Pebble rows but inserted zero — both already
	// present via dual-write under the same content keys.
	if res.EntriesScanned != 2 {
		t.Errorf("scanned = %d, want 2", res.EntriesScanned)
	}
	if res.EntriesCopied != 0 {
		t.Errorf("copied = %d, want 0 (dual-write already stored both)", res.EntriesCopied)
	}
	if _, total, _ := sqlStore.Query(context.Background(), ActivityFilter{}); total != 2 {
		t.Errorf("after backfill SQLite total = %d, want 2 (no duplicates)", total)
	}
}

// TestSQLActivity_BackfillStreamsMultipleBatches exercises the streaming backfill
// across MORE than one sqlBackfillBatch window — the multi-batch flush path the
// two-row dual-write test never reaches. It proves the streamed copy+parity end to
// end: every distinct Pebble row lands in SQLite exactly once, content-identical
// rows dedup to a single SQLite row during the copy, per-tier parity passes, and
// the sentinel is written so a second run short-circuits.
//
// It is the regression guard for the OOM fix: the old backfill materialised the
// whole tier before the first insert, so this many rows exercised none of the
// batching that keeps memory bounded. Here they force at least three stream
// batches through a single Pebble iterator.
func TestSQLActivity_BackfillStreamsMultipleBatches(t *testing.T) {
	pebbleStore := newTestPebbleActivityStore(t)
	sqlStore := newTestSQLStore(t)

	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	// More than 2×sqlBackfillBatch so streamTierEntries flushes ≥3 batches.
	const distinct = sqlBackfillBatch*2 + 37
	entries := make([]ActivityEntry, 0, distinct+2)
	for i := range distinct {
		entries = append(entries, ActivityEntry{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Tier:      "change", Type: "metadata_changed", Level: "info",
			Source: "test", Summary: fmt.Sprintf("entry-%04d", i),
		})
	}
	// Two content-duplicates of the first entry (identical fields ⇒ identical
	// src_key), appended as extra Pebble rows. During the streamed copy they must
	// collapse into the single SQLite row for that content — proving dedup holds
	// through the batched copy, not just within one recordBatch call.
	entries = append(entries, entries[0], entries[0])

	if n, err := pebbleStore.RecordBatch(entries); err != nil || n != len(entries) {
		t.Fatalf("seed pebble: wrote %d/%d err=%v", n, len(entries), err)
	}

	res, err := BackfillPebbleActivityToSQL(context.Background(), pebbleStore, sqlStore, false)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if !res.ParityOK {
		t.Fatalf("parity failed: %+v", res)
	}
	// Every Pebble row was scanned: the distinct set plus the 2 duplicates.
	if want := len(entries); res.EntriesScanned != want {
		t.Errorf("scanned = %d, want %d", res.EntriesScanned, want)
	}
	// Distinct content keys inserted once; the 2 duplicates deduped away.
	if res.EntriesCopied != distinct {
		t.Errorf("copied = %d, want %d (duplicates dedup)", res.EntriesCopied, distinct)
	}
	if _, total, _ := sqlStore.Query(context.Background(), ActivityFilter{}); total != distinct {
		t.Errorf("SQLite total = %d, want %d", total, distinct)
	}

	// Second run: the sentinel is present, so the backfill short-circuits without
	// re-scanning — AlreadyDone and ParityOK, zero further copies.
	res2, err := BackfillPebbleActivityToSQL(context.Background(), pebbleStore, sqlStore, false)
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if !res2.AlreadyDone || !res2.ParityOK {
		t.Errorf("second run = %+v, want AlreadyDone && ParityOK", res2)
	}
	if _, total, _ := sqlStore.Query(context.Background(), ActivityFilter{}); total != distinct {
		t.Errorf("after second run SQLite total = %d, want %d (unchanged)", total, distinct)
	}
}

// TestSQLActivity_LargeDetailsStoredCompressed proves the details codec is wired
// end to end through the real store: a large repetitive details payload is stored
// as a compressed BLOB (much smaller than the raw JSON) yet reads back byte-for-
// byte. This is the regression guard for the disk blow-up that raw JSON caused.
func TestSQLActivity_LargeDetailsStoredCompressed(t *testing.T) {
	s := newTestSQLStore(t)
	ctx := context.Background()

	bigVal := strings.Repeat("ITLSafetyCompare mismatch; dropped_updates=11; ", 5000)
	e := ActivityEntry{
		Timestamp: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		Tier:      "change", Type: "itl_apply", Level: "warn", Source: "itunes",
		Summary: "ITL apply", Details: map[string]any{"err": bigVal, "attempts": "120"},
	}
	rawJSON, _ := json.Marshal(e.Details)

	if _, err := s.recordBatch(ctx, []ActivityEntry{e}); err != nil {
		t.Fatalf("record: %v", err)
	}

	// Stored column must be a compressed BLOB far smaller than the raw JSON.
	var stored []byte
	if err := s.reader.QueryRowContext(ctx,
		`SELECT details FROM activity WHERE type = 'itl_apply' LIMIT 1`).Scan(&stored); err != nil {
		t.Fatalf("read stored details: %v", err)
	}
	if len(stored) == 0 || stored[0] != detailsFmtZstd {
		t.Fatalf("stored details not zstd-tagged: len=%d tag=%#v", len(stored), stored)
	}
	if len(stored) >= len(rawJSON)/4 {
		t.Errorf("stored %d bytes not << raw %d (expected heavy compression)", len(stored), len(rawJSON))
	}

	// And it reads back through the normal query path, fully intact.
	entries, _, err := s.Query(ctx, ActivityFilter{Type: "itl_apply"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Details["err"] != bigVal {
		t.Errorf("details round-trip mismatch: got %d-byte err, want %d",
			len(entries[0].Details["err"].(string)), len(bigVal))
	}
}

// TestSQLActivity_BackfillDryRunCountsWithoutWriting proves dryRun streams and
// counts the whole history but writes nothing: no SQLite rows, no sentinel, so a
// later real run still does the work.
func TestSQLActivity_BackfillDryRunCountsWithoutWriting(t *testing.T) {
	pebbleStore := newTestPebbleActivityStore(t)
	sqlStore := newTestSQLStore(t)

	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	const n = sqlBackfillBatch + 11
	entries := make([]ActivityEntry, 0, n)
	for i := range n {
		entries = append(entries, ActivityEntry{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Tier:      "change", Type: "t", Level: "info",
			Source: "test", Summary: fmt.Sprintf("dry-%04d", i),
		})
	}
	if wrote, err := pebbleStore.RecordBatch(entries); err != nil || wrote != n {
		t.Fatalf("seed pebble: wrote %d/%d err=%v", wrote, n, err)
	}

	res, err := BackfillPebbleActivityToSQL(context.Background(), pebbleStore, sqlStore, true)
	if err != nil {
		t.Fatalf("dry-run backfill: %v", err)
	}
	if !res.DryRun || res.EntriesScanned != n {
		t.Errorf("dry-run = %+v, want DryRun && scanned %d", res, n)
	}
	if res.EntriesCopied != 0 {
		t.Errorf("dry-run copied = %d, want 0 (no writes)", res.EntriesCopied)
	}
	if _, total, _ := sqlStore.Query(context.Background(), ActivityFilter{}); total != 0 {
		t.Errorf("SQLite total after dry-run = %d, want 0", total)
	}
	if pebbleStore.SQLBackfillDone() {
		t.Errorf("dry-run wrote the sentinel; it must not")
	}
}
