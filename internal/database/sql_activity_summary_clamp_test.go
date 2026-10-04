// file: internal/database/sql_activity_summary_clamp_test.go
// version: 1.2.1
// guid: 8b47e0c9-2f13-45da-9e60-c4a1d5382bf7
// last-edited: 2026-10-04

package database

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
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

// VacuumActivity must leave the -wal file truncated, not merely checkpointed.
//
// This is the half that a "did the main file shrink?" assertion misses, and it
// is the half that decides whether the user gets their disk space back. In WAL
// mode VACUUM writes the rebuilt database THROUGH the WAL, and SQLite's
// automatic checkpoint is PASSIVE — it recycles the WAL in place at its
// high-water mark and never shrinks the file. Hit for real on prod 2026-09-08:
// the main file fell 22,898,438,144 → 11,447,480,320 while the WAL held at
// 11,514,065,152 and stayed there, so the net reclaim was about zero.
func TestVacuumActivity_TruncatesTheWAL(t *testing.T) {
	s := newTestSQLStore(t)

	// Build a WAL worth truncating: write, then clamp, which rewrites every one
	// of these rows and pushes their freed overflow pages into the WAL.
	for range 40 {
		insertLegacyOversizedSummary(t, s, strings.Repeat("v", activitySummaryMax*8))
	}
	if _, err := s.ClampOversizedSummaries(context.Background(), ClampSummariesOptions{}); err != nil {
		t.Fatalf("clamp: %v", err)
	}

	walPath := s.path + "-wal"
	before, err := os.Stat(walPath)
	if err != nil {
		t.Skipf("no -wal file at %s (%v); nothing to assert", walPath, err)
	}
	if before.Size() == 0 {
		t.Skip("WAL already empty before vacuum; fixture did not build one")
	}

	if _, err := s.VacuumActivity(context.Background()); err != nil {
		t.Fatalf("vacuum: %v", err)
	}

	after, err := os.Stat(walPath)
	if err != nil {
		return // truncate-to-removal is an acceptable outcome
	}
	if after.Size() >= before.Size() {
		t.Errorf("WAL did not shrink: %d bytes before vacuum, %d after. "+
			"VACUUM in WAL mode must be followed by PRAGMA wal_checkpoint(TRUNCATE); "+
			"the automatic checkpoint is PASSIVE and never shrinks the file, so the "+
			"space VACUUM freed stays held by the -wal", before.Size(), after.Size())
	}
}

// TestVacuumActivity_TruncatesTheWALWhileTheCheckpointerRuns is the regression
// test for VacuumActivity treating a busy TRUNCATE as success.
//
// The background checkpointer runs PASSIVE/TRUNCATE on its own connection. A
// TRUNCATE issued while that one holds the checkpoint lock returns a normal
// result row with busy=1 and does nothing, which is NOT an SQL error. The old
// code ran the pragma through ExecContext and threw that row away, so the vacuum
// reported success with the -wal still full: the 2026-09-08 incident the
// truncate exists to prevent. A 1 ms checkpointer interval makes the collision
// near-certain, so this test failed on every run before the fix.
//
// It passes now mainly because the vacuum's checkpoints run on the same single
// connection (s.ckpt) as the background loop's, so the two can no longer run at
// the same moment; the busy retry is the second line of defence, exercised by
// TestVacuumActivity_BusyTruncateSucceedsOnALaterAttempt.
func TestVacuumActivity_TruncatesTheWALWhileTheCheckpointerRuns(t *testing.T) {
	s, _ := openCkptTestStore(t, time.Millisecond)

	for range 40 {
		insertLegacyOversizedSummary(t, s, strings.Repeat("v", activitySummaryMax*8))
	}
	if _, err := s.ClampOversizedSummaries(context.Background(), ClampSummariesOptions{}); err != nil {
		t.Fatalf("clamp: %v", err)
	}

	for i := range 5 {
		if _, err := s.VacuumActivity(context.Background()); err != nil {
			t.Fatalf("vacuum %d: %v", i, err)
		}
		// Success must mean the WAL really is empty. Writes are quiescent, so
		// a 0-byte (or removed) -wal is the only state a completed TRUNCATE
		// can leave.
		if fi, err := os.Stat(s.path + "-wal"); err == nil && fi.Size() != 0 {
			t.Fatalf("vacuum %d reported success but the -wal still holds %d bytes", i, fi.Size())
		}
	}
}

