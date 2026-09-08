// file: internal/database/sql_activity_digest_resume_test.go
// version: 1.0.0
// guid: ab22d7a8-af3b-4943-8e3c-ae5c1cb67e60
// last-edited: 2026-09-08

// Tests for the ONE tier the Pebble→SQLite backfill must never resume.
//
// The resume cursor added with the checkpoint is sound only where every writer
// either dual-writes to SQLite or appends forward in time. CompactByDay does
// neither: it keys its row at startOfDay (BACKDATED) and routes to the active
// backend only, which is Pebble for the whole migration. A digest written below
// a stored cursor would therefore be stepped over by the resume — and because
// the parity pass only ever re-presents the batch it just copied, never
// re-reading Pebble, the skip is invisible to the copy AND the verify, and the
// tier still ends `clean`.
//
// See activityNonResumableTiers in sql_activity_progress.go.

package database

import (
	"context"
	"testing"
	"time"
)

// seedTiers bases its timestamps at 2026-04-01; anything before that sorts below
// every seeded key, which is what a backdated CompactByDay digest looks like.
var backdatedBelowSeed = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

// recordPebbleOnly writes straight to the Pebble store, mirroring the maintenance
// paths that bypass MigratingActivityStore's dual-write.
func recordPebbleOnly(t *testing.T, s *PebbleActivityStore, e ActivityEntry) {
	t.Helper()
	if n, err := s.RecordBatch([]ActivityEntry{e}); err != nil || n != 1 {
		t.Fatalf("pebble-only record: wrote %d err=%v", n, err)
	}
}

// sqlSummaries returns every Summary currently in the SQLite store.
func sqlSummaries(t *testing.T, s *SQLActivityStore) []string {
	t.Helper()
	entries, _, err := s.Query(context.Background(), ActivityFilter{})
	if err != nil {
		t.Fatalf("query sql store: %v", err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Summary)
	}
	return out
}

