// file: internal/maintenance/jobs/scan_chapter_groups.go
// version: 1.2.0
// guid: a1000019-0000-0000-0000-000000000019
// last-edited: 2026-07-07

package jobs

import (
	"context"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"log/slog"
)

func init() { maintenance.Register(&scanChapterGroupsJob{}) }

type scanChapterGroupsJob struct{}

func (j *scanChapterGroupsJob) ID() string       { return "scan-chapter-groups" }
func (j *scanChapterGroupsJob) Name() string     { return "Scan Chapter Groups" }
func (j *scanChapterGroupsJob) Category() string { return "files" }
func (j *scanChapterGroupsJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: false}
}
func (j *scanChapterGroupsJob) Description() string {
	return "Report books that look like multi-chapter parts of the same audiobook"
}
func (j *scanChapterGroupsJob) CanResume() bool { return false }
func (j *scanChapterGroupsJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, _ bool) error {
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	groups := scanner.DetectChapterGroups(books, 2, 600)
	reporter.SetTotal(len(groups))
	for _, g := range groups {
		reporter.Increment()
		detail := fmt.Sprintf("title=%q files=%d duration=%.0fs ids=%v", g.CommonTitle, g.FileCount, g.TotalDuration, g.BookIDs)
		slog.Info("chapter group detected", "details", detail)
	}
	slog.Info("scan-chapter-groups complete groups", "groups_count", len(groups))
	return nil
}
