// file: internal/database/sql_activity_compact_liveness_test.go
// version: 1.0.0
// guid: 4d1f8b62-3a7e-4c95-b0d8-6e2a9f13c7b4
// last-edited: 2026-09-13

// Liveness tests for SQLActivityStore.CompactByDay.
//
// On 2026-09-13 maintenance.compact-activity-log was cancelled by the stuck-op
// watchdog (ProgressTimeout 20m) with "sql_activity: compact range: context
// canceled": the SQLite tier went silent between the Pebble tier finishing and
// its own first chunk. These tests pin every stretch of that path to a bounded
// step that reports: per committed chunk, per sample-scan window, and a gate
// wait that a cancel can interrupt.

package database

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordDayBatch writes n info-level change-tier rows one second apart from
// day, in one batch (recordDayEntries' one-row-per-call is too slow for a
// multi-chunk fixture under -race).
func recordDayBatch(t *testing.T, s *SQLActivityStore, day time.Time, n int) {
	t.Helper()
	entries := make([]ActivityEntry, n)
	for i := range entries {
		entries[i] = ActivityEntry{
			Tier:      "change",
			Type:      "book_file_repoint",
			Level:     "info",
			Source:    "repair",
			BookID:    "book-1",
			Summary:   "repointed file",
			Timestamp: day.Add(time.Duration(i) * time.Second),
		}
	}
	got, err := s.RecordBatch(entries)
	require.NoError(t, err)
	require.Equal(t, n, got)
}

// eventLog collects compaction events; safe for concurrent use.
type eventLog struct {
	mu     sync.Mutex
	events []CompactProgressEvent
}

func (l *eventLog) add(ev CompactProgressEvent) {
	l.mu.Lock()
	l.events = append(l.events, ev)
	l.mu.Unlock()
}

func (l *eventLog) snapshot() []CompactProgressEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]CompactProgressEvent(nil), l.events...)
}

// progressCounts returns the EntriesDeleted of every non-heartbeat event, in
// order, with consecutive duplicates removed (the per-day event repeats the
// last chunk's total).
func progressCounts(evs []CompactProgressEvent) []int {
	var out []int
	for _, ev := range evs {
		if ev.Heartbeat || ev.Done {
			continue
		}
		if len(out) > 0 && out[len(out)-1] == ev.Result.EntriesDeleted {
			continue
		}
		out = append(out, ev.Result.EntriesDeleted)
	}
	return out
}