// TestVacuumActivity_ReportsSpaceStillHeldWhenTruncateStaysBusy: a reader that
// keeps a WAL snapshot open blocks TRUNCATE for as long as it lives. Retrying
// cannot fix that, so after the bounded retries VacuumActivity must return the
// "space still held" error rather than claim the WAL was emptied.
func TestVacuumActivity_ReportsSpaceStillHeldWhenTruncateStaysBusy(t *testing.T) {
	s := newTestSQLStore(t)
	oldAttempts, oldBackoff := vacuumTruncateAttempts, vacuumTruncateMaxBackoff
	vacuumTruncateAttempts, vacuumTruncateMaxBackoff = 2, 10*time.Millisecond
	t.Cleanup(func() { vacuumTruncateAttempts, vacuumTruncateMaxBackoff = oldAttempts, oldBackoff })

	for range 10 {
		insertLegacyOversizedSummary(t, s, strings.Repeat("v", activitySummaryMax*8))
	}

	ctx := context.Background()
	conn, err := s.reader.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM activity").Scan(&n); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.ExecContext(ctx, "ROLLBACK") }()

	var busySeen atomic.Int32 // the hook also fires from the checkpointer goroutine
	hook := ckptHookFn(func(mode string, res walCheckpointResult, _ error) {
		if mode == "TRUNCATE" && res.Busy != 0 {
			busySeen.Add(1)
		}
	})
	s.ckptr.hook.Store(&hook)
	t.Cleanup(func() { s.ckptr.hook.Store(nil) })

	_, err = s.VacuumActivity(ctx)
	if err == nil || !strings.Contains(err.Error(), "space still held") {
		t.Fatalf("VacuumActivity err = %v; want the space-still-held error while a reader pins the WAL", err)
	}
	if got := int(busySeen.Load()); got < vacuumTruncateAttempts {
		t.Errorf("busy TRUNCATE attempts = %d, want at least %d (every attempt retried, none skipped)", got, vacuumTruncateAttempts)
	}
}

// holdReaderSnapshot opens a read transaction on its own connection, which pins
// the current WAL snapshot so a TRUNCATE checkpoint reports busy until release
// is called. release is idempotent.
func holdReaderSnapshot(t *testing.T, s *SQLActivityStore) (release func()) {
	t.Helper()
	ctx := context.Background()
	conn, err := s.reader.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM activity").Scan(&n); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
			_ = conn.Close()
		})
	}
	t.Cleanup(release)
	return release
}

// TestVacuumActivity_BusyTruncateSucceedsOnALaterAttempt: a TRUNCATE that is
// busy on its first attempt must be retried, and a later attempt that gets
// through must count as success with the WAL emptied.
func TestVacuumActivity_BusyTruncateSucceedsOnALaterAttempt(t *testing.T) {
	s := newTestSQLStore(t)
	for range 10 {
		insertLegacyOversizedSummary(t, s, strings.Repeat("v", activitySummaryMax*8))
	}
	release := holdReaderSnapshot(t, s)

	var busy, truncates atomic.Int32
	hook := ckptHookFn(func(mode string, res walCheckpointResult, _ error) {
		if mode != "TRUNCATE" {
			return
		}
		truncates.Add(1)
		if res.Busy != 0 && busy.Add(1) == 1 {
			release() // whatever held the WAL lets go before the retry
		}
	})
	s.ckptr.hook.Store(&hook)
	t.Cleanup(func() { s.ckptr.hook.Store(nil) })

	if _, err := s.VacuumActivity(context.Background()); err != nil {
		t.Fatalf("vacuum: %v (a busy first attempt must be retried, not reported)", err)
	}
	if got := busy.Load(); got != 1 {
		t.Fatalf("busy TRUNCATE attempts = %d, want exactly 1 (the fixture must make the first attempt busy)", got)
	}
	if got := truncates.Load(); got < 2 {
		t.Fatalf("TRUNCATE attempts = %d, want at least 2 (busy, then the retry that succeeded)", got)
	}
	if fi, err := os.Stat(s.path + "-wal"); err == nil && fi.Size() != 0 {
		t.Fatalf("vacuum reported success but the -wal still holds %d bytes", fi.Size())
	}
}

