// file: internal/database/activity_compaction_bounded_test.go
// version: 1.2.4
// guid: 5b7e0a34-16cf-4d29-8e71-c30a9d4f2b16
// last-edited: 2026-10-19

package database

import (
	"context"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dayEntries builds n change-tier entries one second apart from day.
func dayEntries(day time.Time, n int) []ActivityEntry {
	entries := make([]ActivityEntry, n)
	for i := range entries {
		entries[i] = ActivityEntry{
			Tier:      "change",
			Type:      "metadata_applied",
			Level:     "info",
			Source:    "pipeline",
			BookID:    "book-1",
			Summary:   "applied metadata",
			Timestamp: day.Add(time.Duration(i) * time.Second),
		}
	}
	return entries
}

// recordDayEntries writes n change-tier entries spread across one UTC day, one
// Record call per entry.
func recordDayEntries(t *testing.T, rec func(ActivityEntry) (int64, error), day time.Time, n int) {
	t.Helper()
	for _, e := range dayEntries(day, n) {
		_, err := rec(e)
		require.NoError(t, err)
	}
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

// TestPebbleCompactByDay_MergesOnRetry: compacting a
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

// TestPebbleCompactByDay_InterruptedDeleteDoesNotDoubleCount pins the
// chunk-atomic rewrite. CompactByDay used to write the day's digest in one
// commit and delete the day's entries in later ones, so a process killed
// between them left survivors that the next run recounted and merged into a
// digest that already included them. Pebble is the only activity backend, so
// this is the path every manual "compact everything" takes.
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
