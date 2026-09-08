// file: internal/plugins/maintenance/db.go
// version: 1.2.0
// guid: d4e5f6a7-b8c9-0123-def0-345678901234
// last-edited: 2026-09-08

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

func (p *Plugin) dbOptimizeDef() sdk.OperationDef {
	sched := "0 2 * * 0" // 02:00 every Sunday
	return sdk.OperationDef{
		ID:              "maintenance.db-optimize",
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Optimize database",
		Description:     "Runs VACUUM/ANALYZE/WAL-checkpoint on all database stores.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.db-optimize",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         60 * time.Minute,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runDBOptimize,
	}
}

func (p *Plugin) runDBOptimize(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	storesOptimized := 0
	storesTotal := 3
	startTotal := time.Now()

	// 1. Main store
	//
	// The log line here said "VACUUM, ANALYZE, WAL checkpoint" until 2026-09-08.
	// That is SQLite vocabulary; the main store is Pebble and Optimize() is a
	// full `db.Compact(nil, 0xff)`. Nobody re-read it when the engine changed.
	_ = reporter.Log(slog.LevelInfo, "Compacting main database (Pebble, full keyspace)...")
	_ = reporter.UpdateProgress(0, storesTotal, "Compacting main database...")
	t1 := time.Now()
	if err := optimizeMainWithHeartbeat(ctx, store, reporter, storesTotal, optimizeMainHeartbeatInterval); err != nil {
		_ = reporter.Log(slog.LevelError, fmt.Sprintf("Main DB optimization failed: %v", err))
	} else {
		storesOptimized++
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Main database optimized in %s", time.Since(t1).Round(time.Millisecond)))
	}

	// 2. AI scan store
	_ = reporter.UpdateProgress(1, storesTotal, "Optimizing AI scan database...")
	if err := p.deps.OptimizeAIScanStore(ctx); err != nil {
		_ = reporter.Log(slog.LevelError, fmt.Sprintf("AI scan DB optimization failed: %v", err))
	} else {
		storesOptimized++
	}

	// 3. OpenLibrary store
	_ = reporter.UpdateProgress(2, storesTotal, "Optimizing OpenLibrary cache...")
	if err := p.deps.OptimizeOLStore(ctx); err != nil {
		_ = reporter.Log(slog.LevelError, fmt.Sprintf("OL cache optimization failed: %v", err))
	} else {
		storesOptimized++
	}

	_ = reporter.UpdateProgress(storesTotal, storesTotal,
		fmt.Sprintf("Database optimization complete: %d/%d stores in %s",
			storesOptimized, storesTotal, time.Since(startTotal).Round(time.Millisecond)))
	return nil
}

// optimizeMainHeartbeatInterval is how often the compaction reports in. It must
// stay comfortably under the registry's 5m ProgressTimeout so a healthy
// compaction never comes within reach of a stuck strike, and long enough that
// the reporter is not writing a row every second for an operation that runs for
// half an hour.
const optimizeMainHeartbeatInterval = 30 * time.Second

// optimizeMainWithHeartbeat runs the main-store compaction while reporting real
// progress from Pebble's own counters.
//
// WHY THIS EXISTS. store.Optimize() is a single blocking `db.Compact(nil, 0xff)`
// with no callback, and runDBOptimize only called UpdateProgress at its three
// step boundaries. The operations registry cancels an op that reports nothing
// for five minutes, so on any database large enough for a full compaction to
// exceed 5m -- 31 GB on production -- db-optimize was killed EVERY time, and it
// is scheduled weekly (0 2 * * 0). Measured on prod 2026-09-08: enqueued
// 10:25:45, "canceling stuck op ... no progress for 5m9s" at 10:30:54.
//
// Two things made that worse than a plain failure. The op's own Timeout is 60m,
// so the deadline the author chose never applied -- a different, shorter clock
// fired first. And the cancellation did not stop anything: the op is declared
// Cancellable:false and PebbleStore.Optimize hardcodes context.Background(), so
// the compaction ran on unsupervised for another 20 minutes while the registry
// reported it dropped. An op whose status says "dead" while its work continues
// is worse than either outcome on its own.
//
// WHY COUNTERS AND NOT A TICKER. The obvious fix -- beat every 30s regardless --
// would satisfy the watchdog and destroy it at the same time: a genuinely wedged
// compaction would keep reporting healthy forever. registry/types.go:200 records
// that raising ProgressTimeout "hides the first case permanently", and a blind
// heartbeat is the same mistake wearing a different hat. So this reports only
// when Pebble says work actually happened -- EstimatedDebt fell, or the
// completed-compaction Count rose. If both are flat while compactions are still
// in progress, that IS a wedge, nothing is reported, and the watchdog correctly
// kills it at five minutes.
// compactableStore is the two methods the heartbeat actually needs. Narrow on
// purpose: OpsStore is 55 methods, and a fake for it would be 53 stubs written
// to exercise two. The narrow interface is also the documentation -- it says
// exactly what this function touches.
type compactableStore interface {
	Optimize(ctx context.Context) error
	CompactionStats() database.CompactionStats
}

func optimizeMainWithHeartbeat(ctx context.Context, store compactableStore, reporter sdk.Reporter, storesTotal int, interval time.Duration) error {
	done := make(chan error, 1)
	// ctx goes to Optimize, which now honours it. There is deliberately NO
	// <-ctx.Done() case in the select below: on cancellation Optimize returns
	// and `done` fires, so waiting on `done` alone is what guarantees this
	// function never outlives the compaction goroutine it started.
	go func() { done <- store.Optimize(ctx) }()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	start := time.Now()
	prev := store.CompactionStats()
	for {
		select {
		case err := <-done:
			return err
		case <-ticker.C:
			cur := store.CompactionStats()
			// Strictly: debt shrank, or a compaction completed. Equality is not
			// progress -- that is the stall this check exists to expose.
			moved := cur.EstimatedDebt < prev.EstimatedDebt || cur.Count > prev.Count
			if !moved {
				// Deliberately silent. Saying nothing is what lets the watchdog
				// do its job; see the note above.
				continue
			}
			_ = reporter.UpdateProgress(0, storesTotal, fmt.Sprintf(
				"Compacting main database: %d compactions, %s left to compact, %s elapsed",
				cur.Count, humanBytes(cur.EstimatedDebt), time.Since(start).Round(time.Second)))
			prev = cur
		}
	}
}

// humanBytes renders a byte count for an operator reading a progress line.
func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
