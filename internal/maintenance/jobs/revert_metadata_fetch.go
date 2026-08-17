// file: internal/maintenance/jobs/revert_metadata_fetch.go
// version: 1.2.0
// guid: c8d4e2b3-5f6a-7b8c-9d0e-1f2a3b4c5d6e
// last-edited: 2026-08-17

package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

func init() { maintenance.Register(&revertMetadataFetchJob{}) }

type revertMetadataFetchJob struct{}

type rmf_params struct {
	OperationIDs []string `json:"fetch_op_ids"`
}

func (j *revertMetadataFetchJob) ID() string       { return "revert-metadata-fetch" }
func (j *revertMetadataFetchJob) Name() string     { return "Revert Metadata Fetch" }
func (j *revertMetadataFetchJob) Category() string { return "Metadata" }
func (j *revertMetadataFetchJob) Description() string {
	return "Rolls back DB changes made by one or more bulk-fetch-metadata operations"
}
func (j *revertMetadataFetchJob) DefaultParams() any { return &rmf_params{OperationIDs: []string{}} }
func (j *revertMetadataFetchJob) CanResume() bool    { return false }

func (j *revertMetadataFetchJob) Run(ctx context.Context, store maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) error {
	opID := maintenance.OperationIDFromCtx(ctx)

	// Load parameters: fetch_op_ids from operation params if stored.
	var operationIDs []string
	if opID != "" {
		if raw, err := store.GetOperationParams(opID); err == nil && len(raw) > 0 {
			var p rmf_params
			if jerr := json.Unmarshal(raw, &p); jerr == nil {
				operationIDs = p.OperationIDs
			}
		}
	}

	if len(operationIDs) == 0 {
		return fmt.Errorf("fetch_op_ids required: pass a list of bulk_metadata_fetch operation IDs to revert")
	}

	// Collect the earliest start time across all operations.
	var revertAfter time.Time
	bookIDSet := map[string]bool{}

	for _, fetchOpID := range operationIDs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		op, err := store.GetOperationByID(fetchOpID)
		if err != nil || op == nil {
			continue
		}
		if op.Type != "bulk_metadata_fetch" {
			return fmt.Errorf("operation %s is not a bulk_metadata_fetch (got %q)", fetchOpID, op.Type)
		}
		ts := op.CreatedAt
		if op.StartedAt != nil {
			ts = *op.StartedAt
		}
		if revertAfter.IsZero() || ts.Before(revertAfter) {
			revertAfter = ts
		}

		results, err := store.GetOperationResults(fetchOpID)
		if err != nil {
			return fmt.Errorf("failed to load results for %s: %w", fetchOpID, err)
		}
		for _, r := range results {
			if r.Status == "updated" {
				bookIDSet[r.BookID] = true
			}
		}
	}

	slog.Info("revert-metadata-fetch reverting books, changes after", "bookIDSet_count", len(bookIDSet), "revertAfter", revertAfter.Format(time.RFC3339))
	reporter.SetTotal(len(bookIDSet))

	reverted, skipped, errors := 0, 0, 0

	for bookID := range bookIDSet {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		reporter.Increment()

		book, err := store.GetBookByID(bookID)
		if err != nil || book == nil {
			errors++
			continue
		}

		history, err := store.GetBookChangeHistory(bookID, 50)
		if err != nil {
			errors++
			continue
		}

		type revertEntry struct {
			field string
			prev  string
		}
		byField := map[string]revertEntry{}
		for _, h := range history {
			if h.ChangeType != "fetched" {
				continue
			}
			if h.ChangedAt.Before(revertAfter) {
				continue
			}
			prev := ""
			if h.PreviousValue != nil {
				if jerr := json.Unmarshal([]byte(*h.PreviousValue), &prev); jerr != nil {
					prev = *h.PreviousValue
				}
			}
			byField[h.Field] = revertEntry{field: h.Field, prev: prev}
		}

		if len(byField) == 0 {
			skipped++
			continue
		}

		didChange := false
		for _, e := range byField {
			switch e.field {
			case "title":
				book.Title = e.prev
				didChange = true
			case "author_name":
				if e.prev == "" {
					book.AuthorID = nil
					didChange = true
				} else {
					if author, aerr := store.GetAuthorByName(e.prev); aerr == nil && author != nil {
						book.AuthorID = &author.ID
						didChange = true
					}
				}
			case "publisher":
				if e.prev == "" {
					book.Publisher = nil
				} else {
					book.Publisher = &e.prev
				}
				didChange = true
			case "language":
				if e.prev == "" {
					book.Language = nil
				} else {
					book.Language = &e.prev
				}
				didChange = true
			case "audiobook_release_year":
				if e.prev == "" {
					book.AudiobookReleaseYear = nil
				} else if yr, yerr := strconv.Atoi(e.prev); yerr == nil {
					book.AudiobookReleaseYear = &yr
				}
				didChange = true
			case "isbn10":
				if e.prev == "" {
					book.ISBN10 = nil
				} else {
					book.ISBN10 = &e.prev
				}
				didChange = true
			case "isbn13":
				if e.prev == "" {
					book.ISBN13 = nil
				} else {
					book.ISBN13 = &e.prev
				}
				didChange = true
			}
		}

		if didChange {
			if !dryRun {
				if _, uerr := store.UpdateBook(bookID, book); uerr != nil {
					slog.Warn("revert-metadata-fetch UpdateBook", "bookID", bookID, "uerr", uerr)
					errors++
				} else {
					reverted++
				}
			} else {
				reverted++ // dry-run: count as "would revert"
			}
		} else {
			skipped++
		}
	}

	slog.Info("revert-metadata-fetch done — reverted skipped errors", "reverted", reverted, "skipped", skipped, "errors", errors)
	summary := fmt.Sprintf("Reverted %d books (skipped: %d, errors: %d)", reverted, skipped, errors)
	slog.Info(summary)
	return nil
}

// Policy declares the bridge's existing behaviour verbatim: see DefaultPolicy.
func (j *revertMetadataFetchJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}
