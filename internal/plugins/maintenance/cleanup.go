// file: internal/plugins/maintenance/cleanup.go
// version: 1.28.0
// guid: c3d4e5f6-a7b8-9012-cdef-234567890123
// last-edited: 2026-10-27

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/appdirs"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/sweep"
	"github.com/falkcorp/audiobook-organizer/internal/transcode"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
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
	err := p.deps.RunAutoPurgeSoftDeleted(ctx, opID)
	p.deps.ActivityFlushOp(opID)
	if err != nil {
		// Refused (the undo records could not be read) or stopped before it
		// decided every book (timeout, shutdown, watchdog, quiesce): a failed
		// run, not a completed one that did nothing. What is left is picked up
		// by the next run.
		_ = reporter.Log(slog.LevelWarn, "Purge did not finish: "+err.Error())
		prog.Done("Purge did not finish: " + err.Error())
		return err
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

// --- transcode-temp-cleanup ---

// transcodeTempMaxAge is the age past which a transcode temp file is stale: the
// value the retired per-import-path ticker used. Fixed rather than a parameter,
// because a caller-supplied age is a deletion threshold (a huge hour count
// overflows time.Duration negative and would mark every file stale).
const transcodeTempMaxAge = 2 * time.Hour

// transcodeTempCleanupDef sweeps stale transcode temp files under every import
// path. It is a def of its own, not a mode of maintenance.temp-file-cleanup,
// because the registry dedupes an enqueue on def id alone when the def carries a
// cron Schedule: sharing temp-file-cleanup's id would make the hourly import-path
// run and the daily library-root run (and library-optimize's child enqueue)
// silently swallow each other. It declares NO Schedule (the hourly
// transcode_temp_cleanup scheduler task drives it) and shares temp-file-cleanup's
// ConcurrencyKey, which serializes the two without deduping them.
func (p *Plugin) transcodeTempCleanupDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.transcode-temp-cleanup",
		Preview:         sdk.PreviewSpec{Params: opmode.DryRunParams{}},
		Permissions:     []auth.Permission{auth.PermSettingsManage}, // deletes files
		Liveness:        sdk.LivenessNone,
		ProgressTimeout: tempFileCleanupProgressTimeout, // LivenessNone requires an explicit budget
		Plugin:          "maintenance",
		DisplayName:     "Clean stale transcode temp files",
		Description:     "Removes transcode temp files older than 2 hours under every import path. Previews (counts only) unless dry_run is false.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.temp-file-cleanup",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         1 * time.Hour,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead, sdk.CapFilesWrite},
		Run:             p.runTranscodeTempCleanup,
	}
}

// runTranscodeTempCleanup reads the import paths on every run, so one added
// after boot is swept without a restart (the boot-time tickers this replaced
// never saw it). Each import path is its own walk root and this DELETES, so
// appdirs.Current() is passed exactly as the tickers did: an app directory
// configured inside an import path is skipped. A path that is gone is logged
// and skipped, not fatal. Without dry_run=false it only counts (preview).
func (p *Plugin) runTranscodeTempCleanup(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	// Owner rule 2026-09-25: an omitted mode is a PREVIEW. The hourly scheduler
	// task passes dry_run=false explicitly.
	dryRun, err := opmode.ParseDryRun("maintenance.transcode-temp-cleanup", raw)
	if err != nil {
		return err
	}
	paths, err := p.deps.OpsStore().GetAllImportPaths()
	if err != nil {
		return fmt.Errorf("list import paths: %w", err)
	}
	app := appdirs.Current()
	removed, swept := 0, 0
	for _, ip := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, statErr := os.Stat(ip.Path); statErr != nil {
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("Skipping import path %q: %v", ip.Path, statErr))
			continue
		}
		removed += transcode.CleanupStaleTempFiles(ip.Path, app, transcodeTempMaxAge, dryRun)
		swept++
	}
	verb := "Removed"
	if dryRun {
		verb = "Preview: would remove"
	}
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("%s %d stale transcode temp files from %d of %d import paths", verb, removed, swept, len(paths)))
	return nil
}

// --- cleanup-activity-log ---

