// file: internal/maintenance/jobs/cleanup_backups.go
// version: 1.2.0
// guid: a1000021-0000-0000-0000-000000000021
// last-edited: 2026-08-17

package jobs

import (
	"context"
	"os"
	"path/filepath"
	"regexp"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"log/slog"
)

func init() { maintenance.Register(&cleanupBackupsJob{}) }

var backupFileRe = regexp.MustCompile(`(?i)\.(backup|bak)$|\.bak-\d{8}-\d{6}$`)

type cleanupBackupsJob struct{}

func (j *cleanupBackupsJob) ID() string       { return "cleanup-backups" }
func (j *cleanupBackupsJob) Name() string     { return "Cleanup Backups" }
func (j *cleanupBackupsJob) Category() string { return "cleanup" }
func (j *cleanupBackupsJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: false}
}
func (j *cleanupBackupsJob) Description() string {
	return "Delete .backup and .bak files from the library root"
}
func (j *cleanupBackupsJob) CanResume() bool { return false }
func (j *cleanupBackupsJob) Run(ctx context.Context, _ database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
	root := config.AppConfig.RootDir
	if root == "" {
		slog.Warn("cleanup-backups RootDir not configured")
		return nil
	}
	removed := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !backupFileRe.MatchString(filepath.Base(path)) {
			return nil
		}
		if !dryRun {
			if rerr := os.Remove(path); rerr == nil {
				removed++
			}
		} else {
			removed++
			slog.Info("would remove" + path)
		}
		return nil
	})
	_ = removed
	slog.Info("cleanup-backups complete")
	return err
}

// Policy declares the bridge's existing behaviour verbatim: see DefaultPolicy.
func (j *cleanupBackupsJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}
