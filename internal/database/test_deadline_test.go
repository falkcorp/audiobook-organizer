// file: internal/database/test_deadline_test.go
// version: 1.1.0
// guid: e4cb4c5c-b25d-414e-815a-01772db5bb16
// last-edited: 2026-09-26

package database

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"
)

// Per-test deadlines for blocking waits in this package's tests.
//
// A bare wg.Wait() or <-ch in a test never fails: if the thing it waits on
// deadlocks or loses its signal, the test hangs until go test's own -timeout
// fires, burning the whole package budget and reporting a goroutine dump rather
// than the condition that never arrived. embedding_store_chaos_test.go hit
// exactly that (a Close/commitPipeline deadlock surfaced only as "Go Tests
// cancelled"). These helpers bound the wait and fail the test with a message
// naming the helper and what it was waiting for.
//
// The bound is testWaitDefault, shortened when the test's own deadline
// (t.Deadline, from -timeout) is closer than that (see computeWaitBudget).
//
// A timed-out wait NEVER hands control back to the test while the awaited
// goroutines are still running. Handing it back (t.Fatalf / t.FailNow) runs
// the test's deferred calls and t.Cleanup funcs, and in this package those
// close the Pebble store; a worker still inside a store call then dies with
// "panic: pebble: closed", which kills the whole test binary and buries the
// real failure. TestSetReviewItemDecision_ConcurrentDecisionsKeepStatusIndexExact
// failed exactly that way in `make ci` once the package had used 1495s of its
// 25m -timeout and every wait's bound had collapsed to the 100ms floor. So on
// timeout awaitOrFatal:
//
//  1. writes the diagnosis and an all-goroutine dump to stderr at once (a
//     non-verbose t.Errorf is buffered until the test ends and is lost if the
//     process dies first), and marks the test failed with t.Errorf;
//  2. keeps waiting for the goroutines for a grace period (waitBudget.grace);
//     if they exit, it calls t.FailNow, which is safe now because nothing can
//     outlive the test's cleanup;
//  3. if they are still running when the grace expires, it exits the test
//     binary (os.Exit(1)) with a final stderr line saying why. That ends the
//     package run. Before this, a goroutine parked forever on a channel or
//     lock was simply leaked and later tests kept running; a goroutine still
//     using the store crashed the binary with a misleading panic. The helper
//     cannot tell those apart, so it takes the one outcome that is never
//     misleading.
//
// A t.Cleanup-registered join was considered and rejected: most callers close
// their store with a defer, which runs during FailNow's Goexit BEFORE any
// t.Cleanup, so no cleanup can be ordered ahead of it; and joining a truly
// deadlocked goroutine would just hang until go test's alarm.

// testWaitDefault is the longest any single bounded wait may block. Every wait
// routed through these helpers joins goroutines the test itself started or a
// signal it expects promptly; the slowest of them takes a few seconds under
// -race, so 30s is generous headroom while still failing in seconds-to-tens of
// seconds instead of minutes. It also caps the grace period.
const testWaitDefault = 30 * time.Second

// testWaitCleanupMargin is reserved out of the test's remaining deadline when
// computing the bound, so a failure has time to be reported and cleaned up
// before go test's timeout panic.
const testWaitCleanupMargin = 5 * time.Second

// testWaitMinBound keeps the bound positive when the deadline is already inside
// the cleanup margin.
const testWaitMinBound = 100 * time.Millisecond

// testWaitExitMargin is how far before the package deadline the grace period
// ends, so the helper's own exit (and its message) beats go test's alarm.
const testWaitExitMargin = 1 * time.Second

// waitRegime records how a wait's bound was derived, which decides what a
// timeout means.
type waitRegime int

const (
	// waitFull: the full testWaitDefault. A timeout here is a deadlock or a lost
	// signal.
	waitFull waitRegime = iota
	// waitShortened: the package -timeout left less than testWaitDefault plus
	// the cleanup margin, so the bound was cut. A timeout here says the package
	// is running out of time; it is not evidence of a deadlock.
	waitShortened
	// waitFloored: the package deadline was already inside the cleanup margin,
	// so the bound collapsed to testWaitMinBound. Same meaning, more so.
	waitFloored
)

// waitBudget is one bounded wait's time allowance.
type waitBudget struct {
	bound       time.Duration // how long to wait before declaring the wait failed
	grace       time.Duration // how much longer to wait for the goroutines to exit after that
	regime      waitRegime
	hasDeadline bool
	left        time.Duration // time left to the package deadline when the wait began
}

// computeWaitBudget is the pure core of testWaitBudget.
//
//	bound = min(testWaitDefault, left-testWaitCleanupMargin), floored at testWaitMinBound
//	grace = min(testWaitDefault, left-bound-testWaitExitMargin), floored at 0
//
// With no deadline (-timeout 0) both are testWaitDefault.
func computeWaitBudget(deadline time.Time, hasDeadline bool, now time.Time) waitBudget {
	b := waitBudget{bound: testWaitDefault, grace: testWaitDefault, regime: waitFull, hasDeadline: hasDeadline}
	if !hasDeadline {
		return b
	}
	b.left = deadline.Sub(now)
	if remaining := b.left - testWaitCleanupMargin; remaining < b.bound {
		if remaining < testWaitMinBound {
			b.bound, b.regime = testWaitMinBound, waitFloored
		} else {
			b.bound, b.regime = remaining, waitShortened
		}
	}
	b.grace = min(testWaitDefault, max(b.left-b.bound-testWaitExitMargin, 0))
	return b
}

