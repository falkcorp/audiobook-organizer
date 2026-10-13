// file: internal/plugins/maintenance/cleanup.go
// version: 1.19.0
// guid: c3d4e5f6-a7b8-9012-cdef-234567890123
// last-edited: 2026-10-13

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/appdirs"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- purge-deleted ---

// purgeDeletedDef is the only registration of the soft-deleted-book purge.
// scheduler.purge-deleted ran a second copy of the same body under its own ID
// and ConcurrencyKey until 2026-10-11 (04-P4b); it resolves here as a FormerID,
// and the purge_deleted scheduler task enqueues this def. Permissions and the 2h
// Timeout are the stricter gate and longer budget the merged defs carried: the
// purge deletes books and files, so the alias must not widen who can run it.
func (p *Plugin) purgeDeletedDef() sdk.OperationDef {
	sched := "0 3 * * *" // 03:00 daily
	return sdk.OperationDef{
		ID:              "maintenance.purge-deleted",
		FormerIDs:       []string{"scheduler.purge-deleted"},
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Purge soft-deleted books",
		Description:     "Permanently removes soft-deleted books that have exceeded the retention period.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.purge-deleted",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite, sdk.CapFilesWrite},
		Run:             p.runPurgeDeleted,
	}
}

func (p *Plugin) runPurgeDeleted(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	_ = reporter.Log(slog.LevelInfo, "Starting purge of soft-deleted books")
	prog := sdk.NewProgress(reporter, 0)
	prog.Start("Purging soft-deleted books...")
	opID := ctxOpID(ctx)
	p.deps.RunAutoPurgeSoftDeleted(ctx, opID)
	p.deps.ActivityFlushOp(opID)
	if ctx.Err() != nil {
		// Timeout, shutdown, watchdog or quiesce stopped the purge between
		// books; what is left will be picked up by the next run.
		msg := "Purge stopped early: " + ctx.Err().Error()
		_ = reporter.Log(slog.LevelWarn, msg)
		prog.Done(msg)
		return nil
	}
	_ = reporter.Log(slog.LevelInfo, "Purge complete")
	prog.Done("Purge complete")
	return nil
}

// --- tombstone-cleanup ---

// tombstoneCleanupDef is the only registration of the tombstone chain
// resolution. scheduler.tombstone-cleanup ran the same body under its own ID
// and ConcurrencyKey until 2026-10-10; it resolves here as a FormerID, and the
// tombstone_cleanup scheduler task enqueues this def. Permissions and the 1h
// Timeout are the stricter gate and longer budget the merged def carried.
func (p *Plugin) tombstoneCleanupDef() sdk.OperationDef {
	sched := "0 4 * * *" // 04:00 daily
	return sdk.OperationDef{
		ID:              "maintenance.tombstone-cleanup",
		FormerIDs:       []string{"scheduler.tombstone-cleanup"},
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Resolve author tombstone chains",
		Description:     "Flattens multi-hop author tombstone chains (A→B→C becomes A→C).",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.tombstone-cleanup",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         1 * time.Hour,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runTombstoneCleanup,
	}
}

func (p *Plugin) runTombstoneCleanup(_ context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	_ = reporter.Log(slog.LevelInfo, "Starting author tombstone chain resolution")
	prog := sdk.NewProgress(reporter, 0)
	prog.Start("Resolving tombstone chains...")
	updated, err := store.ResolveTombstoneChains()
	if err != nil {
		return fmt.Errorf("tombstone chain resolution failed: %w", err)
	}
	resultMsg := fmt.Sprintf("Resolved %d tombstone chains", updated)
	_ = reporter.Log(slog.LevelInfo, resultMsg)
	prog.Done(resultMsg)
	return nil
}

// --- temp-file-cleanup ---

