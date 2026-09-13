// file: internal/maintenance/jobs/recompute_itunes_paths.go
// version: 1.5.0
// guid: a1000013-0000-0000-0000-000000000013
// last-edited: 2026-09-12

package jobs

import (
	"context"
	"fmt"

	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

func init() { maintenance.Register(&recomputeITunesPathsJob{}) }

type recomputeITunesPathsJob struct{}

func (j *recomputeITunesPathsJob) ID() string       { return "recompute-itunes-paths" }
func (j *recomputeITunesPathsJob) Name() string     { return "Recompute iTunes Paths" }
func (j *recomputeITunesPathsJob) Category() string { return "itunes" }
func (j *recomputeITunesPathsJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: false}
}
func (j *recomputeITunesPathsJob) Description() string {
	return "Recompute iTunes path mapping for all book files"
}
func (j *recomputeITunesPathsJob) CanResume() bool { return false }

// keptRowLogLimit caps how many kept rows one run lists in its log. The total
// is always in the summary line.
const keptRowLogLimit = 200

func (j *recomputeITunesPathsJob) Run(ctx context.Context, store maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) error {
	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return err
	}
	reporter.SetTotal(len(files))
	updated, kept := 0, 0
	for i := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		reporter.Increment()
		c := files[i]
		want := metafetch.ComputeITunesPath(c.FilePath)
		if want == c.ITunesPath {
			continue
		}
		if want == "" {
			// No mapping covers FilePath (or none is configured), so there is
			// nothing to compute. Never write "" over a stored path: that
			// would stop the book being written back to iTunes. List the row
			// and leave it, as path_reconcile.go does.
			kept++
			if kept <= keptRowLogLimit {
				reporter.Log("warn", fmt.Sprintf(
					"book_file %s (book %s): no iTunes path mapping covers %s; kept stored iTunes path %s",
					logger.SanitizeLogValue(c.ID), logger.SanitizeLogValue(c.BookID),
					logger.SanitizeLogValue(c.FilePath), logger.SanitizeLogValue(c.ITunesPath)), nil)
			}
			continue
		}
		if !dryRun {
			// Hydrate the full row and mutate/write THAT — never a
			// hand-built BookFile{} from Core fields, which would wipe the
			// stored fingerprint. See
			// docs/audits/2026-07-05-updatebookfile-memdb-writeback-fingerprint-wipe.md.
			full, herr := store.GetBookFiles(c.BookID)
			if herr != nil {
				slog.Error("recompute-itunes-paths hydrate failed", "details", herr.Error())
				continue
			}
			var target *database.BookFile
			for j := range full {
				if full[j].ID == c.ID {
					target = &full[j]
					break
				}
			}
			if target == nil {
				slog.Warn("recompute-itunes-paths: hydrate: row not found", "id", c.ID)
				continue
			}
			target.ITunesPath = want
			if uerr := store.UpdateBookFile(target.ID, target); uerr != nil {
				msg := uerr.Error()
				slog.Error("recompute-itunes-paths UpdateBookFile failed", "details", msg)
				continue
			}
		}
		updated++
	}
	verb := "updated"
	if dryRun {
		verb = "would update"
	}
	summary := fmt.Sprintf("recompute-itunes-paths: %s %d book_file rows; kept %d rows whose file path no iTunes mapping covers",
		verb, updated, kept)
	if kept > keptRowLogLimit {
		summary += fmt.Sprintf(" (first %d listed)", keptRowLogLimit)
	}
	reporter.Log("info", summary, nil)
	slog.Info("recompute-itunes-paths complete", "updated", updated, "kept_unmapped", kept, "dry_run", dryRun)
	return nil
}

// Policy declares the bridge's existing behaviour verbatim: see DefaultPolicy.
func (j *recomputeITunesPathsJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}
