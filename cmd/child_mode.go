// file: cmd/child_mode.go
// version: 1.3.6
// guid: 8c1d2e3f-4a5b-6c7d-8e9f-0a1b2c3d4e5f
// last-edited: 2026-11-10

package cmd

import (
	"context"
	"errors"
	"fmt"
	"github.com/falkcorp/audiobook-organizer/internal/books"
	"os"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server"
)

// newServer is already defined in root.go as a package-level var pointing
// to server.NewServer; we use it here to share the test override.

// initializeChildStore is how the op-runner child opens the live store. The child is not an offline CLI subcommand: the
// serving parent re-execs it while the store is open under the parent's
// process, so it keeps the standalone opener (app.OpenOffline refuses a
// store a running service holds). It passes the resolved state directory so
// the opener reads the swap slot first, and refuses while a swap is staged
// or in flight (R1-25; TestCLISubcommandsUseOfflineOpen,
// TestRunOperationRunner_StagedSwapRefusesTheOpen,
// TestRunOperationRunner_ResolvesTheStateDirectory). A var so a test can stub
// it.
var initializeChildStore = database.InitializeStoreWith

// errChildOverIdentityStore is the child's refusal to run over a store whose
// identity cutover (M3) is done. Under core identity the parent builds its
// server with server.WithIdentity over handles only core's boot makes (its auth
// service, its identity view, the bridge); the child is a fresh process that
// opens the store standalone and has none of them. Its server would run HEAD's
// own gates, including the user-count first-run bypass, over a store whose user,
// role, session and API-key rows are drained (empty), so every one of those
// gates would read "no accounts yet" and open. It refuses instead.
var errChildOverIdentityStore = errors.New("child mode: this store's identity cutover is done, so its accounts, " +
	"roles, sessions and API keys are core's, and an operation child cannot be given core's identity; refusing to " +
	"run it under books' own authentication gates, which read the drained user tables as an install with no " +
	"accounts. Run the operation in the serving process (Isolate: false)")

// refuseChildOverIdentityStore fails closed: a store whose identity cutover is
// done, one that cannot say, and a read that fails all refuse. Only a store the
// cutover has not run on (HEAD's own tables hold the accounts) proceeds, which
// is the one store HEAD's gates are correct for.
func refuseChildOverIdentityStore(ctx context.Context, store database.MigrationReader) error {
	if store == nil {
		return errors.New("child mode: the store cannot report whether its identity cutover is done; refusing to run")
	}
	done, err := store.DataMigrationDone(ctx, cutover.IdentityCutoverID)
	if err != nil {
		return fmt.Errorf("child mode: read the identity cutover's state: %w", err)
	}
	if done {
		return errChildOverIdentityStore
	}
	return nil
}

// init registers the parent-side environment provider for
// registry.runSubprocess. When the parent re-execs the binary as a child,
// it appends these KEY=VALUE strings to the child's env so the child can
// open the same store and load the same config the parent has.
func init() {
	registry.ChildEnvFunc = func() []string {
		// WHY Snapshot: consistent multi-field read; the parent may call UpdateConfig
		// concurrently while this closure executes.
		c := config.Snapshot()
		return []string{
			fmt.Sprintf("%s=%s", registry.EnvChildDBPath, c.DatabasePath),
			fmt.Sprintf("%s=%s", registry.EnvChildDBType, c.DatabaseType),
			fmt.Sprintf("%s=%s", registry.EnvChildRootDir, c.RootDir),
		}
	}
}

