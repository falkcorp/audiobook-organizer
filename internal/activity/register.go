// file: internal/activity/register.go
// version: 1.3.0
// last-edited: 2026-07-03
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

	// activitystore: Pebble-only activity log backend (NutsDB retired — see
	// package doc). Returns nil when DatabasePath is unset — host code must
	// Override serviceregistry.KeyActivityStore with a pre-built instance in
	// that case (test paths).
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyActivityStore,
		Needs:  []string{serviceregistry.KeyConfig, "pebble-activitystore"},
		Groups: []string{serviceregistry.KeyActivity},
		Build: func(c *serviceregistry.Container) (any, error) {
			cfg := serviceregistry.Get[*config.Config](c, serviceregistry.KeyConfig)
			if cfg.DatabasePath == "" {
				return nil, fmt.Errorf("activitystore: DatabasePath not configured")
			}

			pebbleStore, hasPebble := serviceregistry.TryGet[*database.PebbleActivityStore](c, "pebble-activitystore")
			if !hasPebble || pebbleStore == nil {
				// No more NutsDB fallback — a missing Pebble backend is now a
				// hard error rather than a degraded mode.
				return nil, fmt.Errorf("activitystore: pebble activity store not available")
			}

			slog.Info("[activity] Pebble-only activity store wired")
			return pebbleStore, nil
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