// tempFileCleanupDef is the only registration of the orphaned temp-file sweep.
// scheduler.temp-file-cleanup ran the same sweep under its own ID and
// ConcurrencyKey until 2026-10-10; it resolves here as a FormerID, and the
// temp_file_cleanup scheduler task enqueues this def. Permissions and the 1h
// budgets are the stricter gate and longer budgets the merged def carried: the
// sweep deletes files, so the alias must not widen who can run it.
func (p *Plugin) tempFileCleanupDef() sdk.OperationDef {
	sched := "30 1 * * *" // 01:30 daily
	return sdk.OperationDef{
		ID:              "maintenance.temp-file-cleanup",
		FormerIDs:       []string{"scheduler.temp-file-cleanup"},
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Liveness:        sdk.LivenessNone,
		ProgressTimeout: tempFileCleanupProgressTimeout, // LivenessNone requires an explicit budget
		Plugin:          "maintenance",
		DisplayName:     "Clean orphaned temp files",
		Description:     "Removes orphaned *.tmp.m4b / *.tmp.m4a files left by crashed ffmpeg operations.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.temp-file-cleanup",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         1 * time.Hour,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead, sdk.CapFilesWrite},
		Run:             p.runTempFileCleanup,
	}
}

// tempFileCleanupProgressTimeout is temp-file-cleanup's LivenessNone budget.
// maintenance.library-optimize runs this op as a child and derives its own
// watchdog budget from this value (optimizeProgressTimeout).
const tempFileCleanupProgressTimeout = 1 * time.Hour

func (p *Plugin) runTempFileCleanup(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	opID := ctxOpID(ctx)
	// CleanupOrphanedTempFiles flushes the op's batched entries itself, before
	// its summary entry, so no ActivityFlushOp call follows it here.
	removed := p.deps.CleanupOrphanedTempFiles(p.deps.RootDir(), opID)
	msg := fmt.Sprintf("Removed %d orphaned temp files", removed)
	_ = reporter.Log(slog.LevelInfo, msg)
	return nil
}

// --- cleanup-activity-log ---

func (p *Plugin) cleanupActivityLogDef() sdk.OperationDef {
	sched := "0 0 * * *" // midnight daily
	return sdk.OperationDef{
		ID:       "maintenance.cleanup-activity-log",
		Liveness: sdk.LivenessManual,
		// Raised from the 5m default on 2026-09-10, when every pass started
		// running on both activity backends. Compaction and Summarize stamp
		// per chunk/group, and the wrapper stamps as each backend finishes
		// each pass, but Prune has no context on the interface and cannot
		// report mid-run: a first-night SQLite prune over a never-pruned
		// history is the longest silence this op can have. Same value as
		// compact-activity-log, for the same reason.
		ProgressTimeout: 20 * time.Minute,
		Plugin:          "maintenance",
		DisplayName:     "Clean activity log",
		Description:     "Compacts old change entries into daily digests, prunes old debug entries, and removes orphaned activity index entries, on every activity database.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.cleanup-activity-log",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runCleanupActivityLog,
	}
}

