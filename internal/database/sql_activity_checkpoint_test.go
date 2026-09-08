// file: internal/database/sql_activity_checkpoint_test.go
// version: 1.0.0
// guid: a9f127ce-e5b7-4da2-b2ee-f2761a0d087d
// last-edited: 2026-09-08

// Tests for the Pebble→SQLite backfill's DURABLE checkpoint.
//
// The headline test here is
// TestSQLActivityBackfill_AnInterruptedParityFailureSurvivesAResumeWithACleanTail.
// It guards the bug that adding a resume cursor would otherwise have introduced:
// the parity verdict lived only in memory, so a run that failed parity, died, and
// resumed would re-derive "clean" from its untainted tail and flip production
// reads onto an unverified copy. See sql_activity_progress.go's package comment.

package database

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

// tierKeys returns every primary key in a tier, in iteration order, by streaming
// it one row at a time so each batch's lastKey names exactly one row.
func tierKeys(t *testing.T, s *PebbleActivityStore, tier string) []string {
	t.Helper()
	var keys []string
	_, err := s.streamTierEntries(context.Background(), tier, 1, nil,
		func(_ []ActivityEntry, lastKey []byte) error {
			keys = append(keys, string(lastKey))
			return nil
		})
	if err != nil {
		t.Fatalf("stream tier %q: %v", tier, err)
	}
	return keys
}

// rawProgress reads the progress blob WITHOUT the normalisation that
// loadActivityBackfillProgress applies. Tests that assert a tier was recorded as
// `failed` must use this: the loader deliberately rewrites `failed` to a full
// re-scan, so going through it would hide the very state under test.
func rawProgress(t *testing.T, db *pebble.DB) ActivityBackfillProgress {
	t.Helper()
	val, closer, err := db.Get([]byte(ActivitySQLBackfillProgressKey))
	if err != nil {
		t.Fatalf("progress blob absent: %v", err)
	}
	defer closer.Close()
	var p ActivityBackfillProgress
	if err := json.Unmarshal(val, &p); err != nil {
		t.Fatalf("progress blob unmarshal: %v", err)
	}
	return p
}

func TestStreamTierEntries_StartAfterResumesStrictlyAfterTheCursor(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	seedTiers(t, s, map[string]int{"change": 10})

	keys := tierKeys(t, s, "change")
	if len(keys) != 10 {
		t.Fatalf("seeded keys = %d, want 10", len(keys))
	}

	// Resume after the 4th row: rows 5..10 must come back, and the cursor row
	// itself must NOT be re-emitted (the bound is exclusive).
	cursor := keys[3]
	var got []string
	n, err := s.streamTierEntries(context.Background(), "change", 2, []byte(cursor),
		func(batch []ActivityEntry, _ []byte) error {
			for range batch {
				got = append(got, "")
			}
			return nil
		})
	if err != nil {
		t.Fatalf("resumed stream: %v", err)
	}
	if n != 6 || len(got) != 6 {
		t.Fatalf("resumed stream returned %d rows (callback saw %d), want 6", n, len(got))
	}

	resumedKeys := func() []string {
		var ks []string
		_, err := s.streamTierEntries(context.Background(), "change", 1, []byte(cursor),
			func(_ []ActivityEntry, lastKey []byte) error {
				ks = append(ks, string(lastKey))
				return nil
			})
		if err != nil {
			t.Fatalf("resumed key stream: %v", err)
		}
		return ks
	}()
	for _, k := range resumedKeys {
		if k <= cursor {
			t.Fatalf("resumed stream emitted key %q at or before the cursor %q", k, cursor)
		}
	}
	if resumedKeys[0] != keys[4] {
		t.Errorf("first resumed key = %q, want %q (the row after the cursor)", resumedKeys[0], keys[4])
	}
}

