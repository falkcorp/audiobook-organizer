// file: internal/database/sql_activity_summary_clamp_test.go
// version: 1.3.0
// guid: 8b47e0c9-2f13-45da-9e60-c4a1d5382bf7
// last-edited: 2026-10-06

package database

import (
	"context"
	"database/sql"
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
func TestVacuumActivity_ReaderReleasedMidTruncateNeverTruncatesUnderTheCopy(t *testing.T) {
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

// TestVacuumActivity_LateReaderDoesNotStallWritersThroughTheTruncate is the
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
func TestVacuumActivity_LateReaderDoesNotStallWritersThroughTheTruncate(t *testing.T) {
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

// TestVacuumActivity_BusyTruncateRetriesHaveTheirOwnBudget is the deterministic
// form of the SF1 flake (TestVacuumActivity_ReaderReleasedMidTruncate failing
// "WAL not reset after 8 attempts (last checkpoint busy=1 wal_frames=2153
// checkpointed=2153)" in 15 of 135 loaded runs). A reader that predates the
// VACUUM holds PASSIVE short for some attempts; after it lets go, a writer
// holds the write lock for some busy TRUNCATEs. Each phase alone fits in its
// budget, together they exceed the old shared one: attempts that waited out a
// reader used to leave too few for the busy TRUNCATEs a steady stream of
// Records causes. The write lock is held well past vacuumTruncateResetWaitMS,
// so each of those TRUNCATEs is busy whatever the wait is.
func TestVacuumActivity_BusyTruncateRetriesHaveTheirOwnBudget(t *testing.T) {
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

// TestVacuumActivity_GiveUpNamesTheReason: when the busy-TRUNCATE budget runs
// out, the error must say so rather than blame a reader with counts that look
// complete (busy=1, wal_frames == checkpointed).
func TestVacuumActivity_GiveUpNamesTheReason(t *testing.T) {
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
