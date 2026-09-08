// file: internal/activity/sql_migration.go
// version: 1.2.0
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
	"sync"
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

	// ctx/cancel/wg make the backfill goroutine cancellable AND joinable.
	// Previously it was a bare `go func()` on context.Background() with no Stop
	// method at all, so nothing could reach it: it scanned the whole activity
	// keyspace on the SHARED main Pebble handle, and a restart inside that
	// window closed the store underneath it. Pebble panics on use-after-close
	// rather than returning an error, so that is a process-killing panic during
	// shutdown, on a goroutine with no recover.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
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
	// Not the startup ctx — that is cancelled once Start returns, and the copy
	// must outlive boot. This one is owned by the starter and cancelled by Stop.
	s.ctx, s.cancel = context.WithCancel(context.Background())

	s.wg.Go(func() {
		// Interruptible settle delay. A plain time.Sleep here meant a restart in
		// the first minute after boot had to wait the delay out before the
		// goroutine could even observe that it should stop.
		select {
		case <-time.After(sqlMigrationSettleDelay):
		case <-s.ctx.Done():
			slog.Info("[activity] shutdown during settle delay — Pebble→SQLite backfill not started")
			return
		}
		slog.Info("[activity] starting Pebble→SQLite activity backfill (background)")
		res, err := database.BackfillPebbleActivityToSQL(s.ctx, pebble, sqlStore, false)
		if err != nil {
			if s.ctx.Err() != nil {
				// Cancelled by shutdown, not broken. The backfill is idempotent
				// by content key and resumable, so the next boot continues.
				slog.Info("[activity] Pebble→SQLite backfill interrupted by shutdown — resumes next boot")
				return
			}
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
	})
	return nil
}

// Stop cancels the backfill and WAITS for it to return, so the activity Pebble
// store cannot be closed while the scan is still reading it.
//
// The wait is unbounded on purpose. The backfill checks ctx between tiers and
// inside streamTierEntries, so it returns promptly; and the alternative — giving
// up after a timeout — reintroduces exactly the use-after-close panic this
// exists to prevent, on the shutdown path where no recover is in scope.
func (s *sqlMigrationStarter) Stop(_ context.Context) error {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	return nil
}

var _ interface {
	Start(context.Context) error
	Stop(context.Context) error
} = (*sqlMigrationStarter)(nil)
