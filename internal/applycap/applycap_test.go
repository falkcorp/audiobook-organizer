// file: internal/applycap/applycap_test.go
// version: 1.2.0
// guid: 9e2b7c41-3f8a-4d16-b6e5-0a7c4d9f2e18
// last-edited: 2026-10-19

package applycap

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEffective_ZeroAndNegativeMeanDefaultNeverUnlimited(t *testing.T) {
	for _, in := range []int{0, -1, -5000} {
		if got := Effective(in); got != Default {
			t.Fatalf("Effective(%d) = %d, want Default %d", in, got, Default)
		}
	}
	if got := Effective(12); got != 12 {
		t.Fatalf("Effective(12) = %d, want 12", got)
	}
}

func TestCheck_RefusesCapPlusOneAllowsCap(t *testing.T) {
	// Known-good twin: exactly the cap passes.
	if err := Check("op", Default, 0); err != nil {
		t.Fatalf("Check at cap: unexpected error %v", err)
	}
	// Bogus value: one over is refused with a typed error that names the op.
	err := Check("batch-apply", Default+1, 0)
	var ex *ExceededError
	if !errors.As(err, &ex) {
		t.Fatalf("Check over cap: got %v, want *ExceededError", err)
	}
	if ex.Op != "batch-apply" || ex.Requested != Default+1 || ex.Cap != Default {
		t.Fatalf("ExceededError fields = %+v", *ex)
	}
	for _, want := range []string{"batch-apply", "5001", "5000", "bulk_apply_max_items", "nothing was applied"} {
		if !strings.Contains(ex.Error(), want) {
			t.Errorf("error text %q lacks %q", ex.Error(), want)
		}
	}
	// A configured cap is honoured in both directions.
	if err := Check("op", 3, 3); err != nil {
		t.Fatalf("Check(3, cap 3): %v", err)
	}
	if err := Check("op", 4, 3); err == nil {
		t.Fatal("Check(4, cap 3): want refusal")
	}
}

func TestFits(t *testing.T) {
	if !Fits(Default, 0) || Fits(Default+1, 0) || !Fits(2, 2) || Fits(3, 2) {
		t.Fatal("Fits disagrees with Check")
	}
}

func TestCounter_RefusesTheCapPlusOneth(t *testing.T) {
	c := NewCounter("auto-match", 3)
	for i := range 3 {
		if err := c.Admit(); err != nil {
			t.Fatalf("Admit #%d: %v", i+1, err)
		}
	}
	err := c.Admit()
	var ex *ExceededError
	if !errors.As(err, &ex) || ex.Requested != 4 || ex.Cap != 3 {
		t.Fatalf("4th Admit: got %v, want ExceededError{Requested:4 Cap:3}", err)
	}
	// The refused apply is not counted, and refusing is sticky.
	if int(c.admitted.Load()) != 3 {
		t.Fatalf("admitted after refusal = %d, want 3", int(c.admitted.Load()))
	}
	if err := c.Admit(); err == nil {
		t.Fatal("5th Admit: want refusal")
	}
	if int(c.admitted.Load()) != 3 {
		t.Fatalf("admitted after second refusal = %d, want 3", int(c.admitted.Load()))
	}
}

func TestCounter_ConcurrentAdmitsNeverExceedCap(t *testing.T) {
	const limit, workers, perWorker = 50, 8, 100
	c := NewCounter("op", limit)
	var wg sync.WaitGroup
	var okCount, refused atomicCounter
	for range workers {
		wg.Go(func() {
			for range perWorker {
				if c.Admit() == nil {
					okCount.inc()
				} else {
					refused.inc()
				}
			}
		})
	}
	wg.Wait()
	if okCount.n != limit || refused.n != workers*perWorker-limit || int(c.admitted.Load()) != limit {
		t.Fatalf("ok=%d refused=%d admitted=%d, want %d/%d/%d",
			okCount.n, refused.n, int(c.admitted.Load()), limit, workers*perWorker-limit, limit)
	}
}

type atomicCounter struct {
	mu sync.Mutex
	n  int
}

func (a *atomicCounter) inc() { a.mu.Lock(); a.n++; a.mu.Unlock() }

func TestBatchLedger_ChunksAddUpToTheDeclaredTotal(t *testing.T) {
	l := NewBatchLedger(time.Minute, 10, 5)
	require.NoError(t, l.Admit("op", "u", "b1", 0, 500, 1200, 5000))
	require.NoError(t, l.Admit("op", "u", "b1", 1, 500, 1200, 5000))
	require.NoError(t, l.Admit("op", "u", "b1", 2, 200, 1200, 5000))
	err := l.Admit("op", "u", "b1", 3, 1, 1200, 5000)
	var be *BatchError
	require.ErrorAs(t, err, &be, "a chunk past the declared total is refused")
	require.Equal(t, BatchExceeded, be.Kind)
	require.Equal(t, "BATCH_EXCEEDED", be.Code())
	require.Equal(t, 1201, be.Requested)
	require.Equal(t, 1200, be.Cap)
	require.NotContains(t, be.Error(), "nothing was applied", "a mid-batch refusal must not claim nothing was applied")
	require.Contains(t, be.Error(), "already queued")
}

func TestBatchLedger_RefusesADeclaredTotalOverTheCap(t *testing.T) {
	l := NewBatchLedger(time.Minute, 10, 5)
	var ex *ExceededError
	require.ErrorAs(t, l.Admit("op", "u", "b1", 0, 10, 5001, 5000), &ex, "a total over the cap is refused on the first chunk, however small")
	require.Equal(t, 5001, ex.Requested)
	require.Equal(t, 5000, ex.Cap)
	// A zero configured cap is the default, never unlimited.
	require.Error(t, l.Admit("op", "u", "b2", 0, 1, Default+1, 0))
}

