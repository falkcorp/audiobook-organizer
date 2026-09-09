// file: internal/database/activity_compaction_bounded_test.go
// version: 1.2.0
// guid: 5b7e0a34-16cf-4d29-8e71-c30a9d4f2b16
// last-edited: 2026-09-09

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordDayEntries writes n change-tier entries spread across one UTC day.
func recordDayEntries(t *testing.T, rec func(ActivityEntry) (int64, error), day time.Time, n int) {
	t.Helper()
	for i := range n {
		_, err := rec(ActivityEntry{
			Tier:      "change",
			Type:      "metadata_applied",
			Level:     "info",
			Source:    "pipeline",
			BookID:    "book-1",
			Summary:   "applied metadata",
			Timestamp: day.Add(time.Duration(i) * time.Second),
		})
		require.NoError(t, err)
	}
}

// countRows returns how many non-digest rows remain in the SQL store.
func countNonDigestRows(t *testing.T, s *SQLActivityStore) int {
	t.Helper()
	var n int
	require.NoError(t, s.reader.QueryRow(`SELECT COUNT(*) FROM activity WHERE tier <> 'digest'`).Scan(&n))
	return n
}

func countDigestRows(t *testing.T, s *SQLActivityStore) int {
	t.Helper()
	var n int
	require.NoError(t, s.reader.QueryRow(`SELECT COUNT(*) FROM activity WHERE tier = 'digest'`).Scan(&n))
	return n
}

// TestSQLCompactByDay_DeletesMoreThanOneChunk is the regression test for the one
// unchunked statement in sql_activity_store.go. commitDayDigest used to delete a
// whole calendar day inside the digest transaction; on production that day held
// 4,786,930 rows. The delete is now chunked at sqlActDeleteChunk, so this asserts
// the loop actually iterates: a day with strictly more than one chunk of rows
// must be fully removed, not just the first chunk.
//
// The count is deliberately sqlActDeleteChunk+1 rather than a round number: an
// off-by-one that stopped after the first chunk (n < chunk breaks the loop) would
// leave exactly 1 row behind and pass any assertion looser than "zero remain".
func TestSQLCompactByDay_DeletesMoreThanOneChunk(t *testing.T) {
	s := newTestSQLStore(t)
	day := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)
	total := sqlActDeleteChunk + 1
	recordDayEntries(t, s.Record, day, total)

	require.Equal(t, total, countNonDigestRows(t, s))

	res, err := s.CompactByDay(context.Background(), day.Add(24*time.Hour))
	require.NoError(t, err)

	assert.Equal(t, 1, res.DaysCompacted)
	assert.Equal(t, total, res.EntriesDeleted, "every source row in the day must be reported deleted")
	assert.Equal(t, 0, countNonDigestRows(t, s), "no source row may survive a completed compaction")
	assert.Equal(t, 1, countDigestRows(t, s))
}

