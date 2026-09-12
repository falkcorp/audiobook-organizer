// file: internal/database/test_deadline_test.go
// version: 1.0.0
// guid: e4cb4c5c-b25d-414e-815a-01772db5bb16
// last-edited: 2026-09-12

package database

import (
	"context"
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
// (t.Deadline, from -timeout) is closer than that, so the helper's t.Fatalf
// always fires before go test's timeout panic and leaves testWaitCleanupMargin
// for t.Cleanup / deferred store closes to run.

// testWaitDefault is the longest any single bounded wait may block. Every wait
// routed through these helpers joins goroutines the test itself started or a
// signal it expects promptly; the slowest of them takes a few seconds under
// -race, so 30s is generous headroom while still failing in seconds-to-tens of
// seconds instead of minutes.
const testWaitDefault = 30 * time.Second

// testWaitCleanupMargin is reserved out of the test's remaining deadline so the
// failure is reported and cleanups run before go test's timeout panic.
const testWaitCleanupMargin = 5 * time.Second

// testWaitMinBound keeps the bound positive when the deadline is already inside
// the cleanup margin.
const testWaitMinBound = 100 * time.Millisecond

// testWaitBound returns how long a single bounded wait may block in t:
// min(testWaitDefault, time.Until(t.Deadline()) - testWaitCleanupMargin),
// floored at testWaitMinBound. With no deadline (-timeout 0) it is
// testWaitDefault.
func testWaitBound(t *testing.T) time.Duration {
	t.Helper()
	bound := testWaitDefault
	if deadline, ok := t.Deadline(); ok {
		if remaining := time.Until(deadline) - testWaitCleanupMargin; remaining < bound {
			bound = max(remaining, testWaitMinBound)
		}
	}
	return bound
}

// awaitOrFatal runs block in a goroutine and waits for it to return under a
// context derived from t.Context() with testWaitBound as its timeout. On
// timeout it fails the test naming helper and what. The goroutine running block
// is leaked on timeout; the test has already failed, and a deadlocked wait
// cannot be interrupted from outside.
func awaitOrFatal(t *testing.T, helper, what string, block func()) {
	t.Helper()
	bound := testWaitBound(t)
	ctx, cancel := context.WithTimeout(t.Context(), bound)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		block()
	}()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("%s: still waiting for %s after %v (per-test wait deadline, see test_deadline_test.go): "+
			"the awaited goroutines or signal never arrived, which is a deadlock or a lost signal, not slowness",
			helper, what, bound)
	}
}

// waitGroupOrFatal is a bounded wg.Wait(): it fails the test if wg is not done
// within testWaitBound. what names the goroutines being joined.
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
// test if nothing is received (and ch is not closed) within testWaitBound.
func recvOrFatal[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	var v T
	awaitOrFatal(t, "recvOrFatal", what, func() { v = <-ch })
	return v
}
