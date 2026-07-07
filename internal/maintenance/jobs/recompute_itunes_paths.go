// file: internal/maintenance/jobs/recompute_itunes_paths.go
// version: 1.2.0
// guid: a1000013-0000-0000-0000-000000000013
// last-edited: 2026-07-06

package jobs

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"log/slog")

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
func (j *recomputeITunesPathsJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return err
	}
	reporter.SetTotal(len(files))
	updated := 0
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
	_ = updated
	slog.Info("recompute-itunes-paths complete")
	return nil
}