// TestSQLCompactByDay_InterruptedDeleteDoesNotDoubleCount reproduces the
// production failure mode — the process is killed part-way through compacting a
// day — and pins the invariant that survives it: the digest's OriginalCount must
// equal the number of rows the day ACTUALLY held, no matter how many times the
// job was interrupted and resumed.
//
// This test replaces one that could not fail. The earlier version simulated an
// interruption by compacting a day to completion and then RE-RECORDING fresh
// rows for it, so it exercised the late-arrival merge path and never an
// interrupted delete. Under that fixture 40 + 15 = 55 is the correct answer, so
// the assertion passed against code that was wrong: compaction used to build the
// whole day's digest, commit it, and delete afterwards, and on resume it
// recounted the survivors and ADDED them to a digest that already included them.
// A day of 5,001 rows interrupted after 5,000 would report 10,001.
//
// The interruption here is real and deterministic: compactDayChunk is called
// once, which is exactly one committed unit of work, and the process "dies"
// simply by the test not calling it again. Because that unit folds the counts
// and deletes the rows it counted in a single transaction, the state left behind
// is the true post-kill state — 1 row remaining, a digest describing the 5,000
// that are gone.
func TestSQLCompactByDay_InterruptedDeleteDoesNotDoubleCount(t *testing.T) {
	s := newTestSQLStore(t)
	day := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)
	total := sqlActDeleteChunk + 1
	recordDayEntries(t, s.Record, day, total)

	lo := day.UnixNano()
	hi := day.Add(24 * time.Hour).UnixNano()

	// The kill: one chunk commits, the rest never runs.
	n, err := s.compactDayChunk(context.Background(), day, lo, hi)
	require.NoError(t, err)
	require.Equal(t, sqlActDeleteChunk, n)
	require.Equal(t, 1, countNonDigestRows(t, s), "one row must survive the interrupted run")
	require.Equal(t, 1, countDigestRows(t, s))
	assert.Equal(t, sqlActDeleteChunk, digestOriginalCount(t, s),
		"a partial digest must count only the rows it actually consumed")

	// The resume.
	res, err := s.CompactByDay(context.Background(), day.Add(24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 1, res.EntriesDeleted, "the resume must report only the rows IT deleted")
	assert.Equal(t, 0, countNonDigestRows(t, s))
	assert.Equal(t, 1, countDigestRows(t, s), "the resume must MERGE, not add a second digest")

	assert.Equal(t, total, digestOriginalCount(t, s),
		"the digest must account for every row the day held, exactly once")
	var summary string
	require.NoError(t, s.reader.QueryRow(
		`SELECT summary FROM activity WHERE tier = 'digest'`).Scan(&summary))
	assert.Contains(t, summary, fmt.Sprintf("%d entries", total))
}

// TestSQLCompactByDay_LateArrivalsStillMerge covers the other reason a day can be
// compacted twice: rows written for a day that was already digested. Those ARE
// new rows, so they must be ADDED to the existing digest rather than replacing
// it — the opposite requirement to the interrupted-resume case above, and the
// reason both are pinned separately.
func TestSQLCompactByDay_LateArrivalsStillMerge(t *testing.T) {
	s := newTestSQLStore(t)
	day := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)
	recordDayEntries(t, s.Record, day, 40)

	_, err := s.CompactByDay(context.Background(), day.Add(24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, 40, digestOriginalCount(t, s))

	recordDayEntries(t, s.Record, day, 15)
	_, err = s.CompactByDay(context.Background(), day.Add(24*time.Hour))
	require.NoError(t, err)

	assert.Equal(t, 1, countDigestRows(t, s))
	assert.Equal(t, 55, digestOriginalCount(t, s),
		"rows written after a day was digested must be added to it")
}

// digestOriginalCount reads OriginalCount out of the single stored daily digest.
func digestOriginalCount(t *testing.T, s *SQLActivityStore) int {
	t.Helper()
	var stored []byte
	require.NoError(t, s.reader.QueryRow(
		`SELECT details FROM activity WHERE tier = 'digest' AND type = 'daily_digest'`).Scan(&stored))
	raw, err := decodeActivityDetails(stored)
	require.NoError(t, err)
	var dd DigestDetails
	require.NoError(t, json.Unmarshal(raw, &dd))
	return dd.OriginalCount
}

