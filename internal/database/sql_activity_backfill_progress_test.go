// file: internal/database/sql_activity_backfill_progress_test.go
// version: 1.0.0
// guid: 8e4b1f07-6c92-4d35-a1e8-5b0d3f7a9c26
// last-edited: 2026-09-07

package database

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// captureLogs redirects the default slog logger into a slice for the duration of
// the test and returns an accessor. The backfill logs through the package-level
// slog functions, so capturing means swapping the default logger; the test must
// therefore not run in parallel with anything else that reads it.
func captureLogs(t *testing.T) func() []slog.Record {
	t.Helper()
	var mu sync.Mutex
	var recs []slog.Record

	prev := slog.Default()
	slog.SetDefault(slog.New(&capturingHandler{mu: &mu, recs: &recs}))
	t.Cleanup(func() { slog.SetDefault(prev) })

	return func() []slog.Record {
		mu.Lock()
		defer mu.Unlock()
		return append([]slog.Record(nil), recs...)
	}
}

type capturingHandler struct {
	mu   *sync.Mutex
	recs *[]slog.Record
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.recs = append(*h.recs, r.Clone())
	return nil
}
func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler      { return h }

// attrsOf flattens a record's attributes into a map for assertions.
func attrsOf(r slog.Record) map[string]any {
	out := make(map[string]any, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		out[a.Key] = a.Value.Any()
		return true
	})
	return out
}

// recordsWithMsg returns every captured record whose message matches.
func recordsWithMsg(recs []slog.Record, msg string) []slog.Record {
	var out []slog.Record
	for _, r := range recs {
		if r.Message == msg {
			out = append(out, r)
		}
	}
	return out
}

// seedTiers writes n distinct entries into each named tier of a Pebble activity
// store and returns the store.
func seedTiers(t *testing.T, s *PebbleActivityStore, perTier map[string]int) {
	t.Helper()
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	var entries []ActivityEntry
	i := 0
	for tier, n := range perTier {
		for range n {
			entries = append(entries, ActivityEntry{
				Timestamp: base.Add(time.Duration(i) * time.Second),
				Tier:      tier, Type: "t", Level: "info",
				Source:  "seed",
				Summary: tier + "-" + time.Duration(i).String(),
			})
			i++
		}
	}
	if n, err := s.RecordBatch(entries); err != nil || n != len(entries) {
		t.Fatalf("seed pebble: wrote %d/%d err=%v", n, len(entries), err)
	}
}

// TestSQLActivityBackfill_TierCompleteLogIsEmittedPerTier is the regression guard
// for the defect this change fixes: BackfillPebbleActivityToSQL used to log only
// "processing tier" per tier and a single final summary, so on production a run
// that had been working for 3+ hours on one tier was indistinguishable from a
// wedged one. Every tier must now emit a terminal line carrying its own counts.
func TestSQLActivityBackfill_TierCompleteLogIsEmittedPerTier(t *testing.T) {
	pebbleStore := newTestPebbleActivityStore(t)
	sqlStore := newTestSQLStore(t)
	seedTiers(t, pebbleStore, map[string]int{"change": 7, "info": 3})

	logs := captureLogs(t)
	res, err := BackfillPebbleActivityToSQL(context.Background(), pebbleStore, sqlStore, false)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if !res.ParityOK {
		t.Fatalf("parity failed: %+v", res)
	}

	done := recordsWithMsg(logs(), "[activity-sql-backfill] tier complete")
	if len(done) != len(actTiers) {
		t.Fatalf("tier-complete lines = %d, want %d (one per tier)", len(done), len(actTiers))
	}

	// The seeded tiers must report their real counts, not zeros.
	got := map[string]int{}
	for _, r := range done {
		a := attrsOf(r)
		tier, _ := a["tier"].(string)
		scanned, ok := a["scanned"].(int64)
		if !ok {
			t.Fatalf("tier %q: 'scanned' attr missing or not an int, attrs=%v", tier, a)
		}
		if _, ok := a["elapsed"].(string); !ok {
			t.Errorf("tier %q: 'elapsed' attr missing — a duration is the whole point of the line", tier)
		}
		got[tier] = int(scanned)
	}
	if got["change"] != 7 {
		t.Errorf("tier-complete scanned for change = %d, want 7", got["change"])
	}
	if got["info"] != 3 {
		t.Errorf("tier-complete scanned for info = %d, want 3", got["info"])
	}
}

