// file: internal/database/sql_activity_summary_clamp_test.go
// version: 1.2.4
// guid: 8b47e0c9-2f13-45da-9e60-c4a1d5382bf7
// last-edited: 2026-10-06

package database

import (
	"context"
	"fmt"
	"os"
	"slices"
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

// ckptCall is one checkpoint the hook observed.
type ckptCall struct {
	mode string
	res  walCheckpointResult
}

// recordCheckpoints installs a hook that records every checkpoint in order and
// returns a snapshot function. Use it on a store whose background checkpointer
// is held off (openCkptTestStore with a long interval) so only the code under
// test shows up.
func recordCheckpoints(t *testing.T, s *SQLActivityStore, also func(ckptCall)) (calls func() []ckptCall) {
	t.Helper()
	var mu sync.Mutex
	var got []ckptCall
	hook := ckptHookFn(func(mode string, res walCheckpointResult, _ error) {
		c := ckptCall{mode: mode, res: res}
		mu.Lock()
		got = append(got, c)
		mu.Unlock()
		if also != nil {
			also(c)
		}
	})
	s.ckptr.hook.Store(&hook)
	t.Cleanup(func() { s.ckptr.hook.Store(nil) })
	return func() []ckptCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]ckptCall(nil), got...)
	}
}

// requireTruncateOnlyAfterCompletePassive fails if any TRUNCATE in calls was
// not immediately preceded by a PASSIVE that reported complete(): such a
// TRUNCATE copied the remaining frames while holding the WAL write lock.
func requireTruncateOnlyAfterCompletePassive(t *testing.T, calls []ckptCall) {
	t.Helper()
	for k, c := range calls {
		if c.mode != "TRUNCATE" {
			continue
		}
		if k == 0 || calls[k-1].mode != "PASSIVE" || !calls[k-1].res.complete() {
			t.Fatalf("checkpoint %d is a TRUNCATE not preceded by a complete PASSIVE: %+v", k, calls)
		}
	}
}

// TestVacuumActivity_ReportsSpaceStillHeldWhileAReaderPinsTheWAL: a reader
// that opened its snapshot before the VACUUM keeps PASSIVE from copying the
// VACUUM's frames for as long as it lives. Retrying cannot fix that, so after
// the bounded attempts VacuumActivity must return the "space still held" error
// rather than claim the WAL was emptied, and it must never issue a TRUNCATE in
// the meantime: that TRUNCATE would hold the write lock while waiting for the
// reader and while copying.
func TestVacuumActivity_ReportsSpaceStillHeldWhileAReaderPinsTheWAL(t *testing.T) {
	s, _ := openCkptTestStore(t, time.Hour)
	oldAttempts, oldBackoff := vacuumTruncateAttempts, vacuumTruncateMaxBackoff
	vacuumTruncateAttempts, vacuumTruncateMaxBackoff = 3, 10*time.Millisecond
	t.Cleanup(func() { vacuumTruncateAttempts, vacuumTruncateMaxBackoff = oldAttempts, oldBackoff })

	for range 10 {
		insertLegacyOversizedSummary(t, s, strings.Repeat("v", activitySummaryMax*8))
	}
	holdReaderSnapshot(t, s)
	calls := recordCheckpoints(t, s, nil)

	_, err := s.VacuumActivity(context.Background())
	if err == nil || !strings.Contains(err.Error(), "space still held") {
		t.Fatalf("VacuumActivity err = %v; want the space-still-held error while a reader pins the WAL", err)
	}
	got := calls()
	passives := 0
	for _, c := range got {
		if c.mode == "TRUNCATE" {
			t.Fatalf("a TRUNCATE was issued although PASSIVE never completed: %+v", got)
		}
		if c.res.complete() {
			t.Fatalf("fixture: a PASSIVE completed although the reader pins the WAL: %+v", got)
		}
		passives++
	}
	if passives != vacuumTruncateAttempts {
		t.Errorf("PASSIVE attempts = %d, want %d (every attempt retried): %+v", passives, vacuumTruncateAttempts, got)
	}
}

// holdReaderSnapshot opens a read transaction on its own connection, which pins
// the current WAL snapshot: PASSIVE cannot copy frames written after it, and
// TRUNCATE cannot reset the WAL, until release is called. release is
// idempotent and safe to call from another goroutine.
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

