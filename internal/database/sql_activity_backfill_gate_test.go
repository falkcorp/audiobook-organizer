// file: internal/database/sql_activity_backfill_gate_test.go
// version: 1.1.0
// guid: 5e9c3b27-1a8d-4f64-b0c2-7d3e6a9f1c58
// last-edited: 2026-09-10

package database

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The backfill gate: no row-deleting maintenance pass may land between the
// backfill copying a batch into SQLite and re-presenting it for parity. A
// delete inside that window makes the re-present insert the row again, which
// the backfill counts as a copied row that did not land — a parity failure
// that blocks the read flip and forces a full rescan on the next boot, caused
// by our own nightly maintenance rather than by any lost write.

// gateFixture returns a SQLite store holding n old debug rows.
func gateFixture(t *testing.T, n int) (*SQLActivityStore, []ActivityEntry) {
	t.Helper()
	s := newTestSQLStore(t)
	base := time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)
	entries := make([]ActivityEntry, n)
	for i := range n {
		entries[i] = ActivityEntry{
			Timestamp: base.Add(time.Duration(i) * time.Minute), Tier: "debug", Type: "scan_progress",
			Level: "debug", Source: "test", Summary: "old debug row",
		}
	}
	return s, entries
}

// TestSQLActivityStore_CopyAndVerifyBatch_CleanCopyReinsertsNothing pins the
// contract the backfill relies on: a batch presented twice inserts once.
func TestSQLActivityStore_CopyAndVerifyBatch_CleanCopyReinsertsNothing(t *testing.T) {
	s, entries := gateFixture(t, 5)
	copied, reinserted, err := s.copyAndVerifyBatch(context.Background(), entries)
	if err != nil {
		t.Fatalf("copyAndVerifyBatch: %v", err)
	}
	if copied != 5 || reinserted != 0 {
		t.Errorf("copied=%d reinserted=%d, want 5/0", copied, reinserted)
	}
}