func (p *Plugin) runCleanupActivityLog(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	// This def declares LivenessManual, and until 2026-09-10 the run never
	// called UpdateProgress — only Log at the end, which the watchdog does not
	// count — so any night whose compaction backlog exceeded ProgressTimeout
	// was cancelled as never_reported. The compaction store emits per-chunk
	// events, and since the other passes started running on both backends the
	// summarize pass emits per-group events and the migrating wrapper emits a
	// completion per backend per pass; forwarding all of them is what keeps
	// this op alive.
	_ = reporter.UpdateProgress(0, 0, "starting activity log cleanup")
	ctx = database.WithMaintenanceProgress(ctx, maintenanceProgressToReporter(reporter))

	// NO SECOND COMPACTION. While nightly compaction is enabled it owns the
	// compaction pass (maintenance.nightly-compact-activity-log, cutoff = start
	// of the local day), and this op must not run its own 14-day pass on top:
	// two compactors with two cutoffs is the configuration this replaced. The
	// legacy pass survives only for a deployment that turned nightly
	// compaction off, with its original semantics (0 or less means 14 days).
	compactMsg := "compaction skipped (nightly compaction is enabled)"
	if !p.deps.ActivityLogNightlyCompactionEnabled() {
		days := p.deps.ActivityLogCompactionDays()
		if days <= 0 {
			days = 14
		}
		res, err := p.deps.CompactActivityEntries(ctx, time.Now().AddDate(0, 0, -days), compactProgressToReporter(reporter))
		if err != nil {
			return fmt.Errorf("compact activity: %w", err)
		}
		compactMsg = fmt.Sprintf("compacted %d", res.DaysCompacted)
	}
	summarized, pruned, indexOrphans, err := p.deps.MaintainActivityLog(
		ctx,
		p.deps.ActivityLogRetentionChangeDays(),
		p.deps.ActivityLogRetentionDebugDays(),
	)
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("Activity log cleanup: %s, summarized %d, pruned %d, orphaned index entries removed %d",
		compactMsg, summarized, pruned, indexOrphans)
	logging.Info(ctx, msg)
	_ = reporter.Log(slog.LevelInfo, msg)
	return nil
}

// --- optimize-activity-db ---

// optimizeActivityDBDef refreshes the activity store's query-planner statistics.
//
// WHY A SEPARATE OP RATHER THAN A STEP INSIDE cleanup-activity-log. Compaction
// is what invalidates the statistics, so appending this to that job is the
// obvious pairing — and it is the wrong one, because of the shape of the cost.
// Measured 2026-09-09 on a reflink copy of the production activity database
// (13,256,788 rows / 19.5 GB / 6 indexes, host at load average ~30):
//
//	ANALYZE, no analysis_limit, no stored stats  : >600 s, did NOT complete
//	ANALYZE, analysis_limit=400, no stored stats :  344 s
//	PRAGMA optimize, limit=400, no stored stats  :  394 s
//	PRAGMA optimize, limit=400, stats present    :    0.002 s
//
// So the recurring cost is ~2 ms and the one-time bootstrap is ~6.5 minutes.
// Folding that bootstrap into cleanup-activity-log would spend a fifth of that
// job's budget, once, on the very run that has the most compaction backlog to
// work through — starving the thing the statistics are meant to help. Split
// apart, each has its own timeout and its own failure record, and a bootstrap
// that overruns cannot take compaction down with it.
//
// NIGHTLY, not weekly: at 2 ms steady-state the schedule costs nothing, and
// statistics that are one day stale are strictly better than seven. The budget
// was 15m, ~2.3x the measured bootstrap, which is the only run that can
// approach it; every subsequent run finishes in milliseconds.
//
// It is NOT done at startup. 394 s of I/O on the boot path would delay every
// restart on a host that restarts several times a day.
//
// 45m, NOT 15m, SINCE 2026-09-19. This run also builds the SQLite activity
// table's covering time indexes the first time it sees a database without them
// (SQLActivityStore.OptimizeStatistics): two whole-table index builds plus an
// ANALYZE of just those two indexes. On a 1M-row / ~2 GB fixture that was 5.0 s
// on an idle Mac; production is 13.2M rows / 19.5 GB on a host that has run at
// load average ~30, and a build cancelled at the deadline rolls back and would
// be retried — and cancelled — every night. Every later run is still ~2 ms, so
// the wider budget only changes how long a genuinely wedged statement survives.
//
// LIVENESS: NONE, WITH A 45m BUDGET. The whole run is one SQL statement that
// cannot report progress, so the only true declaration is LivenessNone. Until
// 2026-09-11 this def declared LivenessManual and posted a reporter.Log line
// before the call on the belief that the line kept the watchdog satisfied. It
// does not: reporter.Log deliberately does not stamp liveness, only
// UpdateProgress does. So the watchdog's default 5-minute ProgressTimeout
// killed the bootstrap every time, before the 15m Timeout above could matter.
// Production logged "bootstrap analyze: context canceled" at 5m on 09-10 and
// again on 09-11. The statistics were never stored, and every run was a new
// bootstrap. ProgressTimeout equals Timeout because an op that never reports
// has nothing to measure staleness against except wall time. A heartbeat
// goroutine was rejected for the reason db.go's db-optimize comment gives: a
// blind heartbeat keeps a genuinely wedged statement alive forever.
func (p *Plugin) optimizeActivityDBDef() sdk.OperationDef {
	sched := "40 3 * * *" // 03:40 daily — after the compaction window, before the morning
	return sdk.OperationDef{
		ID:              "maintenance.optimize-activity-db",
		Liveness:        sdk.LivenessNone,
		ProgressTimeout: 45 * time.Minute, // LivenessNone requires an explicit budget; see above
		Plugin:          "maintenance",
		DisplayName:     "Optimize activity database",
		Description:     "Refreshes the activity store's query-planner statistics (SQLite ANALYZE / PRAGMA optimize).",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.optimize-activity-db",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         45 * time.Minute,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runOptimizeActivityDB,
	}
}

