// file: internal/maintenance/jobs/cleanup_backups.go
// version: 1.4.0
// guid: a1000021-0000-0000-0000-000000000021
// last-edited: 2026-08-30

package jobs

import (
	"context"
	"os"
	"path/filepath"
	"regexp"

	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/appdirs"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
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
func (j *cleanupBackupsJob) Run(ctx context.Context, _ maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) error {
	root := config.AppConfig.RootDir
	if root == "" {
		slog.Warn("cleanup-backups RootDir not configured")
		return nil
	}
	// The application keeps its own state INSIDE the library root: a backup
	// directory of multi-GB database archives and an OpenLibrary dump
	// directory holding an embedded database. Both are operator-settable to
	// names with no leading dot, so nothing here ever excluded them.
	//
	// backupFileRe does not currently match a database archive
	// (backup.go names those "audiobooks_<type>_<timestamp>.tar.{gz,zst}"),
	// so today this job walks ~90 GB and deletes nothing from it. That is a
	// NAMING COINCIDENCE, not a control -- the same class of accidental
	// protection PR #2974 replaced with a rule. A ".backup"- or ".bak"-suffixed
	// file parked in either tree by an operator or a future archive format
	// would be deleted with no warning.
	app := appdirs.Current()
	removed := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if pathutil.ShouldSkipDir(root, path, app) {
				return filepath.SkipDir
			}
			return nil
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