// TestSQLOptimizeStatistics_BootstrapsThenIncremental covers the ANALYZE
// scheduling requirement.
//
// The first call must BOOTSTRAP: production's activity database had no
// sqlite_stat1 table at all on 2026-09-09, and PRAGMA optimize decides what to
// re-analyze by comparing against stored statistics, so with none stored it
// cannot be relied on to create them. The second call must report the backend as
// supported and leave statistics in place.
func TestSQLOptimizeStatistics_BootstrapsThenIncremental(t *testing.T) {
	s := newTestSQLStore(t)
	day := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)
	recordDayEntries(t, s.Record, day, 200)

	// Precondition: a fresh database has no statistics, which is the production
	// state this method exists for.
	var have int
	require.NoError(t, s.reader.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='sqlite_stat1'`).Scan(&have))
	require.Equal(t, 0, have, "a fresh activity database must start with no sqlite_stat1")

	first, err := s.OptimizeStatistics(context.Background())
	require.NoError(t, err)
	assert.True(t, first.Supported)
	assert.True(t, first.Bootstrapped, "the first run on a database with no statistics must bootstrap")
	assert.Positive(t, first.TablesAnalyzed, "bootstrap must actually produce statistics")

	second, err := s.OptimizeStatistics(context.Background())
	require.NoError(t, err)
	assert.True(t, second.Supported)
	assert.False(t, second.Bootstrapped, "a database that already has statistics must take the incremental path")
	assert.Positive(t, second.TablesAnalyzed, "statistics must survive the incremental run")
}

// TestPebbleOptimizeStatistics_ReportsUnsupported pins that a backend with no
// planner reports Supported=false explicitly rather than a bare zero result. A
// caller that cannot tell "this backend has none" from "the call did nothing"
// will report a silent no-op as success.
func TestPebbleOptimizeStatistics_ReportsUnsupported(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	res, err := s.OptimizeStatistics(context.Background())
	require.NoError(t, err)
	assert.False(t, res.Supported)
	assert.Zero(t, res.TablesAnalyzed)
}

// TestPebbleCompactByDay_IsBoundedNotMaterialized is the regression test for the
// unbounded materialization CompactByDay used to do.
//
// The old implementation called scanTierKVs once per compactable tier and
// concatenated every decoded row into one slice before grouping by day, so peak
// heap was proportional to the number of rows compacted. Boundedness is asserted
// through entriesDecoded, the counter this file already maintains as the
// instrument for "is this path bounded", and in particular it must not grow with
// the number of DAYS because of repeated whole-keyspace scans.
//
// The bound here was 2N until 2026-09-09, because the old two-phase path decoded
// each row twice: once in streamDayEntries to fold it into the digest, and again
// in deleteDayEntries to derive its secondary index keys. The chunk-atomic
// rewrite decodes exactly once — claimDayChunk keeps the decoded entry and the
// same batch that stages the digest also stages the deletes — so a 2N bound now
// passes with 100% headroom and would not notice a regression back to
// double-decoding. Hold it to N plus a small per-day allowance (the digest row
// itself is re-read once per chunk) so the assertion measures something again.
func TestPebbleCompactByDay_IsBoundedNotMaterialized(t *testing.T) {
	s := newTestPebbleActivityStore(t)

	const days = 3
	const perDay = 400
	first := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)
	for d := range days {
		recordDayEntries(t, s.Record, first.AddDate(0, 0, d), perDay)
	}
	cutoff := first.AddDate(0, 0, days)

	before := s.entriesDecoded.Load()
	res, err := s.CompactByDay(context.Background(), cutoff)
	require.NoError(t, err)
	decoded := s.entriesDecoded.Load() - before

	assert.Equal(t, days, res.DaysCompacted)
	assert.Equal(t, days*perDay, res.EntriesDeleted)

	// Exactly one decode per row, plus a handful per day for the digest row the
	// chunk re-reads. Anything materially above that means a pass is re-scanning
	// rows it has already seen — including a regression to decoding once to count
	// and again to delete.
	assert.LessOrEqual(t, decoded, int64(days*perDay+days*10),
		"compaction must decode each row exactly once, not rescan the keyspace per day")
}

// TestPebbleCompactByDay_DigestCountsSurviveTheRewrite checks the rewrite kept
// the digest's meaning: OriginalCount describes every row the day held, even
// though only maxDigestItems of them are sampled into Items.
func TestPebbleCompactByDay_DigestCountsSurviveTheRewrite(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	day := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)
	total := maxDigestItems + 25
	recordDayEntries(t, s.Record, day, total)

	res, err := s.CompactByDay(context.Background(), day.Add(24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, res.DaysCompacted)
	require.Equal(t, total, res.EntriesDeleted)

	dd, key, err := s.findExistingDigest(day.Format("2006-01-02"))
	require.NoError(t, err)
	require.NotNil(t, key)
	assert.Equal(t, total, dd.OriginalCount, "the digest must count every row, not just the sampled ones")
	assert.Len(t, dd.Items, maxDigestItems, "items stay capped so memory cannot grow with the day")
	assert.True(t, dd.Truncated)
	assert.Equal(t, total-maxDigestItems, dd.TruncatedCount)
}

// TestPebbleCompactByDay_MergesOnRetry mirrors the SQL resume test: compacting a
// date twice must merge into one digest whose OriginalCount is the sum, never
// produce a second digest for the same date.
func TestPebbleCompactByDay_MergesOnRetry(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	day := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)

	recordDayEntries(t, s.Record, day, 10)
	_, err := s.CompactByDay(context.Background(), day.Add(24*time.Hour))
	require.NoError(t, err)

	recordDayEntries(t, s.Record, day, 7)
	_, err = s.CompactByDay(context.Background(), day.Add(24*time.Hour))
	require.NoError(t, err)

	dd, key, err := s.findExistingDigest(day.Format("2006-01-02"))
	require.NoError(t, err)
	require.NotNil(t, key)
	assert.Equal(t, 17, dd.OriginalCount, "a second compaction of the same date must merge, not replace or duplicate")
}

// TestPebbleCompactByDay_InterruptedDeleteDoesNotDoubleCount is the Pebble twin
// of TestSQLCompactByDay_InterruptedDeleteDoesNotDoubleCount, and it exists
// because the Pebble backend had the identical defect for the identical reason:
// CompactByDay wrote the day's digest in one commit and deleted the day's
// entries in later ones, so a process killed between them left survivors that
// the next run recounted and merged into a digest that already included them.
//
// This matters more here than on the SQL side, not less: production is still
// reading and compacting through the Pebble backend (read_secondary=false), so
// this is the path a manual "compact everything" actually takes today.
func TestPebbleCompactByDay_InterruptedDeleteDoesNotDoubleCount(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	day := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)
	total := pactCompactDeleteBatch + 1
	recordDayEntries(t, s.Record, day, total)

	dateKey := day.Format("2006-01-02")
	dayEnd := day.Add(24 * time.Hour)

	// The kill: exactly one chunk commits.
	n, err := s.compactDayChunk(context.Background(), day, dayEnd, dateKey)
	require.NoError(t, err)
	require.Equal(t, pactCompactDeleteBatch, n)

	dd, key, err := s.findExistingDigest(dateKey)
	require.NoError(t, err)
	require.NotNil(t, key)
	assert.Equal(t, pactCompactDeleteBatch, dd.OriginalCount,
		"a partial digest must count only the entries it actually consumed")

	// The resume.
	res, err := s.CompactByDay(context.Background(), dayEnd)
	require.NoError(t, err)
	assert.Equal(t, 1, res.EntriesDeleted, "the resume must report only the entries IT deleted")

	dd, key, err = s.findExistingDigest(dateKey)
	require.NoError(t, err)
	require.NotNil(t, key)
	assert.Equal(t, total, dd.OriginalCount,
		"the digest must account for every entry the day held, exactly once")
	assert.Equal(t, total-len(dd.Items), dd.TruncatedCount,
		"TruncatedCount must be derived from the totals, not accumulated per chunk")
}

// TestPebbleCompactByDay_CountsUndecodableEntries pins that an entry whose
// stored JSON cannot be read is still counted before it is deleted.
//
// Compaction DELETES these rows, so if they were skipped for counting the day's
// OriginalCount would quietly shrink and a codec bug would present as a quiet
// day rather than as damage. The previous streaming implementation did skip
// them: it recorded the failure in a decode tally and never counted the row.
func TestPebbleCompactByDay_CountsUndecodableEntries(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	day := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)
	recordDayEntries(t, s.Record, day, 5)

	// Plant a row whose value is not valid entry JSON, in a compactable tier.
	badKey := pactPrimaryKey("change", day.Add(time.Hour), "0000000000000000000000BAD0")
	require.NoError(t, s.db.Set(badKey, []byte("{not json"), pebble.Sync))

	res, err := s.CompactByDay(context.Background(), day.Add(24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 6, res.EntriesDeleted, "the unreadable row must be deleted, not left to be re-claimed forever")

	dd, key, err := s.findExistingDigest(day.Format("2006-01-02"))
	require.NoError(t, err)
	require.NotNil(t, key)
	assert.Equal(t, 6, dd.OriginalCount, "the unreadable row must still be counted")
	assert.Equal(t, 1, dd.Counts["unreadable"])
}