func (p *Plugin) runOptimizeActivityDB(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	// The bootstrap run takes minutes inside a single SQL statement, so this
	// line is posted BEFORE the call to tell a human watching the op why it is
	// silent. It is for people only: Log does not reset the watchdog's progress
	// clock. The def's LivenessNone + ProgressTimeout is what keeps the
	// watchdog off this op.
	_ = reporter.Log(slog.LevelInfo, "Refreshing activity-store query planner statistics "+
		"(first run on a database with no stored statistics takes several minutes)")

	res, err := p.deps.OptimizeActivityStatistics(ctx)
	if err != nil {
		return err
	}
	if !res.Supported {
		msg := "Activity backend keeps no query-planner statistics; nothing to optimize"
		logging.Info(ctx, msg)
		_ = reporter.Log(slog.LevelInfo, msg)
		return nil
	}
	msg := fmt.Sprintf("Activity statistics refreshed: %d stat rows in %s (bootstrap=%t)",
		res.TablesAnalyzed, res.Duration.Round(time.Millisecond), res.Bootstrapped)
	logging.Info(ctx, msg)
	_ = reporter.Log(slog.LevelInfo, msg)
	return nil
}

// --- purge-old-logs ---

func (p *Plugin) purgeOldLogsDef() sdk.OperationDef {
	sched := "0 2 * * 0" // 02:00 every Sunday
	return sdk.OperationDef{
		ID: "maintenance.purge-old-logs",
		// LivenessManual: runPurgeOldLogs reports after every committed chunk
		// (5,000 rows) AND every 10,000 scanned rows (the store's scan
		// heartbeat), so a run that scans months of unexpired rows with nothing
		// to delete still reports. Before 2026-10-11 the whole backlog went in
		// one commit and the op could not report, so it was LivenessNone with a
		// 30-minute silence budget. The budget stays at 30 minutes: a row-count
		// heartbeat cannot fire while a single iterator step is skipping a long
		// run of point tombstones (what the first purge leaves at the front of
		// the syslog: keyspace until compaction drops them), and that stretch
		// is slow but not stuck.
		Liveness:        sdk.LivenessManual,
		ProgressTimeout: 30 * time.Minute,
		Plugin:          "maintenance",
		DisplayName:     "Prune old logs",
		Description:     "Prunes system log entries and legacy operation log rows older than the configured retention period (log_retention_days). Never touches undo history.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.purge-old-logs",
		// Cancellable: the prune checks ctx between chunks and every 10,000
		// scanned rows, so a cancel (or the
		// Timeout below) stops it with every committed chunk kept and the rest
		// left for the next run. It was false while one batch could not be
		// interrupted.
		Cancellable:  true,
		Isolate:      false,
		Timeout:      30 * time.Minute,
		Schedule:     &sched,
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:          p.runPurgeOldLogs,
	}
}