// testWaitBudgetOverride, when non-nil, replaces the computed budget. Only this
// file's child-process tests set it, to force a tiny bound deterministically
// rather than depend on how long the child took to start.
var testWaitBudgetOverride *waitBudget

// testWaitBudget returns the budget for one bounded wait in t.
func testWaitBudget(t *testing.T) waitBudget {
	t.Helper()
	if testWaitBudgetOverride != nil {
		return *testWaitBudgetOverride
	}
	deadline, ok := t.Deadline()
	return computeWaitBudget(deadline, ok, time.Now())
}

// diagnosis says what a timeout under this budget means.
func (b waitBudget) diagnosis() string {
	switch b.regime {
	case waitShortened:
		return fmt.Sprintf("PACKAGE -timeout NEARLY EXHAUSTED, not evidence of a deadlock: %v was left when this "+
			"wait began, so its bound was cut from %v to %v (%v reserved for cleanup). The package is too slow "+
			"for its -timeout; this wait may simply have been starved",
			b.left.Round(time.Millisecond), testWaitDefault, b.bound.Round(time.Millisecond), testWaitCleanupMargin)
	case waitFloored:
		return fmt.Sprintf("PACKAGE -timeout NEARLY EXHAUSTED, not evidence of a deadlock: only %v was left when "+
			"this wait began (inside the %v cleanup margin), so its bound collapsed to the %v floor. The package "+
			"is too slow for its -timeout; this wait was starved",
			b.left.Round(time.Millisecond), testWaitCleanupMargin, testWaitMinBound)
	default:
		return fmt.Sprintf("the awaited goroutines or signal never arrived within the full %v per-wait bound, "+
			"which is a deadlock or a lost signal, not slowness", testWaitDefault)
	}
}

// allGoroutineStacks returns runtime.Stack for every goroutine, growing the
// buffer until the whole dump fits.
func allGoroutineStacks() []byte {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return buf[:n]
		}
		buf = make([]byte, 2*len(buf))
	}
}

// awaitOrFatal runs block in a goroutine and waits up to the budget's bound for
// it to return. On timeout it reports (stderr with a goroutine dump, plus
// t.Errorf), waits up to the grace for block to return and then calls
// t.FailNow; if block is still running after the grace it exits the test
// binary. It never returns or FailNows while block is still running; the file
// comment explains why.
func awaitOrFatal(t *testing.T, helper, what string, block func()) {
	t.Helper()
	budget := testWaitBudget(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		block()
	}()

	bound := time.NewTimer(budget.bound)
	select {
	case <-done:
		bound.Stop()
		return
	case <-bound.C:
	}

	diag := fmt.Sprintf("%s: %s: still waiting for %s after %v (per-test wait bound, see test_deadline_test.go): %s",
		t.Name(), helper, what, budget.bound.Round(time.Millisecond), budget.diagnosis())
	fmt.Fprintf(os.Stderr, "%s\n--- all goroutines at wait timeout (%s) ---\n%s--- end goroutine dump ---\n",
		diag, t.Name(), allGoroutineStacks())
	t.Errorf("%s (goroutine dump written to stderr)", diag)

	expired := time.Now()
	grace := time.NewTimer(budget.grace)
	defer grace.Stop()
	select {
	case <-done:
		t.Logf("%s: %s: %s finished %v after the wait bound expired; failing only now, so this test's deferred "+
			"calls and cleanups cannot close state under them", t.Name(), helper, what,
			time.Since(expired).Round(time.Millisecond))
		t.FailNow()
	case <-grace.C:
	}

	fmt.Fprintf(os.Stderr, "%s: %s: %s still running %v after the wait bound expired; EXITING THE TEST BINARY "+
		"instead of failing the test normally, because that would run this test's deferred calls and t.Cleanup "+
		"funcs, which close state (the Pebble store) those goroutines are still using and would turn this failure "+
		"into an unrelated panic from the closed store. Diagnosis: %s\n",
		t.Name(), helper, what, budget.grace.Round(time.Millisecond), budget.diagnosis())
	os.Exit(1)
}

// waitGroupOrFatal is a bounded wg.Wait(): it fails the test if wg is not done
// within the wait bound. what names the goroutines being joined.
func waitGroupOrFatal(t *testing.T, wg *sync.WaitGroup, what string) {
	t.Helper()
	awaitOrFatal(t, "waitGroupOrFatal", what, wg.Wait)
}

// waitOrFatal bounds an arbitrary blocking call (for example a method that
// blocks on an internal channel) the same way. what names the condition.
func waitOrFatal(t *testing.T, what string, block func()) {
	t.Helper()
	awaitOrFatal(t, "waitOrFatal", what, block)
}

// recvOrFatal is a bounded <-ch: it returns the received value, or fails the
// test if nothing is received (and ch is not closed) within the wait bound. v
// is read only after the receiving goroutine has returned, so it is never
// raced.
func recvOrFatal[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	var v T
	awaitOrFatal(t, "recvOrFatal", what, func() { v = <-ch })
	return v
}