// A cursor that does not belong to the tier must be IGNORED, never obeyed.
//
// This is the sharpest edge in the whole design. An out-of-range cursor makes the
// iterator yield zero rows; the tier would then finish with reinserted == 0 and be
// recorded CLEAN having verified nothing — a corrupt cursor fabricating the exact
// verdict that flips production reads onto SQLite.
func TestResumeCursorFor_RejectsACursorFromAnotherTier(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	seedTiers(t, s, map[string]int{"change": 4, "info": 4})

	infoKey := tierKeys(t, s, "info")[2]
	if got := resumeCursorFor("change", infoKey); got != nil {
		t.Fatalf("resumeCursorFor accepted a foreign cursor %q -> %q, want nil (restart the tier)", infoKey, got)
	}
	if got := resumeCursorFor("change", "total-garbage"); got != nil {
		t.Fatalf("resumeCursorFor accepted garbage -> %q, want nil", got)
	}
	// An own-tier cursor is still accepted, so the rejection above is a real
	// discriminator and not a function that always returns nil.
	changeKey := tierKeys(t, s, "change")[1]
	if got := resumeCursorFor("change", changeKey); string(got) != changeKey {
		t.Fatalf("resumeCursorFor rejected its own tier's cursor %q -> %q", changeKey, got)
	}

	// And the store itself refuses an out-of-range bound rather than scanning
	// nothing: belt and braces behind resumeCursorFor.
	n, err := s.streamTierEntries(context.Background(), "change", 10, []byte(infoKey),
		func(_ []ActivityEntry, _ []byte) error { return nil })
	if err != nil {
		t.Fatalf("stream with foreign cursor: %v", err)
	}
	if n != 4 {
		t.Errorf("stream with a foreign cursor scanned %d rows, want all 4 — an out-of-range "+
			"bound must never be silently honoured into an empty scan", n)
	}
}

func TestActivityBackfillProgress_AFailedTierReloadsAsAFullRescan(t *testing.T) {
	s := newTestPebbleActivityStore(t)

	p := &ActivityBackfillProgress{
		Version: activityProgressVersion,
		Tiers: map[string]*ActivityBackfillTierProgress{
			"change": {State: activityTierFailed, Cursor: "act:change:x", Scanned: 900, Copied: 900, Reinserted: 4},
			"info":   {State: activityTierClean, Scanned: 10, Copied: 10},
		},
	}
	if err := saveActivityBackfillProgress(s.db, p, true); err != nil {
		t.Fatalf("save: %v", err)
	}

	got := loadActivityBackfillProgress(s.db)
	ch := got.Tiers["change"]
	if ch.State != activityTierInProgress {
		t.Errorf("failed tier reloaded as %q, want %q", ch.State, activityTierInProgress)
	}
	if ch.Cursor != "" || ch.Scanned != 0 || ch.Copied != 0 || ch.Reinserted != 0 {
		t.Errorf("failed tier kept resume state %+v — it must be re-scanned in FULL, "+
			"or a clean tail could launder the earlier failure", ch)
	}
	if in := got.Tiers["info"]; in.State != activityTierClean || in.Scanned != 10 {
		t.Errorf("clean tier was disturbed: %+v", in)
	}
	if got.AllTiersClean() {
		t.Error("AllTiersClean() = true with only one tier clean")
	}
}

func TestActivityBackfillProgress_UnreadableBlobDegradesToAFullRescan(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	if err := s.db.Set([]byte(ActivitySQLBackfillProgressKey), []byte("{not json"), pebble.Sync); err != nil {
		t.Fatalf("seed corrupt blob: %v", err)
	}
	got := loadActivityBackfillProgress(s.db)
	if got == nil {
		t.Fatal("loadActivityBackfillProgress returned nil on a corrupt blob")
	}
	if len(got.Tiers) != 0 || got.AllTiersClean() {
		t.Errorf("corrupt blob produced usable progress %+v — the safe reading is "+
			"'no progress', i.e. a full re-scan and full re-verification", got)
	}
}

