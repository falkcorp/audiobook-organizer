// file: internal/maintenance/jobs/enrich_book_files.go
// version: 1.2.0
// guid: a1000009-0000-0000-0000-000000000009
// last-edited: 2026-07-06

package jobs

import (
	"context"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"log/slog")

func init() { maintenance.Register(&enrichBookFilesJob{}) }

var trackNumRe = regexp.MustCompile(`^(\d+)[\s._\-]`)

type enrichBookFilesJob struct{}

func (j *enrichBookFilesJob) ID() string       { return "enrich-book-files" }
func (j *enrichBookFilesJob) Name() string     { return "Enrich Book Files" }
func (j *enrichBookFilesJob) Category() string { return "files" }
func (j *enrichBookFilesJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: false}
}
func (j *enrichBookFilesJob) Description() string {
	return "Backfill track numbers for book_files from filenames"
}
func (j *enrichBookFilesJob) CanResume() bool { return false }
func (j *enrichBookFilesJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
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
		if c.TrackNumber != 0 {
			continue
		}
		stem := strings.TrimSuffix(filepath.Base(c.FilePath), filepath.Ext(c.FilePath))
		m := trackNumRe.FindStringSubmatch(stem)
		if m == nil {
			continue
		}
		n, perr := strconv.Atoi(m[1])
		if perr != nil || n <= 0 {
			continue
		}
		if !dryRun {
			// Hydrate the full row and mutate/write THAT — never a
			// hand-built BookFile{} from Core fields, which would wipe the
			// stored fingerprint. See
			// docs/audits/2026-07-05-updatebookfile-memdb-writeback-fingerprint-wipe.md.
			full, herr := store.GetBookFiles(c.BookID)
			if herr != nil {
				slog.Error("enrich-book-files hydrate failed", "details", herr.Error())
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
				slog.Warn("enrich-book-files: hydrate: row not found", "id", c.ID)
				continue
			}
			target.TrackNumber = n
			if uerr := store.UpdateBookFile(target.ID, target); uerr != nil {
				msg := uerr.Error()
				slog.Error("enrich-book-files UpdateBookFile failed", "details", msg)
				continue
			}
		}
		updated++
	}
	_ = updated
	slog.Info("enrich-book-files complete")
	return nil
}
