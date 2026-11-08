// file: cmd/serve_app.go
// version: 1.0.5
// guid: c0933565-208e-4f5b-b8ec-3af1ebc3ee07
// last-edited: 2026-11-08

package cmd

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/module"
)

// injectStartExtender hands the store the start-timeout request it makes
// before its long steps: app.ExtendStart, which is stateless because the
// store opens before app.New builds the App (R1-23 moves the open inside it
// and retires this seam). It is serve's only; other commands run without one.
func injectStartExtender() { database.SetStartExtender(app.ExtendStart) }

// serveLog logs the composed serve path.
var serveLog = logger.New("serve")

// runComposedApp is serve's composition root (06 §6.11.1 step 2): with
// serve's pre-server steps done and store open, it builds
// app.New(ctx, opts, books.Module(store)), installs the App's process
// defaults (the default logger, the OTel globals), binds books' served
// registry, and runs the App until SIGINT or SIGTERM. The kernel owns the
// listeners and the stop order within one shutdown budget (06 §6.5.2,
// 05-health §5H.4).
func runComposedApp(ctx context.Context, store database.Store, in books.ServeInputs) error {
	pebble, ok := store.(*database.PebbleStore)
	if !ok {
		return fmt.Errorf("serve: books needs the Pebble store, got %T", store)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	mod := books.Module(pebble)
	opts, err := books.Options(mod, in)
	if err != nil {
		return err
	}
	// A failure before Run leaves the server Register built with nothing to
	// stop it, and the App's metrics Provider and logging open, so the OTLP
	// export and the span batcher (the failing boot's last spans) would be
	// dropped at exit. Release the server, then close the App, before the
	// store closes: the order Run's own stop ends with (hooks, then core).
	// Release after the start hook has begun, and Close after Run has begun,
	// do nothing, because Run's stop order owns both, so this runs on every
	// return.
	var a *app.App
	defer func() {
		books.Release(mod)
		if a != nil {
			closeUnrunApp(a)
		}
	}()
	a, err = app.New(ctx, opts, mod)
	if err != nil {
		return err
	}
	restore, err := app.InstallProcessDefaults(a)
	if err != nil {
		return err
	}
	defer restore()
	// Process-scoped, so the throttle registry's store lifetime matches the
	// process; after the server is built, as before. Deliberately NOT inside
	// NewServer -- see the doc comment.
	server.AttachProviderThrottleStore(store)
	if err := books.Attach(mod, a); err != nil {
		return err
	}

	runErr := a.Run(ctx)
	// A Run that failed before the start hook began leaves the server
	// unstarted; Release tears it down and closes Stopped before it returns.
	books.Release(mod)
	if !booksStopped(mod) {
		serveLog.Error("books' stop hook was still running at the end of the shutdown budget; closing the store under it")
	}
	return runErr
}

// closeUnrunApp closes an App whose Run never began, releasing what New
// built (06 §6.4.5), the metrics Provider among it, so its last OTLP export
// and span batch are flushed rather than dropped at exit. After Run has
// begun, App.Close does nothing and returns nil. No timeout is set here:
// Close bounds itself with core.shutdown.budget (R1Values) and ignores ctx's
// deadline, so a context bound would only read as one it does not have.
func closeUnrunApp(a *app.App) {
	if err := a.Close(context.Background()); err != nil {
		serveLog.Warn("closing the app after a failed boot: %v", err)
	}
}

// booksStopped reports whether books' server has finished its stop, without
// waiting. When Run returns, books' StopHook has returned unless the kernel
// abandoned it at the end of the shutdown budget (06 §6.5.2). serve then
// closes the store at once, with no extra wait:
//
//   - The budget is the whole stop window (05-health §5H.4). The unit's
//     TimeoutStopSec is 30s against the kernel's 25s default budget, so a
//     wait after Run is cut short by SIGKILL, and the store's Close, whose
//     first act is a WAL sync, never runs. The 30s wait that was here did
//     exactly that (R1-15 review m2).
//   - Closing under a running StopHook is safe. Every Pebble access holds a
//     lease on the main store's GatedDB, and every store books opens over it
//     (activity, embedding, AI scan) shares that DB and closes as a no-op.
//     PebbleStore.Close refuses new leases, syncs the WAL, waits for held
//     ones (DefaultCloseDrainTimeout), and leaves the engine open with the
//     WAL synced if one outlives the wait. A write after that gets an error
//     wrapping pebble.ErrClosed, never a use-after-close panic
//     (internal/database/pebble_gate.go; TestStoreCloseUnderAbandonedStop
//     drives it through books' own wiring).
func booksStopped(mod module.Module) bool {
	select {
	case <-books.Stopped(mod):
		return true
	default:
		return false
	}
}