// THE BLOCKER TEST.
//
// Setup is the state a crash leaves behind: tier `change` is mid-flight and has
// ALREADY had 5 rows fail the parity re-presentation. The resumed run then scans a
// completely clean tail. Before the durable verdict, that run would have started
// from res.ParityOK = true, seen nothing wrong, and written the sentinel — flipping
// reads onto a copy with 5 rows missing.
//
// Carrying Reinserted across the resume is what makes it fail closed.
func TestSQLActivityBackfill_AnInterruptedParityFailureSurvivesAResumeWithACleanTail(t *testing.T) {
	pebbleStore := newTestPebbleActivityStore(t)
	sqlStore := newTestSQLStore(t)
	seedTiers(t, pebbleStore, map[string]int{"change": 6, "info": 3})

	// A previous, killed attempt recorded 5 parity failures on `change`.
	prior := &ActivityBackfillProgress{
		Version: activityProgressVersion,
		Tiers: map[string]*ActivityBackfillTierProgress{
			"change": {State: activityTierInProgress, Reinserted: 5},
		},
	}
	if err := saveActivityBackfillProgress(pebbleStore.db, prior, true); err != nil {
		t.Fatalf("seed prior progress: %v", err)
	}

	res, err := BackfillPebbleActivityToSQL(context.Background(), pebbleStore, sqlStore, false)
	if err == nil {
		t.Fatal("backfill returned nil error despite an inherited parity failure — " +
			"the resumed run laundered it into a clean verdict")
	}
	if !strings.Contains(err.Error(), "parity") {
		t.Errorf("error %q does not name the parity failure", err)
	}
	if res.ParityOK {
		t.Error("res.ParityOK = true with an inherited parity failure")
	}
	if IsActivitySQLBackfillDone(pebbleStore.db) {
		t.Fatal("SENTINEL WRITTEN despite unverified data — reads would flip to SQLite")
	}
	if got := rawProgress(t, pebbleStore.db).Tiers["change"]; got.State != activityTierFailed {
		t.Errorf("tier `change` recorded as %q, want %q", got.State, activityTierFailed)
	}
}

// The failure above must still SELF-HEAL: a genuine full re-run re-scans the tier
// from zero and, finding it clean, may complete. A permanently sticky flag would
// wedge the migration forever, which is the opposite failure.
func TestSQLActivityBackfill_AFullRerunClearsAPreviouslyFailedTier(t *testing.T) {
	pebbleStore := newTestPebbleActivityStore(t)
	sqlStore := newTestSQLStore(t)
	seedTiers(t, pebbleStore, map[string]int{"change": 6, "info": 3})

	prior := &ActivityBackfillProgress{
		Version: activityProgressVersion,
		Tiers: map[string]*ActivityBackfillTierProgress{
			"change": {State: activityTierFailed, Cursor: "act:change:stale", Scanned: 4, Copied: 4, Reinserted: 5},
		},
	}
	if err := saveActivityBackfillProgress(pebbleStore.db, prior, true); err != nil {
		t.Fatalf("seed prior progress: %v", err)
	}

	res, err := BackfillPebbleActivityToSQL(context.Background(), pebbleStore, sqlStore, false)
	if err != nil {
		t.Fatalf("full re-run should clear a failed tier, got: %v", err)
	}
	if !res.ParityOK || !IsActivitySQLBackfillDone(pebbleStore.db) {
		t.Fatalf("full re-run did not complete: parity=%v sentinel=%v", res.ParityOK, IsActivitySQLBackfillDone(pebbleStore.db))
	}
	// Re-scanned in full, not resumed from the stale cursor's row 4.
	if res.PerTierScanned["change"] != 6 {
		t.Errorf("change scanned = %d, want 6 (a failed tier is re-scanned in FULL)", res.PerTierScanned["change"])
	}
}

