// file: internal/maintenance/jobs/scan_chapter_groups.go
// version: 1.6.0
// guid: a1000019-0000-0000-0000-000000000019
// last-edited: 2026-09-19

package jobs

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

func init() { maintenance.Register(&scanChapterGroupsJob{}) }

type scanChapterGroupsJob struct{}

func (j *scanChapterGroupsJob) ID() string       { return "scan-chapter-groups" }
func (j *scanChapterGroupsJob) Name() string     { return "Scan Chapter Groups" }
func (j *scanChapterGroupsJob) Category() string { return "files" }

// DefaultParams advertises every key the job reads. The scan never writes, so
// it advertises no dry_run.
func (j *scanChapterGroupsJob) DefaultParams() any {
	return struct {
		MinFiles           int    `json:"min_files"`
		MaxPerFileDuration int    `json:"max_per_file_duration"`
		PathPrefix         string `json:"path_prefix"`
	}{MinFiles: chapterDefaultMinFiles, MaxPerFileDuration: chapterDefaultMaxPerFileDuration}
}
func (j *scanChapterGroupsJob) Description() string {
	return "Report books that look like multi-chapter parts of the same audiobook"
}
func (j *scanChapterGroupsJob) CanResume() bool { return false }

// Run detects chapter groups with the run's params and persists them as the
// operation's structured result. It writes nothing else.
func (j *scanChapterGroupsJob) Run(ctx context.Context, store maintenance.JobStore, reporter maintenance.ProgressReporter, _ bool) error {
	p, err := decodeChapterGroupParams(ctx)
	if err != nil {
		return err
	}
	p.DryRun = true // a scan is always read-only; say so in the echoed params
	det, err := detectChapterGroupsForRun(ctx, store, p)
	if err != nil {
		return err
	}
	res := chapterGroupsResult{
		Job:                          j.ID(),
		DryRun:                       true,
		Params:                       p,
		GroupsFound:                  len(det.Groups),
		GroupsSkippedUnknownDuration: det.SkippedUnknownDuration,
		GroupsSkippedDuplicateCopies: det.SkippedDuplicateCopies,
		BooksExcluded:                det.SkippedExcluded,
		Groups:                       make([]chapterGroupOutcome, 0, len(det.Groups)),
	}
	reporter.SetTotal(len(det.Groups))
	for _, g := range det.Groups {
		reporter.Increment()
		res.TotalBooksAffected += len(g.BookIDs)
		res.Groups = append(res.Groups, newChapterGroupOutcome(g))
	}
	chapterLog.Info("scan-chapter-groups complete: groups=%d books=%d skipped_unknown_duration=%d",
		res.GroupsFound, res.TotalBooksAffected, res.GroupsSkippedUnknownDuration)
	return maintenance.SetResult(ctx, res)
}

// Policy declares the bridge's existing behaviour verbatim: see DefaultPolicy.
func (j *scanChapterGroupsJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}
