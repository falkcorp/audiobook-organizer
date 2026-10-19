// file: internal/activity/register.go
// version: 2.0.0
// last-edited: 2026-10-19
// guid: c4d5e6f7-a8b9-0009-2345-000000000009

// Package activity — service registry wiring for the activity log.
//
// WHY Pebble-only (TASK-22, 2026-07-03):
//   - The NutsDB→Pebble migration (T024) is complete: the
//     "activity_pebble_v1_done" backfill flag has been set on prod, so reads
//     have already been coming from Pebble with no read benefit left in
//     NutsDB. Activity is now Pebble-only — NutsDB is no longer opened here at
//     all, which also removes the process-lifetime NutsDB Close() goroutine
//     from this path (see TODO.md NUTSDB-CLOSE-GOROUTINE-LEAK).
//
// The SQLite activity backend, its Pebble-to-SQLite migration wrapper and the
// activity-sql-migration Starter were deleted in 01-P75 (owner decision D11;
// the last build with them is tagged activity-sqlite-last). An
// activity_backend of "sqlite" now fails the activitystore build with a
// message instead of being served by Pebble behind the operator's back.
package activity

import (
	"fmt"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func init() {
	// pebble-activitystore: PebbleDB-backed activity store, sharing the main
	// PebbleDB instance. Built only when the main store is a *PebbleStore.
	// Returns a nil *PebbleActivityStore on other backends so dual-write
	// wiring below can detect and fall back gracefully.
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "pebble-activitystore",
		Needs:  []string{serviceregistry.KeyStore},
		Groups: []string{serviceregistry.KeyActivity},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[any](c, serviceregistry.KeyStore)
			// AsPebbleStore, not a bare assertion: a store can be
			// decorated (the server did so with indexedStore in NewServer until
			// 07-S3), and a bare store.(*database.PebbleStore) fails through a
			// decorator --
			// silently taking the "non-Pebble backend" branch below, which
			// looks like a supported configuration rather than a bug. The
			// FromStore constructor does that unwrap inside internal/database,
			// so this package never names the concrete type. See
			// database/store_capability.go.
			s := database.NewPebbleActivityStoreFromStore(store)
			if s == nil {
				// Non-Pebble main store (a test double) — return nil pointer;
				// the activitystore Build treats nil as a hard error (no fallback backend).
				return (*database.PebbleActivityStore)(nil), nil
			}
			slog.Info("[activity] Pebble activity store initialised")
			// The rollback guard for the source/type/level filter indexes (rows
			// a rolled-back build wrote without filter keys) is not run from
			// here: Server.startBackfills enqueues it as
			// maintenance.activity-filter-index-backfill {"reconcile": true}
			// (04-P6), where it shows on the Operations page, can be cancelled,
			// and waits for the store's warmup. The store remembers the
			// seen-through mark as of this construction for it
			// (PebbleActivityStore.reconcileFrom, durable across a restart).
			return s, nil
		},
	})

	// activitystore: Pebble-only activity log backend (NutsDB retired — see
	// package doc). Returns nil when DatabasePath is unset — host code must
	// Override serviceregistry.KeyActivityStore with a pre-built instance in
	// that case (test paths).
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyActivityStore,
		Needs:  []string{serviceregistry.KeyConfig, "pebble-activitystore"},
		Groups: []string{serviceregistry.KeyActivity},
		Build: func(c *serviceregistry.Container) (any, error) {
			cfg := config.GetConfig(c)
			if cfg.DatabasePath == "" {
				return nil, fmt.Errorf("activitystore: DatabasePath not configured")
			}

			// Validate before anything else: a conf that still names the
			// removed SQLite backend must stop here with the reason, even on a
			// host whose Pebble store is fine. Config.Validate refuses the same
			// values at startup; this is the backstop for any path that builds
			// the container without it (tests, tools).
			if err := config.ValidateActivityBackend(cfg.ActivityBackend); err != nil {
				return nil, fmt.Errorf("activitystore: %w", err)
			}

			pebbleStore, hasPebble := serviceregistry.TryGet[*database.PebbleActivityStore](c, "pebble-activitystore")
			if !hasPebble || pebbleStore == nil {
				// No more NutsDB fallback — a missing Pebble backend is now a
				// hard error rather than a degraded mode.
				return nil, fmt.Errorf("activitystore: pebble activity store not available")
			}
			slog.Info("[activity] Pebble activity store wired")
			return pebbleStore, nil
		},
	})

	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyActivity,
		Needs:  []string{serviceregistry.KeyActivityStore},
		Groups: []string{serviceregistry.KeyActivity},
		Build: func(c *serviceregistry.Container) (any, error) {
			// Use the ActivityStorer interface: production wires the Pebble
			// store, and tests override KeyActivityStore with their own doubles.
			store := serviceregistry.Get[database.ActivityStorer](c, serviceregistry.KeyActivityStore)
			return NewService(store), nil
		},
	})

	// activitywriter: io.Writer that tees log output to stdout and captures
	// parsed entries into the activity store. Implements Starter and Stopper
	// for lifecycle management. Depends on activity service for its store.
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "activitywriter",
		Needs:  []string{serviceregistry.KeyActivity},
		Groups: []string{serviceregistry.KeyActivity},
		Build: func(c *serviceregistry.Container) (any, error) {
			activitySvc := serviceregistry.Get[*Service](c, serviceregistry.KeyActivity)
			return NewWriter(activitySvc.Store(), 10000), nil
		},
	})
}
