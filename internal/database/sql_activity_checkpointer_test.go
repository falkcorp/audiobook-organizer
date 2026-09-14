// file: internal/database/sql_activity_checkpointer_test.go
// version: 1.0.0
// guid: 8d3f6a1e-2b7c-4e90-9f15-6a4c0b8e7d23
// last-edited: 2026-09-14

// Tests for the SQLActivityStore's WAL checkpointing (sql_activity_checkpointer.go):
// foreground writes never checkpoint, the background checkpointer does, the
// compaction checkpoints between chunks, and Close cannot run past its budget.

package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openCkptTestStore opens a store with the given background interval. An hour
// means "the background checkpointer never ticks during this test".
func openCkptTestStore(t *testing.T, interval time.Duration) (*SQLActivityStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "activity.sqlite")
	s, err := openSQLiteActivityStore(path, interval)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

// bulkyEntry is an entry large enough (~4 KB) that a few hundred of them fill
// more than SQLite's default 1000-page autocheckpoint threshold.
func bulkyEntry(i int, ts time.Time) ActivityEntry {
	return ActivityEntry{
		Tier:      "change",
		Type:      "metadata_applied",
		Level:     "info",
		Source:    "pipeline",
		Summary:   "bulky",
		Timestamp: ts.Add(time.Duration(i) * time.Millisecond),
		Details:   map[string]any{"blob": strings.Repeat("x", 4096), "i": i},
	}
}

// recordHook installs a checkpoint observer and returns a snapshot function
// listing the modes seen so far.
func recordHook(s *SQLActivityStore) func() []string {
	var mu sync.Mutex
	var modes []string
	fn := ckptHookFn(func(mode string, _ walCheckpointResult, _ error) {
		mu.Lock()
		modes = append(modes, mode)
		mu.Unlock()
	})
	s.ckptr.hook.Store(&fn)
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), modes...)
	}
}

// TestSQLActivityStore_ForegroundWritesNeverCheckpoint pins the 2026-09-14
// outage's root cause: under SQLite's default wal_autocheckpoint=1000 the write
// that crosses the threshold runs the checkpoint inline. With it off, 1,500
// separate ~4 KB commits leave every frame in the WAL for the checkpointer.
func TestSQLActivityStore_ForegroundWritesNeverCheckpoint(t *testing.T) {
	s, _ := openCkptTestStore(t, time.Hour)

	for _, db := range []*sql.DB{s.writer, s.reader, s.ckpt} {
		var auto int
		require.NoError(t, db.QueryRow("PRAGMA wal_autocheckpoint").Scan(&auto))
		assert.Equal(t, 0, auto, "every connection must have wal_autocheckpoint=0")
	}
	var limit int64
	require.NoError(t, s.writer.QueryRow("PRAGMA journal_size_limit").Scan(&limit))
	assert.Equal(t, int64(sqlActJournalSizeLimit), limit)

	base := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)
	for i := range 1500 {
		_, err := s.Record(bulkyEntry(i, base))
		require.NoError(t, err)
	}

	res, err := s.walCheckpoint(context.Background(), "PASSIVE")
	require.NoError(t, err)
	assert.Greater(t, res.Log, 2000,
		"the WAL must still hold every frame the writes produced; a small count means a foreground commit checkpointed and restarted the log")
	assert.True(t, res.complete(), "the explicit PASSIVE checkpoint must copy them: %+v", res)
}

// TestSQLActivityStore_BackgroundCheckpointerRunsAndTruncatesWhenIdle proves
// the goroutine the constructor starts actually checkpoints, and resets the WAL
// file to zero bytes once writes stop.
func TestSQLActivityStore_BackgroundCheckpointerRunsAndTruncatesWhenIdle(t *testing.T) {
	s, path := openCkptTestStore(t, 20*time.Millisecond)
	modes := recordHook(s)

	base := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)
	for i := range 300 {
		_, err := s.Record(bulkyEntry(i, base))
		require.NoError(t, err)
	}

	require.Eventually(t, func() bool {
		st, err := os.Stat(path + "-wal")
		return err == nil && st.Size() == 0 && s.ckptr.runs.Load() >= 2
	}, 10*time.Second, 10*time.Millisecond, "background checkpointer never truncated the WAL")

	seen := modes()
	assert.Contains(t, seen, "PASSIVE")
	assert.Contains(t, seen, "TRUNCATE")
}

