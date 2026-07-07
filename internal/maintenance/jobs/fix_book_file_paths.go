// file: internal/maintenance/jobs/fix_book_file_paths.go
// version: 1.2.0
// guid: a1000011-0000-0000-0000-000000000011
// last-edited: 2026-07-06

package jobs

import (
	"context"
	"os"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"log/slog")

func init() { maintenance.Register(&fixBookFilePathsJob{}) }

type fixBookFilePathsJob struct{}

func (j *fixBookFilePathsJob) ID() string       { return "fix-book-file-paths" }
func (j *fixBookFilePathsJob) Name() string     { return "Fix Book File Paths" }
func (j *fixBookFilePathsJob) Category() string { return "files" }
func (j *fixBookFilePathsJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: true}
}
func (j *fixBookFilePathsJob) Description() string {
	return "Mark book_files as missing when they no longer exist on disk"
}
func (j *fixBookFilePathsJob) CanResume() bool { return false }
func (j *fixBookFilePathsJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return err
	}
	reporter.SetTotal(len(files))
	marked := 0
	for i := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		reporter.Increment()
		c := files[i]
		if c.Missing {
			continue
		}
		if _, serr := os.Stat(c.FilePath); os.IsNotExist(serr) {
			if !dryRun {
				// Hydrate the full row and mutate/write THAT — never a
				// hand-built BookFile{} from Core fields, which would wipe
				// the stored fingerprint. See
				// docs/audits/2026-07-05-updatebookfile-memdb-writeback-fingerprint-wipe.md.
				full, herr := store.GetBookFiles(c.BookID)
				if herr != nil {
					slog.Error("fix-book-file-paths hydrate failed", "details", herr.Error())
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
					slog.Warn("fix-book-file-paths: hydrate: row not found", "id", c.ID)
					continue
				}
				target.Missing = true
				if uerr := store.UpdateBookFile(target.ID, target); uerr != nil {
					msg := uerr.Error()
					slog.Error("fix-book-file-paths UpdateBookFile failed", "details", msg)
					continue
				}
			}
			marked++
		}
	}
	_ = marked
	slog.Info("fix-book-file-paths complete")
	return nil
}
