// file: internal/plugins/maintenance/db.go
// version: 1.3.0
// guid: d4e5f6a7-b8c9-0123-def0-345678901234
// last-edited: 2026-10-02

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/compactprogress"
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
	runDBOptimizeSteps(ctx, store, p.deps, reporter, compactprogress.DefaultInterval)
	return nil
}

// runDBOptimizeSteps compacts the main, AI-scan and OpenLibrary stores, each
// through compactprogress.RunStep so the op log shows what Pebble is doing
// every interval and a before/after line when each store finishes. Before
// 2026-10-02 the only lines were "Compacting main database..." and, ~28
// minutes later, "Main database optimized in ...".
//
// scheduler.db-optimize (internal/scheduler/extra_ops.go) is the same work
// under another ID and reports the same way.
func runDBOptimizeSteps(ctx context.Context, store compactableStore, aux StoreOptimizer, reporter sdk.Reporter, interval time.Duration) {
	storesOptimized := 0
	const storesTotal = 3
	startTotal := time.Now()

	// 1. Main store
	//
	// The log line here said "VACUUM, ANALYZE, WAL checkpoint" until 2026-09-08.
	// That is SQLite vocabulary; the main store is Pebble and Optimize() is a
	// full `db.Compact(nil, 0xff)`. Nobody re-read it when the engine changed.
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"Compacting main database (Pebble, full keyspace); engine counters are logged every %s", interval))
	_ = reporter.UpdateProgress(0, storesTotal, "Compacting main database...")
	if err := optimizeMainWithHeartbeat(ctx, store, reporter, storesTotal, interval); err == nil {
		storesOptimized++
	}

	// 2. AI scan store
	_ = reporter.UpdateProgress(1, storesTotal, "Optimizing AI scan database...")
	if _, owned := aux.AIScanStoreCompactionStats(); !owned {
		// Absent, or (the production wiring) sharing the main database, in
		// which case step 1 already compacted its keys and Optimize is a no-op.
		if err := aux.OptimizeAIScanStore(ctx); err != nil {
			_ = reporter.Log(slog.LevelError, fmt.Sprintf("AI scan DB optimization failed: %v", err))
		} else {
			storesOptimized++
			_ = reporter.Log(slog.LevelInfo,
				"AI scan store has no separate database (absent or shares the main one); nothing separate to compact")
		}
	} else {
		res := compactprogress.RunStep(ctx, reporter, compactprogress.Step{
			Label:    fmt.Sprintf("Compacting AI scan database (2/%d)", storesTotal),
			Run:      aux.OptimizeAIScanStore,
			Stats:    func() compactprogress.Stats { st, _ := aux.AIScanStoreCompactionStats(); return st },
			Frame:    func(msg string) { _ = reporter.UpdateProgress(1, storesTotal, msg) },
			Interval: interval,
		})
		if res.Err == nil {
			storesOptimized++
		}
	}

	// 3. OpenLibrary store
	_ = reporter.UpdateProgress(2, storesTotal, "Optimizing OpenLibrary cache...")
	if _, ok := aux.OLStoreCompactionStats(); !ok {
		_ = reporter.Log(slog.LevelInfo, "OpenLibrary cache not initialized, skipping")
	} else {
		res := compactprogress.RunStep(ctx, reporter, compactprogress.Step{
			Label:    fmt.Sprintf("Compacting OpenLibrary cache (3/%d)", storesTotal),
			Run:      aux.OptimizeOLStore,
			Stats:    func() compactprogress.Stats { st, _ := aux.OLStoreCompactionStats(); return st },
			Frame:    func(msg string) { _ = reporter.UpdateProgress(2, storesTotal, msg) },
			Interval: interval,
		})
		if res.Err == nil {
			storesOptimized++
		}
	}

	_ = reporter.UpdateProgress(storesTotal, storesTotal,
		fmt.Sprintf("Database optimization complete: %d/%d stores in %s",
			storesOptimized, storesTotal, time.Since(startTotal).Round(time.Millisecond)))
}

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
// Cancellable:false and PebbleStore.Optimize hardcoded context.Background(), so
// the compaction ran on unsupervised for another 20 minutes while the registry
// reported it dropped. An op whose status says "dead" while its work continues
// is worse than either outcome on its own.
//
// WHY COUNTERS AND NOT A TICKER. A frame on every tick regardless of work
// would satisfy the watchdog and destroy it at the same time: a genuinely
// wedged compaction would keep reporting healthy forever. So progress FRAMES
// go out only when Pebble says work happened (compactprogress.Moved: bytes
// written by a compaction, a compaction completed, or debt fell). Since
// 2026-10-02 the sampling itself lives in compactprogress.RunStep, which also
// LOGS a descriptive line every tick -- logs do not stamp liveness, so that
// adds visibility without masking a wedge.
//
// compactableStore is the two methods the heartbeat actually needs. Narrow on
// purpose: OpsStore is 55 methods, and a fake for it would be 53 stubs written
// to exercise two.
type compactableStore interface {
	Optimize(ctx context.Context) error
	CompactionStats() database.CompactionStats
}

func optimizeMainWithHeartbeat(ctx context.Context, store compactableStore, reporter sdk.Reporter, storesTotal int, interval time.Duration) error {
	res := compactprogress.RunStep(ctx, reporter, compactprogress.Step{
		Label:    fmt.Sprintf("Compacting main database (1/%d)", storesTotal),
		Run:      store.Optimize,
		Stats:    store.CompactionStats,
		Frame:    func(msg string) { _ = reporter.UpdateProgress(0, storesTotal, msg) },
		Interval: interval,
	})
	return res.Err
}
