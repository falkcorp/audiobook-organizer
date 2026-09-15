// file: internal/plugins/maintenance/author.go
// version: 1.7.0
// guid: e5f6a7b8-c9d0-1234-ef01-456789012345
// last-edited: 2026-09-15

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- author-dedup-scan ---

func (p *Plugin) authorDedupScanDef() sdk.OperationDef {
	sched := "0 1 * * *" // 01:00 daily
	return sdk.OperationDef{
		ID:              "maintenance.author-dedup-scan",
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Author dedup scan",
		Description:     "Refreshes author duplicate-group cache using fuzzy name matching.",
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.author-dedup-scan",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         60 * time.Minute,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead},
		Run:             p.runAuthorDedupScan,
	}
}

func (p *Plugin) runAuthorDedupScan(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	_ = reporter.Log(slog.LevelInfo, "Starting author dedup scan")
	loadProg := sdk.NewProgress(reporter, 0)
	loadProg.Start("Fetching authors...")

	authors, err := store.GetAllAuthors()
	if err != nil {
		return fmt.Errorf("failed to get authors: %w", err)
	}

	bookCounts, err := store.GetAllAuthorBookCounts()
	if err != nil {
		// Fail rather than rank duplicates against an empty count map: every
		// author would tie at zero books and the scan would still report
		// success. This is also where ErrMemdbIncomplete surfaces once that
		// getter is guarded.
		return fmt.Errorf("failed to get author book counts: %w", err)
	}
	booksWithCounts := 0
	for _, cnt := range bookCounts {
		if cnt > 0 {
			booksWithCounts++
		}
	}
	msg := fmt.Sprintf("Fetched %d authors (%d with book counts), running duplicate comparison...",
		len(authors), booksWithCounts)
	_ = reporter.Log(slog.LevelInfo, msg)

	total := len(authors)
	prog := sdk.NewProgress(reporter, total)
	prog.Start(msg)
	groups := dedup.FindDuplicateAuthors(authors, 0.85, func(id int) int { return bookCounts[id] },
		func(current, t int, message string) {
			prog.StepN(current, message)
		},
	)

	resultMsg := fmt.Sprintf("Dedup scan complete: %d duplicate groups found across %d authors",
		len(groups), total)
	_ = reporter.Log(slog.LevelInfo, resultMsg)
	prog.Done(resultMsg)
	return nil
}

// --- author-split-scan ---

func (p *Plugin) authorSplitScanDef() sdk.OperationDef {
	sched := "0 2 * * 1" // 02:00 every Monday
	return sdk.OperationDef{
		ID:              "maintenance.author-split-scan",
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Author split scan",
		Description:     "Finds and splits composite author names (e.g. 'Smith, J. & Jones, A.').",
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.author-split-scan",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         60 * time.Minute,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runAuthorSplitScan,
	}
}