// THE HEADLINE TEST. A digest backdated below a checkpointed cursor must still
// reach SQLite. Before activityNonResumableTiers this row was skipped silently
// and the tier was still recorded clean.
func TestSQLActivityBackfill_ABackdatedDigestBelowTheCursorIsStillCopied(t *testing.T) {
	pebbleStore := newTestPebbleActivityStore(t)
	sqlStore := newTestSQLStore(t)
	seedTiers(t, pebbleStore, map[string]int{"digest": 6})

	keys := tierKeys(t, pebbleStore, "digest")
	if len(keys) != 6 {
		t.Fatalf("seeded digest keys = %d, want 6", len(keys))
	}

	// A run that got four rows into `digest` and was then killed.
	prior := &ActivityBackfillProgress{
		Version: activityProgressVersion,
		Tiers: map[string]*ActivityBackfillTierProgress{
			"digest": {State: activityTierInProgress, Cursor: keys[3], Scanned: 4, Copied: 4},
		},
	}
	if err := saveActivityBackfillProgress(pebbleStore.db, prior, true); err != nil {
		t.Fatalf("seed prior progress: %v", err)
	}

	// While it was down, CompactByDay compacted an OLDER day: a Pebble-only row
	// whose key sorts below the stored cursor.
	const backdated = "compacted-digest-for-an-older-day"
	recordPebbleOnly(t, pebbleStore, ActivityEntry{
		Timestamp: backdatedBelowSeed,
		Tier:      "digest", Type: "daily_digest", Level: "info",
		Source: "compaction", Summary: backdated,
	})

	res, err := BackfillPebbleActivityToSQL(context.Background(), pebbleStore, sqlStore, false)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var found bool
	for _, s := range sqlSummaries(t, sqlStore) {
		if s == backdated {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("the backdated digest never reached SQLite: the resume bound stepped over it, "+
			"and the per-batch parity pass cannot see a row it never read. The run still "+
			"reported ParityOK=%v after scanning %d digest rows — a clean verdict over "+
			"unverified data is exactly what this must prevent.",
			res.ParityOK, res.PerTierScanned["digest"])
	}

	// Full re-scan, not a resume: all 7 rows, and the checkpoint's stale counters
	// were dropped rather than added to.
	if got := res.PerTierScanned["digest"]; got != 7 {
		t.Errorf("digest scanned = %d, want 7 (a full re-scan of 6 seeded + 1 backdated). "+
			"4 would mean it resumed; 11 would mean stale counters were carried into a re-scan", got)
	}
	if !res.ParityOK {
		t.Errorf("ParityOK = false, want true — the tier was fully scanned and verified")
	}
}

// The discriminator: refusing `digest` must not be a blanket refusal to resume.
// If this passed while every tier restarted from zero, the checkpoint would be
// doing nothing and the test above would still be green.
func TestResumeCursorFor_RefusesDigestButStillResumesOtherTiers(t *testing.T) {
	s := newTestPebbleActivityStore(t)
	seedTiers(t, s, map[string]int{"digest": 3, "change": 3})

	digestCursor := tierKeys(t, s, "digest")[1]
	if got := resumeCursorFor("digest", digestCursor); got != nil {
		t.Errorf("resumeCursorFor(digest, own-tier cursor) = %q, want nil — CompactByDay can "+
			"backdate a Pebble-only row below this cursor", got)
	}

	changeCursor := tierKeys(t, s, "change")[1]
	got := resumeCursorFor("change", changeCursor)
	if got == nil {
		t.Fatalf("resumeCursorFor(change, own-tier cursor) = nil, want the cursor — `change` is " +
			"the ~5.7M-row tier the whole checkpoint exists to resume")
	}
	if string(got) != changeCursor {
		t.Errorf("resumeCursorFor(change) = %q, want %q", got, changeCursor)
	}
}

// Dropping a discarded cursor's counters must NOT drop the parity failures with
// them. Reinserted is an integrity signal, not progress: an interrupted attempt
// records it with no cursor written yet, so "nothing to resume from" must not be
// read as "nothing went wrong". A forced full re-scan (the `digest` case) is the
// sharpest version — it never resumes, so it hits this path every single time.
func TestSQLActivityBackfill_AFullRescanKeepsAnInheritedParityFailure(t *testing.T) {
	pebbleStore := newTestPebbleActivityStore(t)
	sqlStore := newTestSQLStore(t)
	seedTiers(t, pebbleStore, map[string]int{"digest": 4})

	prior := &ActivityBackfillProgress{
		Version: activityProgressVersion,
		Tiers: map[string]*ActivityBackfillTierProgress{
			// Progress counters that must be dropped, alongside a parity failure
			// that must NOT be.
			"digest": {State: activityTierInProgress, Scanned: 900, Copied: 900, Reinserted: 5},
		},
	}
	if err := saveActivityBackfillProgress(pebbleStore.db, prior, true); err != nil {
		t.Fatalf("seed prior progress: %v", err)
	}

	res, err := BackfillPebbleActivityToSQL(context.Background(), pebbleStore, sqlStore, false)
	if err == nil {
		t.Fatal("backfill returned nil error: resetting the discarded cursor's counters also " +
			"cleared the inherited parity failure, laundering it into a clean verdict")
	}
	if res.ParityOK {
		t.Error("res.ParityOK = true with an inherited parity failure")
	}
	if IsActivitySQLBackfillDone(pebbleStore.db) {
		t.Fatal("SENTINEL WRITTEN despite unverified data — reads would flip to SQLite")
	}
	// The progress counters WERE dropped, even though the failure was kept.
	if got := res.PerTierScanned["digest"]; got != 4 {
		t.Errorf("digest scanned = %d, want 4 — stale progress counters survived the re-scan", got)
	}
}

// A tier that restarts from row zero must drop the counters that belonged to the
// cursor it just discarded, or every restart inflates the tier's totals.
func TestSQLActivityBackfill_AFullRescanDropsTheCheckpointCounters(t *testing.T) {
	pebbleStore := newTestPebbleActivityStore(t)
	sqlStore := newTestSQLStore(t)
	seedTiers(t, pebbleStore, map[string]int{"digest": 4})

	prior := &ActivityBackfillProgress{
		Version: activityProgressVersion,
		Tiers: map[string]*ActivityBackfillTierProgress{
			"digest": {
				State:  activityTierInProgress,
				Cursor: tierKeys(t, pebbleStore, "digest")[2],
				// Deliberately absurd: if these survive a full re-scan they will
				// show up in the totals.
				Scanned: 900, Copied: 900,
			},
		},
	}
	if err := saveActivityBackfillProgress(pebbleStore.db, prior, true); err != nil {
		t.Fatalf("seed prior progress: %v", err)
	}

	res, err := BackfillPebbleActivityToSQL(context.Background(), pebbleStore, sqlStore, false)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if got := res.PerTierScanned["digest"]; got != 4 {
		t.Errorf("digest scanned = %d, want 4 — the discarded cursor's counters were carried "+
			"into the re-scan that replaced it", got)
	}
}
