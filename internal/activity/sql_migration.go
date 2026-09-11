// file: internal/activity/sql_migration.go
// version: 1.6.0
// guid: 8e3b1f47-2a90-4c6d-b5e1-9f0c7d2a6b58
// last-edited: 2026-09-10

// Package activity — background driver for the Pebble → SQLite activity cutover.
//
// sqlMigrationStarter runs the one-time backfill AFTER the container has
// started (Starter.Start), off the request path, and flips reads to SQLite only
// when the backfill reports verified per-tier parity. Until then reads stay on
// Pebble, so the activity UI is never served an empty or half-copied SQLite.
package activity

import (
	"context"
	"fmt"
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

	// ops is the operations-v2 store the run reports its status to. Optional:
	// nil means the migration runs exactly as before, silently. See
	// sql_migration_report.go for why the run reports to a row rather than
	// becoming a registry-owned operation.
	ops migrationOpsRecorder

	// bus is the operations SSE hub. Also optional, and its absence degrades
	// differently from ops': the row is still written and still accurate, it just
	// stops updating between page loads (see sql_migration_report.go).
	bus migrationEventPublisher

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

		// The row is inserted HERE, not in Start, so a shutdown during the settle
		// delay leaves no phantom "running" operation behind for a user to wonder
		// about. Everything below reports through it; none of it can fail the run.
		rep := &migrationOpReporter{ops: s.ops, bus: s.bus}
		rep.begin(time.Now().UTC())

		res, err := database.BackfillPebbleActivityToSQLWithProgress(
			s.ctx, pebble, sqlStore, false, rep.observe)
		if err != nil {
			if s.ctx.Err() != nil {
				// Cancelled by shutdown, not broken. The backfill is idempotent
				// by content key and resumable, so the next boot continues.
				slog.Info("[activity] Pebble→SQLite backfill interrupted by shutdown — resumes next boot")
				// interrupted_DROPPED, not interrupted_quiesced, and the difference
				// is load-bearing: isResumableV2Status treats quiesced as resumable,
				// so the next boot's resumeAfterStartup would pull this row into the
				// resume machinery only to drop it as "unknown def" (no OperationDef
				// is registered — see sql_migration_report.go). A row nothing can
				// resume belongs in a TERMINAL state, and the restart is not a lost
				// run: the starter begins a fresh one from the checkpoint, with its
				// own row.
				rep.finish(migrationStatusInterruptedByShutdown,
					"interrupted by shutdown — resumes from its checkpoint on the next start", nil)
				return
			}
			slog.Error("[activity] Pebble→SQLite backfill failed — reads stay on Pebble", "err", err)
			rep.finish("failed", "migration failed — activity reads stay on the old store", err)
			return
		}
		if !res.ParityOK {
			slog.Error("[activity] Pebble→SQLite backfill parity failed — reads stay on Pebble")
			rep.finish("failed",
				"copy could not be verified — activity reads stay on the old store", nil)
			return
		}
		s.mig.SetReadSecondary(true)
		slog.Info("[activity] activity log migrated to SQLite — reads now served from SQLite",
			"scanned", res.EntriesScanned, "copied", res.EntriesCopied)
		rep.finish("completed", fmt.Sprintf(
			"activity log migrated: %s rows scanned, %s copied — reads now served from SQLite",
			humanCount(res.EntriesScanned), humanCount(res.EntriesCopied)), nil)
	})
	return nil
}

// Stop cancels the backfill and WAITS for it to return, so the activity Pebble
// store cannot be closed while the scan is still reading it.
//
// The wait is unbounded on purpose. The backfill checks ctx between tiers and
// inside streamTierEntries, and its per-batch acquisition of the SQLite
// maintenance gate is ctx-aware too (SQLActivityStore.acquireBackfillGate), so
// it returns promptly even while a long Summarize/Prune holds that gate — which
// a plain RLock would have made us wait out, since Prune has no context of its
// own. The alternative — giving up after a timeout — reintroduces exactly the
// use-after-close panic this exists to prevent, on the shutdown path where no
// recover is in scope.
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