// RunOperationRunner is the entry point for operation-runner child mode.
// It is invoked from main.go before cobra is given a chance to parse args.
// On success it never returns (registry.RunChildMode calls os.Exit).
//
// The child re-uses the same server.NewServer construction path as the
// parent so every plugin OperationDef is registered. It does NOT call
// Server.Start — no HTTP listener, no scheduler, no background workers
// are launched. The child connects to the parent's unix socket
// (UOS_SOCKET), runs a single op, and exits.
func RunOperationRunner() {
	// Resolve database configuration from environment overrides set by the
	// parent. Fall back to whatever may already be in AppConfig, then to a
	// reasonable default — but in practice the parent always sets them.
	// WHY Mutate: these writes happen before any goroutines start in child mode,
	// but Mutate is still correct and cheap; it also satisfies the race detector.
	config.Mutate(func(c *config.Config) {
		if v := os.Getenv(registry.EnvChildDBPath); v != "" {
			c.DatabasePath = v
		}
		if v := os.Getenv(registry.EnvChildDBType); v != "" {
			c.DatabaseType = v
		}
		if v := os.Getenv(registry.EnvChildRootDir); v != "" {
			c.RootDir = v
		}
		if c.DatabasePath == "" {
			c.DatabasePath = "audiobooks.pebble"
		}
		if c.DatabaseType == "" {
			c.DatabaseType = "pebble"
		}
	})

	// The HNSW clean-stop marker beside the database is the parent's: this
	// open must not take it (database.DisableHNSWCleanStopClaim). Every path
	// out of this function exits the process today, so the deferred restore
	// only matters to a future caller that returns; it keeps the switch from
	// outliving the run.
	restoreClaim := database.DisableHNSWCleanStopClaim()
	defer restoreClaim()
	// The state directory, resolved as the parent resolved it
	// (resolveChildPaths: core's step 0 over the parent's store path and the same environment): nothing in this fresh process has, so SecureStateDir would be
	// the unresolved default, and where that cannot be created the parent
	// fell back to the database directory, where the swap slot and the swap
	// lock are. Everything later in the child (the updater's slot check
	// among them) reads the resolved directory too.
	dbPath, stateDir, err := resolveChildPaths(os.Getenv(registry.EnvChildDBPath))
	if err != nil {
		fmt.Fprintf(os.Stderr, "child mode: resolve the state directory %s: %v\n", stateDir, err)
		os.Exit(2)
	}
	snap := config.Snapshot()
	// The identity cutover is checked as the store opens, before the opener's
	// opv3 reconcile writes to it and before it becomes the global store: see
	// errChildOverIdentityStore. newServer below is built without
	// server.WithIdentity, so this refusal (database.StandaloneOptions.Admit) is
	// what keeps it off a migrated store. A refusal comes back as the opener's
	// error, which closes the store.
	store, err := initializeChildStore(snap.DatabaseType, dbPath,
		books.StandaloneOptions(database.StandaloneOptions{
			StateDir: stateDir,
			Admit: func(s database.MigrationReader) error {
				return refuseChildOverIdentityStore(context.Background(), s)
			},
		}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "child mode: initializeChildStore: %v\n", err)
		os.Exit(2)
	}
	// HEAD loaded the stored settings here (config.LoadConfigFromDatabase);
	// F3-07 deleted that loader, so the child's settings are what the parent
	// hands it: the flags, the environment and config.yaml, plus the library
	// root the parent passes in EnvChildRootDir, which the maintenance plugin
	// gate in server.go requires to be non-empty.
	if config.Snapshot().RootDir == "" {
		if v := os.Getenv(registry.EnvChildRootDir); v != "" {
			config.Mutate(func(c *config.Config) { c.RootDir = v })
		}
	}

	// An op run in this child process counts its Google Books lookups on the
	// same persisted daily budget as the parent: without the attach it would
	// count against an in-memory zero and bypass the shared quota.
	server.AttachGoogleBooksBudgetStore(store)

	srv, err := newServer(store)
	if err != nil {
		fmt.Fprintf(os.Stderr, "child mode: newServer: %v\n", err)
		os.Exit(2)
	}
	reg := srv.OpRegistry()
	if reg == nil {
		fmt.Fprintln(os.Stderr, "child mode: server has no opRegistry")
		os.Exit(2)
	}

	// Never returns.
	registry.RunChildMode(reg)
}
