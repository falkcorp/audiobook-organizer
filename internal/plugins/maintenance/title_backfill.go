// file: internal/plugins/maintenance/title_backfill.go
// version: 1.3.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-07-07

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/titleutil"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

type titleBackfillParams struct {
	DryRun bool `json:"dryRun"`
}

// pendingUpdate holds a book whose title needs stripping. book is Core (slim)
// — Title/ID are Core-safe fields, and the actual writeback in phase 2
// hydrates a full row via GetBookByID before applying newTitle, so it never
// wipes the denormalized Author/Series on Pebble.
type pendingUpdate struct {
	book     database.BookCore
	newTitle string
}

func (p *Plugin) titleBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.title-backfill",
		Plugin:          "maintenance",
		DisplayName:     "Strip chapter prefixes from book titles",
		Description:     "Scans all books and removes leading chapter/track markers from Book.Title (e.g. '(76/85) Tarkin' → 'Tarkin'). Default dry-run previews changes; set dryRun=false to apply.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.title-backfill",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runTitleBackfill,
	}
}

func (p *Plugin) runTitleBackfill(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params := titleBackfillParams{DryRun: true} // safe default
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}

	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	if params.DryRun {
		_ = reporter.Log(slog.LevelInfo, "DRY RUN — no changes will be written")
	}

	// Phase 1: get real total upfront — O(1) counter lookup in PebbleDB.
	// Falls back to 0 (indeterminate bar) if the count isn't available.
	totalBooks, countErr := store.CountPrimaryBooks()
	if countErr != nil || totalBooks <= 0 {
		totalBooks = 0
	}
	_ = reporter.UpdateProgress(0, totalBooks, "Phase 1/2: scanning titles…")

	const pageSize = 500
	offset := 0
	var scanned, skipped int
	var toUpdate []pendingUpdate

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		books, err := store.GetAllBooksCore(pageSize, offset)
		if err != nil {
			return fmt.Errorf("GetAllBooksCore offset=%d: %w", offset, err)
		}
		if len(books) == 0 {
			break
		}

		for _, book := range books {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			scanned++

			cleaned := titleutil.StripChapterPrefix(book.Title)
			switch {
			case cleaned == book.Title:
				skipped++
			case cleaned == "":
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
					"book %s: title %q stripped to empty, skipping", book.ID, book.Title))
				skipped++
			default:
				toUpdate = append(toUpdate, pendingUpdate{book: book, newTitle: cleaned})
			}
		}

		offset += len(books)

		// Use real total if available; fall back to scanned so bar moves.
		total := totalBooks
		if total == 0 {
			total = scanned
		}
		_ = reporter.UpdateProgress(scanned, total,
			fmt.Sprintf("Phase 1/2: scanned %d/%d — %d titles to fix", scanned, total, len(toUpdate)))

		if len(books) < pageSize {
			break
		}
	}

	// Phase 2: apply (or preview). We now know exactly how many to change,
	// so the bar switches to changed/toChange with real percentages.
	toChange := len(toUpdate)
	if toChange == 0 {
		suffix := ""
		if params.DryRun {
			suffix = " (dry run)"
		}
		result := fmt.Sprintf("Scanned %d books: 0 titles need updating, %d skipped%s", scanned, skipped, suffix)
		_ = reporter.Log(slog.LevelInfo, result)
		_ = reporter.UpdateProgress(1, 1, result)
		return nil
	}

	_ = reporter.UpdateProgress(0, toChange,
		fmt.Sprintf("Phase 2/2: updating %d titles…", toChange))

	var changed, errCount int
	for i, u := range toUpdate {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
			"book %s: %q → %q", u.book.ID, u.book.Title, u.newTitle))

		if !params.DryRun {
			// Hydrate before writeback — u.book is Core (slim); writing it
			// straight through UpdateBook would wipe Author/Series.
			full, herr := store.GetBookByID(u.book.ID)
			if herr != nil || full == nil {
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
					"book %s: hydrate failed: %v", u.book.ID, herr))
				errCount++
				continue
			}
			full.Title = u.newTitle
			if _, err := store.UpdateBook(full.ID, full); err != nil {
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
					"book %s: UpdateBook failed: %v", u.book.ID, err))
				errCount++
				continue
			}
		}
		changed++

		_ = reporter.UpdateProgress(i+1, toChange,
			fmt.Sprintf("Phase 2/2: updated %d/%d titles", i+1, toChange))
	}

	suffix := ""
	if params.DryRun {
		suffix = " (dry run — no writes)"
	}
	result := fmt.Sprintf("Scanned %d books: %d titles updated, %d skipped, %d errors%s",
		scanned, changed, skipped, errCount, suffix)
	_ = reporter.Log(slog.LevelInfo, result)
	_ = reporter.UpdateProgress(toChange, toChange, result)

	if errCount > 0 {
		return fmt.Errorf("%d UpdateBook errors (see op log for details)", errCount)
	}
	return nil
}
