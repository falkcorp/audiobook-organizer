// file: cmd/cli_store.go
// version: 1.0.0
// guid: bd8ce310-ae03-4f86-893a-7b69f8b973bb
// last-edited: 2026-11-08

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/spf13/cobra"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// openCLIStore is how every ABO CLI subcommand that reads or writes the
// library store opens it (06 §6.4.6; R1-25, TestCLISubcommandsUseOfflineOpen
// enforces it): through core's guard-aware offline open, never one of its
// own. A var so a test can stub it. The returned closer takes no arguments
// and reports its own failures, so `defer closer()` is enough.
var openCLIStore = openOfflineStore

// openOfflineStore opens the store the loaded config names through
// app.OpenOffline, which refuses what a serve boot would refuse and more: a
// staged or in-flight swap (restorejournal.ErrSwapPending), a store that was
// never started (storage.ErrNoStore, an empty store) and a store with
// migration steps still pending (storage.ErrOfflinePendingSteps), the last
// two telling the user to start the service once first. A read-only open
// writes nothing; a read-write open runs books' boot repairs, the writes
// HEAD's standalone open made (RunBootRepairs), and registers the store as
// the process-wide global store for the code paths that still read it.
//
// Every failure after the offline open succeeded closes what was opened.
func openOfflineStore(ctx context.Context, readOnly bool) (database.Store, func(), error) {
	opts, err := offlineOptions()
	if err != nil {
		return nil, nil, err
	}
	o, err := app.OpenOffline(ctx, opts, readOnly, books.Module())
	if err != nil {
		return nil, nil, err
	}
	h, err := o.Storage(books.ID)
	if err != nil {
		return nil, nil, errors.Join(err, o.Close())
	}
	store, err := database.NewBooksStore(h.Legacy)
	if err != nil {
		return nil, nil, errors.Join(fmt.Errorf("open the books store: %w", err), o.Close())
	}
	if !readOnly {
		if err := store.RunBootRepairs(ctx); err != nil {
			return nil, nil, errors.Join(fmt.Errorf("run the store's boot repairs: %w", err), store.Close(), o.Close())
		}
	}
	database.SetGlobalStore(store)
	closer := func() {
		database.SetGlobalStore(nil)
		if err := errors.Join(store.Close(), o.Close()); err != nil {
			fmt.Fprintf(os.Stderr, "close store: %v\n", err)
		}
	}
	return store, closer, nil
}

// commandContext is cmd's context, context.Background() when the command
// runs without one (a test calling RunE directly).
func commandContext(cmd *cobra.Command) context.Context {
	if cmd != nil {
		if ctx := cmd.Context(); ctx != nil {
			return ctx
		}
	}
	return context.Background()
}

// refuseBusySwapSlot is the guard of the CLI paths that bypass
// app.OpenOffline on purpose (diagnostics query --raw, reserved-prefs: they
// must work on a store the guard refuses): they open the store directory
// itself, so they must at least not touch one a staged or in-flight swap is
// about to replace or has half replaced. A slot that cannot be read refuses
// too.
func refuseBusySwapSlot() error {
	dir := config.SecureStateDir()
	st, err := restorejournal.ReadSlot(vfs.Default, dir)
	if err != nil {
		return fmt.Errorf("read the swap slot under %s: %w", dir, err)
	}
	return st.Err()
}