// The actual payoff: a tier verified by an earlier process is not read again.
// Production was re-reading ~6.5M rows after every restart.
func TestSQLActivityBackfill_ResumeSkipsTiersAlreadyVerified(t *testing.T) {
	pebbleStore := newTestPebbleActivityStore(t)
	sqlStore := newTestSQLStore(t)
	seedTiers(t, pebbleStore, map[string]int{"change": 5, "info": 2})

	prior := &ActivityBackfillProgress{
		Version: activityProgressVersion,
		Tiers: map[string]*ActivityBackfillTierProgress{
			"change": {State: activityTierClean, Scanned: 5, Copied: 5},
		},
	}
	if err := saveActivityBackfillProgress(pebbleStore.db, prior, true); err != nil {
		t.Fatalf("seed prior progress: %v", err)
	}

	logs := captureLogs(t)
	res, err := BackfillPebbleActivityToSQL(context.Background(), pebbleStore, sqlStore, false)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}

	skipped := recordsWithMsg(logs(), "[activity-sql-backfill] tier already verified by an earlier run — skipping")
	if len(skipped) != 1 {
		t.Fatalf("skip lines = %d, want exactly 1 (`change`)", len(skipped))
	}
	// The skipped tier still contributes its stored counts, so totals stay whole.
	if res.PerTierScanned["change"] != 5 || res.PerTierCopied["change"] != 5 {
		t.Errorf("skipped tier lost its counts: scanned=%d copied=%d, want 5/5",
			res.PerTierScanned["change"], res.PerTierCopied["change"])
	}
	// `change` was NOT re-read: it never emits a tier-complete line.
	for _, r := range recordsWithMsg(logs(), "[activity-sql-backfill] tier complete") {
		if tier, _ := attrsOf(r)["tier"].(string); tier == "change" {
			t.Error("tier `change` was re-scanned despite carrying a clean verdict")
		}
	}
	if !IsActivitySQLBackfillDone(pebbleStore.db) {
		t.Error("sentinel not written even though every tier ended clean")
	}
}

// Completing the migration removes the resume state: the sentinel now answers
// everything it tracked, and stale progress would only mislead a later reader.
func TestSQLActivityBackfill_ProgressBlobIsClearedOnCompletion(t *testing.T) {
	pebbleStore := newTestPebbleActivityStore(t)
	sqlStore := newTestSQLStore(t)
	seedTiers(t, pebbleStore, map[string]int{"change": 3})

	if _, err := BackfillPebbleActivityToSQL(context.Background(), pebbleStore, sqlStore, false); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if _, closer, err := pebbleStore.db.Get([]byte(ActivitySQLBackfillProgressKey)); err == nil {
		closer.Close()
		t.Error("progress blob survived a completed migration")
	}
}

// A dry run must neither consult nor mutate durable migration state: it reports
// what a real run WOULD read, so it can never skip a tier it was asked to size.
func TestSQLActivityBackfill_DryRunNeitherSkipsNorWritesProgress(t *testing.T) {
	pebbleStore := newTestPebbleActivityStore(t)
	sqlStore := newTestSQLStore(t)
	seedTiers(t, pebbleStore, map[string]int{"change": 4})

	prior := &ActivityBackfillProgress{
		Version: activityProgressVersion,
		Tiers: map[string]*ActivityBackfillTierProgress{
			"change": {State: activityTierClean, Scanned: 999, Copied: 999},
		},
	}
	if err := saveActivityBackfillProgress(pebbleStore.db, prior, true); err != nil {
		t.Fatalf("seed prior progress: %v", err)
	}

	res, err := BackfillPebbleActivityToSQL(context.Background(), pebbleStore, sqlStore, true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if res.PerTierScanned["change"] != 4 {
		t.Errorf("dry run scanned %d rows of `change`, want 4 — a dry run must not "+
			"skip tiers on the strength of a stored verdict", res.PerTierScanned["change"])
	}
	if got := rawProgress(t, pebbleStore.db).Tiers["change"]; got.Scanned != 999 {
		t.Errorf("dry run mutated durable progress: %+v", got)
	}
}
