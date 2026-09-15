// file: internal/maintenance/jobs/backfill_metadata_source_hash.go
// version: 1.5.0
// guid: a1000015-0000-0000-0000-000000000015
// last-edited: 2026-09-15

package jobs

import (
	"context"
	"crypto/sha256"
	"fmt"

	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

func init() { maintenance.Register(&backfillMetadataSourceHashJob{}) }

type backfillMetadataSourceHashJob struct{}

func (j *backfillMetadataSourceHashJob) ID() string       { return "backfill-metadata-source-hash" }
func (j *backfillMetadataSourceHashJob) Name() string     { return "Backfill Metadata Source Hash" }
func (j *backfillMetadataSourceHashJob) Category() string { return "files" }
func (j *backfillMetadataSourceHashJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: false}
}
func (j *backfillMetadataSourceHashJob) Description() string {
	return "Compute MetadataSourceHash for books that have one missing"
}
func (j *backfillMetadataSourceHashJob) CanResume() bool { return false }
func (j *backfillMetadataSourceHashJob) Run(ctx context.Context, store maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) error {
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return err
	}
	reporter.SetTotal(len(books))
	updated := 0
	for i := range books {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		reporter.Increment()
		book := &books[i]
		if book.MetadataSourceHash != nil && *book.MetadataSourceHash != "" {
			continue
		}
		src, id := bookMetadataSourceAndID(book)
		if src == "" || id == "" {
			continue
		}
		raw := fmt.Sprintf("%s:%s", src, id)
		sum := sha256.Sum256([]byte(raw))
		hash := fmt.Sprintf("%x", sum)
		if !dryRun {
			// Fill through ModifyBook: it re-reads the full row (book is a
			// slim Core projection) under the book's write lock and sets only
			// MetadataSourceHash, so a column another writer commits
			// meanwhile is not reverted (audit A1#15). The "still empty"
			// decision is re-made on the fresh row.
			full, uerr := store.ModifyBook(book.ID, func(cur *database.Book) error {
				if cur.MetadataSourceHash != nil && *cur.MetadataSourceHash != "" {
					return database.ErrSkipBookWrite
				}
				cur.MetadataSourceHash = &hash
				return nil
			})
			if uerr != nil {
				msg := uerr.Error()
				slog.Error("backfill-metadata-source-hash ModifyBook failed", "details", msg)
				continue
			}
			if full == nil {
				slog.Error("backfill-metadata-source-hash book vanished before write", "id", book.ID)
				continue
			}
		}
		updated++
	}
	_ = updated
	slog.Info("backfill-metadata-source-hash complete")
	return nil
}

func bookMetadataSourceAndID(book *database.BookCore) (string, string) {
	if book.MetadataSource == nil {
		return "", ""
	}
	src := *book.MetadataSource
	switch src {
	case "audible":
		if book.ASIN != nil && *book.ASIN != "" {
			return src, *book.ASIN
		}
	case "openlibrary":
		if book.OpenLibraryID != nil && *book.OpenLibraryID != "" {
			return src, *book.OpenLibraryID
		}
	case "google_books":
		if book.GoogleBooksID != nil && *book.GoogleBooksID != "" {
			return src, *book.GoogleBooksID
		}
	case "hardcover":
		if book.HardcoverID != nil && *book.HardcoverID != "" {
			return src, *book.HardcoverID
		}
	}
	return "", ""
}

// Policy declares the bridge's existing behaviour verbatim: see DefaultPolicy.
func (j *backfillMetadataSourceHashJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}