// TestSQLActivityStore_PruneWaitsForInFlightBackfillBatch: while a batch holds
// the gate, Prune must block rather than delete under it; once the batch
// releases the gate, Prune proceeds and deletes what the batch copied.
func TestSQLActivityStore_PruneWaitsForInFlightBackfillBatch(t *testing.T) {
	s, entries := gateFixture(t, 5)

	// Stand in for the backfill: hold the read side exactly as
	// copyAndVerifyBatch does, with the copy already done.
	s.backfillGate.RLock()
	if _, err := s.recordBatch(context.Background(), entries); err != nil {
		s.backfillGate.RUnlock()
		t.Fatalf("copy: %v", err)
	}

	pruned := make(chan int, 1)
	go func() {
		n, err := s.Prune(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), "debug")
		if err != nil {
			t.Errorf("Prune: %v", err)
		}
		pruned <- n
	}()

	// Prune cannot complete while the batch holds the gate. This direction is
	// deterministic: a mutex that is held is held.
	select {
	case n := <-pruned:
		s.backfillGate.RUnlock()
		t.Fatalf("Prune deleted %d rows while a backfill batch was between copy and re-present", n)
	case <-time.After(100 * time.Millisecond):
	}

	// The re-present still sees every row: nothing was deleted under the batch.
	if n, err := s.recordBatch(context.Background(), entries); err != nil || n != 0 {
		s.backfillGate.RUnlock()
		t.Fatalf("re-present inserted %d rows (err %v); a maintenance delete landed inside the copy/verify window", n, err)
	}
	s.backfillGate.RUnlock()

	select {
	case n := <-pruned:
		if n != 5 {
			t.Errorf("Prune deleted %d rows after the batch released the gate, want 5", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Prune never ran after the backfill batch released the gate")
	}
}

// TestSQLActivityStore_CopyAndVerifyBatch_CanceledWhileWaitingForGate pins the
// other half of the gate's contract: waiting for it must be interruptible.
//
// The write side is held for a whole maintenance run, and Prune has no context
// on ActivityStorer so its run cannot be cut short. A plain RLock in
// copyAndVerifyBatch would therefore park the backfill goroutine for the length
// of that run — and sqlMigrationStarter.Stop waits on that goroutine without a
// bound, so shutdown would block for the whole pass. The batch must instead
// give up on ctx, and must leave nothing locked when it does.
func TestSQLActivityStore_CopyAndVerifyBatch_CanceledWhileWaitingForGate(t *testing.T) {
	s, entries := gateFixture(t, 3)

	// Stand in for a long Prune: the write side, held with no way to cut it short.
	s.backfillGate.Lock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	type batchResult struct {
		copied, reinserted int
		err                error
	}
	done := make(chan batchResult, 1)
	start := time.Now()
	go func() {
		copied, reinserted, err := s.copyAndVerifyBatch(ctx, entries)
		done <- batchResult{copied, reinserted, err}
	}()

	var res batchResult
	select {
	case res = <-done:
	case <-time.After(time.Second):
		s.backfillGate.Unlock()
		t.Fatal("copyAndVerifyBatch never returned while a maintenance pass held the gate: acquisition is not ctx-aware, so shutdown waits out the whole pass")
	}
	if !errors.Is(res.err, context.Canceled) {
		s.backfillGate.Unlock()
		t.Fatalf("copyAndVerifyBatch err = %v, want a context.Canceled the backfill can classify as a shutdown", res.err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Errorf("copyAndVerifyBatch took %v to give up, want well under 1s", waited)
	}
	if res.copied != 0 || res.reinserted != 0 {
		t.Errorf("copied=%d reinserted=%d after giving up, want 0/0: nothing was written", res.copied, res.reinserted)
	}

	// The maintenance pass finishes. The abandoned acquisition must release the
	// read lock it eventually takes, or the gate is leaked and every later
	// deleting pass deadlocks.
	s.backfillGate.Unlock()

	relocked := make(chan struct{})
	go func() {
		s.backfillGate.Lock()
		close(relocked)
	}()
	select {
	case <-relocked:
		s.backfillGate.Unlock()
	case <-time.After(10 * time.Second):
		t.Fatal("the backfill gate is still held for reading after the abandoned batch: the orphaned acquisition never released it")
	}
}

// TestSQLActivityStore_DeletingPassesTakeTheGate pins that every deleting pass
// takes the write side, not just Prune: a new deleting method that forgets the
// gate reopens the window. Each is started while the gate is held for reading
// and must not return until it is released.
func TestSQLActivityStore_DeletingPassesTakeTheGate(t *testing.T) {
	cutoff := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	passes := map[string]func(s *SQLActivityStore) error{
		"CompactByDay": func(s *SQLActivityStore) error { _, err := s.CompactByDay(context.Background(), cutoff); return err },
		"Summarize": func(s *SQLActivityStore) error {
			_, err := s.Summarize(context.Background(), cutoff, "debug")
			return err
		},
		"Prune":   func(s *SQLActivityStore) error { _, err := s.Prune(cutoff, "debug"); return err },
		"WipeAll": func(s *SQLActivityStore) error { _, err := s.WipeAllActivity(context.Background()); return err },
	}
	for name, pass := range passes {
		t.Run(name, func(t *testing.T) {
			s, entries := gateFixture(t, 2)
			if _, err := s.recordBatch(context.Background(), entries); err != nil {
				t.Fatalf("seed: %v", err)
			}
			s.backfillGate.RLock()
			done := make(chan error, 1)
			go func() { done <- pass(s) }()
			select {
			case err := <-done:
				s.backfillGate.RUnlock()
				t.Fatalf("%s returned (err %v) while the backfill gate was held for reading: it does not take the gate", name, err)
			case <-time.After(100 * time.Millisecond):
			}
			s.backfillGate.RUnlock()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("%s: %v", name, err)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("%s never returned after the gate was released", name)
			}
		})
	}
}