// TestSQLCompactByDay_CheckpointsBetweenChunks pins the incremental checkpoint:
// a day spanning three chunks must checkpoint after each chunk, not once when
// the whole day is done. The per-day checkpoint it replaces let a 4.7M-row day
// (~950 chunks) accumulate in the WAL, and a deploy that interrupted it left
// the multi-GB WAL the next boot had to copy.
func TestSQLCompactByDay_CheckpointsBetweenChunks(t *testing.T) {
	s, _ := openCkptTestStore(t, time.Hour)
	day := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)

	total := 2*sqlActDeleteChunk + 1
	entries := make([]ActivityEntry, 0, total)
	for i := range total {
		entries = append(entries, ActivityEntry{
			Tier: "change", Type: "metadata_applied", Level: "info", Source: "pipeline",
			BookID: "book-1", Summary: "applied metadata",
			Timestamp: day.Add(time.Duration(i) * time.Second),
		})
	}
	n, err := s.RecordBatch(entries)
	require.NoError(t, err)
	require.Equal(t, total, n)

	var mu sync.Mutex
	passiveAfterChunk := 0
	fn := ckptHookFn(func(mode string, _ walCheckpointResult, _ error) {
		if mode == "PASSIVE" {
			mu.Lock()
			passiveAfterChunk++
			mu.Unlock()
		}
	})
	s.ckptr.hook.Store(&fn)

	res, err := s.CompactByDay(context.Background(), day.Add(24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, total, res.EntriesDeleted)

	mu.Lock()
	defer mu.Unlock()
	// 3 chunks (5000 + 5000 + 1) each checkpoint, plus the end-of-day pass.
	assert.GreaterOrEqual(t, passiveAfterChunk, 4,
		"compaction must checkpoint after every committed chunk, not only at the end of the day")
}

// TestSQLActivityStore_CloseIsBoundedWhenTheCheckpointCannotFinish pins the
// shutdown half: a final checkpoint that cannot complete (here a reader pins an
// old snapshot, which is what a big WAL looks like to the clock) must not hold
// Close past its budget, and nothing written may be lost.
func TestSQLActivityStore_CloseIsBoundedWhenTheCheckpointCannotFinish(t *testing.T) {
	s, path := openCkptTestStore(t, time.Hour)
	base := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)
	for i := range 50 {
		_, err := s.Record(bulkyEntry(i, base))
		require.NoError(t, err)
	}

	// A reader holding a snapshot from before the next writes: TRUNCATE must
	// wait for it, and cannot finish while it is open.
	other, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)")
	require.NoError(t, err)
	defer other.Close()
	tx, err := other.Begin()
	require.NoError(t, err)
	var before int
	require.NoError(t, tx.QueryRow("SELECT COUNT(*) FROM activity").Scan(&before))
	require.Equal(t, 50, before)

	for i := 50; i < 100; i++ {
		_, err := s.Record(bulkyEntry(i, base))
		require.NoError(t, err)
	}

	start := time.Now()
	require.NoError(t, s.Close())
	elapsed := time.Since(start)
	assert.Less(t, elapsed, sqlActCloseCheckpointBudget+2*time.Second,
		"Close must not wait on a checkpoint that cannot finish (took %s)", elapsed)

	require.NoError(t, tx.Rollback())

	// Every committed row survives: reopen and count.
	s2, err := openSQLiteActivityStore(path, time.Hour)
	require.NoError(t, err)
	defer s2.Close()
	var after int
	require.NoError(t, s2.reader.QueryRow("SELECT COUNT(*) FROM activity").Scan(&after))
	assert.Equal(t, 100, after)
}

// TestSQLActivityStore_CloseIsIdempotent: Close stops a goroutine now, so a
// second call must not block or panic.
func TestSQLActivityStore_CloseIsIdempotent(t *testing.T) {
	s, _ := openCkptTestStore(t, 10*time.Millisecond)
	require.NoError(t, s.Close())
	require.NoError(t, s.Close())
}
