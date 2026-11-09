// file: internal/database/store_init.go
// version: 1.2.1
// guid: 7ffff60d-2915-4c59-a775-321f8367f69b
// last-edited: 2026-11-09

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

// InitializeStoreWith opens the store for a standalone command and returns
// it: NewPebbleStore's open through core storage (books' migrations run
// there, as core steps), then the opv3 reconcile HEAD ran here, before the
// caller builds anything that resumes operations. The package-level
// globalStore is also set for back-compat with code paths that still use
// GetGlobalStore(); new code should prefer the returned Store and pass it
// explicitly down the call chain (SERVER-GLOBAL-STORE-AUDIT, in flight).
//
// The SQLite backend was removed in fable5 TASK-022. Any dbType value other
// than "pebble" (or empty) returns an error (ValidateDatabaseType).
func InitializeStoreWith(dbType, path string, o StandaloneOptions) (Store, error) {
	if err := ValidateDatabaseType(dbType); err != nil {
		return nil, err
	}
	s, err := OpenStandalone(path, o)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize PebbleDB store: %w", err)
	}
	SetGlobalStore(s)
	// Every open, not only when migration 065 runs: rebuild the opv3 records
	// a previous binary left stale (ReconcileOpsV3WithV2). A failure is
	// logged, not returned: opv2: is still the source of truth the registry
	// runs from, and the next open retries.
	if _, err := s.ReconcileOpsV3WithV2(context.Background()); err != nil {
		opsV3Log.Error("opv3 reconcile failed; opv3 records may be stale until each run's next write, and the next boot retries: %v", err)
	}
	return s, nil
}

// ValidateDatabaseType refuses a database_type other than "pebble" (or
// empty): the standalone opener checks it, and so does serve before core
// storage opens database_path, so neither opens a store the config does not
// name as a Pebble one.
func ValidateDatabaseType(dbType string) error {
	switch dbType {
	case "pebble", "":
		return nil
	case "sqlite", "sqlite3":
		return fmt.Errorf("database_type %q is not supported: SQLite3 support has been removed and PebbleDB is the only database backend; set database_type to \"pebble\" (or drop --db-type %s) and point database_path at a PebbleDB directory", dbType, dbType)
	default:
		return fmt.Errorf("unsupported database type: %s (only 'pebble' is supported)", dbType)
	}
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
