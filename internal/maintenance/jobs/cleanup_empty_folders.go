// file: internal/maintenance/jobs/cleanup_empty_folders.go
// version: 1.3.0
// guid: a1000006-0000-0000-0000-000000000006
// last-edited: 2026-08-17

package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"log/slog"
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

func (j *cleanupEmptyFoldersJob) Run(ctx context.Context, _ database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
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

// Policy: ResumeDrop, which is what the bridge does today — deliberately NOT
// ResumeRequeue despite CanResume() being true and this job checkpointing nothing.
// It advertises dry_run:true, and server.resumeV2Op re-enqueues with nil params,
// under which DryRun would silently resolve to false and run the job for real.
// Revisit in PR-2, where the replay is testable. See RequeuePolicy's doc comment
// and todo.d/20260817-resumerequeue-two-divergent-implementations.md.
func (j *cleanupEmptyFoldersJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}