func (p *Plugin) cleanupActivityLogDef() sdk.OperationDef {
	sched := "0 0 * * *" // midnight daily
	return sdk.OperationDef{
		ID:       "maintenance.cleanup-activity-log",
		Liveness: sdk.LivenessManual,
		// Raised from the 5m default on 2026-09-10, when every pass ran on
		// both the Pebble and the SQLite activity backend and a first-night
		// SQLite prune over a never-pruned history was the longest silence
		// this op could have. Compaction, Summarize and Prune now all stamp
		// per chunk/group/batch on the one Pebble store; the value is kept,
		// the same as compact-activity-log, rather than tightened without a
		// measurement.
		ProgressTimeout: 20 * time.Minute,
		Plugin:          "maintenance",
		DisplayName:     "Clean activity log",
		Description:     "Compacts old change entries into daily digests, prunes old debug entries, and removes orphaned activity index entries.",
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
	// events, the summarize pass per-group events and the prune per-batch
	// events, and MaintainActivityLog stamps each phase boundary; forwarding
	// all of them is what keeps this op alive.
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
// simply absent from the rerun's walk, a remove that fails (a file that vanished
// after the walk saw it included) only logs a warning and is not counted, and
// the age test gives the same answer on the rerun
// (TestCleanupOldBackups_RequeueIsIdempotent, and
// TestRunCleanupOldBackups_FailedRemoveIsLoggedNotCounted for the failed remove).
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
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Scanning %s for .bak-* files older than %d days", rootDir, retentionDays))

	// The walk, the app-dir guard, the name-time age test and the remove live in
	// sweep.RemoveOldBackupFiles. This op supplies the one thing that is its own:
	// which names are backups. Only a name fileops wrote is one, aged by the time
	// in that name (a create_backups backup is a hardlink and keeps the original
	// audio's mtime, often years old, so mtime would delete a fresh backup on the
	// next run; fileops KeepBackup, AGE CONTRACT). Anything else that merely
	// contains ".bak-" (a book called "x.bak-notes.m4b") is kept at any age.
	removed, err := sweep.RemoveOldBackupFiles(ctx, rootDir, appdirs.Current(),
		fileops.BackupNameTime,
		maxAge,
		func(level slog.Level, msg string, kv ...any) {
			switch {
			case level >= slog.LevelWarn:
				logging.Warn(ctx, msg, kv...)
			case level >= slog.LevelInfo:
				logging.Info(ctx, msg, kv...)
			default:
				logging.Debug(ctx, msg, kv...)
			}
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

// TrashCleanupResult is the result payload of maintenance.trash-cleanup. The
// id lists are what the operation page links from: a purge that kept files, or
// gave up, is not "purged N" and must be found from the result, not only from
// the log.
type TrashCleanupResult struct {
	Purged int `json:"purged"`
	Failed int `json:"failed"`
	// KeptFiles counts the versions purged with files left on disk because
	// another row or book names them; they are also counted in Purged.
	KeptFiles      int      `json:"kept_files"`
	KeptVersionIDs []string `json:"kept_version_ids,omitempty"`
	KeptBookIDs    []string `json:"kept_book_ids,omitempty"`
	KeptFilePaths  []string `json:"kept_file_paths,omitempty"`
	// NeedsAttention counts the versions whose purge gave up on a file it cannot
	// remove (purge_failed); the cleanup does not retry them and does not count
	// them in Failed.
	NeedsAttention           int      `json:"needs_attention"`
	NeedsAttentionVersionIDs []string `json:"needs_attention_version_ids,omitempty"`
	NeedsAttentionBookIDs    []string `json:"needs_attention_book_ids,omitempty"`
	// StoppedForMerge is true when a merge or combine held the merge lock for
	// the whole wait, so the run stopped there and left the remaining versions
	// for the next night. It is not a failure and does not turn the op red.
	StoppedForMerge bool `json:"stopped_for_merge,omitempty"`
	// StoppedForMergeNights is how many nights in a row, this one included, the
	// run stopped for the merge lock. At versions.MergeStopEscalateAfter or more
	// the page and the log say the trash is not being purged; it is still not a
	// failure and does not turn the op red.
	StoppedForMergeNights int `json:"stopped_for_merge_nights,omitempty"`
	// StoppedForMergeRepeatedly is StoppedForMergeNights >= versions.MergeStopEscalateAfter,
	// decided here so the page does not repeat the threshold.
	StoppedForMergeRepeatedly bool `json:"stopped_for_merge_repeatedly,omitempty"`
}

func (p *Plugin) runTrashCleanup(_ context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	res, err := p.deps.CleanupTrashedVersions()
	payload := TrashCleanupResult{Purged: res.Purged, Failed: res.Failed, KeptFiles: len(res.KeptFiles), NeedsAttention: len(res.NeedsAttention), StoppedForMerge: res.StoppedForMerge, StoppedForMergeNights: res.MergeStopStreak, StoppedForMergeRepeatedly: res.MergeStopStreak >= versions.MergeStopEscalateAfter}
	for _, k := range res.KeptFiles {
		payload.KeptVersionIDs = append(payload.KeptVersionIDs, k.VersionID)
		payload.KeptBookIDs = append(payload.KeptBookIDs, k.BookID)
		for _, f := range k.Files {
			payload.KeptFilePaths = append(payload.KeptFilePaths, f.Path)
		}
	}
	for _, k := range res.NeedsAttention {
		payload.NeedsAttentionVersionIDs = append(payload.NeedsAttentionVersionIDs, k.VersionID)
		payload.NeedsAttentionBookIDs = append(payload.NeedsAttentionBookIDs, k.BookID)
	}
	// A result the page cannot read back would leave a run that purged versions
	// looking like a run that did nothing; a failed write is reported on the op.
	setErr := opsregistry.ReporterSetResult(reporter, payload)

	if len(res.KeptFiles) > 0 {
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("Trash cleanup: %d of the purged versions kept files another row or book names (versions %s, books %s)",
			len(res.KeptFiles), strings.Join(payload.KeptVersionIDs, ", "), strings.Join(payload.KeptBookIDs, ", ")))
	}
	if len(res.NeedsAttention) > 0 {
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("Trash cleanup: %d version(s) cannot be purged (a file cannot be removed: permission or a read-only filesystem) and are not retried; Purge now retries one, or hard delete drops it (versions %s, books %s)",
			len(res.NeedsAttention), strings.Join(payload.NeedsAttentionVersionIDs, ", "), strings.Join(payload.NeedsAttentionBookIDs, ", ")))
	}
	// The summary line is built before the error is looked at: a run that failed
	// some purges has still purged, kept, deferred and stopped for the lock, and
	// the log of the night it failed is the one the owner reads.
	msg := fmt.Sprintf("Trash cleanup: purged %d versions", res.Purged)
	if n := len(res.KeptFiles); n > 0 {
		msg = fmt.Sprintf("Trash cleanup: purged %d versions, %d of them with files kept (versions %s)", res.Purged, n, strings.Join(payload.KeptVersionIDs, ", "))
	}
	if n := len(res.NeedsAttention); n > 0 {
		msg += fmt.Sprintf("; %d need attention (versions %s)", n, strings.Join(payload.NeedsAttentionVersionIDs, ", "))
	}
	if res.StoppedForMerge {
		msg += "; stopped early: a merge or combine was running, the rest runs next night"
	}
	if res.MergeStopStreak >= versions.MergeStopEscalateAfter {
		// Not a failure (the op stays green), but three nights of nothing purged
		// because the merge lock was never free is something to go and look at.
		warn := fmt.Sprintf("Trash cleanup: stopped for the merge lock %d nights in a row, so the trash is not being purged; check what holds the merge lock around this time (a long combine, or merges queuing on it)", res.MergeStopStreak)
		_ = reporter.Log(slog.LevelWarn, warn)
	}
	if err != nil {
		// Some due purges failed (or the trash could not be listed): say so in
		// the operation log and fail the op, so a night in which nothing could
		// be purged does not read as "purged 0 versions" with a success status.
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("%s; %d failed: %v", msg, res.Failed, err))
		return err
	}
	_ = reporter.Log(slog.LevelInfo, msg)
	if setErr != nil {
		return fmt.Errorf("trash cleanup: persist result: %w", setErr)
	}
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
