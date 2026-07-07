// file: internal/maintenance/jobs/backfill_book_files.go
// version: 1.2.0
// guid: a1000005-0000-0000-0000-000000000005
// last-edited: 2026-07-07

package jobs

import (
	"context"
	"path/filepath"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	ulid "github.com/oklog/ulid/v2"
	"log/slog")

func init() { maintenance.Register(&backfillBookFilesJob{}) }

type backfillBookFilesJob struct{}

func (j *backfillBookFilesJob) ID() string       { return "backfill-book-files" }
func (j *backfillBookFilesJob) Name() string     { return "Backfill Book Files" }
func (j *backfillBookFilesJob) Category() string { return "files" }
func (j *backfillBookFilesJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: true}
}
func (j *backfillBookFilesJob) Description() string {
	return "Create book_files rows for books that have none"
}
func (j *backfillBookFilesJob) CanResume() bool { return false }
func (j *backfillBookFilesJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return err
	}
	reporter.SetTotal(len(books))
	created := 0
	for i := range books {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		book := &books[i]
		reporter.Increment()
		files, err := store.GetBookFiles(book.ID)
		if err != nil {
			continue
		}
		if len(files) > 0 {
			continue
		}
		audioFiles := metafetch.AudioFilesInDir(book.FilePath)
		for _, fp := range audioFiles {
			bf := &database.BookFile{
				ID:       ulid.Make().String(),
				BookID:   book.ID,
				FilePath: fp,
				Format:   filepath.Ext(fp),
			}
			if !dryRun {
				if cerr := store.CreateBookFile(bf); cerr != nil {
					msg := cerr.Error()
					slog.Error("failed to create book file", "details", msg)
					continue
				}
			}
			created++
		}
	}
	_ = created
	slog.Info("backfill-book-files complete")
	return nil
}
