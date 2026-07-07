// file: internal/maintenance/jobs/scan_duplicate_files.go
// version: 1.2.0
// guid: a1000016-0000-0000-0000-000000000016
// last-edited: 2026-07-06

package jobs

import (
	"context"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"log/slog"
)

func init() { maintenance.Register(&scanDuplicateFilesJob{}) }

type scanDuplicateFilesJob struct{}

func (j *scanDuplicateFilesJob) ID() string       { return "scan-duplicate-files" }
func (j *scanDuplicateFilesJob) Name() string     { return "Scan Duplicate Files" }
func (j *scanDuplicateFilesJob) Category() string { return "dedup" }
func (j *scanDuplicateFilesJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: false}
}
func (j *scanDuplicateFilesJob) Description() string {
	return "Scan for book files sharing the same hash"
}
func (j *scanDuplicateFilesJob) CanResume() bool { return false }
func (j *scanDuplicateFilesJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, _ bool) error {
	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return err
	}
	reporter.SetTotal(len(files))
	byHash := map[string][]string{}
	for i := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		reporter.Increment()
		if files[i].FileHash == "" {
			continue
		}
		byHash[files[i].FileHash] = append(byHash[files[i].FileHash], files[i].FilePath)
	}
	dups := 0
	for hash, paths := range byHash {
		if len(paths) > 1 {
			dups++
			detail := fmt.Sprintf("hash=%s paths=%v", hash, paths)
			slog.Warn("duplicate files detected", "details", detail)
		}
	}
	slog.Info("scan-duplicate-files complete duplicate groups", "dups", dups)
	return nil
}
