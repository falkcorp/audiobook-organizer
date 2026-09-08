// file: internal/server/file_io_pool_shutdown_test.go
// version: 1.0.0
// guid: 6afc6fa0-a6da-44ed-a974-2584e955bc4d
// last-edited: 2026-09-07
//
// Shutdown-safety tests for FileIOPool. Both tests are written to FAIL against
// the pre-2026-09-07 implementation, which guarded submission with a bare
// atomic flag and started its overflow work with an unjoined `go func()`.

package server

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestFileIOPool_StopJoinsOverflowGoroutines pins the invariant stated in
// worker's doc comment: every goroutine the pool starts is enrolled in p.wg, so
// Stop's wg.Wait is a complete join.
//
// Against the old code the overflow goroutine ran under a bare `go func()`,
// Stop waited only for the worker set, and this returned with the overflow work
// still running — after Stop had reported "all jobs complete" and while the
// caller was free to close the database that fn writes to.
func TestFileIOPool_StopJoinsOverflowGoroutines(t *testing.T) {
	const workers = 2
	p := NewFileIOPool(workers)

	// Occupy every worker so nothing drains p.ch. Wait for them to be inside
	// fn rather than merely submitted, otherwise the buffer fill below races
	// the drain and the overflow path may never be reached.
	started := make(chan struct{}, workers)
	release := make(chan struct{})
	for i := range workers {
		p.SubmitTyped(fmt.Sprintf("blocker-%d", i), "hold", func() {
			started <- struct{}{}
			<-release
		})
	}
	for range workers {
		<-started
	}

	// Fill the 500-slot buffer. With both workers parked these all sit in the
	// channel, so the next submit has nowhere to go but the overflow path.
	for i := range 500 {
		p.SubmitTyped(fmt.Sprintf("filler-%d", i), "noop", func() {})
	}

	// The overflow semaphore holds `workers` slots, so this many go down the
	// overflow path without any of them blocking on it.
	var finished atomic.Int64
	for i := range workers {
		p.SubmitTyped(fmt.Sprintf("overflow-%d", i), "slow", func() {
			// Long enough that a Stop which does not join will win the race
			// deterministically, short enough not to slow the suite.
			time.Sleep(100 * time.Millisecond)
			finished.Add(1)
		})
	}

	close(release)
	p.Stop()

	if got := finished.Load(); got != workers {
		t.Fatalf("Stop returned with %d/%d overflow goroutines finished; Stop must join every goroutine the pool starts", got, workers)
	}
}

// TestFileIOPool_SubmitDuringStop_DoesNotPanic hammers the submit/Stop window.
//
// The old guard was `if atomic.LoadInt32(&p.stopped) == 1 { return }` followed
// by a send on p.ch. A submitter that passed the check just before Stop ran
// sent on a closed channel. Nothing recovers that — the pool's only recover()
// is inside worker — so it takes the process down. A panic in any of these
// goroutines fails the test by crashing the binary; there is no assertion to
// make beyond surviving.
//
// Stop must land in the MIDDLE of a running submit stream. An earlier version
// of this test gated the submitters and called Stop immediately after opening
// the gate; Stop won every time, every submitter then read stopped as true and
// returned early, and the test passed against the old code as happily as
// against the new one. It has to be a steady stream with Stop dropped into it.
func TestFileIOPool_SubmitDuringStop_DoesNotPanic(t *testing.T) {
	const (
		rounds     = 200
		submitters = 8
	)
	for range rounds {
		p := NewFileIOPool(4)

		var stop atomic.Bool
		var wg sync.WaitGroup
		for s := range submitters {
			wg.Go(func() {
				for j := 0; !stop.Load(); j++ {
					p.SubmitTyped(fmt.Sprintf("b-%d-%d", s, j), "apply_metadata", func() {})
				}
			})
		}

		// Let the stream reach full rate before cutting it off, so Stop's
		// close(p.ch) collides with sends already in flight.
		time.Sleep(time.Millisecond)
		p.Stop()
		stop.Store(true)
		wg.Wait()
	}
}

// TestFileIOPool_StopIsIdempotent guards the CompareAndSwap semantics that the
// mutex rewrite replaced: a second Stop must return without closing p.ch twice.
func TestFileIOPool_StopIsIdempotent(t *testing.T) {
	p := NewFileIOPool(2)
	p.Stop()
	p.Stop()
	p.Stop()
}