// TestVacuumActivity_BusyTruncateSucceedsOnALaterAttempt: once PASSIVE has
// copied everything, a TRUNCATE can still be busy, because a reader that opened
// after a later write keeps the WAL from being reset. That attempt must be retried,
// and the later attempt that gets through counts as success with the WAL empty.
func TestVacuumActivity_BusyTruncateSucceedsOnALaterAttempt(t *testing.T) {
	s, _ := openCkptTestStore(t, time.Hour)
	for range 10 {
		insertLegacyOversizedSummary(t, s, strings.Repeat("v", activitySummaryMax*8))
	}

	var release func()
	var busy atomic.Int32
	calls := recordCheckpoints(t, s, func(c ckptCall) {
		switch {
		case c.mode == "PASSIVE" && c.res.complete() && release == nil:
			// A write and then a reader arrive between the copy and the reset.
			// The write matters: with every frame already copied, a new
			// reader reads the database file alone and does not hold the WAL.
			if _, err := s.Record(ActivityEntry{Timestamp: time.Now(), Tier: "info", Type: "t",
				Level: "info", Source: "s", Summary: "between copy and reset"}); err != nil {
				t.Errorf("record: %v", err)
			}
			release = holdReaderSnapshot(t, s)
		case c.mode == "TRUNCATE" && c.res.Busy != 0 && busy.Add(1) == 1:
			release() // and lets go before the retry
		}
	})

	if _, err := s.VacuumActivity(context.Background()); err != nil {
		t.Fatalf("vacuum: %v (a busy TRUNCATE must be retried, not reported)", err)
	}
	got := calls()
	requireTruncateOnlyAfterCompletePassive(t, got)
	if n := busy.Load(); n != 1 {
		t.Fatalf("busy TRUNCATE attempts = %d, want exactly 1 (the fixture must make the first TRUNCATE busy): %+v", n, got)
	}
	if fi, err := os.Stat(s.path + "-wal"); err == nil && fi.Size() != 0 {
		t.Fatalf("vacuum reported success but the -wal still holds %d bytes", fi.Size())
	}
}

// TestVacuumActivity_ReaderReleasedMidTruncateNeverTruncatesUnderTheCopy is the
// SF-A regression test. A reader that opened before the VACUUM keeps PASSIVE
// short of the VACUUM's frames; it lets go part-way through the truncate phase.
// The old code issued TRUNCATE after every PASSIVE regardless, so once the
// reader left that TRUNCATE copied the remaining frames (about 16k in review)
// while holding the write lock, and with the reader held throughout each of
// the 8 TRUNCATEs held the lock for about 1 s (a 2.78 s Record). Now a TRUNCATE
// is issued only after a PASSIVE that copied everything, and Records running
// through the whole phase must never fail or stall.
func TestVacuumActivity_ReaderReleasedMidTruncateNeverTruncatesUnderTheCopy(t *testing.T) {
	s, _ := openCkptTestStore(t, time.Hour)
	for range 40 {
		insertLegacyOversizedSummary(t, s, strings.Repeat("v", activitySummaryMax*8))
	}
	release := holdReaderSnapshot(t, s)
	if _, err := s.writer.Exec(`VACUUM`); err != nil {
		t.Fatalf("vacuum: %v", err)
	}

	var incomplete atomic.Int32
	calls := recordCheckpoints(t, s, func(c ckptCall) {
		// Let go after the reader has visibly blocked one PASSIVE.
		if c.mode == "PASSIVE" && !c.res.complete() && incomplete.Add(1) == 1 {
			go func() {
				time.Sleep(100 * time.Millisecond)
				release()
			}()
		}
	})

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
			if _, err := s.Record(ActivityEntry{Timestamp: base.Add(time.Duration(i) * time.Millisecond),
				Tier: "info", Type: "t", Level: "info", Source: "s", Summary: fmt.Sprintf("mid-truncate-%d", i)}); err != nil {
				recErrs.Add(1)
				fmt.Fprintf(os.Stderr, "Record during truncate failed: %v\n", err)
			}
			if d := int64(time.Since(start)); d > maxLatency.Load() {
				maxLatency.Store(d)
			}
			time.Sleep(time.Millisecond)
		}
	}()

	err := s.truncateWALAfterVacuum(context.Background())
	close(stop)
	waitGroupOrFatal(t, &wg, "the Record loop")
	if err != nil {
		t.Fatalf("truncateWALAfterVacuum: %v (the reader let go, so a later attempt must succeed)", err)
	}
	got := calls()
	t.Logf("checkpoints %+v; slowest Record %v", got, time.Duration(maxLatency.Load()))
	if incomplete.Load() == 0 {
		t.Fatalf("fixture: no PASSIVE was held short by the reader: %+v", got)
	}
	requireTruncateOnlyAfterCompletePassive(t, got)
	if n := recErrs.Load(); n != 0 {
		t.Fatalf("%d Records failed during the truncate phase", n)
	}
	// Generous for -race on a loaded disk; the old behaviour held the lock for
	// whole seconds per attempt.
	if lat := time.Duration(maxLatency.Load()); lat > 2*time.Second {
		t.Fatalf("slowest Record took %v during the truncate phase: writers were blocked", lat)
	}
}