// THE HEADLINE TEST. A single day of several chunks must report after EVERY
// committed chunk, carrying the day, so the op watchdog hears from a 4.8M-row
// day ~960 times instead of once at the end. The expected sequence is exact:
// dropping the per-chunk report leaves only the per-day [15001].
func TestSQLCompactByDay_ReportsProgressPerChunk(t *testing.T) {
	s := newTestSQLStore(t)
	day := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	total := 3*sqlActDeleteChunk + 1
	recordDayBatch(t, s, day, total)

	var log eventLog
	ctx := WithCompactProgress(context.Background(), log.add)
	res, err := s.CompactByDay(ctx, day.Add(24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, total, res.EntriesDeleted)

	evs := log.snapshot()
	assert.Equal(t,
		[]int{sqlActDeleteChunk, 2 * sqlActDeleteChunk, 3 * sqlActDeleteChunk, total},
		progressCounts(evs), "one progress event per committed chunk")
	for _, ev := range evs {
		assert.Equal(t, "sqlite", ev.Backend)
		assert.True(t, ev.Day.Equal(day), "event day = %s, want %s", ev.Day, day)
	}
}

// The digest sample used to be one unbounded statement run before the first
// chunk, inside its write transaction. It is now a windowed walk that stamps a
// heartbeat per window, and must still find an error that only occurs at the
// very END of the day (the case that forced the old statement to walk it all)
// and rank it ahead of the normal rows.
func TestSQLCompactByDay_SampleScanHeartbeatsPerWindowAndFindsLateErrors(t *testing.T) {
	s := newTestSQLStore(t)
	day := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	normal := 2*sqlActSampleWindow + 10
	recordDayBatch(t, s, day, normal)
	lateErr := day.Add(23 * time.Hour)
	_, err := s.RecordBatch([]ActivityEntry{{
		Tier: "change", Type: "repair_failed", Level: "error", Source: "repair",
		Summary: "late failure", Timestamp: lateErr,
	}})
	require.NoError(t, err)

	var log eventLog
	ctx := WithCompactProgress(context.Background(), log.add)
	_, err = s.CompactByDay(ctx, day.Add(24*time.Hour))
	require.NoError(t, err)

	// Heartbeats come before any chunk commits, one per window walked.
	beats := 0
	for _, ev := range log.snapshot() {
		if !ev.Heartbeat {
			break
		}
		beats++
		assert.Equal(t, 0, ev.Result.EntriesDeleted, "a heartbeat is not progress")
	}
	assert.GreaterOrEqual(t, beats, 3, "the sample walk of %d rows must heartbeat once per %d-row window",
		normal+1, sqlActSampleWindow)

	dd := readOnlyDigest(t, s)
	require.Len(t, dd.Items, maxDigestItems)
	assert.True(t, dd.Items[0].Timestamp.Equal(lateErr),
		"the day's only error must lead the sample (audit → error/warn → normal); first item at %s", dd.Items[0].Timestamp)
	assert.Equal(t, normal+1, dd.OriginalCount)
}

// Cancelling after a chunk committed must leave an EXACT partial digest: it
// counts precisely the rows that are gone, the survivors are all still there,
// and a resume completes the day without double-counting. (A partial digest is
// intended — making the day one transaction is the 29-minute, unkillable
// statement this design replaced; see CompactByDay.)
func TestSQLCompactByDay_CancelMidDayLeavesNoTornDigest(t *testing.T) {
	s := newTestSQLStore(t)
	day := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	total := 2*sqlActDeleteChunk + 1
	recordDayBatch(t, s, day, total)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = WithCompactProgress(ctx, func(ev CompactProgressEvent) {
		if !ev.Heartbeat && ev.Result.EntriesDeleted > 0 {
			cancel() // the kill lands right after the first committed chunk
		}
	})
	res, err := s.CompactByDay(ctx, day.Add(24*time.Hour))
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, sqlActDeleteChunk, res.EntriesDeleted, "the result reports committed work only")

	assert.Equal(t, total-sqlActDeleteChunk, countNonDigestRows(t, s), "no row deleted without being counted")
	assert.Equal(t, 1, countDigestRows(t, s))
	assert.Equal(t, sqlActDeleteChunk, digestOriginalCount(t, s),
		"the partial digest counts exactly the rows that are gone")

	res, err = s.CompactByDay(context.Background(), day.Add(24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, total-sqlActDeleteChunk, res.EntriesDeleted)
	assert.Equal(t, 0, countNonDigestRows(t, s))
	assert.Equal(t, 1, countDigestRows(t, s))
	assert.Equal(t, total, digestOriginalCount(t, s), "every row counted exactly once across the cancel")
}

// A cancel during the sample walk — before any chunk — must write nothing: no
// digest, no deleted row.
func TestSQLCompactByDay_CancelDuringSampleWritesNothing(t *testing.T) {
	s := newTestSQLStore(t)
	day := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	total := 2*sqlActSampleWindow + 1
	recordDayBatch(t, s, day, total)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = WithCompactProgress(ctx, func(ev CompactProgressEvent) {
		if ev.Heartbeat {
			cancel()
		}
	})
	res, err := s.CompactByDay(ctx, day.Add(24*time.Hour))
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, CompactResult{}, res)
	assert.Equal(t, total, countNonDigestRows(t, s))
	assert.Equal(t, 0, countDigestRows(t, s))
}

// CompactByDay used to take the backfill gate with a raw Lock: a compaction
// parked behind a held read side could neither report nor be cancelled, and
// surfaced only when the watchdog's cancel let it through to its first query.
func TestSQLCompactByDay_GateWaitIsCancellable(t *testing.T) {
	s := newTestSQLStore(t)
	day := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	recordDayBatch(t, s, day, 10)

	s.backfillGate.RLock() // a backfill batch that never finishes
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := s.CompactByDay(ctx, day.Add(24*time.Hour))
	elapsed := time.Since(start)
	s.backfillGate.RUnlock()

	require.True(t, errors.Is(err, context.DeadlineExceeded), "err = %v", err)
	assert.Less(t, elapsed, 5*time.Second, "the gate wait must end with the context")
	assert.Equal(t, 10, countNonDigestRows(t, s))
}

// Empty days between two populated ones are skipped by the per-day MIN seek;
// both populated days are still compacted and reported with their own day.
func TestSQLCompactByDay_SkipsEmptyDaysAndLabelsEachDay(t *testing.T) {
	s := newTestSQLStore(t)
	d1 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	recordDayBatch(t, s, d1, 5)
	recordDayBatch(t, s, d2, 7)

	var log eventLog
	res, err := s.CompactByDay(WithCompactProgress(context.Background(), log.add), d2.Add(24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, CompactResult{DaysCompacted: 2, EntriesDeleted: 12}, res)

	days := map[string]bool{}
	for _, ev := range log.snapshot() {
		days[ev.Day.Format("2006-01-02")] = true
	}
	assert.Equal(t, map[string]bool{"2026-09-01": true, "2026-09-07": true}, days,
		"only the two populated days are visited")
	assert.Equal(t, 2, countDigestRows(t, s))
}

// readOnlyDigest decodes the single stored daily digest.
func readOnlyDigest(t *testing.T, s *SQLActivityStore) DigestDetails {
	t.Helper()
	var stored []byte
	require.NoError(t, s.reader.QueryRow(
		`SELECT details FROM activity WHERE tier = 'digest' AND type = 'daily_digest'`).Scan(&stored))
	raw, err := decodeActivityDetails(stored)
	require.NoError(t, err)
	var dd DigestDetails
	require.NoError(t, json.Unmarshal(raw, &dd))
	return dd
}
