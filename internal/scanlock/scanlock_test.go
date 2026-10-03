// file: internal/scanlock/scanlock_test.go
// version: 1.1.2
// guid: 17e29a0f-0309-4e70-ad4c-1f5d29bcdfac
// last-edited: 2026-10-03

package scanlock

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestDifferentBooksNeverBlockEachOther(t *testing.T) {
	tb := New()
	a, err := tb.LockSet(context.Background(), []string{"book-a"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	// Keyed, not striped: any other id is free while book-a is held.
	for _, id := range []string{"book-b", "book-c", "book-aa", ""} {
		h, ok := tb.TryLockSet([]string{id})
		if !ok {
			t.Fatalf("TryLockSet(%q) blocked by an unrelated book", id)
		}
		h.Release()
	}
}

func TestTryLockSetIsAllOrNothing(t *testing.T) {
	tb := New()
	b, _ := tb.LockSet(context.Background(), []string{"b"})
	if _, ok := tb.TryLockSet([]string{"a", "b", "c"}); ok {
		t.Fatal("TryLockSet succeeded while b was held")
	}
	// Nothing of the failed attempt may remain held.
	if h, ok := tb.TryLockSet([]string{"a", "c"}); !ok {
		t.Fatal("a failed TryLockSet left a or c held")
	} else {
		h.Release()
	}
	b.Release()
	if n := tb.Held(); n != 0 {
		t.Fatalf("table not empty after last release: %d entries", n)
	}
}

func TestLockSetTimeoutReleasesPartialAcquisitions(t *testing.T) {
	tb := New()
	c, _ := tb.LockSet(context.Background(), []string{"c"})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := tb.LockSet(ctx, []string{"a", "c"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
	if h, ok := tb.TryLockSet([]string{"a"}); !ok {
		t.Fatal("a stayed held after the timed-out LockSet")
	} else {
		h.Release()
	}
	c.Release()
	if n := tb.Held(); n != 0 {
		t.Fatalf("table not empty: %d", n)
	}
}

func TestLockSetWaitsThenAcquires(t *testing.T) {
	tb := New()
	x, _ := tb.LockSet(context.Background(), []string{"x"})
	got := make(chan struct{})
	go func() {
		h, err := tb.LockSet(context.Background(), []string{"x"})
		if err == nil {
			close(got)
			h.Release()
		}
	}()
	select {
	case <-got:
		t.Fatal("acquired x while it was held")
	case <-time.After(20 * time.Millisecond):
	}
	x.Release()
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter never acquired x after release")
	}
}

func TestRetainKeepsTheHoldUntilEveryShareIsReleased(t *testing.T) {
	tb := New()
	h, _ := tb.LockSet(context.Background(), []string{"k"})
	done := h.Retain()
	h.Release()
	h.Release() // idempotent
	if _, ok := tb.TryLockSet([]string{"k"}); ok {
		t.Fatal("k free while a retained share is outstanding")
	}
	done()
	done() // idempotent
	if g, ok := tb.TryLockSet([]string{"k"}); !ok {
		t.Fatal("k still held after every share released")
	} else {
		g.Release()
	}
}

func TestOverlappingSetsNeverDeadlock(t *testing.T) {
	tb := New()
	sets := [][]string{{"a", "b"}, {"b", "a"}, {"c", "a"}, {"b", "c"}, {"a", "b", "c"}}
	var wg sync.WaitGroup
	for i := range 200 {
		wg.Go(func() {
			h, err := tb.LockSet(context.Background(), sets[i%len(sets)])
			if err != nil {
				t.Error(err)
				return
			}
			h.Release()
		})
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sorted-set acquisition deadlocked")
	}
	if n := tb.Held(); n != 0 {
		t.Fatalf("table not empty: %d", n)
	}
}

// A pending file job makes the book busy for the scanner (the Idle acquires)
// but not for another apply (plain LockSet).
func TestPendingBlocksOnlyTheIdleAcquires(t *testing.T) {
	tb := New()
	done := tb.MarkPending("p")
	if _, ok := tb.TryLockSetIdle([]string{"p"}); ok {
		t.Fatal("scanner acquired a book with pending file work")
	}
	h, ok := tb.TryLockSet([]string{"p"})
	if !ok {
		t.Fatal("a second apply was blocked by the first apply's pending file job")
	}
	h.Release()

	// The waiting LockSetIdle takes p and q, sees p's pending mark, releases
	// both ("-" q, then "-" p) and parks on the idle channel. The trace tells
	// the test exactly when that release has happened, so "it is waiting" is
	// an observed fact rather than a 30ms guess. The hook runs under the
	// table mutex: it only does a non-blocking send.
	parked := make(chan struct{}, 1)
	tb.SetTrace(func(ev Event) {
		if ev.Op == "-" && ev.Key == "p" {
			select {
			case parked <- struct{}{}:
			default:
			}
		}
	})
	got := make(chan error, 1)
	go func() {
		g, err := tb.LockSetIdle(context.Background(), []string{"p", "q"})
		if err == nil {
			// Release BEFORE signalling: the test reads tb.Held() as soon as
			// got fires. Signalling first raced it ("table not empty: 2" on
			// PR #3639, "1" on the CI coverage runner, 2026-10-03).
			g.Release()
		}
		got <- err
	}()
	select {
	case <-parked:
	case <-time.After(10 * time.Second):
		t.Fatal("LockSetIdle never released its keys to wait for the pending mark")
	}
	// It has released and is parked on idle, which only done() closes: it
	// cannot have returned, whatever the scheduler does.
	select {
	case err := <-got:
		t.Fatalf("LockSetIdle returned (err=%v) while file work was pending", err)
	default:
	}
	tb.SetTrace(nil)
	done()
	done() // idempotent
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("LockSetIdle: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("LockSetIdle never acquired after the pending mark cleared")
	}
	if n := tb.Held(); n != 0 {
		t.Fatalf("table not empty: %d", n)
	}
}

func TestLockSetIdleHonoursTimeoutWhilePending(t *testing.T) {
	tb := New()
	done := tb.MarkPending("p")
	defer done()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := tb.LockSetIdle(ctx, []string{"p"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
}

func TestHoldFromContext(t *testing.T) {
	tb := New()
	h, _ := tb.LockSet(context.Background(), []string{"z", "y"})
	defer h.Release()
	ctx := WithHold(context.Background(), h)
	if got := HoldFrom(ctx); got != h || !got.Has("y") || got.Has("q") {
		t.Fatal("HoldFrom/Has wrong")
	}
	if HoldFrom(context.Background()) != nil {
		t.Fatal("HoldFrom on a bare ctx should be nil")
	}
}
