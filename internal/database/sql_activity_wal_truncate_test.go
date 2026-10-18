// file: internal/database/sql_activity_wal_truncate_test.go
// version: 2.4.0
// guid: 8b47e0c9-2f13-45da-9e60-c4a1d5382bf7
// last-edited: 2026-10-18

package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// shrinkLegacyRows rewrites every row's summary to one byte, the way a clamp
// pass did, so the freed overflow pages land in the WAL and give the truncate
// something to return.
func shrinkLegacyRows(t *testing.T, s *SQLActivityStore) {
	t.Helper()
	if _, err := s.writer.Exec(`UPDATE activity SET summary = 'x'`); err != nil {
		t.Fatalf("shrink legacy rows: %v", err)
	}
}

// legacySrcKeySeq makes each fixture row's src_key unique.
var legacySrcKeySeq int

// insertLegacyOversizedSummary writes a row the way it existed BEFORE
// clampActivitySummary was added to the write path: an oversized summary that
// spills onto overflow pages. The truncate tests use these rows as a workload
// that leaves a large WAL behind once shrinkLegacyRows rewrites them. It has to
// bypass Record on purpose, because Record clamps.
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

// VacuumActivity must leave the -wal file truncated, not merely checkpointed.
//
// This is the half that a "did the main file shrink?" assertion misses, and it
// is the half that decides whether the user gets their disk space back. In WAL
// mode VACUUM writes the rebuilt database THROUGH the WAL, and SQLite's
// automatic checkpoint is PASSIVE — it recycles the WAL in place at its
// high-water mark and never shrinks the file. Hit for real on prod 2026-09-08:
// the main file fell 22,898,438,144 → 11,447,480,320 while the WAL held at
// 11,514,065,152 and stayed there, so the net reclaim was about zero.
func TestVacuumTruncate_TruncatesTheWAL(t *testing.T) {
	s := newTestSQLStore(t)

	// Build a WAL worth truncating: write, then clamp, which rewrites every one
	// of these rows and pushes their freed overflow pages into the WAL.
	for range 40 {
		insertLegacyOversizedSummary(t, s, strings.Repeat("v", activitySummaryMax*8))
	}
	shrinkLegacyRows(t, s)

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

// TestVacuumTruncate_TruncatesTheWALWhileTheCheckpointerRuns is the regression
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
// TestVacuumTruncate_BusyTruncateSucceedsOnALaterAttempt.
func TestVacuumTruncate_TruncatesTheWALWhileTheCheckpointerRuns(t *testing.T) {
	s, _ := openCkptTestStore(t, time.Millisecond)

	for range 40 {
		insertLegacyOversizedSummary(t, s, strings.Repeat("v", activitySummaryMax*8))
	}
	shrinkLegacyRows(t, s)

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

// requireTruncateLeftoverBounded fails if the PASSIVE right before any TRUNCATE
// had more than vacuumTruncateMaxLeftoverFrames frames of its own to copy,
// computed from the checkpoint results alone: that PASSIVE's Log minus the
// Checkpointed of the PASSIVE before it in calls (0 if none; a Log below that
// means the WAL restarted, and all Log frames count). The frames the TRUNCATE
// copies under the write lock are the ones Records wrote while that PASSIVE
// ran, so a PASSIVE with few frames to copy is what keeps them few however long
// the earlier copies took. A single PASSIVE straight after the VACUUM fails
// this: it had every frame the VACUUM left to copy, and a TRUNCATE after it
// inherits everything written during that whole copy (the 2026-10-06 CI
// failure: a 6.13 s PASSIVE, then a 3.79 s TRUNCATE).
//
// It is NOT an independent oracle. It recomputes the rule from the recorded
// results instead of calling passiveNewFrames, so an edit to one does not
// silently edit the other, but it encodes the same rule, with only the
// Log-below-previous restart signal (it cannot see the WAL header generation
// production also reads, so after an unseen restart it can undercount exactly
// where production's fallback would). What it checks is that the trace obeys
// the rule, not that the rule bounds the TRUNCATE's copy. The quantity that
// rule exists to bound, how many frames the TRUNCATE itself copied, is
// truncateCopiedFrames, which is measured, not derived; the tests log it.
func requireTruncateLeftoverBounded(t *testing.T, calls []ckptCall) {
	t.Helper()
	for k, c := range calls {
		if c.mode != "TRUNCATE" {
			continue
		}
		if k == 0 || calls[k-1].mode != "PASSIVE" {
			t.Fatalf("checkpoint %d is a TRUNCATE not directly after a PASSIVE: %+v", k, calls)
		}
		last := calls[k-1].res
		prev := 0
		for j := k - 2; j >= 0; j-- {
			if calls[j].mode == "PASSIVE" && calls[j].res.Log >= 0 {
				prev = calls[j].res.Checkpointed
				break
			}
		}
		left := last.Log - prev
		if last.Log < prev {
			left = last.Log
		}
		if left > vacuumTruncateMaxLeftoverFrames {
			t.Fatalf("checkpoint %d is a TRUNCATE after a PASSIVE that had %d frames to copy (Log %d, previous "+
				"PASSIVE Checkpointed %d), over the %d bound: the TRUNCATE inherits every frame written during that "+
				"copy and copies them under the write lock: %+v",
				k, left, last.Log, prev, vacuumTruncateMaxLeftoverFrames, calls)
		}
	}
}

// truncateCopiedFrames is, for each TRUNCATE in calls, how many frames it
// copied that the PASSIVE right before it had not (its Checkpointed minus that
// PASSIVE's): the frames it copied while holding the WAL write lock. -1 when
// the TRUNCATE reported no counts. Logged for diagnosis; the bound on it is
// statistical (frames written during one short PASSIVE plus the reset wait),
// so asserting it would bring back the wall-clock flake shape.
func truncateCopiedFrames(calls []ckptCall) []int {
	var out []int
	for k, c := range calls {
		if c.mode != "TRUNCATE" || k == 0 {
			continue
		}
		if c.res.Checkpointed < 0 || calls[k-1].res.Checkpointed < 0 {
			out = append(out, -1)
			continue
		}
		out = append(out, c.res.Checkpointed-calls[k-1].res.Checkpointed)
	}
	return out
}

// requireFixtureOutgrowsLeftoverBound fails a test whose VACUUM left no more
// than vacuumTruncateMaxLeftoverFrames frames. requireTruncateLeftoverBounded
// only tells a single-PASSIVE-then-TRUNCATE implementation apart from the
// converging one when that first PASSIVE had MORE than the bound to copy; with
// a smaller fixture the old code passes it too, and the test proves nothing.
func requireFixtureOutgrowsLeftoverBound(t *testing.T, vacuumFrames int) {
	t.Helper()
	if vacuumFrames <= vacuumTruncateMaxLeftoverFrames {
		t.Fatalf("fixture too small: VACUUM left %d WAL frames, not more than vacuumTruncateMaxLeftoverFrames "+
			"(%d), so the leftover check cannot distinguish a TRUNCATE straight after the first PASSIVE",
			vacuumFrames, vacuumTruncateMaxLeftoverFrames)
	}
}

// walFrames returns how many frames the -wal holds: a 32-byte header, then
// frames of a 24-byte header plus one page each. It is exact only while
// nothing has reset or restarted the WAL, which holds on a fresh store whose
// background checkpointer is held off (every connection has autocheckpoint 0).
func walFrames(t *testing.T, s *SQLActivityStore) int {
	t.Helper()
	var pageSize int64
	if err := s.reader.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(s.path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	return int((fi.Size() - 32) / (pageSize + 24))
}

// requirePassiveCopiedVacuumFramesBeforeTruncate fails unless some PASSIVE
// before the first TRUNCATE had copied at least vacuumFrames frames: only then
// did the TRUNCATE, which holds the WAL write lock, have none of the VACUUM's
// frames left to copy.
//
// Not "the PASSIVE right before the TRUNCATE": after a PASSIVE copies every
// frame, the next Record's commit can restart the WAL from frame 1, and when
// passiveUntilShort runs another round (it overcounts on any sign of a restart)
// the PASSIVE right before the TRUNCATE reports the restarted WAL, Log 0 and
// Checkpointed 0. That is the best case, not a violation: SQLite restarts the
// WAL only once every frame in it is in the database file. Seen in 3 of 450
// loaded runs on 2026-10-06. Checkpointed counts from frame 1 of whichever WAL
// generation it reports, so a PASSIVE with Checkpointed >= vacuumFrames either
// copied the VACUUM's frames itself or came after a restart that required it.
func requirePassiveCopiedVacuumFramesBeforeTruncate(t *testing.T, calls []ckptCall, vacuumFrames int) {
	t.Helper()
	first := slices.IndexFunc(calls, func(c ckptCall) bool { return c.mode == "TRUNCATE" })
	if first < 1 {
		t.Fatalf("no TRUNCATE preceded by a PASSIVE was issued: %+v", calls)
	}
	most := 0
	for _, c := range calls[:first] {
		if c.mode == "PASSIVE" {
			most = max(most, c.res.Checkpointed)
		}
	}
	if most < vacuumFrames {
		t.Errorf("no PASSIVE before the first TRUNCATE had copied the %d frames the VACUUM left (most: %d): "+
			"the TRUNCATE would copy the rest while holding the write lock: %+v", vacuumFrames, most, calls)
	}
}

// TestVacuumTruncate_ReportsSpaceStillHeldWhileAReaderPinsTheWAL: a reader
// that opened its snapshot before the VACUUM keeps PASSIVE from copying the
// VACUUM's frames for as long as it lives. Retrying cannot fix that, so after
// the bounded attempts VacuumActivity must return the "space still held" error
// rather than claim the WAL was emptied, and it must never issue a TRUNCATE in
// the meantime: that TRUNCATE would hold the write lock while waiting for the
// reader and while copying.
func TestVacuumTruncate_ReportsSpaceStillHeldWhileAReaderPinsTheWAL(t *testing.T) {
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
	if !strings.Contains(err.Error(), string(walReasonReaderHeld)) {
		t.Errorf("error %q does not name the reason %q", err, walReasonReaderHeld)
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

// TestVacuumTruncate_BusyTruncateSucceedsOnALaterAttempt: once PASSIVE has
// copied everything, a TRUNCATE can still be busy, because a reader that opened
// after a later write keeps the WAL from being reset. That attempt must be retried,
// and the later attempt that gets through counts as success with the WAL empty.
func TestVacuumTruncate_BusyTruncateSucceedsOnALaterAttempt(t *testing.T) {
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

// TestVacuumTruncate_ReaderReleasedMidTruncateNeverTruncatesUnderTheCopy is the
// SF-A regression test. A reader that opened before the VACUUM keeps PASSIVE
// short of the VACUUM's frames; it lets go part-way through the truncate phase.
// The old code issued TRUNCATE after every PASSIVE regardless, so once the
// reader left that TRUNCATE copied the remaining frames (about 16k in review)
// while holding the write lock, and with the reader held throughout each of
// the 8 TRUNCATEs held the lock for about 1 s (a 2.78 s Record). Now a TRUNCATE
// is issued only after a PASSIVE that copied everything, and Records running
// through the whole phase must never fail.
//
// It asserts the property its name claims with ordering evidence, not a
// wall-clock bound on Record latency:
//   - no TRUNCATE is issued while the reader is still held (released is set
//     before the reader lets go, so a TRUNCATE seen while it is false can only
//     have run with the reader pinning the copy short);
//   - every TRUNCATE follows a PASSIVE that reported complete;
//   - a PASSIVE before the first TRUNCATE had copied every frame the VACUUM
//     left, so none of them were copied under the write lock;
//   - the PASSIVE before every TRUNCATE had at most
//     vacuumTruncateMaxLeftoverFrames frames of its own to copy, so the frames
//     Records wrote while it ran, which the TRUNCATE copies under the lock, are
//     few (requireTruncateLeftoverBounded);
//   - no Record fails.
//
// It used to bound the slowest Record at 2 s instead. That failed on a loaded
// -race CI runner (run 37453234558): the completing PASSIVE took 6.13 s, the
// TRUNCATE after it 3.79 s, and a Record waited 3.84 s behind that TRUNCATE.
// The TRUNCATE was copying the frames Records had committed during the 6.13 s
// PASSIVE (PASSIVE only copies what was in the WAL when it started). That was
// a real defect in truncateWALAfterVacuum, fixed by repeating PASSIVE until one
// is short; the frame-count bound now catches it on any runner, where the
// wall-clock bound depended on the runner's disk and CPU. The slowest Record is
// logged for diagnosis, not asserted.
func TestVacuumTruncate_ReaderReleasedMidTruncateNeverTruncatesUnderTheCopy(t *testing.T) {
	s, _ := openCkptTestStore(t, time.Hour)
	for range 40 {
		insertLegacyOversizedSummary(t, s, strings.Repeat("v", activitySummaryMax*8))
	}
	release := holdReaderSnapshot(t, s)
	if _, err := s.writer.Exec(`VACUUM`); err != nil {
		t.Fatalf("vacuum: %v", err)
	}
	vacuumFrames := walFrames(t, s)
	requireFixtureOutgrowsLeftoverBound(t, vacuumFrames)

	var incomplete, truncatesWhileHeld atomic.Int32
	var released atomic.Bool
	calls := recordCheckpoints(t, s, func(c ckptCall) {
		if c.mode == "TRUNCATE" && !released.Load() {
			truncatesWhileHeld.Add(1)
		}
		// Let go after the reader has visibly blocked one PASSIVE.
		if c.mode == "PASSIVE" && !c.res.complete() && incomplete.Add(1) == 1 {
			go func() {
				time.Sleep(100 * time.Millisecond)
				// Before release, not after: a TRUNCATE between the two
				// would otherwise count as issued while the reader was held.
				released.Store(true)
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
	got := calls()
	// Logged before any assertion so a failure carries the trace.
	t.Logf("checkpoints %+v; frames each TRUNCATE copied %v; slowest Record %v",
		got, truncateCopiedFrames(got), time.Duration(maxLatency.Load()))
	if err != nil {
		t.Fatalf("truncateWALAfterVacuum: %v (the reader let go, so a later attempt must succeed)", err)
	}
	if incomplete.Load() == 0 {
		t.Fatalf("fixture: no PASSIVE was held short by the reader: %+v", got)
	}
	if n := truncatesWhileHeld.Load(); n != 0 {
		t.Fatalf("%d TRUNCATEs were issued while the reader still held PASSIVE short of the VACUUM's frames: %+v", n, got)
	}
	requireTruncateOnlyAfterCompletePassive(t, got)
	requirePassiveCopiedVacuumFramesBeforeTruncate(t, got, vacuumFrames)
	requireTruncateLeftoverBounded(t, got)
	if n := recErrs.Load(); n != 0 {
		t.Fatalf("%d Records failed during the truncate phase", n)
	}
}

// TestVacuumTruncate_IdleTickReclaimsWhatAGivenUpTruncateLeft: when the vacuum
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
func TestVacuumTruncate_IdleTickReclaimsWhatAGivenUpTruncateLeft(t *testing.T) {
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

// TestVacuumTruncate_TruncateDoesNotStallForegroundWrites is the regression
// test for the post-vacuum TRUNCATE holding the WAL write lock for the whole
// frame copy. After a VACUUM the WAL holds the entire rebuilt database, and a
// TRUNCATE that copies it blocks every Record for that long; on prod (an 11 GB
// WAL) Records would wait out busy_timeout(10000) and fail with SQLITE_BUSY.
// truncateWALAfterVacuum now copies with PASSIVE first, which does not block
// writers, so the TRUNCATE only resets an already-copied WAL.
//
// It asserts three things while Records run continuously through the truncate:
//   - no Record fails;
//   - every TRUNCATE follows a PASSIVE that reported complete, and a PASSIVE
//     before the first TRUNCATE had copied every frame the VACUUM left (a
//     PASSIVE may report busy and copy nothing; that is retried),
//     so the TRUNCATE that holds the write lock copied none of them;
//   - the PASSIVE before every TRUNCATE had at most
//     vacuumTruncateMaxLeftoverFrames frames of its own to copy, so the frames
//     written while it ran, which the TRUNCATE copies, are few.
//
// It used to also fail when the slowest Record reached half the longest
// checkpoint's Elapsed (when that was at least 1 s). That wall-clock ratio
// depended on the runner: CI run 37453234558 saw a 6.13 s PASSIVE, a 3.79 s
// TRUNCATE after it and a 3.84 s Record behind the TRUNCATE. The frame-count
// checks above cover the same failure deterministically; the timings are
// logged only.
func TestVacuumTruncate_TruncateDoesNotStallForegroundWrites(t *testing.T) {
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
	vacuumFrames := walFrames(t, s)
	requireFixtureOutgrowsLeftoverBound(t, vacuumFrames)

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
	t.Logf("frame copy took %v; slowest Record %v; checkpoints %+v; frames each TRUNCATE copied %v; vacuum frames %d",
		copyTime, lat, got, truncateCopiedFrames(got), vacuumFrames)
	// The deterministic check: the PASSIVE right before every TRUNCATE
	// reported complete, and a PASSIVE before the first TRUNCATE had copied
	// every frame the VACUUM wrote, so the TRUNCATE that holds the write lock
	// had none of them left to copy.
	//
	// Not "the FIRST checkpoint is PASSIVE and did the copy": a PASSIVE can
	// legitimately report busy=1 with log/checkpointed of -1 and copy nothing.
	// PASSIVE never waits in the busy handler, so one that cannot take the
	// checkpoint lock or read the WAL index header at once (here the only
	// other activity is the concurrent Records' commits) gives up immediately,
	// and truncateWALAfterVacuum retries it after its backoff. CI hit
	// exactly that on 2026-10-06: [PASSIVE busy, PASSIVE 3289/3289, TRUNCATE],
	// which is correct behaviour that the old index-0 assertion failed.
	requireTruncateOnlyAfterCompletePassive(t, got)
	requirePassiveCopiedVacuumFramesBeforeTruncate(t, got, vacuumFrames)
	requireTruncateLeftoverBounded(t, got)
}

// TestVacuumTruncate_LateReaderDoesNotStallWritersThroughTheTruncate is the
// regression test for the checkpoint connection's busy_timeout. A reader that
// opens AFTER the VACUUM does not stop PASSIVE from copying every frame, so
// the TRUNCATE is issued; it takes the WAL write lock and then has to wait for
// that reader. With busy_timeout(1000) on the checkpoint connection each
// attempt held the lock for a full second while it waited, and Records stalled
// for 2.5 s in review.
//
// How long a TRUNCATE can wait for a reader while holding the lock is the
// busy_timeout it runs with: SQLite's busy handler is the only thing that makes
// it wait. The checkpoint connection rests at 0 and the post-vacuum TRUNCATE
// alone runs with vacuumTruncateResetWaitMS (see that var for why the wait
// exists and why it does not stall writers). So the test asserts, on the live
// connection and deterministically, that the resting value is 0, that the
// per-statement value is far below the 1000 ms that stalled Records, and that
// the resting value is back to 0 after the truncate phase (the restore in
// truncateWithResetWait, which a missed restore would leak into the background
// loop's TRUNCATE). It does not bound Record latency with a wall-clock number:
// the earlier 500 ms bound discriminated a 1 s regression only by a 2x margin
// over -race and loaded-runner noise. The run below still proves the behaviour:
// a TRUNCATE is issued while the reader is held, every one reports busy
// (returns after its bounded wait rather than waiting for the reader to leave),
// and no Record fails. The slowest Record is logged for diagnosis.
func TestVacuumTruncate_LateReaderDoesNotStallWritersThroughTheTruncate(t *testing.T) {
	s, _ := openCkptTestStore(t, time.Hour)
	var ckptBusyMS int
	if err := s.ckpt.QueryRow(`PRAGMA busy_timeout`).Scan(&ckptBusyMS); err != nil {
		t.Fatal(err)
	}
	if ckptBusyMS != 0 {
		t.Fatalf("checkpoint connection busy_timeout = %d ms; it must be 0: a TRUNCATE waits that long for a "+
			"reader while holding the WAL write lock, and every Record waits with it", ckptBusyMS)
	}
	// 100 ms: a tenth of the 1000 ms busy_timeout that stalled Records 2.5 s.
	if vacuumTruncateResetWaitMS > 100 {
		t.Fatalf("vacuumTruncateResetWaitMS = %d; the post-vacuum TRUNCATE waits up to that long for readers "+
			"while holding the WAL write lock, so it must stay far below the 1000 ms that stalled Records",
			vacuumTruncateResetWaitMS)
	}
	oldAttempts, oldBackoff := vacuumTruncateAttempts, vacuumTruncateMaxBackoff
	oldBusy := vacuumTruncateBusyAttempts
	vacuumTruncateAttempts, vacuumTruncateMaxBackoff, vacuumTruncateBusyAttempts = 4, 20*time.Millisecond, 4
	t.Cleanup(func() {
		vacuumTruncateAttempts, vacuumTruncateMaxBackoff, vacuumTruncateBusyAttempts = oldAttempts, oldBackoff, oldBusy
	})

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
	// after the PASSIVE that converged and before the TRUNCATE it unlocks, so
	// that TRUNCATE is issued (a write before it would leave
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
	// Start them on the CONVERGED PASSIVE (complete, and at most
	// vacuumTruncateMaxLeftoverFrames frames past the previous PASSIVE's
	// Checkpointed), not the first complete one. The first complete PASSIVE
	// after the VACUUM has every VACUUM frame as new, so passiveUntilShort
	// runs another round; writers started then could commit past the late
	// reader's snapshot before that round, hold every later PASSIVE short,
	// and no TRUNCATE would ever be issued (CI on 2026-10-06, PR #3781:
	// [PASSIVE 856/856, PASSIVE 862/856, ...] and "no TRUNCATE was issued").
	// After a converged PASSIVE the TRUNCATE decision is already made: the
	// hook runs before passiveUntilShort classifies the result, and nothing
	// the writers do changes it.
	prevCkpt := -1 // the previous PASSIVE's Checkpointed; only this goroutine touches it
	calls := recordCheckpoints(t, s, func(c ckptCall) {
		if c.mode != "PASSIVE" || c.res.Log < 0 {
			return
		}
		converged := prevCkpt >= 0 && c.res.complete() && c.res.Log-prevCkpt <= vacuumTruncateMaxLeftoverFrames
		prevCkpt = c.res.Checkpointed
		if converged {
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
		if c.mode != "TRUNCATE" {
			continue
		}
		truncates++
		// The reader is held for the whole test, so no TRUNCATE can reset the
		// WAL; each must come back busy rather than as a completed reset.
		if c.res.Busy != 1 {
			t.Errorf("a TRUNCATE reported busy=%d while the late reader was still held: %+v", c.res.Busy, got)
		}
	}
	if truncates == 0 {
		t.Fatalf("fixture: no TRUNCATE was issued, so the late reader was never waited on: %+v", got)
	}
	requireTruncateOnlyAfterCompletePassive(t, got)
	if n := recErrs.Load(); n != 0 {
		t.Fatalf("%d Records failed during the truncate phase", n)
	}
	if err := s.ckpt.QueryRow(`PRAGMA busy_timeout`).Scan(&ckptBusyMS); err != nil {
		t.Fatal(err)
	}
	if ckptBusyMS != sqlActCkptBusyTimeoutMS {
		t.Fatalf("checkpoint connection busy_timeout = %d ms after the truncate phase; the TRUNCATE's raised wait "+
			"must be restored to %d before the connection goes back to the background loop",
			ckptBusyMS, sqlActCkptBusyTimeoutMS)
	}
}

// TestVacuumTruncate_BusyTruncateRetriesHaveTheirOwnBudget is the deterministic
// form of the SF1 flake (TestVacuumTruncate_ReaderReleasedMidTruncate failing
// "WAL not reset after 8 attempts (last checkpoint busy=1 wal_frames=2153
// checkpointed=2153)" in 15 of 135 loaded runs). A reader that predates the
// VACUUM holds PASSIVE short for some attempts; after it lets go, a writer
// holds the write lock for some busy TRUNCATEs. Each phase alone fits in its
// budget, together they exceed the old shared one: attempts that waited out a
// reader used to leave too few for the busy TRUNCATEs a steady stream of
// Records causes. The write lock is held well past vacuumTruncateResetWaitMS,
// so each of those TRUNCATEs is busy whatever the wait is.
func TestVacuumTruncate_BusyTruncateRetriesHaveTheirOwnBudget(t *testing.T) {
	s, _ := openCkptTestStore(t, time.Hour)
	oldAttempts, oldBackoff := vacuumTruncateAttempts, vacuumTruncateMaxBackoff
	oldBusy, oldPause := vacuumTruncateBusyAttempts, vacuumTruncateBusyRetryPause
	vacuumTruncateAttempts, vacuumTruncateMaxBackoff = 4, 10*time.Millisecond
	vacuumTruncateBusyAttempts, vacuumTruncateBusyRetryPause = 4, time.Millisecond
	t.Cleanup(func() {
		vacuumTruncateAttempts, vacuumTruncateMaxBackoff = oldAttempts, oldBackoff
		vacuumTruncateBusyAttempts, vacuumTruncateBusyRetryPause = oldBusy, oldPause
	})
	// Each phase uses one fewer than its own budget; together they use more
	// than either budget, which is what the old single counter allowed.
	readerHeld := vacuumTruncateAttempts - 1
	busyTruncates := vacuumTruncateBusyAttempts - 1
	if readerHeld+busyTruncates < vacuumTruncateAttempts {
		t.Fatalf("fixture: %d+%d attempts would fit in one shared budget of %d",
			readerHeld, busyTruncates, vacuumTruncateAttempts)
	}

	for range 10 {
		insertLegacyOversizedSummary(t, s, strings.Repeat("v", activitySummaryMax*8))
	}
	releaseReader := holdReaderSnapshot(t, s)
	if _, err := s.writer.Exec(`VACUUM`); err != nil {
		t.Fatalf("vacuum: %v", err)
	}

	ctx := context.Background()
	var lock *sql.Conn
	t.Cleanup(func() {
		if lock != nil {
			_, _ = lock.ExecContext(ctx, "ROLLBACK")
			_ = lock.Close()
		}
	})
	// The hook runs on truncateWALAfterVacuum's goroutine, between checkpoints,
	// so these plain counters are not shared with another goroutine.
	incomplete, busy := 0, 0
	calls := recordCheckpoints(t, s, func(c ckptCall) {
		switch {
		case c.mode == "PASSIVE" && c.res.Log >= 0 && !c.res.complete():
			incomplete++
			if incomplete == readerHeld {
				releaseReader()
				conn, err := s.reader.Conn(ctx)
				if err != nil {
					t.Errorf("lock conn: %v", err)
					return
				}
				if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
					t.Errorf("take the write lock: %v", err)
					_ = conn.Close()
					return
				}
				lock = conn
			}
		case c.mode == "TRUNCATE" && c.res.Busy != 0:
			busy++
			if busy == busyTruncates && lock != nil {
				if _, err := lock.ExecContext(ctx, "ROLLBACK"); err != nil {
					t.Errorf("release the write lock: %v", err)
				}
				_ = lock.Close()
				lock = nil
			}
		}
	})

	err := s.truncateWALAfterVacuum(ctx)
	got := calls()
	t.Logf("checkpoints %+v", got)
	if err != nil {
		t.Fatalf("truncateWALAfterVacuum: %v (%d reader-held attempts and %d busy TRUNCATEs each fit their own "+
			"budget, so it must succeed)", err, readerHeld, busyTruncates)
	}
	if incomplete != readerHeld || busy != busyTruncates {
		t.Fatalf("fixture: %d reader-held PASSIVEs and %d busy TRUNCATEs, want %d and %d: %+v",
			incomplete, busy, readerHeld, busyTruncates, got)
	}
	requireTruncateOnlyAfterCompletePassive(t, got)
	if fi, err := os.Stat(s.path + "-wal"); err == nil && fi.Size() != 0 {
		t.Fatalf("truncate reported success but the -wal still holds %d bytes", fi.Size())
	}
}

// TestVacuumTruncate_GiveUpNamesTheReason: when the busy-TRUNCATE budget runs
// out, the error must say so rather than blame a reader with counts that look
// complete (busy=1, wal_frames == checkpointed).
func TestVacuumTruncate_GiveUpNamesTheReason(t *testing.T) {
	s, _ := openCkptTestStore(t, time.Hour)
	oldBusy, oldPause := vacuumTruncateBusyAttempts, vacuumTruncateBusyRetryPause
	vacuumTruncateBusyAttempts, vacuumTruncateBusyRetryPause = 2, time.Millisecond
	t.Cleanup(func() { vacuumTruncateBusyAttempts, vacuumTruncateBusyRetryPause = oldBusy, oldPause })

	for range 5 {
		insertLegacyOversizedSummary(t, s, strings.Repeat("v", activitySummaryMax*8))
	}
	ctx := context.Background()
	lock, err := s.reader.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = lock.ExecContext(ctx, "ROLLBACK"); _ = lock.Close() })
	if _, err := lock.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}

	err = s.truncateWALAfterVacuum(ctx)
	if err == nil {
		t.Fatal("truncateWALAfterVacuum succeeded while another connection held the write lock")
	}
	if !strings.Contains(err.Error(), string(walReasonBusyTruncate)) ||
		strings.Contains(err.Error(), string(walReasonReaderHeld)) {
		t.Fatalf("error %q should name %q and not %q", err, walReasonBusyTruncate, walReasonReaderHeld)
	}
}

// TestPassiveNewFrames pins the frame arithmetic, including both restart
// signals: an explicit restart (the WAL header generation changed) and a Log
// below the previous Checkpointed.
func TestPassiveNewFrames(t *testing.T) {
	for _, tc := range []struct {
		name            string
		prev, log, ckpt int
		restarted       bool
		want            int
	}{
		{"first PASSIVE counts the whole WAL", 0, 900, 900, false, 900},
		{"same generation counts only the growth", 900, 1000, 1000, false, 100},
		{"no growth", 1000, 1000, 1000, false, 0},
		{"Log below previous is a restart", 1000, 40, 40, false, 40},
		{"a seen restart counts every frame even when Log >= previous", 1000, 1200, 1200, true, 1200},
		{"a seen restart with a small Log", 1000, 40, 40, true, 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := passiveNewFrames(tc.prev, walCheckpointResult{Log: tc.log, Checkpointed: tc.ckpt}, tc.restarted)
			if got != tc.want {
				t.Fatalf("passiveNewFrames(%d, log=%d, restarted=%v) = %d, want %d",
					tc.prev, tc.log, tc.restarted, got, tc.want)
			}
		})
	}
}

// TestClassifyPassive pins how each PASSIVE row is read, in particular that
// (0,-1,-1), the row for a database not in WAL mode, ends the truncate as
// done instead of being retried for the whole budget and reported as a failure,
// and that (1,-1,-1) is a busy checkpoint lock, not a reader.
func TestClassifyPassive(t *testing.T) {
	small, big := vacuumTruncateMaxLeftoverFrames, vacuumTruncateMaxLeftoverFrames+1
	for _, tc := range []struct {
		name       string
		res        walCheckpointResult
		newFrames  int
		wantDone   bool
		wantConv   bool
		wantNotWAL bool
		wantReason walNotResetReason
	}{
		{"not in WAL mode", walCheckpointResult{Busy: 0, Log: -1, Checkpointed: -1}, -1, true, false, true, ""},
		{"checkpoint lock busy", walCheckpointResult{Busy: 1, Log: -1, Checkpointed: -1}, -1, true, false, false,
			walReasonCheckpointBusy},
		{"reader held", walCheckpointResult{Log: 900, Checkpointed: 300}, 900, true, false, false, walReasonReaderHeld},
		{"complete and short", walCheckpointResult{Log: 900, Checkpointed: 900}, small, true, true, false, ""},
		{"complete but long: another round", walCheckpointResult{Log: 900, Checkpointed: 900}, big, false, false,
			false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, done := classifyPassive(tc.res, tc.newFrames)
			if done != tc.wantDone || out.converged != tc.wantConv || out.notWAL != tc.wantNotWAL ||
				out.reason != tc.wantReason {
				t.Fatalf("classifyPassive(%+v, %d) = %+v done=%v; want done=%v converged=%v notWAL=%v reason=%q",
					tc.res, tc.newFrames, out, done, tc.wantDone, tc.wantConv, tc.wantNotWAL, tc.wantReason)
			}
		})
	}
}

// TestPassiveUntilShort_SeesARestartLogCannotShow is the N1 regression test.
// After a complete PASSIVE of P frames the next commit restarts the WAL from
// frame 1. If the restarted WAL then grows to Log' >= P frames, Log' - P is
// all the old arithmetic could see, and it undercounts: every one of the Log'
// frames is new. The WAL header's generation exposes the restart, so the
// PASSIVE over Log' frames must not count as short.
func TestPassiveUntilShort_SeesARestartLogCannotShow(t *testing.T) {
	s, _ := openCkptTestStore(t, time.Hour)
	oldMax := vacuumTruncateMaxLeftoverFrames
	t.Cleanup(func() { vacuumTruncateMaxLeftoverFrames = oldMax })
	ctx := context.Background()
	walPath := s.path + "-wal"

	// Phase 1: a small WAL, fully copied. Converges at once (bound lifted).
	insertLegacyOversizedSummary(t, s, strings.Repeat("a", activitySummaryMax*2))
	vacuumTruncateMaxLeftoverFrames = 1 << 30
	tr := passiveTracker{walPath: walPath}
	first, err := s.passiveUntilShort(ctx, &tr)
	if err != nil || !first.converged {
		t.Fatalf("phase 1: %+v, %v; want converged", first, err)
	}
	p := first.res.Checkpointed
	genBefore, ok := readWALGeneration(walPath)
	if !ok {
		t.Fatal("fixture: could not read the WAL header")
	}

	// Phase 2: the first commit restarts the WAL (every frame is copied and
	// no reader is on it); write enough that the restarted WAL outgrows P.
	for range 20 {
		insertLegacyOversizedSummary(t, s, strings.Repeat("b", activitySummaryMax*8))
	}
	if genAfter, ok := readWALGeneration(walPath); !ok || genAfter == genBefore {
		t.Fatalf("fixture: the WAL did not restart (generation %+v -> %+v)", genBefore, genAfter)
	}
	logNow := walFrames(t, s)
	if logNow <= p {
		t.Fatalf("fixture: restarted WAL has %d frames, not more than the %d before the restart", logNow, p)
	}
	// Place the bound where the two arithmetics disagree: Log'-P is within it,
	// Log' is not.
	vacuumTruncateMaxLeftoverFrames = logNow - 1
	if logNow-p > vacuumTruncateMaxLeftoverFrames {
		t.Fatalf("fixture: Log'-P = %d is already over the bound", logNow-p)
	}
	calls := recordCheckpoints(t, s, nil)
	second, err := s.passiveUntilShort(ctx, &tr)
	if err != nil || !second.converged {
		t.Fatalf("phase 2: %+v, %v; want converged", second, err)
	}
	got := calls()
	// The PASSIVE over the restarted WAL had Log' new frames, over the bound,
	// so it must be followed by another round (which then has nothing new).
	if len(got) != 2 {
		t.Fatalf("PASSIVE rounds after the restart = %d, want 2 (the first copied %d new frames, over the bound %d, "+
			"and must not count as short): %+v", len(got), logNow, vacuumTruncateMaxLeftoverFrames, got)
	}
}

// freedPagesStore builds a store whose activity table holds freed pages: oversized
// rows are written and then shrunk, which leaves their overflow pages on the
// freelist for a VACUUM to return.
func freedPagesStore(t *testing.T) *SQLActivityStore {
	t.Helper()
	return freedPagesStoreWithInterval(t, 0)
}

// freedPagesStoreWithInterval is freedPagesStore with an explicit checkpoint
// interval; zero means the production default (newTestSQLStore).
func freedPagesStoreWithInterval(t *testing.T, interval time.Duration) *SQLActivityStore {
	t.Helper()
	var s *SQLActivityStore
	if interval > 0 {
		var err error
		s, err = openSQLiteActivityStore(filepath.Join(t.TempDir(), "activity.sqlite"), interval)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		s.SetFreeBytesFunc(func(string) (uint64, error) { return 1 << 50, nil })
	} else {
		s = newTestSQLStore(t)
	}
	for range 60 {
		insertLegacyOversizedSummary(t, s, strings.Repeat("v", activitySummaryMax*8))
	}
	shrinkLegacyRows(t, s)
	return s
}

// TestRunActivityVacuum_PreviewMeasuresAndWritesNothing: a dry run reports the
// freelist and the reclaimable estimate, leaves After zero, and changes neither
// the main file nor the -wal by a single byte.
func TestRunActivityVacuum_PreviewMeasuresAndWritesNothing(t *testing.T) {
	// A checkpoint interval far longer than the test: the background
	// checkpointer would otherwise be free to checkpoint between the byte
	// snapshot and the comparison and flake this test.
	s := freedPagesStoreWithInterval(t, time.Hour)
	ctx := context.Background()

	mainBefore, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	walBefore, _ := os.ReadFile(s.path + "-wal")

	rep, err := RunActivityVacuum(ctx, s, true)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !rep.Supported || !rep.DryRun {
		t.Fatalf("report = %+v, want Supported and DryRun", rep)
	}
	if rep.Before.FreelistCount == 0 || rep.Before.PageCount == 0 || rep.Before.PageSize == 0 {
		t.Errorf("preview did not see the freed pages: %+v", rep.Before)
	}
	if want := rep.Before.FreelistCount * rep.Before.PageSize; rep.Before.ReclaimableBytes != want {
		t.Errorf("ReclaimableBytes = %d, want freelist*page_size = %d", rep.Before.ReclaimableBytes, want)
	}
	if rep.After != (ActivityVacuumStats{}) || rep.BytesReclaimed != 0 || rep.Duration != 0 {
		t.Errorf("a preview must not report a run: %+v", rep)
	}

	mainAfter, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	walAfter, _ := os.ReadFile(s.path + "-wal")
	if string(mainAfter) != string(mainBefore) || string(walAfter) != string(walBefore) {
		t.Error("preview changed the database or its WAL")
	}
	after, err := s.VacuumStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.FreelistCount != rep.Before.FreelistCount {
		t.Errorf("freelist moved during a preview: %d -> %d", rep.Before.FreelistCount, after.FreelistCount)
	}
}

// TestRunActivityVacuum_LiveShrinksADatabaseWithFreedPages: a live run empties
// the freelist and the database plus its WAL is smaller on disk afterwards.
func TestRunActivityVacuum_LiveShrinksADatabaseWithFreedPages(t *testing.T) {
	s := freedPagesStore(t)

	rep, err := RunActivityVacuum(context.Background(), s, false)
	if err != nil {
		t.Fatalf("live: %v", err)
	}
	if rep.DryRun || !rep.Supported {
		t.Fatalf("report = %+v, want a live supported run", rep)
	}
	if rep.Before.FreelistCount == 0 {
		t.Fatal("fixture built no free pages; the test would prove nothing")
	}
	if rep.After.FreelistCount != 0 {
		t.Errorf("freelist after vacuum = %d, want 0", rep.After.FreelistCount)
	}
	if rep.BytesReclaimed <= 0 || rep.After.DiskBytes() >= rep.Before.DiskBytes() {
		t.Errorf("vacuum freed nothing: before %d bytes, after %d, reclaimed %d",
			rep.Before.DiskBytes(), rep.After.DiskBytes(), rep.BytesReclaimed)
	}
	if rep.Duration <= 0 {
		t.Errorf("Duration = %v, want a measured run", rep.Duration)
	}
}

// TestRunActivityVacuum_NoSQLiteSideIsUnsupportedNotAnError: a backend with no
// SQLite database reports Supported=false and touches nothing.
func TestRunActivityVacuum_NoSQLiteSideIsUnsupportedNotAnError(t *testing.T) {
	rep, err := RunActivityVacuum(context.Background(), nil, false)
	if err != nil || rep.Supported {
		t.Fatalf("got %+v, %v; want unsupported and no error", rep, err)
	}
}

// TestVacuumActivity_TempCopyStaysOffTMPDIR is the 2026-09-09 outage test: with
// TMPDIR pointing at a directory (the root pool on prod) and the pin cleared, a
// live run must re-pin SQLite's temp directory to the database's directory, so
// no etilqs_* file ever appears in TMPDIR.
//
// The check is the read-back of temp_store_directory. A poll for etilqs_* files
// would prove nothing: SQLite unlinks its temp files as soon as they are opened,
// so a directory listing cannot see them.
func TestVacuumActivity_TempCopyStaysOffTMPDIR(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	s := freedPagesStore(t)
	dbDir := filepath.Dir(s.path)
	// Clear the pin open applied, so VacuumActivity has to apply its own.
	if _, err := s.writer.Exec(`PRAGMA temp_store_directory=''`); err != nil {
		t.Fatal(err)
	}

	if _, err := RunActivityVacuum(context.Background(), s, false); err != nil {
		t.Fatalf("live: %v", err)
	}
	var pinned string
	if err := s.writer.QueryRow(`PRAGMA temp_store_directory`).Scan(&pinned); err != nil {
		t.Fatal(err)
	}
	if pinned != dbDir {
		t.Errorf("temp_store_directory = %q, want the database directory %q", pinned, dbDir)
	}
}

// TestOpenPinsTempDirToTheDatabaseDirectory: the pin is applied at open, before
// the schema and covering-index builds that sort the whole table.
func TestOpenPinsTempDirToTheDatabaseDirectory(t *testing.T) {
	s := newTestSQLStore(t)
	var pinned string
	if err := s.writer.QueryRow(`PRAGMA temp_store_directory`).Scan(&pinned); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Dir(s.path); pinned != want {
		t.Errorf("temp_store_directory after open = %q, want %q", pinned, want)
	}
}

// TestVacuumActivity_RefusesWithoutFreeSpace: required > free refuses the live
// run before VACUUM touches anything; the preview reports the same verdict.
func TestVacuumActivity_RefusesWithoutFreeSpace(t *testing.T) {
	s := freedPagesStore(t)
	ctx := context.Background()
	base, err := s.VacuumStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if base.RequiredBytes != vacuumRequiredBytes(base) || base.RequiredBytes < vacuumMarginFloor {
		t.Fatalf("RequiredBytes = %d, want vacuumRequiredBytes (>= the 1 GiB floor)", base.RequiredBytes)
	}

	s.SetFreeBytesFunc(func(string) (uint64, error) { return uint64(base.RequiredBytes) - 1, nil })
	prev, err := RunActivityVacuum(ctx, s, true)
	if err != nil {
		t.Fatalf("a preview that would refuse must still succeed: %v", err)
	}
	if !prev.Before.WouldRefuse || prev.Before.FreeBytes != base.RequiredBytes-1 || prev.Before.RequiredBytes != base.RequiredBytes {
		t.Errorf("preview preflight = %+v", prev.Before)
	}

	rep, err := RunActivityVacuum(ctx, s, false)
	if err == nil || !strings.Contains(err.Error(), "not enough disk space") {
		t.Fatalf("live run error = %v, want a disk-space refusal", err)
	}
	if rep.Before.FreelistCount == 0 {
		t.Fatal("fixture built no free pages; the test would prove nothing")
	}
	after, _ := s.VacuumStats(ctx)
	if after.FreelistCount != rep.Before.FreelistCount {
		t.Errorf("a refused run vacuumed anyway: freelist %d -> %d", rep.Before.FreelistCount, after.FreelistCount)
	}

	// Exactly the required amount is enough.
	s.SetFreeBytesFunc(func(string) (uint64, error) { return uint64(base.RequiredBytes), nil })
	if _, err := RunActivityVacuum(ctx, s, false); err != nil {
		t.Fatalf("free == required must proceed: %v", err)
	}
}

// TestVacuumActivity_RefusesWhenFreeSpaceIsUnreadable: an unreadable figure is not
// proof of enough space.
func TestVacuumActivity_RefusesWhenFreeSpaceIsUnreadable(t *testing.T) {
	s := freedPagesStore(t)
	s.SetFreeBytesFunc(func(string) (uint64, error) { return 0, fmt.Errorf("statfs: boom") })
	rep, err := RunActivityVacuum(context.Background(), s, false)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error = %v, want the free-space failure named", err)
	}
	if !rep.Before.WouldRefuse || rep.Before.PreflightNote == "" {
		t.Errorf("preflight = %+v, want WouldRefuse with a note", rep.Before)
	}
}

func TestVacuumRequiredBytes(t *testing.T) {
	const gib = int64(1) << 30
	for _, tc := range []struct {
		name string
		st   ActivityVacuumStats
		want int64
	}{
		{"tiny db gets the 1 GiB floor", ActivityVacuumStats{PageSize: 4096, PageCount: 2, FreelistCount: 1}, 4096*2 + gib},
		{"large db gets 10 percent", ActivityVacuumStats{PageSize: 4096, PageCount: 30 * gib / 4096, FreelistCount: 10 * gib / 4096}, 40*gib + 4*gib},
		{"freelist above page count clamps to zero", ActivityVacuumStats{PageSize: 4096, PageCount: 1, FreelistCount: 2}, gib},
		// WAL mode: pages not yet checkpointed live in the -wal, so the main
		// file can be far smaller than page_count * page_size. The file size
		// must not feed the estimate (it once read zero here).
		{"pages in the WAL are counted", ActivityVacuumStats{FileBytes: 4096, PageSize: 4096, PageCount: 10 * gib / 4096, FreelistCount: 0}, 20*gib + 2*gib},
	} {
		if got := vacuumRequiredBytes(tc.st); got != tc.want {
			t.Errorf("%s: required = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestRunActivityVacuum_ThroughMigratingStoreInEitherOrder: the SQLite store is
// found whichever side of the migrating wrapper holds it.
func TestRunActivityVacuum_ThroughMigratingStoreInEitherOrder(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sqlPrimary bool
	}{{"pebble primary, sql secondary", false}, {"sql primary, pebble secondary", true}} {
		t.Run(tc.name, func(t *testing.T) {
			sqlStore := freedPagesStore(t)
			pebbleStore := newTestPebbleActivityStore(t)
			var mig *MigratingActivityStore
			if tc.sqlPrimary {
				mig = NewMigratingActivityStore(sqlStore, pebbleStore, false)
			} else {
				mig = NewMigratingActivityStore(pebbleStore, sqlStore, false)
			}
			prev, err := RunActivityVacuum(context.Background(), mig, true)
			if err != nil || !prev.Supported || prev.Before.FreelistCount == 0 {
				t.Fatalf("preview = %+v, %v; want the SQLite store's freelist", prev, err)
			}
			live, err := RunActivityVacuum(context.Background(), mig, false)
			if err != nil || !live.Supported || live.After.FreelistCount != 0 {
				t.Fatalf("live = %+v, %v; want the SQLite store vacuumed", live, err)
			}
		})
	}
}

// TestRunActivityVacuum_PebbleOnlyIsUnsupported: a real Pebble-only store, bare
// and behind a migrating wrapper with no SQLite side, has nothing to vacuum.
func TestRunActivityVacuum_PebbleOnlyIsUnsupported(t *testing.T) {
	p := newTestPebbleActivityStore(t)
	for name, store := range map[string]ActivityStorer{
		"bare":    p,
		"wrapped": NewMigratingActivityStore(p, nil, false),
	} {
		rep, err := RunActivityVacuum(context.Background(), store, false)
		if err != nil || rep.Supported {
			t.Errorf("%s: got %+v, %v; want unsupported and no error", name, rep, err)
		}
	}
}

// TestVacuumActivity_RefusesWhenTheTempDirPinFails: the pin is fatal for a VACUUM,
// unlike at open and in ANALYZE where it only warns.
func TestVacuumActivity_RefusesWhenTheTempDirPinFails(t *testing.T) {
	s := freedPagesStore(t)
	if err := s.writer.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := s.VacuumActivity(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cannot keep its temp copy off TMPDIR") {
		t.Fatalf("error = %v, want the pin failure to refuse the vacuum", err)
	}
}

// TestVacuumActivity_NoFreeSpaceProbeFailsClosed: internal/database does not import
// diskstats (SDK closure), so a store nobody wired a probe into must refuse.
func TestVacuumActivity_NoFreeSpaceProbeFailsClosed(t *testing.T) {
	s := freedPagesStore(t)
	s.SetFreeBytesFunc(nil)
	prev, err := RunActivityVacuum(context.Background(), s, true)
	if err != nil || !prev.Before.WouldRefuse || !strings.Contains(prev.Before.PreflightNote, "free space unknown") {
		t.Fatalf("preview = %+v, %v; want would_refuse with 'free space unknown'", prev.Before, err)
	}
	rep, err := RunActivityVacuum(context.Background(), s, false)
	if err == nil || !strings.Contains(err.Error(), "free space unknown") {
		t.Fatalf("live error = %v, want a free-space-unknown refusal", err)
	}
	if after, _ := s.VacuumStats(context.Background()); after.FreelistCount != rep.Before.FreelistCount {
		t.Error("a refused run vacuumed anyway")
	}
}