// TestVacuumActivity_IdleTickReclaimsWhatAGivenUpTruncateLeft: when the vacuum
// gives up on a busy TRUNCATE and reports the space still held, the background
// checkpointer's idle tick must reset the WAL once the holder lets go, so the
// space is not stranded until a restart.
func TestVacuumActivity_IdleTickReclaimsWhatAGivenUpTruncateLeft(t *testing.T) {
	s, _ := openCkptTestStore(t, 20*time.Millisecond)
	oldAttempts, oldBackoff := vacuumTruncateAttempts, vacuumTruncateMaxBackoff
	vacuumTruncateAttempts, vacuumTruncateMaxBackoff = 1, time.Millisecond
	t.Cleanup(func() { vacuumTruncateAttempts, vacuumTruncateMaxBackoff = oldAttempts, oldBackoff })

	for range 10 {
		insertLegacyOversizedSummary(t, s, strings.Repeat("v", activitySummaryMax*8))
	}
	release := holdReaderSnapshot(t, s)
	if _, err := s.VacuumActivity(context.Background()); err == nil || !strings.Contains(err.Error(), "space still held") {
		t.Fatalf("VacuumActivity err = %v; want space still held while a reader pins the WAL", err)
	}
	walPath := s.path + "-wal"
	if fi, err := os.Stat(walPath); err != nil || fi.Size() == 0 {
		t.Fatalf("fixture: the -wal should still hold the vacuum's frames (stat %v, err %v)", fi, err)
	}

	release()
	deadline := time.Now().Add(10 * time.Second)
	for {
		fi, err := os.Stat(walPath)
		if err != nil || fi.Size() == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the background checkpointer never reset the -wal (%d bytes) after the reader let go", fi.Size())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestVacuumActivity_TruncateDoesNotStallForegroundWrites is the regression
// test for the post-vacuum TRUNCATE holding the WAL write lock for the whole
// frame copy. After a VACUUM the WAL holds the entire rebuilt database, and a
// TRUNCATE that copies it blocks every Record for that long; on prod (an 11 GB
// WAL) Records would wait out busy_timeout(10000) and fail with SQLITE_BUSY.
// truncateWALAfterVacuum now copies with PASSIVE first, which does not block
// writers, so the TRUNCATE only resets an already-copied WAL.
//
// It asserts three things while Records run continuously through the truncate:
//   - no Record fails;
//   - the first checkpoint the truncate runs is PASSIVE, and it does the copy;
//   - the slowest Record is well under the time the frame copy took, which is
//     only true if writers were not blocked for the copy.
func TestVacuumActivity_TruncateDoesNotStallForegroundWrites(t *testing.T) {
	// The background checkpointer is held off (1 h interval): its own ticks would
	// show up in the hook and copy frames this test attributes to the vacuum.
	s, _ := openCkptTestStore(t, time.Hour)
	// ~6.5 MB of rows, so the rebuilt database VACUUM writes through the WAL
	// takes measurable time to copy back. One transaction: separate commits
	// took over a minute on a loaded disk.
	tx, err := s.writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("v", activitySummaryMax*8)
	for i := range 100 {
		if _, err := tx.Exec(s.dialect.rebind(sqlActInsert), fmt.Sprintf("stall-fixture-%d", i),
			time.Now().UnixMilli(), "info", "system", "error", "itunes", "", "", big, nil, "[]", nil); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.Exec(`VACUUM`); err != nil {
		t.Fatalf("vacuum: %v", err)
	}
	// Frames the VACUUM left in the WAL: a 32-byte header, then frames of a
	// 24-byte header plus one page each.
	var pageSize int64
	if err := s.reader.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	walInfo, err := os.Stat(s.path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	vacuumFrames := int((walInfo.Size() - 32) / (pageSize + 24))
	if vacuumFrames < 100 {
		t.Fatalf("fixture: VACUUM left only %d WAL frames", vacuumFrames)
	}

	var mu sync.Mutex
	var modes []string
	var results []walCheckpointResult
	var copyTime time.Duration
	hook := ckptHookFn(func(mode string, res walCheckpointResult, _ error) {
		mu.Lock()
		defer mu.Unlock()
		modes = append(modes, mode)
		results = append(results, res)
		copyTime = max(copyTime, res.Elapsed)
	})
	s.ckptr.hook.Store(&hook)
	t.Cleanup(func() { s.ckptr.hook.Store(nil) })

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var recErrs atomic.Int32
	var maxLatency atomic.Int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		base := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			start := time.Now()
			_, err := s.Record(ActivityEntry{Timestamp: base.Add(time.Duration(i) * time.Millisecond),
				Tier: "info", Type: "t", Level: "info", Source: "s", Summary: fmt.Sprintf("during-truncate-%d", i)})
			if err != nil {
				recErrs.Add(1)
				fmt.Fprintf(os.Stderr, "Record during truncate failed: %v\n", err)
			}
			if d := int64(time.Since(start)); d > maxLatency.Load() {
				maxLatency.Store(d)
			}
			time.Sleep(time.Millisecond)
		}
	}()

	err = s.truncateWALAfterVacuum(context.Background())
	close(stop)
	waitGroupOrFatal(t, &wg, "the Record loop")
	if err != nil {
		t.Fatalf("truncateWALAfterVacuum: %v", err)
	}

	if n := recErrs.Load(); n != 0 {
		t.Fatalf("%d Records failed during the post-vacuum truncate; writers must never error", n)
	}
	mu.Lock()
	defer mu.Unlock()
	lat := time.Duration(maxLatency.Load())
	t.Logf("frame copy took %v; slowest Record %v; checkpoints %v %+v; vacuum frames %d",
		copyTime, lat, modes, results, vacuumFrames)
	// The deterministic check: the first checkpoint is PASSIVE and it copied
	// every frame the VACUUM wrote, so the TRUNCATE that holds the write lock
	// had none of them left to copy.
	if len(modes) == 0 || modes[0] != "PASSIVE" {
		t.Fatalf("checkpoint order = %v; the copy must be done by a PASSIVE checkpoint before any TRUNCATE", modes)
	}
	if results[0].Checkpointed < vacuumFrames {
		t.Errorf("the PASSIVE checkpoint copied %d frames, fewer than the %d the VACUUM left: the TRUNCATE "+
			"would copy the rest while holding the write lock", results[0].Checkpointed, vacuumFrames)
	}
	// The timing check, only where it can discriminate. With the write lock
	// held for the copy, the slowest Record matches the copy time (measured on
	// a TRUNCATE-only build: 3.67 s against 3.55 s, 1.26 s against 1.24 s).
	// Without it, Record latency is unrelated to the copy, but is noisy under
	// -race and load (up to ~280 ms seen), so a short copy proves nothing.
	if copyTime >= time.Second && lat >= copyTime/2 {
		t.Errorf("slowest Record took %v against a %v frame copy: writers were blocked for the copy", lat, copyTime)
	}
}
