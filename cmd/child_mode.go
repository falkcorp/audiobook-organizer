// file: cmd/child_mode.go
// version: 1.1.0
// guid: 8c1d2e3f-4a5b-6c7d-8e9f-0a1b2c3d4e5f
// last-edited: 2026-09-11

package cmd

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// newServer is already defined in root.go as a package-level var pointing
// to server.NewServer; we use it here to share the test override.

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

	snap := config.Snapshot()
	store, err := initializeStore(snap.DatabaseType, snap.DatabasePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "child mode: initializeStore: %v\n", err)
		os.Exit(2)
	}
	// Best-effort: load persisted config so plugin registration sees the
	// same AppConfig as the parent (notably RootDir, which the maintenance
	// plugin gate at server.go:413 requires to be non-empty).
	if err := loadConfigFromDB(store); err != nil {
		slog.Warn("child mode: loadConfigFromDB", "err", err)
	}
	if config.Snapshot().RootDir == "" {
		// Re-apply env override after loadConfigFromDB may have reset it.
		if v := os.Getenv(registry.EnvChildRootDir); v != "" {
			config.Mutate(func(c *config.Config) { c.RootDir = v })
		}
	}

	srv := newServer(store)
	reg := srv.OpRegistry()
	if reg == nil {
		fmt.Fprintln(os.Stderr, "child mode: server has no opRegistry")
		os.Exit(2)
	}

	// Never returns.
	registry.RunChildMode(reg)
}
