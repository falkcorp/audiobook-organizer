// file: internal/books/cutover/m5/parallel_test.go
// version: 1.0.0
// guid: e3a97c15-48d0-4b62-91f7-2c5b0d8e4a36
// last-edited: 2026-11-13

package m5_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// TestM5_ParallelShardsDisjoint runs M5 as it is declared, over thousands of
// synthetic rows, and requires what the runner's sharding promises: the shards
// ran at the same time, no row was stepped by two workers, no row was lost, and
// the accounting balances. Run under -race, it also covers the step's shared
// counters.
//
// The rows are synthetic (Batman titles, one row per second); the corpus holds
// two. The wall time is logged as rows per second for the PR's numbers: the
// disk here is in memory, so it measures the step's own cost per row (one Import
// commit and one delete), not an fsync.
func TestM5_ParallelShardsDisjoint(t *testing.T) {
	const n = 3000
	rows := synth(t, n, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	r := newRig(t, "", withRows(rows), func(r *rig) { r.chunkItems = 64 })
	mem := r.seed(t)

	dm := r.migration()
	inner := dm.Step
	var (
		mu          sync.Mutex
		stepped     = map[string]int{}
		inflight    atomic.Int64
		maxInflight atomic.Int64
		first       atomic.Bool
		met         = make(chan struct{})
		metOnce     sync.Once
	)
	dm.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
		cur := inflight.Add(1)
		defer inflight.Add(-1)
		for {
			m := maxInflight.Load()
			if cur <= m || maxInflight.CompareAndSwap(m, cur) {
				break
			}
		}
		mu.Lock()
		stepped[string(kv.Key)]++
		mu.Unlock()
		if cur >= 2 {
			metOnce.Do(func() { close(met) })
		} else if first.CompareAndSwap(false, true) {
			// The first row waits until another worker is in a Step too, so a
			// runner that stepped its shards one after another would show here.
			select {
			case <-met:
			case <-time.After(10 * time.Second):
			}
		}
		return inner(ctx, kv, w)
	}

	start := time.Now()
	s, err := r.runWith(t, mem, dm)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("boot: %v\n%s", err, r.logs)
	}

	if got := maxInflight.Load(); got < 2 {
		t.Errorf("at most %d Step ran at a time: the shards did not run concurrently", got)
	}
	if len(stepped) != n {
		t.Errorf("%d distinct rows stepped, want %d", len(stepped), n)
	}
	for k, c := range stepped {
		if c != 1 {
			t.Errorf("%s was stepped %d times: two workers share a row", k, c)
		}
	}
	if len(s.Left) != 0 || len(s.Quarantine) != 0 || s.State != "done" {
		t.Errorf("left %d quarantine %d state %q", len(s.Left), len(s.Quarantine), s.State)
	}
	if len(s.Journal) != n {
		t.Errorf("%d journal records, want %d", len(s.Journal), n)
	}
	seen := map[string]int{}
	for _, j := range s.Journal {
		seen[j.SourceID]++
	}
	for k := range rows {
		if seen[k] != 1 {
			t.Errorf("%s has %d journal records, want 1", k, seen[k])
		}
		if c := r.j.callsFor(k); c != 1 {
			t.Errorf("%s was imported %d times, want once", k, c)
		}
	}
	if v := s.Verify; v.Read != n || v.Written != n || v.Quarantined != 0 || len(v.Failures) != 0 {
		t.Errorf("verify = %+v, want %d read and written", v, n)
	}
	t.Logf("%d rows in %s: %.0f rows/s (up to %d workers at once, in-memory filesystem)", n, elapsed.Round(time.Millisecond), float64(n)/elapsed.Seconds(), maxInflight.Load())
}