func (p *Plugin) runAuthorSplitScan(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	_ = reporter.Log(slog.LevelInfo, "Starting author split scan")

	authors, err := store.GetAllAuthors()
	if err != nil {
		return fmt.Errorf("failed to get authors: %w", err)
	}
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Scanning %d authors for composite names...", len(authors)))

	splitCount := 0
	booksUpdated := 0
	errCount := 0
	// H8 (2026-07 error-correction sweep): GetBookAuthors errors below used to
	// be a bare `continue` — the book is left un-relinked to the new
	// individual authors, yet DeleteAuthor(author.ID) below still removes the
	// composite author it pointed to, orphaning that book's author reference.
	// This is a data-affecting miscount, not just a skip, so it gets its own
	// counter and a per-occurrence Warn in addition to the run summary.
	bookAuthorsLookupErrs := 0
	total := len(authors)
	prog := sdk.NewProgress(reporter, total)
	prog.Start(fmt.Sprintf("Scanning %d authors for composite names...", total))

	for i, author := range authors {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		parts := dedup.SplitCompositeAuthorName(author.Name)
		if len(parts) <= 1 {
			if (i+1)%200 == 0 {
				prog.StepN(i+1, fmt.Sprintf("Checked %d/%d authors", i+1, total))
			}
			continue
		}

		// Create/find individual authors
		var newAuthors []database.Author
		for _, name := range parts {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			existing, err := store.GetAuthorByName(name)
			if err == nil && existing != nil {
				newAuthors = append(newAuthors, *existing)
				continue
			}
			created, err := store.CreateAuthor(name)
			if err != nil {
				errCount++
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("Failed to create author %q: %v", name, err))
				continue
			}
			newAuthors = append(newAuthors, *created)
		}
		if len(newAuthors) == 0 {
			continue
		}

		// Re-link books from composite to individual authors. ForRelink
		// includes the trash: DeleteAuthor below sweeps trashed books' junction
		// rows too, so a trashed book skipped here would lose its credit.
		books, err := store.GetBooksByAuthorIDForRelinkCore(author.ID)
		if err != nil {
			errCount++
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("Failed to get books for author %q: %v", author.Name, err))
			continue
		}

		for _, book := range books {
			bookAuthors, err := store.GetBookAuthors(book.ID)
			if err != nil {
				bookAuthorsLookupErrs++
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
					"author split: GetBookAuthors failed for book %s (author %q not relinked): %v",
					book.ID, author.Name, err))
				continue
			}
			role := "author"
			for _, ba := range bookAuthors {
				if ba.AuthorID == author.ID {
					role = ba.Role
					break
				}
			}
			var updated []database.BookAuthor
			for _, ba := range bookAuthors {
				if ba.AuthorID != author.ID {
					updated = append(updated, ba)
				}
			}
			for _, na := range newAuthors {
				alreadyLinked := false
				for _, ba := range updated {
					if ba.AuthorID == na.ID {
						alreadyLinked = true
						break
					}
				}
				if !alreadyLinked {
					updated = append(updated, database.BookAuthor{
						BookID:   book.ID,
						AuthorID: na.ID,
						Role:     role,
						Position: len(updated),
					})
				}
			}
			if err := store.SetBookAuthors(book.ID, updated); err != nil {
				errCount++
				continue
			}
			if book.AuthorID != nil && *book.AuthorID == author.ID && len(newAuthors) > 0 {
				firstID := newAuthors[0].ID
				// `book` is a BookCore (heavy fields nil). Do NOT write its
				// ToBook() projection: that carries nil Author/Series AND,
				// because this op changes AuthorID, the guard-preserved Author
				// would be STALE (it still names the composite author).
				// ModifyBook re-reads the full stored row under its write lock
				// and sets BOTH the new AuthorID and a fresh denormalized
				// Author, so the row stays consistent (Author.ID == AuthorID),
				// not preserved-stale (STOREFID W5d-1 / #1887), and a column
				// another writer commits meanwhile is not reverted (audit
				// A1#15). The fresh row decides: a primary moved off the
				// composite meanwhile is left alone. Sibling of
				// internal/scheduler/extra_ops.go — keep in sync.
				newPrimary := newAuthors[0]
				applied := false
				written, err := store.ModifyBook(book.ID, func(full *database.Book) error {
					if full.AuthorID == nil || *full.AuthorID != author.ID {
						return database.ErrSkipBookWrite
					}
					applied = true
					full.AuthorID = &firstID
					full.Author = &newPrimary
					return nil
				})
				switch {
				case err != nil:
					errCount++
					_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("author split: failed to update primary author on book %s: %v", book.ID, err))
				case written == nil:
					errCount++
					_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("author split: book %s no longer exists, primary author not updated", book.ID))
				case !applied:
					_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("author split: book %s left author %q meanwhile, primary author not updated", book.ID, author.Name))
				default:
					booksUpdated++
				}
			}
		}

		// Every per-book failure above is logged and skipped, so the composite
		// may still be credited. Deleting it then would erase that credit.
		if err := database.VerifyAuthorUnlinked(store, author.ID); err != nil {
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("Composite author %q NOT deleted: %v", author.Name, err))
			errCount++
		} else if err := store.DeleteAuthor(author.ID); err != nil {
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("Failed to delete composite author %q: %v", author.Name, err))
			errCount++
		} else {
			splitCount++
			_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Split %q → %v (%d books updated)", author.Name, parts, len(books)))
		}

		if (i+1)%200 == 0 || splitCount%50 == 0 {
			prog.StepN(i+1,
				fmt.Sprintf("Checked %d/%d authors, split %d so far", i+1, total, splitCount))
		}
	}

	// Invalidate dedup cache since authors changed
	p.deps.InvalidateDedupCache()

	resultMsg := fmt.Sprintf("Split %d composite authors, updated %d books (%d errors, %d books not relinked due to lookup errors)",
		splitCount, booksUpdated, errCount, bookAuthorsLookupErrs)
	_ = reporter.Log(slog.LevelInfo, resultMsg)
	prog.Done(resultMsg)
	return nil
}

// --- resolve-production-authors ---

func (p *Plugin) resolveProductionAuthorsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.resolve-production-authors",
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Resolve production company authors",
		Description:     "Resolves real authors for production company entries using external metadata.",
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.resolve-production-authors",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         60 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite, sdk.CapNetworkGeneric},
		Run:             p.runResolveProductionAuthors,
	}
}

func (p *Plugin) runResolveProductionAuthors(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	_ = reporter.Log(slog.LevelInfo, "Starting production author resolution")

	authors, err := store.GetAllAuthors()
	if err != nil {
		return fmt.Errorf("failed to get authors: %w", err)
	}

	var prodAuthors []database.Author
	for _, a := range authors {
		if dedup.IsProductionCompany(a.Name) {
			prodAuthors = append(prodAuthors, a)
		}
	}

	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Found %d production company authors", len(prodAuthors)))
	total := len(prodAuthors)
	resolved := 0
	prog := sdk.NewProgress(reporter, total)
	prog.Start(fmt.Sprintf("Processing %d production companies", total))

	for i := range prodAuthors {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if reporter.IsCanceled() {
			return context.Canceled
		}
		prog.StepN(i+1,
			fmt.Sprintf("Processed %d/%d production companies (%d resolved)", i+1, total, resolved))
	}

	p.deps.InvalidateDedupCache()
	resultMsg := fmt.Sprintf("Resolved %d books across %d production companies", resolved, total)
	_ = reporter.Log(slog.LevelInfo, resultMsg)
	prog.Done(resultMsg)
	return nil
}