func TestBatchLedger_ATotalCannotGrowMidBatch(t *testing.T) {
	l := NewBatchLedger(time.Minute, 10, 5)
	require.NoError(t, l.Admit("op", "u", "b1", 0, 500, 600, 5000))
	var be *BatchError
	require.ErrorAs(t, l.Admit("op", "u", "b1", 1, 100, 5000, 5000), &be)
	require.Equal(t, BatchTotalMismatch, be.Kind)
	require.Equal(t, "BATCH_TOTAL_MISMATCH", be.Code())
}

func TestBatchLedger_AResentChunkIsNotCountedTwice(t *testing.T) {
	l := NewBatchLedger(time.Minute, 10, 5)
	require.NoError(t, l.Admit("op", "u", "b1", 0, 500, 1000, 5000))
	require.NoError(t, l.Admit("op", "u", "b1", 0, 500, 1000, 5000), "the same index again is accepted")
	require.NoError(t, l.Admit("op", "u", "b1", 1, 500, 1000, 5000), "and did not use up the second chunk's room")
	require.Error(t, l.Admit("op", "u", "b1", 0, 400, 1000, 5000), "a resent index of another size is refused")
}

func TestBatchLedger_ReleaseLetsAFailedChunkRetry(t *testing.T) {
	l := NewBatchLedger(time.Minute, 10, 5)
	require.NoError(t, l.Admit("op", "u", "b1", 0, 500, 1000, 5000))
	require.NoError(t, l.Admit("op", "u", "b1", 1, 500, 1000, 5000))
	l.Release("u", "b1", 1)
	require.NoError(t, l.Admit("op", "u", "b1", 1, 500, 1000, 5000))
	require.Error(t, l.Admit("op", "u", "b1", 2, 1, 1000, 5000))
}

// The PoC of the review: refused requests used to be inserted before the check
// and never swept, so 1024 of them filled the ledger and locked every user out.
func TestBatchLedger_RefusedRequestsOccupyNoSlot(t *testing.T) {
	l := NewBatchLedger(time.Minute, 1024, 16)
	now := time.Now()
	l.now = func() time.Time { return now }
	for i := 0; i < 5000; i++ {
		// n=2 against total=1, and a total over the cap, and an empty chunk.
		require.Error(t, l.Admit("op", "attacker", fmt.Sprint("x", i), 0, 2, 1, 5000))
		require.Error(t, l.Admit("op", "attacker", fmt.Sprint("y", i), 0, 1, 99999, 5000))
		require.Error(t, l.Admit("op", "attacker", fmt.Sprint("z", i), 0, 0, 5, 5000))
	}
	require.Empty(t, l.batches, "a refused request recorded nothing")
	now = now.Add(24 * time.Hour)
	require.NoError(t, l.Admit("op", "victim", "v", 0, 500, 1000, 5000))
}

func TestBatchLedger_OneUsersIDCannotPoisonAnothers(t *testing.T) {
	l := NewBatchLedger(time.Minute, 1024, 16)
	// B squats on A's (guessable) batch id with a tiny total.
	require.NoError(t, l.Admit("op", "userB", "shared-id", 0, 1, 1, 5000))
	// A's batch of the same id is a different batch.
	require.NoError(t, l.Admit("op", "userA", "shared-id", 0, 500, 1000, 5000))
	require.NoError(t, l.Admit("op", "userA", "shared-id", 1, 500, 1000, 5000))
	var be *BatchError
	require.ErrorAs(t, l.Admit("op", "userB", "shared-id", 1, 1, 1, 5000), &be, "B is held to B's own total")
}

func TestBatchLedger_AFullLedgerEvictsInsteadOfRefusing(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	l := NewBatchLedger(time.Hour, 8, 3)
	l.now = func() time.Time { return now }
	tick := func() { now = now.Add(time.Second) }
	// One user opens many valid batches: their own oldest goes first.
	for i := 0; i < 10; i++ {
		require.NoError(t, l.Admit("op", "hog", fmt.Sprint("h", i), 0, 1, 10, 5000))
		tick()
	}
	count := func(owner string) (n int) {
		for _, e := range l.batches {
			if e.owner == owner {
				n++
			}
		}
		return
	}
	require.Equal(t, 3, count("hog"), "the per-owner limit holds")
	require.NotContains(t, l.batches, batchKey("hog", "h0"), "the least recently touched one was evicted")
	require.Contains(t, l.batches, batchKey("hog", "h9"))
	// Many users fill the whole ledger; a newcomer is still admitted.
	for i := 0; i < 20; i++ {
		require.NoError(t, l.Admit("op", fmt.Sprint("u", i), "b", 0, 1, 10, 5000))
		tick()
	}
	require.LessOrEqual(t, len(l.batches), 8)
	require.NoError(t, l.Admit("op", "newcomer", "b", 0, 500, 1000, 5000))
	require.Contains(t, l.batches, batchKey("newcomer", "b"))
}

func TestBatchLedger_ForgetsIdleBatches(t *testing.T) {
	l := NewBatchLedger(time.Minute, 2, 2)
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	require.NoError(t, l.Admit("op", "u", "a", 0, 1, 1, 5000))
	now = now.Add(2 * time.Minute)
	require.NoError(t, l.Admit("op", "u", "a", 0, 1, 1, 5000), "a forgotten batch starts over")
	require.Len(t, l.batches, 1)
}
