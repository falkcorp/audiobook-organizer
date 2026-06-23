// file: internal/activity/register.go
// version: 1.2.1
// last-edited: 2026-06-23
// guid: c4d5e6f7-a8b9-0009-2345-000000000009

// Package activity — service registry wiring for the activity log.
//
// WHY backend selection during the migration window (T024):
//   - Before the "activity_pebble_v1_done" backfill flag is set in Pebble, reads
//     come from NutsDB (source of truth). Both backends receive every write.
//   - After the flag is set, reads flip to Pebble. NutsDB still receives writes
//     so a rollback (flip ReadFromPebble=false) is safe without data loss.
//   - The dual-write window ends when NutsDB is removed (follow-up task T024b).
package activity

import (
	"fmt"
	"log/slog"
	"path/filepath"

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
			store := serviceregistry.Get[database.Store](c, serviceregistry.KeyStore)
			ps, ok := store.(*database.PebbleStore)
			if !ok {
				// Non-Pebble backend (test double, SQLite) — return nil pointer;
				// the activitystore Build checks for nil and falls back to NutsDB-only.
				return (*database.PebbleActivityStore)(nil), nil
			}
			s := database.NewPebbleActivityStore(ps.DB())
			slog.Info("[activity] Pebble activity store initialised")
			return s, nil
		},
	})

	// activitystore: activity log backend, wired to dual-write when Pebble is available.
	// Lives in {dirname(DatabasePath)}/activity.nutsdb (NutsDB sidecar).
	// Returns nil when DatabasePath is unset — host code must Override serviceregistry.KeyActivityStore
	// with a pre-built instance in that case (test paths).
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyActivityStore,
		Needs:  []string{serviceregistry.KeyConfig, "pebble-activitystore"},
		Groups: []string{serviceregistry.KeyActivity},
		Build: func(c *serviceregistry.Container) (any, error) {
			cfg := serviceregistry.Get[*config.Config](c, serviceregistry.KeyConfig)
			if cfg.DatabasePath == "" {
				return nil, fmt.Errorf("activitystore: DatabasePath not configured")
			}
			activityDir := filepath.Join(filepath.Dir(cfg.DatabasePath), "activity.nutsdb")
			nutsStore, err := database.NewNutsActivityStore(activityDir)
			if err != nil {
				return nil, fmt.Errorf("activitystore: open nutsdb: %w", err)
			}

			// Try to wrap in dual-write if the Pebble backend is available.
			pebbleStore, hasPebble := serviceregistry.TryGet[*database.PebbleActivityStore](c, "pebble-activitystore")
			if !hasPebble || pebbleStore == nil {
				// Pebble backend not available — NutsDB-only (degraded / test mode).
				slog.Info("[activity] Pebble activity store not available; using NutsDB-only")
				return nutsStore, nil
			}

			// Determine read source from backfill flag.
			// Checked once at startup — the flag is not expected to change at runtime.
			readFromPebble := database.IsActivityPebbleBackfillDone(pebbleStore.DB())
			slog.Info("[activity] dual-write mode enabled",
				"read_from_pebble", readFromPebble,
				"flag", database.ActivityPebbleBackfillKey)
			return database.NewDualWriteActivityStore(nutsStore, pebbleStore, readFromPebble), nil
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
