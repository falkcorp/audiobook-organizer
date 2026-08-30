// file: internal/server/bg_wg_test.go
// version: 1.0.0
// guid: 3a2f7c41-9b6d-4e18-8c05-1d7a4e2b9f63
// last-edited: 2026-08-30

package server

import (
	"slices"
	"sync"
	"testing"
	"time"
)

// TestNamedWaitGroupGoRegistersNameWhileRunning is the direct unit test for
// the name registry that namedWaitGroup exists for. Its production consumer
// is the 30s shutdown-grace log in Server.Shutdown
// (server_lifecycle.go: "still_running", s.bgWG.Running()), which is not
// itself exercised by any test — cache_warmers_bgwg_test.go only calls
// Running() inside a t.Fatalf on timeout, so on the happy path it never
// asserts that a name appears at all.
//
// The assertion that matters for Go(): the name must be visible to a
// concurrent Running() from the moment Go returns, because Go performs Add
// synchronously in the caller before starting the goroutine. If Go ever moved
// the Add inside the goroutine, this test would flake on the first check and
// Wait() could return while work was still in flight.
func TestNamedWaitGroupGoRegistersNameWhileRunning(t *testing.T) {
	var n namedWaitGroup

	release := make(chan struct{})
	started := make(chan struct{})

	n.Go("slow-worker", func() {
		close(started)
		<-release
	})

	// Registered synchronously: no wait for the goroutine to be scheduled.
	if got := n.Running(); !slices.Contains(got, "slow-worker") {
		t.Fatalf("Running() = %v immediately after Go; want it to contain %q", got, "slow-worker")
	}

	<-started
	if got := n.Running(); !slices.Contains(got, "slow-worker") {
		t.Fatalf("Running() = %v while fn is executing; want it to contain %q", got, "slow-worker")
	}

	close(release)

	done := make(chan struct{})
	go func() {
		n.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Wait() did not return after fn finished — still running: %v", n.Running())
	}

	if got := n.Running(); len(got) != 0 {
		t.Fatalf("Running() = %v after Wait(); want empty (Go must deregister the name)", got)
	}
}

// TestNamedWaitGroupGoDeregistersOnPanic pins the defer ordering inside Go:
// Done runs via defer, so a panicking fn still deregisters its name and
// decrements the counter rather than wedging Shutdown's Wait forever. This is
// the property the converted cache warmers rely on — their own
// defer warmerRecover(...) runs first (inside fn), so in production the panic
// is recovered before Go's deferred Done fires.
func TestNamedWaitGroupGoDeregistersOnPanic(t *testing.T) {
	var n namedWaitGroup

	var wg sync.WaitGroup
	wg.Add(1)
	n.Go("panicky", func() {
		defer wg.Done()
		defer func() { _ = recover() }()
		panic("boom")
	})
	wg.Wait()

	done := make(chan struct{})
	go func() {
		n.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Wait() did not return after a recovered panic — still running: %v", n.Running())
	}

	if got := n.Running(); len(got) != 0 {
		t.Fatalf("Running() = %v after a recovered panic; want empty", got)
	}
}

// TestNamedWaitGroupGoSameNameTwice covers the duplicate-name accounting the
// registry documents: two goroutines under one name must both appear in
// Running(), and Wait() must not return until both have finished.
func TestNamedWaitGroupGoSameNameTwice(t *testing.T) {
	var n namedWaitGroup

	release := make(chan struct{})
	n.Go("twin", func() { <-release })
	n.Go("twin", func() { <-release })

	got := n.Running()
	count := 0
	for _, name := range got {
		if name == "twin" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("Running() = %v; want %q twice, got %d", got, "twin", count)
	}

	close(release)
	n.Wait()

	if got := n.Running(); len(got) != 0 {
		t.Fatalf("Running() = %v after Wait(); want empty", got)
	}
}
