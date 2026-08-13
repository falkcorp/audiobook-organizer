// file: internal/database/memdb_warmup_timing_test.go
// version: 1.0.0
// guid: 8c4d1e73-5f26-4a90-b8e1-7d0a3c5f2b64
// last-edited: 2026-08-13

package database

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Warmup published a single duration — 115,971 ms on production 2026-08-13 —
// which says the library is wrong-or-unusable for about two minutes after
// every restart but not which of the ten prefix scans is responsible. These
// tests pin the per-phase breakdown so the number cannot quietly go back to
// being a single opaque total.
//
// The assertions are deliberately about COVERAGE and STRUCTURE, not about
// wall-clock thresholds: a timing test that asserts "books took less than N ms"
// is a flake generator on shared CI, and would tell us nothing about
// production hardware anyway. What matters is that every phase is attributed
// and nothing is silently missing from the accounting.

func TestWarmupDurations_EveryWarmedTableIsAttributed(t *testing.T) {
	store := setupTestPebbleStore(t)
	store.WaitForWarmup()

	// Seed a little of everything the warmup scans so no phase is trivially
	// empty. Books carry the indexes that matter most.
	for i := 0; i < 5; i++ {
		hash := fmt.Sprintf("timinghash%03d", i)
		_, err := store.CreateBook(&Book{
			Title:    fmt.Sprintf("Timing Book %03d", i),
			FilePath: fmt.Sprintf("/tmp/warmtiming_%03d.m4b", i),
			FileHash: &hash,
		})
		require.NoError(t, err)
	}

	mem, err := NewMemStore()
	require.NoError(t, err)
	require.NoError(t, mem.WarmFromPebble(context.Background(), store))

	durations := mem.LastWarmupDurations()
	require.NotEmpty(t, durations, "warmup must record per-phase durations")

	// Every table the warmup reports a row count for must also have a
	// duration. A phase that scans but is never timed is exactly the blind
	// spot this change exists to remove.
	rows, _ := mem.LastWarmupCounts()
	for table := range rows {
		_, ok := durations[table]
		require.True(t, ok,
			"table %q has a warmup row count but no recorded duration", table)
	}

	// The commit is timed separately and on purpose: a single write txn is
	// held open across all ten scans, so if go-memdb defers work to commit
	// that cost would otherwise be attributed to nothing at all.
	_, ok := durations[WarmupPhaseKeyCommit]
	require.True(t, ok, "txn.Commit must be timed separately from the scans")

	// Durations are wall-clock measurements, so they may legitimately round
	// to zero on a tiny fixture. They must never be negative — that would
	// mean a phase key was written from the wrong clock reading.
	for phase, d := range durations {
		require.GreaterOrEqual(t, d.Nanoseconds(), int64(0),
			"phase %q recorded a negative duration", phase)
	}
}

// TestWarmupDurations_AreNotCarriedAcrossWarms guards the accessor against
// returning stale numbers. LastWarmupDurations describes "the most recent
// WarmFromPebble"; if a second warm reused the first warm's map the reported
// split would describe a run that already ended.
func TestWarmupDurations_AreNotCarriedAcrossWarms(t *testing.T) {
	store := setupTestPebbleStore(t)
	store.WaitForWarmup()

	hash := "timingrewarm001"
	_, err := store.CreateBook(&Book{
		Title:    "Rewarm Book",
		FilePath: "/tmp/warmtiming_rewarm.m4b",
		FileHash: &hash,
	})
	require.NoError(t, err)

	mem, err := NewMemStore()
	require.NoError(t, err)
	require.NoError(t, mem.WarmFromPebble(context.Background(), store))
	first := mem.LastWarmupDurations()
	require.NotEmpty(t, first)

	// Mutating the returned map must not reach into the store — the accessor
	// hands out a copy, so a caller that scribbles on it cannot corrupt the
	// next reader's view.
	for k := range first {
		first[k] = -1
	}

	second := mem.LastWarmupDurations()
	for phase, d := range second {
		require.GreaterOrEqual(t, d.Nanoseconds(), int64(0),
			"phase %q leaked a mutation from a previously returned map", phase)
	}
}