func (p *Plugin) runPurgeOldLogs(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	retentionDays := p.deps.LogRetentionDays()
	if retentionDays <= 0 {
		_ = reporter.Log(slog.LevelInfo, "Log purge disabled (log_retention_days is -1, keep forever), skipping")
		return nil
	}
	_ = reporter.UpdateProgress(0, 0, fmt.Sprintf("Pruning logs older than %d days", retentionDays))
	total, err := p.deps.PruneOldLogs(ctx, retentionDays, func(kind string, scanned, totalPruned int) {
		// Called per committed chunk and per scan heartbeat; either way it
		// stamps the watchdog's liveness clock.
		_ = reporter.UpdateProgress(totalPruned, 0, fmt.Sprintf(
			"Pruned %d records so far; scanning %s (%d rows checked)", totalPruned, kind, scanned))
	})
	if err != nil {
		// Chunks committed before the failure or cancel stay deleted; say how
		// many, then return the error unchanged so the run is recorded as
		// failed or canceled rather than as a clean prune.
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("Log purge stopped after pruning %d records: %v", total, err))
		return err
	}
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Pruned %d records older than %d days", total, retentionDays))
	return nil
}

// --- cleanup-old-backups ---

// cleanupOldBackupsDef is the only registration of the .bak-* retention sweep.
// scheduler.cleanup-old-backups ran the same walk under its own ID and
// ConcurrencyKey until 2026-10-13; it resolves here as a FormerID, and the
// cleanup_old_backups scheduler task enqueues this def.
//
// Permissions and the 1h budgets are the scheduler copy's (it gated on
// settings.manage and carried 1h, this def 30m and no gate), so the alias
// neither widens who can run a deleting op nor shortens the budget prod runs
// were sized against.
//
// Writes is empty on purpose. It names the logical DB tables an op mutates for
// the dispatcher's write-set gate (registry.OperationDef.Writes), and this op
// touches no row: it removes files. It also takes no scan stand-down, which
// exists to keep a metadata apply from interleaving with a scan's row writes.
// The scanner reads only audio files, and a .bak-* sibling is not one, so the
// two never meet on a file either.
//
// What keeps the sweep off a backup another op is writing or reading is the age
// test, not a lock. A backup is named <path>.bak-<unix>[-N] at the moment
// fileops.WriteTagsSafe or KeepBackupCopy makes it, so it is seconds old by its
// own name however old the audio's mtime is, and retention is at least one day
// (config.EffectiveBackupRetentionDays never returns less; the run also floors
// a non-positive value at 30). Nothing in the tree restores from a .bak-* file.
//
// ResumePolicy is ResumeRequeue (both copies were ResumeDrop) because the sweep
// is idempotent: it keeps no cursor, a file removed by an interrupted run is
// simply absent from the rerun's walk, a file that vanishes between the walk and
// the remove only logs a warning, and the age test gives the same answer on the
// rerun (TestCleanupOldBackups_RequeueIsIdempotent).
func (p *Plugin) cleanupOldBackupsDef() sdk.OperationDef {
	sched := "0 5 * * *" // 05:00 daily
	return sdk.OperationDef{
		ID:              "maintenance.cleanup-old-backups",
		FormerIDs:       []string{"scheduler.cleanup-old-backups"},
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Liveness:        sdk.LivenessNone,
		ProgressTimeout: 1 * time.Hour, // LivenessNone requires an explicit budget
		Plugin:          "maintenance",
		DisplayName:     "Clean old backup files",
		Description:     "Removes .bak-* backup files past the configured retention period.",
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.cleanup-old-backups",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         1 * time.Hour,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead, sdk.CapFilesWrite},
		Run:             p.runCleanupOldBackups,
	}
}

