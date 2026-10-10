// file: internal/operations/warmup_gate.go
// version: 1.0.1
// guid: 9d3a7e52-6c14-4b0f-8a21-5f7e0c9b3d84
// last-edited: 2026-10-10

package operations

import (
	"context"
	"log/slog"
	"time"
)

// WarmupWaitTimeout bounds how long an operation waits for the store's startup
// warmup before it proceeds anyway. Memdb warmup on a large library takes
// roughly 130-200 seconds; this leaves headroom without ever blocking forever.
const WarmupWaitTimeout = 300 * time.Second

// WarmupStatusMessage is the progress message an operation shows while it waits
// for the store's startup warmup.
const WarmupStatusMessage = "waiting for startup warmup"

// WarmupWaiter is implemented by a store whose reads are slow until an
// in-memory layer has warmed after start (the Pebble store). WaitForWarmupCtx
// returns nil once warmup is finished and ctx.Err() if ctx ends first.
type WarmupWaiter interface {
	WaitForWarmupCtx(ctx context.Context) error
}

// WaitForWarmup holds an operation's body until the store has finished its
// startup warmup, so the body does not run against the slow fallback read path.
//
//   - waiter nil (no store in the decorator chain implements WarmupWaiter):
//     returns nil at once.
//   - warmup already finished: returns nil at once, without calling onWait.
//   - otherwise onWait is called once (it posts WarmupStatusMessage to the
//     operation) and the call blocks until warmup finishes, timeout elapses, or
//     ctx is canceled. A timeout logs a warning once and returns nil: the
//     operation proceeds on the slow path rather than blocking forever. Only a
//     ctx cancel returns an error (ctx.Err()).
//
// The caller's ctx must not carry the operation's own run timeout: the wait is
// not part of the run, so it must neither spend that budget nor be cut short by
// it (the registry waits on the cancel-only context and arms the timeout after).
func WaitForWarmup(ctx context.Context, waiter WarmupWaiter, timeout time.Duration, log *slog.Logger,
	onWait func()) error {
	if waiter == nil {
		return nil
	}
	// Fast path: a zero-length wait tells us whether warmup is already done
	// without announcing a wait that never happens.
	probeCtx, probeCancel := context.WithCancel(ctx)
	probeCancel()
	if err := waiter.WaitForWarmupCtx(probeCtx); err == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if onWait != nil {
		onWait()
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	err := waiter.WaitForWarmupCtx(waitCtx)
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	// Our own deadline fired, not the caller's cancel.
	if log != nil {
		log.Warn("operation: startup warmup did not finish in time; proceeding on the slow read path",
			"timeout", timeout)
	}
	return nil
}
