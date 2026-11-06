// file: internal/database/store_init.go
// version: 1.0.0
// guid: 7ffff60d-2915-4c59-a775-321f8367f69b
// last-edited: 2026-11-06

package database

import (
	"context"
	"fmt"
	"sync"
)

// Global store instance — use GetGlobalStore/SetGlobalStore for concurrent access.
// Direct assignment is allowed in single-goroutine contexts (init, main).
var globalStore Store
var globalStoreMu sync.RWMutex

// GetGlobalStore returns the global store with read-lock protection.
func GetGlobalStore() Store {
	globalStoreMu.RLock()
	s := globalStore
	globalStoreMu.RUnlock()
	return s
}

// SetGlobalStore sets the global store with write-lock protection.
func SetGlobalStore(s Store) {
	globalStoreMu.Lock()
	globalStore = s
	globalStoreMu.Unlock()
}

// InitializeStore initializes the database store based on configuration
// and returns it. The package-level globalStore is also set for back-compat
// with code paths that still use GetGlobalStore(); new code should prefer
// the returned Store and pass it explicitly down the call chain
// (SERVER-GLOBAL-STORE-AUDIT, in flight).
//
// The SQLite backend was removed in fable5 TASK-022. Any dbType value other
// than "pebble" (or empty) returns an error.
func InitializeStore(dbType, path string) (Store, error) {
	var s Store
	var err error

	switch dbType {
	case "sqlite", "sqlite3":
		return nil, fmt.Errorf("database_type %q is not supported: SQLite3 support has been removed and PebbleDB is the only database backend; set database_type to \"pebble\" (or drop --db-type %s) and point database_path at a PebbleDB directory", dbType, dbType)
	case "pebble", "":
		// PebbleDB is the default and only supported backend. Opening it can
		// replay a large WAL, so ask systemd for start time first.
		extendStartTimeout()
		s, err = NewPebbleStore(path)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize PebbleDB store: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported database type: %s (only 'pebble' is supported)", dbType)
	}

	SetGlobalStore(s)

	// Run migrations to ensure schema is up to date
	if err := RunMigrations(s); err != nil {
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	// Every boot, not only when migration 065 runs: rebuild the opv3 records
	// a previous binary left stale (ReconcileOpsV3WithV2). Synchronous and
	// here, before the caller builds the server and the operations registry,
	// because the registry's boot resume acts on opv3 records. A failure is
	// logged, not returned: opv2: is still the source of truth the registry
	// runs from, and the next boot retries.
	if r, ok := s.(opsV3Reconciler); ok {
		if _, err := r.ReconcileOpsV3WithV2(context.Background()); err != nil {
			opsV3Log.Error("opv3 reconcile failed; opv3 records may be stale until each run's next write, and the next boot retries: %v", err)
		}
	}

	return s, nil
}

// opsV3Reconciler is the boot reconcile's one capability, asserted at run
// time so Store is not widened for one boot step (every production store is
// a *PebbleStore and has it).
type opsV3Reconciler interface {
	ReconcileOpsV3WithV2(ctx context.Context) (OpsV3ReconcileReport, error)
}

// CloseStore closes the global store
func CloseStore() error {
	// Grab and nil the global ref under the write lock so a concurrent
	// GetGlobalStore (which only takes the read lock) can never observe
	// a store that CloseStore has already started closing.
	globalStoreMu.Lock()
	store := globalStore
	globalStore = nil
	globalStoreMu.Unlock()

	if store != nil {
		return store.Close()
	}
	return nil
}
