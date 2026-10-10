// file: internal/server/scanlock_order_test.go
// version: 1.1.0
// guid: 3a8d0f52-6c1e-4b97-9e24-d71b5a0c8e63
// last-edited: 2026-10-10

package server

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/scanlock"
)

// lockEvent is one entry of the merged cross-layer trace: Layer "L0" is
// scanlock.Books, "L1" is writeBackPathLocks. Op is "+" (about to acquire),
// "=" (acquired, L0 only), "-" (released), "!" (try failed / wait abandoned,
// L0 only), "p+" / "p-" (pending mark set / cleared, L0 only).
type lockEvent struct {
	Layer, Op, Key string
}

// traceL0L1 records both lock tables into ONE ordered log, so an assertion can
// see how the layers interleave. L0 events are kept only for keys in l0Keys:
// scanlock.Books is process-wide and another test's book must not leak in.
func traceL0L1(t *testing.T, l0Keys ...string) func() []lockEvent {
	t.Helper()
	want := map[string]bool{}
	for _, k := range l0Keys {
		want[k] = true
	}
	var mu sync.Mutex
	var log []lockEvent
	scanlock.Books.SetTrace(func(ev scanlock.Event) {
		if !want[ev.Key] {
			return
		}
		mu.Lock()
		log = append(log, lockEvent{"L0", ev.Op, ev.Key})
		mu.Unlock()
	})
	writeBackPathLocks.setTrace(func(ev string) {
		mu.Lock()
		log = append(log, lockEvent{"L1", ev[:1], ev[1:]})
		mu.Unlock()
	})
	t.Cleanup(func() {
		scanlock.Books.SetTrace(nil)
		writeBackPathLocks.setTrace(nil)
	})
	return func() []lockEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]lockEvent(nil), log...)
	}
}

// requireL0Outermost fails when an L0 key is requested while any L1 key is
// held (rule R2 in internal/scanlock: no L1 or L2 holder takes L0). Taking L0
// under L1 is the inversion that deadlocks against an apply, which holds L0
// and then takes L1 in its file work.
func requireL0Outermost(t *testing.T, log []lockEvent) {
	t.Helper()
	held := map[string]int{}
	holdingL1 := func() string {
		for k, n := range held {
			if n > 0 {
				return k
			}
		}
		return ""
	}
	for _, ev := range log {
		switch {
		case ev.Layer == "L1" && ev.Op == "+":
			held[ev.Key]++
		case ev.Layer == "L1" && ev.Op == "-":
			held[ev.Key]--
		case ev.Layer == "L0" && ev.Op == "+":
			if k := holdingL1(); k != "" {
				t.Fatalf("L0 key %s requested while L1 key %s was held (R2): %v", ev.Key, k, log)
			}
		}
	}
}

func indexOf(log []lockEvent, match func(lockEvent) bool) int {
	for i, ev := range log {
		if match(ev) {
			return i
		}
	}
	return -1
}

// The apply side of the lock order, across both layers, on the real file
// work: an apply holds its book's scan lock (L0), submits the file job to the
// real FileIOPool, and releases. The job then runs FinishApplyFileWork, which
// takes metafetch's book key and the path keys of writeBackPathLocks (L1).
//
// Asserted on the merged trace:
//   - the pool's pending mark is set BEFORE the apply releases L0, so the
//     scanner can never slip in between the database write and the file
//     write (the whole point of MarkPending);
//   - the file job really took L1 keys (the test is not vacuous);
//   - no L0 key is ever requested while an L1 key is held (R2).
//
// Mutation-checked: requesting the book's L0 key inside the job body while an
// L1 key of writeBackPathLocks is held fails the R2 assertion.
func TestLockOrder_ApplyTakesL0ThenFileWorkTakesL1(t *testing.T) {
	if testing.Short() {
		t.Skip("touches the filesystem and the file-work pipeline")
	}
	store := rowWritersStore(t)
	f := newRowWritersFixture(t, store)
	mfs := lockedMetafetch(store)
	id := f.book.ID
	events := traceL0L1(t, id)

	pool := NewFileIOPool(1)
	defer pool.Stop()

	hold, err := scanlock.Books.LockSet(context.Background(), []string{id})
	require.NoError(t, err)
	jobDone := make(chan struct{})
	require.True(t, pool.Submit(id, func() {
		defer close(jobDone)
		_ = mfs.FinishApplyFileWork(context.Background(), id, "", true, true, nil)
	}))
	hold.Release()

	select {
	case <-jobDone:
	case <-time.After(30 * time.Second):
		t.Fatal("the file job deadlocked")
	}
	// The pending mark is cleared after fn returns; wait for its event.
	require.Eventually(t, func() bool {
		return indexOf(events(), func(e lockEvent) bool { return e.Layer == "L0" && e.Op == "p-" }) >= 0
	}, 5*time.Second, 5*time.Millisecond)

	log := events()
	pendingSet := indexOf(log, func(e lockEvent) bool { return e.Layer == "L0" && e.Op == "p+" })
	released := indexOf(log, func(e lockEvent) bool { return e.Layer == "L0" && e.Op == "-" })
	require.GreaterOrEqual(t, pendingSet, 0, "Submit set no pending mark: %v", log)
	require.GreaterOrEqual(t, released, 0, "the apply's L0 release is missing: %v", log)
	require.Less(t, pendingSet, released,
		"the pending mark must be set while the apply still holds L0: %v", log)
	require.GreaterOrEqual(t,
		indexOf(log, func(e lockEvent) bool { return e.Layer == "L1" && e.Op == "+" && strings.HasPrefix(e.Key, "book:") }), 0,
		"the file job took no L1 key; the test proves nothing: %v", log)
	requireL0Outermost(t, log)
}
