// file: cmd/serve_app.go
// version: 1.0.1
// guid: c0933565-208e-4f5b-b8ec-3af1ebc3ee07
// last-edited: 2026-11-08

package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/mediacore/app"
)

// storeCloseWait bounds how long serve waits, after Run returns, for books'
// server to finish its stop before the store closes. The kernel abandons a
// stop hook still running at the end of the shutdown budget; closing the
// store under it would turn its remaining writes into Pebble's
// use-after-close panic.
const storeCloseWait = 30 * time.Second

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
	// stop it: release it before the store closes. After the start hook has
	// begun, Release does nothing.
	defer books.Release(mod)
	a, err := app.New(ctx, opts, mod)
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
	// unstarted, and Stopped closes only when it is released.
	books.Release(mod)
	select {
	case <-books.Stopped(mod):
	case <-time.After(storeCloseWait):
		slog.Error("serve: books' server had not finished stopping; closing the store anyway",
			"waited", storeCloseWait.String())
	}
	return runErr
}
