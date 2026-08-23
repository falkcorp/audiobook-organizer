// file: internal/maintenance/jobs/cleanup_empty_folders.go
// version: 1.5.0
// guid: a1000006-0000-0000-0000-000000000006
// last-edited: 2026-08-23

package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

func init() { maintenance.Register(&cleanupEmptyFoldersJob{}) }

type cleanupEmptyFoldersJob struct{}

func (j *cleanupEmptyFoldersJob) ID() string       { return "cleanup-empty-folders" }
func (j *cleanupEmptyFoldersJob) Name() string     { return "Cleanup Empty Folders" }
func (j *cleanupEmptyFoldersJob) Category() string { return "cleanup" }
func (j *cleanupEmptyFoldersJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: true}
}
func (j *cleanupEmptyFoldersJob) Description() string {
	return "Remove empty directories from the library root (bottom-up walk, deepest first)"
}
func (j *cleanupEmptyFoldersJob) CanResume() bool { return true }

func (j *cleanupEmptyFoldersJob) Run(ctx context.Context, _ maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) error {
	root := config.AppConfig.RootDir
	if root == "" {
		slog.Warn("cleanup-empty-folders RootDir not configured")
		return nil
	}

	// Collect all directories with a top-down walk, then sort deepest first
	// so children are processed before their parents.
	var dirs []string
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() || path == root {
			return nil
		}
		dirs = append(dirs, path)
		return nil
	}); err != nil {
		return fmt.Errorf("cleanup-empty-folders: walk error: %w", err)
	}

	// Sort by descending path length so deepest directories come first.
	sort.Slice(dirs, func(i, k int) bool { return len(dirs[i]) > len(dirs[k]) })

	reporter.SetTotal(len(dirs))
	slog.Info("cleanup-empty-folders found directories to check (dry_run)", "dirs_count", len(dirs), "dryRun", dryRun)

	removed := 0
	for _, dir := range dirs {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			slog.Error("cleanup-empty-folders failed to read", "dir", dir, "err", err)
			reporter.Increment()
			continue
		}

		if len(entries) > 0 {
			reporter.Increment()
			continue
		}

		if dryRun {
			slog.Info("[dry] would remove empty dir", "dir", dir)
		} else {
			if err := os.Remove(dir); err != nil {
				slog.Error("cleanup-empty-folders failed to remove", "dir", dir, "err", err)
			} else {
				slog.Info("removed empty dir", "dir", dir)
				removed++
			}
		}
		reporter.Increment()
	}

	slog.Info("cleanup-empty-folders complete — checked dirs, removed (dry_run)", "dirs_count", len(dirs), "removed", removed, "dryRun", dryRun)
	return nil
}

// Policy: ResumeRestart. CanResume() is true and this job checkpoints nothing,
// so a resume re-runs it; ResumeRestart is what makes that actually happen.
//
// This was ResumeDrop until 2026-08-23, on the reasoning that a dry_run:true job
// could not take ResumeRequeue because server.resumeV2Op re-enqueues with nil
// params, under which DryRun resolves to false and a preview runs for real. That
// reasoning no longer applies, on two independent grounds. First, resumeV2Op is
// unreachable for maintenance: its one caller is fed from GetInterruptedOperations
// (v1 rows) and dispatches only when opRegistry.Def(op.Type) resolves, but v1
// maintenance rows are typed "maintenance:<job>" while v2 defs are
// "maintenance.<job>", and RegisterOp rejects ids containing ":". Second, and
// decisively, ResumeRestart never requeues at all — it updates the existing row
// in place, so Params (dry_run included) is preserved by construction rather than
// reconstructed. TestResume_PreservesParamsAcrossRestartAndRequeue pins that.
//
// ResumeDrop was not a no-op choice: until the v1 op minter was retired, these
// jobs were resumed by server.resumeLegacyOp's default branch off the v1 row, so
// the declared policy never had to be correct. That branch is gone, and without
// this a job advertising CanResume() would silently never resume.
func (j *cleanupEmptyFoldersJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.RestartPolicy()
}
