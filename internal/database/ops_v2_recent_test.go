// file: internal/database/ops_v2_recent_test.go
// version: 1.0.0
// guid: 7f2d9c41-6a85-4b3e-9d17-2e8c5f0a6b94
// last-edited: 2026-10-06

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/oklog/ulid/v2"
)

// writeRecentFixtureRow stores row with its timeline index keys, the way the
// real writers do (stageOpRow in the row's batch).
func writeRecentFixtureRow(t testing.TB, p *PebbleStore, batch *pebble.Batch, row OperationV2Row) {
	t.Helper()
	data, err := json.Marshal(&row)
	if err != nil {
		t.Fatal(err)
	}
	if err := batch.Set(opv2OpKey(row.ID), data, nil); err != nil {
		t.Fatal(err)
	}
	if err := stageOpRow(batch, nil, &row); err != nil {
		t.Fatal(err)
	}
}

// seedRecentOps writes n synthetic operations spread over the last `span`,
// covering every shape the proof has to survive: finished rows, open rows
// started long ago, rows queued but never started (open and cancelled), rows
// whose CompletedAt is before their QueuedAt (queue-clock skew), and ties on
// StartedAt. StartedAt <= CompletedAt always holds (the documented
// assumption).
func seedRecentOps(t testing.TB, p *PebbleStore, rng *rand.Rand, now time.Time, n int, span time.Duration) {
	t.Helper()
	ent := rngReader{rng}
	batch := p.db.NewBatch()
	for i := range n {
		q := now.Add(-time.Duration(rng.Int64N(int64(span))))
		if rng.IntN(10) == 0 {
			q = q.Truncate(time.Minute) // ties
		}
		row := OperationV2Row{
			ID: ulid.MustNew(ulid.Timestamp(q), ent).String(), DefID: "test.def", Plugin: "test",
			Status: "completed", Params: "{}", QueuedAt: q,
		}
		switch k := rng.IntN(100); {
		case k < 70: // started and finished
			s := q.Add(time.Duration(rng.Int64N(int64(time.Minute))))
			c := s.Add(time.Duration(rng.Int64N(int64(30 * time.Minute))))
			if c.After(now) {
				c = now
			}
			if s.After(c) {
				s = c
			}
			row.StartedAt, row.CompletedAt = &s, &c
		case k < 78: // still running, possibly started long ago
			s := q.Add(time.Duration(rng.Int64N(int64(time.Minute))))
			row.Status, row.StartedAt = "running", &s
		case k < 84: // queued, never started, still open
			row.Status = "queued"
		case k < 92: // cancelled before it started
			c := q.Add(time.Duration(rng.Int64N(int64(time.Hour))))
			row.Status, row.CompletedAt = "canceled", &c
		default: // queue-clock skew: completed before it was queued, never started
			c := q.Add(-time.Duration(1 + rng.Int64N(int64(48*time.Hour))))
			row.Status, row.CompletedAt = "failed", &c
		}
		writeRecentFixtureRow(t, p, batch, row)
		if batch.Count() > 5_000 || i == n-1 {
			if err := batch.Commit(pebble.Sync); err != nil {
				t.Fatal(err)
			}
			batch = p.db.NewBatch()
		}
	}
	_ = batch.Close()
}

func reconcileTimeline(t testing.TB, p *PebbleStore) {
	t.Helper()
	if _, err := p.ReconcileOpsV2TimelineIndex(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !p.OpsV2TimelineIndexTrusted() {
		t.Fatal("timeline index not trusted after reconcile")
	}
}

// TestListRecentOperationsV2_EquivalentToAllHistory: for random histories,
// densities and limits, the windowed read returns exactly the rows (and
// order) the all-history read returns.
func TestListRecentOperationsV2_EquivalentToAllHistory(t *testing.T) {
	seeds := 40
	if testing.Short() {
		seeds = 10
	}
	for seed := 1; seed <= seeds; seed++ {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewPCG(uint64(seed), 7))
			p := newTimelineStore(t)
			now := time.Now().UTC()
			// Vary density so some seeds satisfy the first window, some need
			// several doublings, and some fall back to all history.
			spans := []time.Duration{2 * time.Hour, 3 * 24 * time.Hour, 400 * 24 * time.Hour, 900 * 24 * time.Hour}
			n := 20 + rng.IntN(400)
			seedRecentOps(t, p, rng, now, n, spans[seed%len(spans)])
			reconcileTimeline(t, p)
			for _, limit := range []int{1, 5, 17, 100} {
				want, err := p.ListOperationsV2Since(time.Time{}, limit)
				if err != nil {
					t.Fatal(err)
				}
				got, err := ListRecentOperationsV2(p, limit, now)
				if err != nil {
					t.Fatal(err)
				}
				if d := firstDiff(rowIDs(want), rowIDs(got)); d >= 0 {
					t.Fatalf("seed %d n=%d limit=%d: windowed read differs from all history at position %d (want %d rows, got %d)",
						seed, n, limit, d, len(want), len(got))
				}
			}
		})
	}
}

