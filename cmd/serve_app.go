// file: cmd/serve_app.go
// version: 1.1.0
// guid: c0933565-208e-4f5b-b8ec-3af1ebc3ee07
// last-edited: 2026-11-08

package cmd

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/module"
)

// serveLog logs the composed serve path.
var serveLog = logger.New("serve")

// runComposedApp is serve's composition root (06 §6.11.1 step 2): it builds
// app.New(ctx, opts, books.Module()), installs the App's process defaults
// (the default logger, the OTel globals), binds books' store and served
// registry, and runs the App until SIGINT or SIGTERM. The kernel opens the
// store at P2 and owns the listeners and the stop order within one shutdown
// budget (06 §6.5.2, 05-health §5H.4); books' Register runs HEAD's settings
// boot over that store, and its repairs hook closes books' store before the
// kernel closes the core DB.
func runComposedApp(ctx context.Context, in books.ServeInputs) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	mod := books.Module()
	opts, err := books.Options(mod, in)
	if err != nil {
		return err
	}
	// A failure before Run leaves the server and store Register built with
	// nothing to stop them, and the App's metrics Provider, logging and core
	// DB open, so the OTLP export and the span batcher (the failing boot's
	// last spans) would be dropped at exit. Release books, then close the
	// App: the order Run's own stop ends with (hooks, then core). Release
	// after the start hooks have begun, and Close after Run has begun, do
	// nothing, because Run's stop order owns both, so this runs on every
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
	if err := books.Attach(mod, a); err != nil {
		return err
	}

	runErr := a.Run(ctx)
	// A Run that failed before the start hooks began leaves the server and
	// store unstarted; Release tears them down and closes Stopped before it
	// returns.
	books.Release(mod)
	if !booksStopped(mod) {
		serveLog.Error("books' server stop hook was still running at the end of the shutdown budget; its store closed under it")
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
// abandoned it at the end of the shutdown budget (06 §6.5.2). Books' repairs
// hook, stopped after it, has then closed books' store, and the kernel the
// core DB, with no extra wait:
//
//   - The budget is the whole stop window (05-health §5H.4). The unit's
//     TimeoutStopSec is 30s against the kernel's 25s default budget, so a
//     wait for the server would be cut short by SIGKILL before the store's
//     Close, whose first act is a WAL sync, ran. The 30s wait serve had did
//     exactly that (R1-15 review m2).
//   - Closing under a running StopHook is safe. Every Pebble access holds a
//     lease on books' GatedDB, and every store books opens over it
//     (activity, embedding, AI scan) shares that DB and closes as a no-op.
//     PebbleStore.Close refuses new leases, syncs the WAL and waits for held
//     ones (DefaultCloseDrainTimeout); a write after that gets an error
//     wrapping pebble.ErrClosed, never a use-after-close panic
//     (internal/database/pebble_gate.go; TestStoreCloseUnderAbandonedStop
//     drives it through books' own wiring). The kernel's storage close then
//     drains core's own leases up to core.storage.close_timeout and leaves
//     the engine open, its WAL synced, if one outlives that.
func booksStopped(mod module.Module) bool {
	select {
	case <-books.Stopped(mod):
		return true
	default:
		return false
	}
}
