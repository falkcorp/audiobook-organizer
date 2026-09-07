// file: internal/activity/sql_migration.go
// version: 1.0.0
// guid: 8e3b1f47-2a90-4c6d-b5e1-9f0c7d2a6b58
// last-edited: 2026-09-07

// Package activity — background driver for the Pebble → SQLite activity cutover.
//
// sqlMigrationStarter runs the one-time backfill AFTER the container has
// started (Starter.Start), off the request path, and flips reads to SQLite only
// when the backfill reports verified per-tier parity. Until then reads stay on
// Pebble, so the activity UI is never served an empty or half-copied SQLite.
package activity

import (
	"context"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// sqlMigrationSettleDelay lets startup I/O settle before the backfill scans the
// (potentially ~GiB) Pebble activity keyspace, so the migration never competes
// with boot for disk.
const sqlMigrationSettleDelay = 60 * time.Second

// sqlMigrationStarter drives the backfill+flip. mig is nil when the wired store
// is not a migration wrapper (pebble escape hatch or a SQLite-open fallback),
// in which case Start is a no-op.
type sqlMigrationStarter struct {
	mig *database.MigratingActivityStore
}

// Start launches the migration in the background and returns immediately, per
// the Starter contract. It uses a fresh background context (not the startup
// ctx, which is cancelled once Start completes) so the copy survives past boot.
func (s *sqlMigrationStarter) Start(_ context.Context) error {
	if s.mig == nil {
		return nil // pebble-only or fallback: nothing to migrate
	}
	if s.mig.ReadSecondary() {
		return nil // already migrated on a prior boot
	}
	pebble, ok1 := s.mig.Primary().(*database.PebbleActivityStore)
	sqlStore, ok2 := s.mig.Secondary().(*database.SQLActivityStore)
	if !ok1 || !ok2 {
		slog.Warn("[activity] migration wrapper has unexpected backend types; skipping SQLite backfill")
		return nil
	}
	cutoff := s.mig.MigrationCutoff()
	go func() {
		time.Sleep(sqlMigrationSettleDelay)
		bg := context.Background()
		slog.Info("[activity] starting Pebble→SQLite activity backfill (background)", "cutoff", cutoff)
		res, err := database.BackfillPebbleActivityToSQL(bg, pebble, sqlStore, cutoff, false)
		if err != nil {
			slog.Error("[activity] Pebble→SQLite backfill failed — reads stay on Pebble", "err", err)
			return
		}
		if !res.ParityOK {
			slog.Error("[activity] Pebble→SQLite backfill parity failed — reads stay on Pebble")
			return
		}
		s.mig.SetReadSecondary(true)
		slog.Info("[activity] activity log migrated to SQLite — reads now served from SQLite",
			"scanned", res.EntriesScanned, "copied", res.EntriesCopied)
	}()
	return nil
}

var _ interface {
	Start(context.Context) error
} = (*sqlMigrationStarter)(nil)