// countingLister records every window ListRecentOperationsV2 asks for.
type countingLister struct {
	*PebbleStore
	sinces []time.Time
}

func (c *countingLister) ListOperationsV2Since(since time.Time, limit int) ([]OperationV2Row, error) {
	c.sinces = append(c.sinces, since)
	return c.PebbleStore.ListOperationsV2Since(since, limit)
}

// TestListRecentOperationsV2_ReadsOnlyRecentWindow is the fast-path proof:
// with plenty of recent activity it asks for one one-hour window and never
// for all of history, which is the read that decoded every row.
func TestListRecentOperationsV2_ReadsOnlyRecentWindow(t *testing.T) {
	p := newTimelineStore(t)
	now := time.Now().UTC()
	rng := rand.New(rand.NewPCG(3, 3))
	seedRecentOps(t, p, rng, now.Add(-2*time.Hour), 3_000, 300*24*time.Hour) // old history
	seedRecentOps(t, p, rng, now, 60, 30*time.Minute)                        // a busy last half hour
	reconcileTimeline(t, p)

	c := &countingLister{PebbleStore: p}
	if _, err := ListRecentOperationsV2(c, 5, now); err != nil {
		t.Fatal(err)
	}
	if len(c.sinces) != 1 {
		t.Fatalf("asked for %d windows (%v), want 1", len(c.sinces), c.sinces)
	}
	if c.sinces[0].IsZero() {
		t.Fatal("asked for all of history; the point is to read only a recent window")
	}
	if got := now.Sub(c.sinces[0]); got != recentOpsFirstWindow {
		t.Fatalf("first window = %s, want %s", got, recentOpsFirstWindow)
	}
}

// TestListRecentOperationsV2_UntrustedIndexReadsOnce: before the reconcile,
// every windowed read is a full scan, so it must ask exactly once.
func TestListRecentOperationsV2_UntrustedIndexReadsOnce(t *testing.T) {
	p := newTimelineStore(t)
	now := time.Now().UTC()
	seedRecentOps(t, p, rand.New(rand.NewPCG(4, 4)), now, 50, 400*24*time.Hour)
	if p.OpsV2TimelineIndexTrusted() {
		t.Fatal("fresh store reports a trusted index")
	}
	c := &countingLister{PebbleStore: p}
	if _, err := ListRecentOperationsV2(c, 5, now); err != nil {
		t.Fatal(err)
	}
	if len(c.sinces) != 1 || !c.sinces[0].IsZero() {
		t.Fatalf("untrusted index: asked for %v, want one all-history read", c.sinces)
	}
}

// BenchmarkRecentOperationsV2 compares the old status-panel read (all of
// history) with the windowed one on a 50k-operation table.
func BenchmarkRecentOperationsV2(b *testing.B) {
	p := newTimelineStore(b)
	now := time.Now().UTC()
	rng := rand.New(rand.NewPCG(9, 9))
	params := `{"pad":"` + strings.Repeat("x", 600) + `"}`
	ent := rngReader{rng}
	batch := p.db.NewBatch()
	const total = 50_000
	for i := range total {
		// ~1,800 a day, newest first: the prod write rate.
		q := now.Add(-time.Duration(i) * 48 * time.Second)
		s := q.Add(time.Second)
		c := s.Add(time.Minute)
		row := OperationV2Row{
			ID: ulid.MustNew(ulid.Timestamp(q), ent).String(), DefID: "bench.def", Plugin: "bench",
			Status: "completed", Params: params, QueuedAt: q, StartedAt: &s, CompletedAt: &c,
		}
		writeRecentFixtureRow(b, p, batch, row)
		if batch.Count() > 10_000 {
			if err := batch.Commit(pebble.Sync); err != nil {
				b.Fatal(err)
			}
			batch = p.db.NewBatch()
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		b.Fatal(err)
	}
	reconcileTimeline(b, p)
	b.Run("all_history", func(b *testing.B) {
		for b.Loop() {
			if _, err := p.ListOperationsV2Since(time.Time{}, 5); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("recent_window", func(b *testing.B) {
		for b.Loop() {
			if _, err := ListRecentOperationsV2(p, 5, now); err != nil {
				b.Fatal(err)
			}
		}
	})
}
