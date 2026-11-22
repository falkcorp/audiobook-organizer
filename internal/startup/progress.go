// file: internal/startup/progress.go
// version: 1.0.0
// guid: 5f0f3a52-8f0e-4f0e-9d2b-3d4a8c6b1e77
// last-edited: 2026-11-22

package startup

import "context"

// progressKey is the context key of a startup step's progress hook.
type progressKey struct{}

// Progress reports one unit of progress of the startup step running under
// ctx: a page of rows read, a batch written, a chunk copied. Every unit of a
// loop that scales with the library calls it, so the step asks the service
// manager for start time per unit of work and never from a timer
// (05-health §5H.3.4: a hung step must still be killed once its own budget
// runs out). It is a no-op without RunWithProgress above it, so the same
// code serves a boot and a command that runs it outside one, and it is safe
// to call from any goroutine: Pebble's parallel page walks call it from their
// workers.
func Progress(ctx context.Context) {
	if f, _ := ctx.Value(progressKey{}).(func()); f != nil {
		f()
	}
}

// RunWithProgress runs work, and calls extend(step) on the caller's goroutine
// before work starts and once more for every Progress signal work raises
// under the ctx it is handed (signals that arrive while an extension is being
// made coalesce into one). work runs on a goroutine of its own, so a
// Progress call only records the signal, from whichever goroutine raised it,
// and the request is always made here: the extension is not safe to call
// from two goroutines at a time (ExtendStart's contract). It returns work's
// error once work returned and every signal it raised has been answered.
//
// A nil extend runs work unobserved.
func RunWithProgress(ctx context.Context, step string, extend func(step string), work func(ctx context.Context) error) error {
	if extend == nil {
		return work(ctx)
	}
	extend(step)
	signal := make(chan struct{}, 1)
	pctx := context.WithValue(ctx, progressKey{}, func() {
		select {
		case signal <- struct{}{}:
		default: // one is already waiting for its extension
		}
	})
	done := make(chan error, 1)
	go func() { done <- work(pctx) }()
	for {
		select {
		case <-signal:
			extend(step)
		case err := <-done:
			// A signal raised just before work returned still gets its
			// extension: the unit it reports is finished and the step after
			// it has not asked yet.
			select {
			case <-signal:
				extend(step)
			default:
			}
			return err
		}
	}
}