// TestVacuumActivity_IdleTickReclaimsWhatAGivenUpTruncateLeft: when the vacuum
// gives up because a reader pins the WAL and reports the space still held, the
// background checkpointer's idle-tick step must reset the WAL once the reader
// lets go, so the space is not stranded until a restart.
//
// The tick is driven directly (checkpointAndMaybeTruncate with idle=true, the
// call runCheckpointer makes) on a store whose own loop is held off, so the
// test does not depend on when a real loop's ticks land or whether one sees
// the store idle. An earlier version ran a 20 ms loop; while the checkpoint
// connection had busy_timeout(1000), that loop's TRUNCATE held the write lock
// for a second at a time waiting on the test's reader and starved the VACUUM
// ("database is locked"). That the loop calls this step on idle ticks is
// covered by TestSQLActivityStore_BackgroundCheckpointerRunsAndTruncatesWhenIdle.
func TestVacuumActivity_IdleTickReclaimsWhatAGivenUpTruncateLeft(t *testing.T) {
	s, _ := openCkptTestStore(t, time.Hour)
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
	s.checkpointAndMaybeTruncate(context.Background(), true)
	if fi, err := os.Stat(walPath); err == nil && fi.Size() != 0 {
		t.Fatalf("the idle-tick checkpoint did not reset the -wal (%d bytes) after the reader let go", fi.Size())
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
//   - every TRUNCATE follows a PASSIVE that reported complete, and the one
//     before the first TRUNCATE had copied every frame the VACUUM left (a
//     PASSIVE before it may report busy and copy nothing; that is retried);
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

	calls := recordCheckpoints(t, s, nil)

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
	got := calls()
	var copyTime time.Duration
	for _, c := range got {
		copyTime = max(copyTime, c.res.Elapsed)
	}
	lat := time.Duration(maxLatency.Load())
	t.Logf("frame copy took %v; slowest Record %v; checkpoints %+v; vacuum frames %d",
		copyTime, lat, got, vacuumFrames)
	// The deterministic check: the PASSIVE right before the first TRUNCATE
	// reported complete and had copied every frame the VACUUM wrote, so the
	// TRUNCATE that holds the write lock had none of them left to copy.
	//
	// Not "the FIRST checkpoint is PASSIVE and did the copy": a PASSIVE can
	// legitimately report busy=1 with log/checkpointed of -1 and copy nothing.
	// The checkpoint connection has busy_timeout 0, so a PASSIVE that cannot
	// take a WAL lock at once (here the only other activity is the concurrent
	// Records' commits) gives up immediately, and truncateWALAfterVacuum
	// retries it after its backoff. CI hit
	// exactly that on 2026-10-06: [PASSIVE busy, PASSIVE 3289/3289, TRUNCATE],
	// which is correct behaviour that the old index-0 assertion failed.
	requireTruncateOnlyAfterCompletePassive(t, got)
	first := slices.IndexFunc(got, func(c ckptCall) bool { return c.mode == "TRUNCATE" })
	if first < 1 {
		t.Fatalf("no TRUNCATE preceded by a PASSIVE was issued: %+v", got)
	}
	if copied := got[first-1].res.Checkpointed; copied < vacuumFrames {
		t.Errorf("the PASSIVE before the TRUNCATE had copied %d frames, fewer than the %d the VACUUM left: "+
			"the TRUNCATE would copy the rest while holding the write lock", copied, vacuumFrames)
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

// TestVacuumActivity_LateReaderDoesNotStallWritersThroughTheTruncate is the
// regression test for the checkpoint connection's busy_timeout. A reader that
// opens AFTER the VACUUM does not stop PASSIVE from copying every frame, so
// the TRUNCATE is issued; it takes the WAL write lock and then has to wait for
// that reader. With busy_timeout(1000) on the checkpoint connection each
// attempt held the lock for a full second while it waited, and Records stalled
// for 2.5 s in review. With busy_timeout 0 the TRUNCATE reports busy at once
// and releases the lock, so writers barely notice.
func TestVacuumActivity_LateReaderDoesNotStallWritersThroughTheTruncate(t *testing.T) {
	s, _ := openCkptTestStore(t, time.Hour)
	oldAttempts, oldBackoff := vacuumTruncateAttempts, vacuumTruncateMaxBackoff
	vacuumTruncateAttempts, vacuumTruncateMaxBackoff = 4, 20*time.Millisecond
	t.Cleanup(func() { vacuumTruncateAttempts, vacuumTruncateMaxBackoff = oldAttempts, oldBackoff })

	for range 20 {
		insertLegacyOversizedSummary(t, s, strings.Repeat("v", activitySummaryMax*8))
	}
	if _, err := s.writer.Exec(`VACUUM`); err != nil {
		t.Fatalf("vacuum: %v", err)
	}
	// One write after the VACUUM, then the reader: with every frame already
	// copied a new reader would read the database file alone and not hold the
	// WAL, so the write keeps it on the WAL.
	if _, err := s.Record(ActivityEntry{Timestamp: time.Now(), Tier: "info", Type: "t", Level: "info",
		Source: "s", Summary: "before the late reader"}); err != nil {
		t.Fatal(err)
	}
	holdReaderSnapshot(t, s)

	// Slow writers: one Record every 10 ms. They start from the hook, right
	// after the first PASSIVE that copied everything and before the TRUNCATE
	// it unlocks, so that TRUNCATE is issued (a write before it would leave
	// frames past the reader's snapshot and keep PASSIVE short) and the
	// writers are queued on the write lock while it runs.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var started sync.Once
	var recErrs atomic.Int32
	var maxLatency atomic.Int64
	writers := func() {
		defer wg.Done()
		base := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			start := time.Now()
			if _, err := s.Record(ActivityEntry{Timestamp: base.Add(time.Duration(i) * time.Millisecond),
				Tier: "info", Type: "t", Level: "info", Source: "s", Summary: fmt.Sprintf("late-reader-%d", i)}); err != nil {
				recErrs.Add(1)
				fmt.Fprintf(os.Stderr, "Record during truncate failed: %v\n", err)
			}
			if d := int64(time.Since(start)); d > maxLatency.Load() {
				maxLatency.Store(d)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	calls := recordCheckpoints(t, s, func(c ckptCall) {
		if c.mode == "PASSIVE" && c.res.complete() {
			started.Do(func() {
				wg.Add(1)
				go writers()
			})
		}
	})

	err := s.truncateWALAfterVacuum(context.Background())
	close(stop)
	waitGroupOrFatal(t, &wg, "the Record loop")
	got := calls()
	lat := time.Duration(maxLatency.Load())
	t.Logf("truncate err=%v; slowest Record %v; checkpoints %+v", err, lat, got)

	if err == nil {
		t.Fatalf("truncateWALAfterVacuum succeeded although a reader held the WAL throughout")
	}
	truncates := 0
	for _, c := range got {
		if c.mode == "TRUNCATE" {
			truncates++
		}
	}
	if truncates == 0 {
		t.Fatalf("fixture: no TRUNCATE was issued, so the late reader was never waited on: %+v", got)
	}
	requireTruncateOnlyAfterCompletePassive(t, got)
	if n := recErrs.Load(); n != 0 {
		t.Fatalf("%d Records failed during the truncate phase", n)
	}
	// Each TRUNCATE used to hold the write lock for the 1 s busy timeout; the
	// bound leaves room for -race on a loaded disk without allowing that.
	if lat > 500*time.Millisecond {
		t.Fatalf("slowest Record took %v across %d busy TRUNCATEs: the TRUNCATE waited on the reader while holding the write lock", lat, truncates)
	}
}
