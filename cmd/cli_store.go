// file: cmd/cli_store.go
// version: 1.0.2
// guid: bd8ce310-ae03-4f86-893a-7b69f8b973bb
// last-edited: 2026-11-10

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/spf13/cobra"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// openCLIStore is how every ABO CLI subcommand that reads or writes the
// library store opens it (06 §6.4.6; R1-25, TestCLISubcommandsUseOfflineOpen
// enforces it): through core's guard-aware offline open, never one of its
// own. A var so a test can stub it. The returned closer takes no arguments
// and reports its own failures, so `defer closer()` is enough.
var openCLIStore = openOfflineStore

// cliStore is what the CLI subcommands do with the store openCLIStore opens,
// and nothing more: the union of each subcommand's own role interface, so
// every method in it is one a subcommand calls. Not the wide database.Store,
// which TestStoreReferenceCount ratchets down, and not the author and series
// sub-interfaces it embedded until the R1-25 review (D13), which carried
// some forty methods for twelve calls. A subcommand that needs another
// method adds it to its own role.
type cliStore interface {
	seedStore           // seed: its authors, series and books, and its purge
	diagnosticsCLIStore // diagnostics query and cleanup-invalid
	benchAuthorReader   // the dedup bench (built with -tags bench)
	// transcode-skip's one setting per file.
	SetSetting(key, value, typ string, isSecret bool) error
}

// benchAuthorReader is what the dedup bench's extractAuthorData reads. It is
// declared here rather than beside it because that file builds only with
// -tags bench, and cliStore names it in every build.
type benchAuthorReader interface {
	GetAllAuthors() ([]database.Author, error)
	GetAllAuthorBookCounts() (map[int]int, error)
	GetBooksByAuthorIDWithRoleCore(authorID int) ([]database.BookCore, error)
}

// openOfflineStore opens the store the loaded config names through
// app.OpenOffline, which refuses what a serve boot would refuse and more: a
// staged or in-flight swap (restorejournal.ErrSwapPending), a store that was
// never started (storage.ErrNoStore, an empty store) and a store with
// migration steps still pending (storage.ErrOfflinePendingSteps), the last
// two telling the user to start the service once first. A read-only open
// writes nothing; a read-write open discards the HNSW clean-stop marker
// (database.DiscardHNSWCleanStop) and runs books' boot repairs, the writes
// HEAD's standalone open made (RunBootRepairs). Either registers the store
// as the process-wide global store for the code paths that still read it.
//
// Every failure after the offline open succeeded closes what was opened.
func openOfflineStore(ctx context.Context, readOnly bool) (cliStore, func(), error) {
	opts, err := offlineOptions()
	if err != nil {
		return nil, nil, err
	}
	o, err := app.OpenOffline(ctx, opts, readOnly, books.Module())
	if err != nil {
		return nil, nil, err
	}
	if !readOnly {
		// The HNSW clean-stop marker beside the store vouches that the
		// vector snapshot is the graph of its rows. This process writes rows
		// with no HNSW store to count them, so the marker goes before the
		// first write (the boot repairs below), as database.OpenStandalone
		// takes it for the CLI opens this one replaced; after the offline
		// open, so a refused open leaves it, and under the store lock that
		// open holds, so no serve writes it again until this process closes.
		// A marker that may still vouch refuses the open.
		if err := database.DiscardHNSWCleanStop(opts.R1StoragePath); err != nil {
			return nil, nil, errors.Join(fmt.Errorf("discard the HNSW clean-stop marker before writing: %w", err), o.Close())
		}
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
// too. The state directory is resolved first (config.EnsureSecureStateDir),
// as books.OfflineOptions resolves it for restore-offline: where the default
// cannot be created it falls back to the database directory, and the slot a
// staged swap is in is there, not under the unresolved default. It returns
// that directory, so a caller that writes (reserved-prefs --set or
// --delete) applies the owner rule to the same one
// (storage.RefuseNotStateDirOwner).
func refuseBusySwapSlot() (stateDir string, err error) {
	dir, err := config.EnsureSecureStateDir(config.Snapshot().DatabasePath)
	if err != nil {
		return "", fmt.Errorf("resolve the state directory %s, where the swap slot is: %w", dir, err)
	}
	st, err := restorejournal.ReadSlot(vfs.Default, dir)
	if err != nil {
		return "", fmt.Errorf("read the swap slot under %s: %w", dir, err)
	}
	return dir, st.Err()
}
