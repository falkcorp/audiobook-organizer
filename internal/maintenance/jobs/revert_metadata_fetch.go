// file: internal/maintenance/jobs/revert_metadata_fetch.go
// version: 1.4.0
// guid: c8d4e2b3-5f6a-7b8c-9d0e-1f2a3b4c5d6e
// last-edited: 2026-09-02

package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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
	// fetch_op_ids arrives on the run's own params blob, via the context.
	//
	// This used to read store.GetOperationParams(OperationIDFromCtx(ctx)), which
	// loads the Pebble key opstate:<opID>:params. NOTHING on the maintenance path
	// writes that key — it has exactly one writer, operations.SaveParams, whose
	// only remaining callers are internal/organizer/service.go and
	// internal/itunes/service/importer.go. The maintenance dispatcher's call was
	// deleted with the v1 op minter (#2784), so the read survived its writer.
	//
	// For THIS job that was not a degradation, it was total: fetch_op_ids is
	// required, the read always returned nothing, so Run could only ever reach the
	// error below. The job was 100% non-functional and said so in a message that
	// read like operator error.
	//
	// No fallback to the old read. It has no writer on this path, so a fallback
	// could only ever yield nothing — and a fallback that silently yields nothing
	// is worse than none, because it looks like a safety net.
	var operationIDs []string
	if raw := maintenance.RawParamsFromCtx(ctx); len(raw) > 0 {
		var p rmf_params
		if jerr := json.Unmarshal(raw, &p); jerr == nil {
			operationIDs = p.OperationIDs
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

	reverted, skipped, errors, lockedKept := 0, 0, 0, 0

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

		// A revert writes the value the field held BEFORE the fetch. If the
		// user has edited and locked the field since, that pre-fetch value is
		// older than the user's own, and restoring it would be exactly the
		// overwrite the lock promises to prevent. The field names in byField
		// are the database.FieldKey vocabulary (they came from the same
		// RecordChangeHistory writes), so the lock set answers them directly.
		// Locked entries are dropped BEFORE the switch below so that the
		// author_name branch's GetAuthorByName is never reached for them.
		locks, lerr := database.LoadFieldLocks(store, bookID)
		if lerr != nil {
			slog.Warn("revert-metadata-fetch: field locks unreadable; leaving book alone", "bookID", bookID, "err", lerr)
			errors++
			continue
		}
		for field := range byField {
			if locks.Locked(field) {
				delete(byField, field)
				lockedKept++
			}
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

	slog.Info("revert-metadata-fetch done — reverted skipped errors", "reverted", reverted, "skipped", skipped, "errors", errors, "fields_kept_locked", lockedKept)
	summary := fmt.Sprintf("Reverted %d books (skipped: %d, errors: %d, user-locked fields left alone: %d)", reverted, skipped, errors, lockedKept)
	slog.Info(summary)
	return nil
}

// Policy declares the bridge's existing behaviour verbatim: see DefaultPolicy.
func (j *revertMetadataFetchJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}
