// file: internal/scheduler/extra_ops.go
// version: 1.28.0
// guid: a9b8c7d6-e5f4-3210-fedc-ba9876543210
// last-edited: 2026-10-13

// extra_ops registers OperationDefs for 3 scheduler tasks that previously
// used the legacy triggerOperation / triggerOperationWithID helpers.  Each def
// extracts the closure logic into a typed Run func so the UOS v2 dispatcher
// can manage lifecycle, cancellation, and concurrency without the old
// BridgeQueue.
//
// Extracted from internal/server/scheduler_extra_ops.go (SERVER-THIN-RESIDUAL).
// Receiver changed from *Server to *ExtraOpsRegistrar.

package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/appdirs"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/gin-gonic/gin"
)

// ExtraOpsDeps holds the typed dependencies needed by ExtraOpsRegistrar.
// All fields are optional (nil-safe) to match the server's lazy-init pattern.
type ExtraOpsDeps struct {
	ActivityWriter       *activity.Writer
	DedupCache           *cache.Cache[gin.H]
	DedupEngine          *dedup.Engine
	MetadataFetchService *metafetch.Service
	// ScanStandDown is the scan stand-down gate the metadata upgrade op
	// holds while it applies (metadata is never applied during a library scan).
	// Nil = ungated (tests).
	ScanStandDown opsregistry.ScanStandDownGate
}

// ExtraOpsStore is everything the remaining scheduler OperationDefs need from
// the store: metabatch.Store, which the metadata upgrade forwards into its
// service. Was database.Store (398 methods) until 2026-08-19;
// versions.TrashedVersionCleaner and the db-optimize/tombstone methods left
// with scheduler.trash-cleanup, scheduler.db-optimize and
// scheduler.tombstone-cleanup on 2026-10-10, GetAllBooksCore left with
// scheduler.metadata-refresh on 2026-10-11 (04-P4c), GetAuthorByID and
// GetBooksByAuthorIDWithRoleCore left with scheduler.resolve-production-authors
// the same day (04-P4d), and the author, author-link and ModifyBook methods
// left with scheduler.author-split-scan on 2026-10-13 (04-P4e).
type ExtraOpsStore interface {
	metabatch.Store // NewMetadataUpgradeService
}

// ExtraOpsRegistrar holds a Store reference and typed Deps so the 3 scheduler
// OperationDefs can be registered without a *Server pointer.
type ExtraOpsRegistrar struct {
	Deps  ExtraOpsDeps
	Store ExtraOpsStore
}

// NewExtraOpsRegistrar constructs an ExtraOpsRegistrar.
func NewExtraOpsRegistrar(store ExtraOpsStore, deps ExtraOpsDeps) *ExtraOpsRegistrar {
	return &ExtraOpsRegistrar{Store: store, Deps: deps}
}

// extraOpsProgressAdapter bridges opsregistry.Reporter → operations.ProgressReporter
// so the run* helpers can be called from a v2 op Run function without changes.
type extraOpsProgressAdapter struct{ r opsregistry.Reporter }

func (a extraOpsProgressAdapter) UpdateProgress(current, total int, message string) error {
	return a.r.UpdateProgress(current, total, message)
}
func (a extraOpsProgressAdapter) Log(level, message string, details *string) error {
	l := slog.LevelInfo
	switch level {
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	case "debug":
		l = slog.LevelDebug
	}
	var attrs []slog.Attr
	if details != nil {
		attrs = append(attrs, slog.String("details", *details))
	}
	return a.r.Log(l, message, attrs...)
}
func (a extraOpsProgressAdapter) IsCanceled() bool { return a.r.IsCanceled() }

// --- dedup-llm-review ---

// RegisterDedupLLMReviewOp registers the scheduler.dedup-llm-review OperationDef.
func (r *ExtraOpsRegistrar) RegisterDedupLLMReviewOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "scheduler.dedup-llm-review",
		Liveness:        opsregistry.LivenessNone,
		ProgressTimeout: 2 * time.Hour, // LivenessNone requires an explicit budget
		Plugin:          "scheduler",
		DisplayName:     "Dedup LLM Review",
		Description:     "Run LLM review on ambiguous dedup candidates.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "scheduler.dedup-llm-review",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			progress := extraOpsProgressAdapter{r: reporter}
			if r.Deps.DedupEngine == nil {
				_ = progress.Log("info", "Dedup engine not initialized, skipping LLM review", nil)
				return nil
			}
			_ = progress.Log("info", "Starting LLM review of ambiguous dedup candidates", nil)
			return r.Deps.DedupEngine.RunLLMReview(ctx)
		},
	})
}

// --- metadata-upgrade ---

