// file: internal/plugins/maintenance/bounded_call.go
// version: 1.0.0
// guid: 2593968b-b7d3-4939-9ff5-351fd51fbf7b
// last-edited: 2026-09-13

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

// errBoundedCallTimeout is wrapped by boundedCall's error when the bound
// elapsed before fn returned. Test with errors.Is.
var errBoundedCallTimeout = errors.New("timed out")

// Call states for boundedCall. The goroutine and the waiter race to move the
// state out of running; whoever wins decides who accounts for the goroutine.
const (
	boundedRunning int32 = iota
	boundedFinished
	boundedAbandoned
)

// boundedCall runs fn in a goroutine and waits at most bound for it, or until
// ctx is done. It exists for calls that cannot be interrupted: a tag parser
// running in TagLib WASM, a blocking read on a stuck filesystem. The call is
// not canceled when the wait ends, because it cannot be; the goroutine is
// abandoned and either finishes on its own later or leaks for the life of the
// process.
//
// On timeout the error wraps errBoundedCallTimeout; on cancellation it is
// ctx.Err(). If abandoned is non-nil it counts goroutines given up on that
// have not yet returned: it is incremented when the wait gives up and
// decremented when the abandoned fn finally returns, so a caller can cap how
// many pile up. The result channel is buffered, so an abandoned goroutine that
// does return can always send and exit.
//
// Two private copies of this shape existed before it: extractWithTimeout
// (duration_reextract.go, now built on this) and processFileBounded in
// internal/scanner/process_file.go.
func boundedCall[T any](ctx context.Context, bound time.Duration, abandoned *atomic.Int64, fn func() (T, error)) (T, error) {
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	var state atomic.Int32
	go func() {
		v, err := fn()
		ch <- result{v, err}
		if !state.CompareAndSwap(boundedRunning, boundedFinished) && abandoned != nil {
			abandoned.Add(-1) // the waiter gave up on us and counted us; uncount
		}
	}()

	timer := time.NewTimer(bound)
	defer timer.Stop()

	var giveUp error
	select {
	case r := <-ch:
		return r.v, r.err
	case <-timer.C:
		giveUp = fmt.Errorf("%w after %v (uncancellable call abandoned)", errBoundedCallTimeout, bound)
	case <-ctx.Done():
		giveUp = ctx.Err()
	}
	// Count first, then claim: if the goroutine finished in the meantime the
	// claim fails, we uncount, and its result is already in ch.
	if abandoned != nil {
		abandoned.Add(1)
	}
	if !state.CompareAndSwap(boundedRunning, boundedAbandoned) {
		if abandoned != nil {
			abandoned.Add(-1)
		}
		r := <-ch
		return r.v, r.err
	}
	var zero T
	return zero, giveUp
}
