// file: internal/maintenance/jobs/fix_author_narrator_swap.go
// version: 2.5.0
// guid: a1000003-0000-0000-0000-000000000003
// last-edited: 2026-09-15

package jobs

import (
	"context"
	"fmt"
	"strings"

	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

func init() { maintenance.Register(&fixAuthorNarratorSwapJob{}) }

type fixAuthorNarratorSwapJob struct{}

func (j *fixAuthorNarratorSwapJob) ID() string       { return "fix-author-narrator-swap" }
func (j *fixAuthorNarratorSwapJob) Name() string     { return "Fix Author/Narrator Swap" }
func (j *fixAuthorNarratorSwapJob) Category() string { return "library" }
func (j *fixAuthorNarratorSwapJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: true}
}
func (j *fixAuthorNarratorSwapJob) Description() string {
	return "Fix books where author and narrator fields are swapped"
}
func (j *fixAuthorNarratorSwapJob) CanResume() bool { return false }

func (j *fixAuthorNarratorSwapJob) Run(ctx context.Context, store maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) error {
	const batchSize = 500
	offset := 0
	var found, applied int

	for {
		batch, err := store.GetAllBooksCore(batchSize, offset)
		if err != nil {
			return fmt.Errorf("failed to list books: %w", err)
		}
		if len(batch) == 0 {
			break
		}

		reporter.SetTotal(offset + len(batch))

		for i := range batch {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			book := &batch[i]
			if book.AuthorID == nil || book.Narrator == nil || *book.Narrator == "" {
				reporter.Increment()
				continue
			}

			author, aErr := store.GetAuthorByID(*book.AuthorID)
			if aErr != nil || author == nil {
				reporter.Increment()
				continue
			}

			if !strings.EqualFold(author.Name, *book.Narrator) {
				reporter.Increment()
				continue
			}

			found++
			msg := fmt.Sprintf("Author/narrator swap detected: %s = %s", author.Name, *book.Narrator)
			reporter.Log("warn", msg, nil)
			if !dryRun {
				// Clear only AuthorID, under the book's write lock
				// (ModifyBook), so a column another writer commits between
				// the listing read and this write is not reverted (audit
				// A1#15). The decision is re-made on the fresh row: a book
				// whose author was already cleared or changed meanwhile is
				// left alone.
				written, updateErr := store.ModifyBook(book.ID, func(current *database.Book) error {
					if current.AuthorID == nil || *current.AuthorID != *book.AuthorID {
						return database.ErrSkipBookWrite
					}
					current.AuthorID = nil
					return nil
				})
				switch {
				case updateErr != nil:
					errMsg := updateErr.Error()
					slog.Error("Failed to update book", "book", book.ID, "updateErr", updateErr)
					reporter.Log("error", "Failed to update book after swap fix: "+book.ID, &errMsg)
				case written == nil:
					errMsg := "book not found"
					slog.Error("Failed to fetch book", "book", book.ID)
					reporter.Log("error", "Failed to fetch book for swap fix: "+book.ID, &errMsg)
				default:
					applied++
				}
			}
			reporter.Increment()
		}

		if len(batch) < batchSize {
			break
		}
		offset += batchSize
	}

	slog.Info("Done found applied dryRun", "found", found, "applied", applied, "dryRun", dryRun)
	return nil
}

// Policy declares the bridge's existing behaviour verbatim: see DefaultPolicy.
func (j *fixAuthorNarratorSwapJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}