// RegisterMetadataUpgradeOp registers the scheduler.metadata-upgrade OperationDef.
//
// It is the ONLY registration of the metadata upgrade (2026-09-27). Until then
// the same job was also registered as maintenance.metadata-upgrade by the
// maintenance plugin, a second def with its own ConcurrencyKey, so the two
// could run at once over the same books. Nothing in web/src or the handlers
// named the maintenance ID; it stays reachable as a FormerID, so an old
// operations_v2 row or an API enqueue by that ID resolves to this def.
func (r *ExtraOpsRegistrar) RegisterMetadataUpgradeOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "scheduler.metadata-upgrade",
		FormerIDs:       []string{"maintenance.metadata-upgrade"},
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "scheduler",
		DisplayName:     "Metadata Upgrade",
		Description:     "Replace metadata from lower-ranked sources (Open Library, Google Books, Wikipedia) with a higher-ranked source's (Hardcover, Audible) when its match passes the bulk-apply gate. User-locked fields are kept.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         4 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "scheduler.metadata-upgrade",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		// Audible and generic network: the search queries every metadata
		// provider. OpenAI: the search may LLM-rerank its candidates.
		Capabilities: []opsregistry.Capability{
			opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite,
			opsregistry.CapNetworkAudible, opsregistry.CapNetworkGeneric, opsregistry.CapNetworkOpenAI,
		},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) (retErr error) {
			// Metadata is never applied during a library scan: hold the scan
			// stand-down before the first write. A running scan does not make
			// the op refuse: the acquire quiesces it and waits for it to park
			// (bounded by the lease), and the op fails only if it never parks.
			hold, sdErr := opsregistry.HoldScanStandDown(ctx, r.Deps.ScanStandDown, reporter, "scheduler.metadata-upgrade apply")
			if sdErr != nil {
				return sdErr
			}
			defer func() { retErr = hold.Finish(retErr) }()
			ctx, reporter = hold.Context(), hold.Reporter()

			progress := extraOpsProgressAdapter{r: reporter}
			p := sdk.NewProgress(reporter, 0)
			p.Start("Scanning for books with upgradeable metadata sources...")
			if r.Deps.MetadataFetchService == nil {
				return fmt.Errorf("metadata fetch service not initialized")
			}
			svc := metabatch.NewMetadataUpgradeService(r.Store, r.Deps.MetadataFetchService)
			_ = progress.Log("info", "Scanning for books with upgradeable metadata sources...", nil)
			// M7 (2026-07 error-correction sweep): thread progress through so
			// this 4-hour-timeout op reports books processed/total every 25
			// books instead of going silent between start and result.
			result, err := svc.RunUpgrade(ctx, 200, progress)
			if err != nil {
				return err
			}
			msg := fmt.Sprintf("Metadata upgrade complete: checked %d, upgraded %d, skipped %d, "+
				"owner-manual-only %d (Doctor Who / Big Finish / Torchwood), errors %d; sweep after %q -> %q (wrapped %v)",
				result.Checked, result.Upgraded, result.Skipped, result.OwnerManualOnly, result.Errors,
				result.CursorStart, result.CursorEnd, result.Wrapped)
			_ = progress.Log("info", msg, nil)
			p.Done(msg)
			return nil
		},
	})
}

// --- cleanup-old-backups ---

// RegisterCleanupOldBackupsOp registers the scheduler.cleanup-old-backups OperationDef.
func (r *ExtraOpsRegistrar) RegisterCleanupOldBackupsOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "scheduler.cleanup-old-backups",
		Liveness:        opsregistry.LivenessNone,
		ProgressTimeout: 1 * time.Hour, // LivenessNone requires an explicit budget
		Plugin:          "scheduler",
		DisplayName:     "Cleanup Old Backups",
		Description:     "Remove old .bak-* backup files past the configured retention period.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         1 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "scheduler.cleanup-old-backups",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapFilesWrite},
		Run: func(ctx context.Context, _ json.RawMessage, reporter opsregistry.Reporter) error {
			return runCleanupOldBackups(ctx, extraOpsProgressAdapter{r: reporter})
		},
	})
}

// runCleanupOldBackups is the body of scheduler.cleanup-old-backups, lifted out
// of the OperationDef literal so the app-directory guard below can be driven by
// a test without standing up a Registry, a store and a bus. Extracting it
// changes no behaviour; the closure above now only adapts the reporter.
// cleanupProgressLogger is what runCleanupOldBackups actually calls: one
// method. extraOpsProgressAdapter satisfies it, and so does a two-line test
// stub -- which is the point, since the alternative is standing up a Registry,
// a store and a bus to exercise a filesystem guard.
type cleanupProgressLogger interface {
	Log(level, message string, details *string) error
}

func runCleanupOldBackups(ctx context.Context, progress cleanupProgressLogger) error {
	rootDir := config.AppConfig.RootDir
	if rootDir == "" {
		_ = progress.Log("info", "No root directory configured, skipping backup cleanup", nil)
		return nil
	}
	retentionDays := config.EffectiveBackupRetentionDays()
	maxAge := time.Duration(retentionDays) * 24 * time.Hour
	removed := 0
	_ = progress.Log("info", fmt.Sprintf("Scanning %s for .bak-* files older than %d days", rootDir, retentionDays), nil)

	// The library root holds application state as well as books: a backup
	// directory of multi-GB archives and an OpenLibrary dump directory with an
	// embedded database. Both are operator-settable to names with no leading
	// dot, so the dot rule alone never covered them. This op DELETES by
	// filename, so descending into them is not merely wasted I/O over ~90 GB --
	// an operator file named "*.bak-*" parked in the backup directory would be
	// removed.
	//
	// NOTE: this is one of THREE implementations of "delete old backup files"
	// in the tree (see internal/plugins/maintenance/cleanup.go with the same
	// predicate, and internal/maintenance/jobs/cleanup_backups.go with a
	// different regex). All three are guarded, and the first two now share one
	// retention source, config.EffectiveBackupRetentionDays(); consolidating them
	// is a separate change.
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
					slog.Warn("failed to remove old backup:", "path", path, "rmErr", rmErr)
				} else {
					removed++
					slog.Info("cleaned up old backup (age )", "path", path, "age", age.Round(time.Hour))
				}
			}
		}
		return nil
	})
	_ = progress.Log("info", fmt.Sprintf("Backup cleanup complete: removed %d file(s)", removed), nil)
	return err
}