func (p *Plugin) runCleanupOldBackups(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	rootDir := p.deps.RootDir()
	if rootDir == "" {
		_ = reporter.Log(slog.LevelInfo, "No root directory configured, skipping backup cleanup")
		return nil
	}
	retentionDays := p.deps.BackupRetentionDays()
	if retentionDays <= 0 {
		retentionDays = 30
	}
	maxAge := time.Duration(retentionDays) * 24 * time.Hour
	removed := 0
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Scanning %s for .bak-* files older than %d days", rootDir, retentionDays))

	// The library root holds a multi-GB backup directory and an OpenLibrary
	// dump directory, both operator-settable to dot-free names, and this op
	// DELETES by filename. This rule exists in two places with two different
	// predicates (the other is internal/maintenance/jobs/cleanup_backups.go,
	// which uses a different regex and no retention age); consolidating them is
	// a separate change (TODO BACKUP-SWEEP-CONSOLIDATE).
	app := appdirs.Current()
	err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if pathutil.ShouldSkipDir(rootDir, path, app) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.Contains(info.Name(), ".bak-") {
			// Age is the time in the backup's NAME (fileops.BackupTime: <unix>[-N]
			// or the legacy YYYYMMDD-HHMMSS), falling back to mtime for a name
			// without one. A create_backups backup is a hardlink and keeps the
			// original audio's mtime, often years old, so mtime alone would delete
			// a fresh backup on the next run (fileops KeepBackup, AGE CONTRACT).
			age := time.Since(fileops.BackupTime(info.Name(), info.ModTime()))
			if age > maxAge {
				if rmErr := os.Remove(path); rmErr != nil {
					logging.Warn(ctx, "failed to remove old backup", "path", path, "err", rmErr)
				} else {
					removed++
					logging.Info(ctx, "cleaned up old backup", "path", path, "age", age.Round(time.Hour))
				}
			}
		}
		return nil
	})
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Backup cleanup complete: removed %d file(s)", removed))
	return err
}

// --- trash-cleanup ---

// trashCleanupDef is the only registration of the trashed-version purge.
// scheduler.trash-cleanup ran the same body under its own ID and
// ConcurrencyKey until 2026-10-10; it resolves here as a FormerID, and the
// trash_cleanup scheduler task enqueues this def. Permissions and the 1h
// budgets are the stricter gate and longer budgets the merged def carried.
func (p *Plugin) trashCleanupDef() sdk.OperationDef {
	sched := "0 6 * * *" // 06:00 daily
	return sdk.OperationDef{
		ID:              "maintenance.trash-cleanup",
		FormerIDs:       []string{"scheduler.trash-cleanup"},
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Liveness:        sdk.LivenessNone,
		ProgressTimeout: 1 * time.Hour, // LivenessNone requires an explicit budget
		Plugin:          "maintenance",
		DisplayName:     "Purge trashed book versions",
		Description:     "Purges trashed book file versions past their 14-day TTL.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.trash-cleanup",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         1 * time.Hour,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite, sdk.CapFilesWrite},
		Run:             p.runTrashCleanup,
	}
}

func (p *Plugin) runTrashCleanup(_ context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	purged := p.deps.CleanupTrashedVersions()
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Trash cleanup: purged %d versions", purged))
	return nil
}

// ----- helpers -----

// ctxOpID extracts the operation ID stored in context by the UOS worker loop.
func ctxOpID(ctx context.Context) string {
	return OpIDFromContext(ctx)
}

type opIDKey struct{}

// WithOpID returns a context carrying the operation ID for use by run functions.
//
// The only production caller is the run-context decorator installed in
// internal/server (op_run_context.go). If that wiring is ever removed, every
// `if operationID != ""` guard in this package goes quiet and the ops stop
// recording undo history — silently, with no error and a successful-looking
// result. TestOpRunContextDecorator_CarriesOpIDForMaintenanceOps exists to fail
// if that happens.
func WithOpID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, opIDKey{}, id)
}

// OpIDFromContext returns the operation ID carried by ctx, or "" if none is
// present. Exported so the wiring that supplies it can be tested from the
// package that installs it; ops inside this package use ctxOpID.
func OpIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(opIDKey{}).(string); ok {
		return v
	}
	return ""
}
