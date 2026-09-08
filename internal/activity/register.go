// file: internal/activity/register.go
// version: 1.6.0
// last-edited: 2026-09-07
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
package activity

import (
	"fmt"
	"log/slog"
	"strings"

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
			// AsPebbleStore, not a bare assertion: the server decorates the
			// store with indexedStore during Start(), and a bare
			// store.(*database.PebbleStore) fails through that decorator --
			// silently taking the "non-Pebble backend" branch below, which
			// looks like a supported configuration rather than a bug. The
			// FromStore constructor does that unwrap inside internal/database,
			// so this package never names the concrete type. See
			// database/store_capability.go.
			s := database.NewPebbleActivityStoreFromStore(store)
			if s == nil {
				// Non-Pebble backend (test double, SQLite) — return nil pointer;
				// the activitystore Build checks for nil and falls back to NutsDB-only.
				return (*database.PebbleActivityStore)(nil), nil
			}
			slog.Info("[activity] Pebble activity store initialised")
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

			pebbleStore, hasPebble := serviceregistry.TryGet[*database.PebbleActivityStore](c, "pebble-activitystore")
			if !hasPebble || pebbleStore == nil {
				// No more NutsDB fallback — a missing Pebble backend is now a
				// hard error rather than a degraded mode.
				return nil, fmt.Errorf("activitystore: pebble activity store not available")
			}

			// ActivityBackend selects the store. "pebble" is the escape hatch /
			// rollback (Pebble-only, no SQLite opened). Empty or "sqlite"
			// (default) engages the SQLite backend behind a migration wrapper:
			// writes dual to both, reads stay on Pebble until the backfill copies
			// history + verifies parity, then flip (see activity-sql-migration).
			backend := strings.ToLower(strings.TrimSpace(cfg.ActivityBackend))
			if backend == "pebble" {
				slog.Info("[activity] Pebble-only activity store wired (ActivityBackend=pebble)")
				return pebbleStore, nil
			}

			sqlPath := cfg.ResolveActivityDBPath()

			// The configured path says where the database should be; the recorded
			// one says where it actually is. When they disagree there is an
			// existing database somewhere else, and doing nothing about it would
			// silently start a fresh log while the history sat unreachable at the
			// old path — the failure mode this whole path is designed around.
			relocateActivityDBIfNeeded(pebbleStore, sqlPath, cfg.ActivityDBMoveOnChange)

			sqlStore, err := database.OpenSQLiteActivityStore(sqlPath)
			if err != nil {
				// Fail OPEN to Pebble: a SQLite open failure must not take the
				// activity log offline. Loud, then degrade to the proven backend.
				slog.Error("[activity] SQLite activity store failed to open — falling back to Pebble-only",
					"path", sqlPath, "err", err)
				return pebbleStore, nil
			}
			// Record where the database actually is, now that it has opened
			// cleanly. This is what the next boot compares the configured path
			// against; writing it before the open would claim a location that
			// might not work.
			if err := pebbleStore.SetLastActivityDBPath(sqlPath); err != nil {
				// Not fatal, but it does disarm the relocation check: a later
				// path change would start an empty database instead of moving
				// this one, so it must not pass unnoticed.
				slog.Error("[activity] could not record the activity database location — "+
					"a future path change will NOT relocate this database",
					"path", sqlPath, "err", err)
			}

			readSecondary := pebbleStore.SQLBackfillDone()
			slog.Info("[activity] SQLite activity migration wired",
				"sqlite_path", sqlPath, "read_secondary", readSecondary)
			return database.NewMigratingActivityStore(pebbleStore, sqlStore, readSecondary), nil
		},
	})

	// activity-sql-migration: a Starter that runs the one-time Pebble→SQLite
	// backfill in the background and flips reads to SQLite once per-tier parity
	// is verified. A no-op when the store is not a migration wrapper (pebble
	// escape hatch, or a SQLite-open fallback) or when the migration already
	// completed on a prior boot.
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "activity-sql-migration",
		Needs:  []string{serviceregistry.KeyActivityStore},
		Groups: []string{serviceregistry.KeyActivity},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[database.ActivityStorer](c, serviceregistry.KeyActivityStore)
			mig, _ := store.(*database.MigratingActivityStore)
			return &sqlMigrationStarter{mig: mig}, nil
		},
	})

	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyActivity,
		Needs:  []string{serviceregistry.KeyActivityStore},
		Groups: []string{serviceregistry.KeyActivity},
		Build: func(c *serviceregistry.Container) (any, error) {
			// Use the ActivityStorer interface — the activitystore may be any of
			// *NutsActivityStore, *DualWriteActivityStore (all implement ActivityStorer).
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
