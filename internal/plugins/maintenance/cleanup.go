// file: internal/plugins/maintenance/cleanup.go
// version: 1.0.2
// guid: c3d4e5f6-a7b8-9012-cdef-234567890123
// last-edited: 2026-07-12

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

	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- purge-deleted ---

func (p *Plugin) purgeDeletedDef() sdk.OperationDef {
	sched := "0 3 * * *" // 03:00 daily
	return sdk.OperationDef{
		ID:              "maintenance.purge-deleted",
		Plugin:          "maintenance",
		DisplayName:     "Purge soft-deleted books",
		Description:     "Permanently removes soft-deleted books that have exceeded the retention period.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.purge-deleted",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         30 * time.Minute,
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
	p.deps.RunAutoPurgeSoftDeleted(opID)
	p.deps.ActivityFlushOp(opID)
	_ = reporter.Log(slog.LevelInfo, "Purge complete")
	prog.Done("Purge complete")
	return nil
}

// --- tombstone-cleanup ---

func (p *Plugin) tombstoneCleanupDef() sdk.OperationDef {
	sched := "0 4 * * *" // 04:00 daily
	return sdk.OperationDef{
		ID:              "maintenance.tombstone-cleanup",
		Plugin:          "maintenance",
		DisplayName:     "Resolve author tombstone chains",
		Description:     "Flattens multi-hop author tombstone chains (A→B→C becomes A→C).",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.tombstone-cleanup",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         15 * time.Minute,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runTombstoneCleanup,
	}
}

func (p *Plugin) runTombstoneCleanup(_ context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.Store()
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

func (p *Plugin) tempFileCleanupDef() sdk.OperationDef {
	sched := "30 1 * * *" // 01:30 daily
	return sdk.OperationDef{
		ID:              "maintenance.temp-file-cleanup",
		Plugin:          "maintenance",
		DisplayName:     "Clean orphaned temp files",
		Description:     "Removes orphaned *.tmp.m4b / *.tmp.m4a files left by crashed ffmpeg operations.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.temp-file-cleanup",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         20 * time.Minute,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead, sdk.CapFilesWrite},
		Run:             p.runTempFileCleanup,
	}
}

func (p *Plugin) runTempFileCleanup(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	opID := ctxOpID(ctx)
	removed := p.deps.CleanupOrphanedTempFiles(p.deps.RootDir(), opID)
	p.deps.ActivityFlushOp(opID)
	msg := fmt.Sprintf("Removed %d orphaned temp files", removed)
	_ = reporter.Log(slog.LevelInfo, msg)
	return nil
}

// --- cleanup-activity-log ---

func (p *Plugin) cleanupActivityLogDef() sdk.OperationDef {
	sched := "0 0 * * *" // midnight daily
	return sdk.OperationDef{
		ID:              "maintenance.cleanup-activity-log",
		Plugin:          "maintenance",
		DisplayName:     "Clean activity log",
		Description:     "Compacts old change entries into daily digests and prunes old debug entries.",
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
	compacted, summarized, pruned, err := p.deps.CompactActivityLog(
		ctx,
		p.deps.ActivityLogCompactionDays(),
		p.deps.ActivityLogRetentionChangeDays(),
		p.deps.ActivityLogRetentionDebugDays(),
	)
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("Activity log cleanup: compacted %d, summarized %d, pruned %d",
		compacted, summarized, pruned)
	logging.Info(ctx, msg)
	_ = reporter.Log(slog.LevelInfo, msg)
	return nil
}

// --- purge-old-logs ---

func (p *Plugin) purgeOldLogsDef() sdk.OperationDef {
	sched := "0 2 * * 0" // 02:00 every Sunday
	return sdk.OperationDef{
		ID:              "maintenance.purge-old-logs",
		Plugin:          "maintenance",
		DisplayName:     "Prune old operation logs",
		Description:     "Prunes operation logs and system activity logs older than the configured retention period.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.purge-old-logs",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runPurgeOldLogs,
	}
}

func (p *Plugin) runPurgeOldLogs(_ context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	retentionDays := p.deps.LogRetentionDays()
	if retentionDays <= 0 {
		_ = reporter.Log(slog.LevelInfo, "Log retention not configured, skipping")
		return nil
	}
	if err := p.deps.PruneOldLogs(retentionDays); err != nil {
		return err
	}
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Pruned logs older than %d days", retentionDays))
	return nil
}

// --- cleanup-old-backups ---

func (p *Plugin) cleanupOldBackupsDef() sdk.OperationDef {
	sched := "0 5 * * *" // 05:00 daily
	return sdk.OperationDef{
		ID:              "maintenance.cleanup-old-backups",
		Plugin:          "maintenance",
		DisplayName:     "Clean old backup files",
		Description:     "Removes .bak-* backup files past the configured retention period.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.cleanup-old-backups",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,
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

	err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.Contains(info.Name(), ".bak-") {
			age := time.Since(info.ModTime())
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

func (p *Plugin) trashCleanupDef() sdk.OperationDef {
	sched := "0 6 * * *" // 06:00 daily
	return sdk.OperationDef{
		ID:              "maintenance.trash-cleanup",
		Plugin:          "maintenance",
		DisplayName:     "Purge trashed book versions",
		Description:     "Purges trashed book file versions past their 14-day TTL.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.trash-cleanup",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         20 * time.Minute,
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

// --- archive-sweep ---

func (p *Plugin) archiveSweepDef() sdk.OperationDef {
	sched := "0 7 * * *" // 07:00 daily
	return sdk.OperationDef{
		ID:              "maintenance.archive-sweep",
		Plugin:          "maintenance",
		DisplayName:     "Archive sweep",
		Description:     "Removes soft-deleted books past the 30-day retention window.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.archive-sweep",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         20 * time.Minute,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runArchiveSweep,
	}
}

func (p *Plugin) runArchiveSweep(_ context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	cleaned := p.deps.SweepArchivedBooks()
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Archive sweep: cleaned %d books", cleaned))
	return nil
}

// ----- helpers -----

// ctxOpID extracts the operation ID stored in context by the UOS worker loop.
func ctxOpID(ctx context.Context) string {
	if v, ok := ctx.Value(opIDKey{}).(string); ok {
		return v
	}
	return ""
}

type opIDKey struct{}

// WithOpID returns a context carrying the operation ID for use by run functions.
func WithOpID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, opIDKey{}, id)
}