// TestSQLActivityBackfill_PerTierCopiedSeparatesFreshFromResumed proves the
// discriminator PerTierCopied exists for. A resumed run re-streams every already
// copied row and inserts ~zero — from scanned counts alone that is identical to a
// fresh run, which is exactly the ambiguity that made production progress
// unreadable. copied must fall to zero on the second pass while scanned does not.
func TestSQLActivityBackfill_PerTierCopiedSeparatesFreshFromResumed(t *testing.T) {
	pebbleStore := newTestPebbleActivityStore(t)
	sqlStore := newTestSQLStore(t)
	seedTiers(t, pebbleStore, map[string]int{"change": 5, "audit": 2})

	first, err := BackfillPebbleActivityToSQL(context.Background(), pebbleStore, sqlStore, false)
	if err != nil {
		t.Fatalf("first backfill: %v", err)
	}
	if first.PerTierCopied["change"] != 5 || first.PerTierCopied["audit"] != 2 {
		t.Fatalf("first run PerTierCopied = %v, want change=5 audit=2", first.PerTierCopied)
	}
	if first.PerTierScanned["change"] != 5 || first.PerTierScanned["audit"] != 2 {
		t.Fatalf("first run PerTierScanned = %v, want change=5 audit=2", first.PerTierScanned)
	}

	// Simulate a crashed run: the sentinel is written only on success, so a
	// process killed mid-backfill leaves it absent and the next start re-streams
	// everything. Deleting it reproduces that state exactly.
	if err := pebbleStore.db.Delete([]byte(ActivitySQLBackfillKey), pebble.Sync); err != nil {
		t.Fatalf("clear sentinel: %v", err)
	}

	second, err := BackfillPebbleActivityToSQL(context.Background(), pebbleStore, sqlStore, false)
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if second.AlreadyDone {
		t.Fatal("second run short-circuited; the sentinel delete did not take")
	}
	if second.PerTierScanned["change"] != 5 || second.PerTierScanned["audit"] != 2 {
		t.Errorf("resumed run PerTierScanned = %v, want unchanged change=5 audit=2", second.PerTierScanned)
	}
	if second.PerTierCopied["change"] != 0 || second.PerTierCopied["audit"] != 0 {
		t.Errorf("resumed run PerTierCopied = %v, want all zero (every row already present)", second.PerTierCopied)
	}
	if !second.ParityOK {
		t.Errorf("resumed run parity failed: %+v", second)
	}
}

// TestSQLActivityBackfill_DryRunAlsoReportsProgress guards the asymmetry the old
// code had: "processing tier" was suppressed under dryRun, so a dry run over
// millions of rows was silent for its whole duration — the same defect as the
// real run, in the mode used precisely to estimate how long the real run takes.
func TestSQLActivityBackfill_DryRunAlsoReportsProgress(t *testing.T) {
	pebbleStore := newTestPebbleActivityStore(t)
	sqlStore := newTestSQLStore(t)
	seedTiers(t, pebbleStore, map[string]int{"change": 4})

	logs := captureLogs(t)
	res, err := BackfillPebbleActivityToSQL(context.Background(), pebbleStore, sqlStore, true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !res.DryRun {
		t.Fatal("result does not report DryRun")
	}
	if res.EntriesCopied != 0 {
		t.Errorf("dry run copied %d rows, want 0", res.EntriesCopied)
	}

	done := recordsWithMsg(logs(), "[activity-sql-backfill] tier complete")
	if len(done) != len(actTiers) {
		t.Fatalf("dry-run tier-complete lines = %d, want %d", len(done), len(actTiers))
	}
	for _, r := range done {
		a := attrsOf(r)
		if dry, _ := a["dry_run"].(bool); !dry {
			t.Errorf("tier %v: dry_run attr = %v, want true — a reader must not mistake a dry run for a real copy", a["tier"], a["dry_run"])
		}
	}
}
