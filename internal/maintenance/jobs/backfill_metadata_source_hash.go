// file: internal/maintenance/jobs/backfill_metadata_source_hash.go
// version: 1.2.0
// guid: a1000015-0000-0000-0000-000000000015
// last-edited: 2026-07-07

package jobs

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"log/slog")

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
func (j *backfillMetadataSourceHashJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
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
			// Hydrate before writeback — book is Core (slim); writing it
			// straight through UpdateBook would wipe Author/Series.
			full, herr := store.GetBookByID(book.ID)
			if herr != nil || full == nil {
				slog.Error("backfill-metadata-source-hash hydrate failed", "id", book.ID, "err", herr)
				continue
			}
			full.MetadataSourceHash = &hash
			if _, uerr := store.UpdateBook(full.ID, full); uerr != nil {
				msg := uerr.Error()
				slog.Error("backfill-metadata-source-hash UpdateBook failed", "details", msg)
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
