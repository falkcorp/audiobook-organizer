// file: internal/plugins/maintenance/author.go
// version: 1.11.1
// guid: e5f6a7b8-c9d0-1234-ef01-456789012345
// last-edited: 2026-10-11

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
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

// author-split-scan and resolve-production-authors take only a mode. Both
// ignored their params until 2026-09-25 and wrote on every run, including the
// Monday 02:00 schedule. They resolve it through opmode.ParseDryRun: dry_run
// defaults to TRUE when absent, dryRun (camelCase) is accepted as an alias, and
// sending both with different values is an error rather than a guess -- the
// same contract as author-id-repair and duration-reextract, whose comment
// records the prod run that silently previewed because of a misspelled key.

// --- author-split-scan ---

func (p *Plugin) authorSplitScanDef() sdk.OperationDef {
	sched := "0 2 * * 1" // 02:00 every Monday
	return sdk.OperationDef{
		ID:          "maintenance.author-split-scan",
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Author split scan",
		Description: "Finds and splits composite author names (e.g. 'Smith, J. & Jones, A.'). " +
			"DRY RUN BY DEFAULT, including the scheduled run: lists each composite and its parts; " +
			"pass dry_run=false to create the parts, relink books and delete the composite.",
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

func (p *Plugin) runAuthorSplitScan(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	dryRun, err := opmode.ParseDryRun("maintenance.author-split-scan", raw)
	if err != nil {
		return err
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Starting author split scan (dry_run=%v)", dryRun))

	authors, err := store.GetAllAuthors()
	if err != nil {
		return fmt.Errorf("failed to get authors: %w", err)
	}
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Scanning %d authors for composite names...", len(authors)))

	splitCount := 0
	booksUpdated := 0
	errCount := 0
	wouldSplit, wouldCreate, wouldRelink := 0, 0, 0
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

		if dryRun {
			// Reads only: no CreateAuthor, SetBookAuthors, ModifyBook or
			// DeleteAuthor. Every composite is logged with its parts, because
			// the splitter is known to cut titles into "authors" and the list
			// is what an operator reviews before passing dry_run=false.
			missing := 0
			for _, name := range parts {
				name = strings.TrimSpace(name)
				if name == "" {
					continue
				}
				if existing, lerr := store.GetAuthorByName(name); lerr != nil || existing == nil {
					missing++
				}
			}
			books, berr := store.GetBooksByAuthorIDForRelinkCore(author.ID)
			if berr != nil {
				errCount++
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("Failed to get books for author %q: %v", author.Name, berr))
				continue
			}
			wouldSplit++
			wouldCreate += missing
			wouldRelink += len(books)
			_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Would split %q (id %d) → %q (%d new authors, %d books)",
				author.Name, author.ID, parts, missing, len(books)))
			continue
		}

		// Create/find individual authors
		var newAuthors []database.Author
		partFailed := false
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
			if errors.Is(err, database.ErrImplausibleAuthorName) {
				// A junk part ("read by narrator" split out of
				// "Stephen King, read by narrator") is dropped on
				// purpose: it was never a credit worth keeping.
				continue
			}
			if err != nil {
				errCount++
				partFailed = true
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("Failed to create author %q: %v", name, err))
				continue
			}
			newAuthors = append(newAuthors, *created)
		}
		// Every real part must resolve, or the composite is left alone. The
		// relink below REPLACES the composite credit with newAuthors and the
		// composite row is then deleted, so a store error on one part would
		// silently drop that person's credit from every book. Only a part
		// the creation gate refuses (database.ErrImplausibleAuthorName) is
		// dropped, because it was junk rather than a person.
		if partFailed || len(newAuthors) == 0 {
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

	if dryRun {
		resultMsg := fmt.Sprintf("DRY RUN: would split %d composite authors, create %d authors, relink %d books (%d errors); "+
			"pass dry_run=false to write", wouldSplit, wouldCreate, wouldRelink, errCount)
		_ = reporter.Log(slog.LevelInfo, resultMsg)
		prog.Done(resultMsg)
		return nil
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

// resolveProductionAuthorsDef is the only registration of this op.
// scheduler.resolve-production-authors ran a second copy under its own ID and
// ConcurrencyKey until 2026-10-11 (04-P4d); it resolves here as a FormerID and
// the resolve_production_authors scheduler task enqueues this def.
//
// The copies were not twins. The scheduler copy's apply re-fetched metadata by
// title for every book credited to a production company; this def's apply
// resolved nothing (its loop only reported progress) and only invalidated the
// dedup cache. The fetching body moved here, so an apply under either ID does
// the work the scheduler copy did.
//
// Permissions and Timeout are the scheduler copy's (it gated on settings.manage
// and carried 2h), so the alias neither widens who can run a writing op nor
// shortens the budget prod runs were sized against. ResumeDrop is the scheduler
// copy's, not this def's old ResumeRequeue: the apply issues one metadata
// provider lookup per book, a requeue after a restart repeats the lookups for
// every book that did not resolve, against provider quotas, with nobody having
// asked for the second pass, and the preview is cheap enough that the next
// scheduled run replaces a dropped one (entities.resolve-production-author
// drops for the same reason).
//
// Capabilities are the ones scheduler.metadata-upgrade declares for the same
// fetch path: the title search queries the whole metadata source chain
// (Audible among the providers), and candidate scoring calls the embedding
// scorer, an OpenAI-backed client, when metadata_scoring.embedding_enabled is
// on. The scheduler copy's network.openai was right; this def's
// network.generic alone under-declared it.
//
// Writes: neither copy declared a write set, which made the dispatcher's
// write-set gate skip the op. The apply rewrites book rows (fill-only fields
// and the author credit join), can create author rows for the credits it
// resolves and series rows for the series it fills, so it declares those three
// tables, as the author ops in this plugin (author-id-repair, author-path-link)
// declare theirs: it then waits for, rather than interleaves with, another
// running writer of the same rows.
func (p *Plugin) resolveProductionAuthorsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.resolve-production-authors",
		FormerIDs:   []string{"scheduler.resolve-production-authors"},
		Permissions: []auth.Permission{auth.PermSettingsManage},
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Resolve production company authors",
		Description: "Resolves real authors for production company entries using external metadata. " +
			"DRY RUN BY DEFAULT, including the scheduled run: counts the books it would re-fetch; " +
			"pass dry_run=false to fetch and write.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.resolve-production-authors",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		Schedule:        nil,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead, sdk.CapLibraryWrite,
			sdk.CapNetworkAudible, sdk.CapNetworkGeneric, sdk.CapNetworkOpenAI,
		},
		Writes: []sdk.Resource{sdk.ResBooks, sdk.ResAuthors, sdk.ResSeries},
		Run:    p.runResolveProductionAuthors,
	}
}

const (
	// resolveProductionAuthorsDefaultWorkers is the apply's book pool when
	// metadata_scoring.bulk_fetch_workers is unset: the default the bulk
	// metadata fetch uses for the same provider-bound work
	// (server.defaultBulkFetchWorkers).
	resolveProductionAuthorsDefaultWorkers = 4
	// resolveProductionAuthorsMaxWorkers caps a configured value, as
	// metafetch.asin-backfill caps its book pool.
	resolveProductionAuthorsMaxWorkers = 8
)

// resolveProductionAuthorsWorkers is the apply's book pool size. Network-bound,
// so deliberately below NumCPU: each fetch already fans out over the metadata
// source chain (source_fanout_workers per book), and every provider has its own
// token bucket in internal/metadata/providerhttp, so the buckets are the rate
// limit and the pool only keeps them busy.
func resolveProductionAuthorsWorkers() int {
	w := config.AppConfig.MetadataScoring.BulkFetchWorkers
	if w <= 0 {
		w = resolveProductionAuthorsDefaultWorkers
	}
	return min(w, resolveProductionAuthorsMaxWorkers)
}

func (p *Plugin) runResolveProductionAuthors(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) (retErr error) {
	dryRun, err := opmode.ParseDryRun("maintenance.resolve-production-authors", raw)
	if err != nil {
		return err
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	if !dryRun && !p.deps.HasMetadataFetchService() {
		// The apply is a metadata fetch per book. Without the service it would
		// resolve nothing and report success, which is the silent no-op this
		// def used to be.
		return fmt.Errorf("metadata fetch service not initialized")
	}
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Starting production author resolution (dry_run=%v)", dryRun))

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
	lookupFailed := 0
	prog := sdk.NewProgress(reporter, total)
	prog.Start(fmt.Sprintf("Listing books for %d production companies", total))

	// The work list, built sequentially: production companies are few and each
	// costs one indexed read. A book credited to two production companies is
	// listed ONCE, so the apply pool below works on disjoint items and two
	// workers can never fetch-and-apply the same book row at the same time.
	var bookIDs []string
	seen := make(map[string]bool)
	for i, author := range prodAuthors {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if reporter.IsCanceled() {
			return context.Canceled
		}
		books, err := store.GetBooksByAuthorIDWithRoleCore(author.ID)
		if err != nil {
			lookupFailed++
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("Could not list books for production company %q (id %d): %v", author.Name, author.ID, err))
			prog.StepN(i+1, fmt.Sprintf("Listed %d/%d production companies", i+1, total))
			continue
		}
		for _, b := range books {
			if !seen[b.ID] {
				seen[b.ID] = true
				bookIDs = append(bookIDs, b.ID)
			}
		}
		if dryRun {
			_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Would re-fetch metadata for %d books credited to %q (id %d)",
				len(books), author.Name, author.ID))
		}
		prog.StepN(i+1, fmt.Sprintf("Listed %d/%d production companies", i+1, total))
	}

	if dryRun {
		// The fetch applies as well as searches, so the dry run never calls
		// it; it counts the books instead.
		resultMsg := fmt.Sprintf("DRY RUN: would re-fetch metadata for %d books across %d production companies; "+
			"pass dry_run=false to write", len(bookIDs), total)
		_ = reporter.Log(slog.LevelInfo, resultMsg)
		prog.Done(resultMsg)
		return nil
	}
	if len(bookIDs) == 0 {
		// Nothing to fetch: finish without taking the scan stand-down, which
		// would park a running scan for no write.
		resultMsg := fmt.Sprintf("Resolved 0 books across %d production companies (no books to re-fetch, %d companies' book lists unreadable)",
			total, lookupFailed)
		_ = reporter.Log(slog.LevelInfo, resultMsg)
		prog.Done(resultMsg)
		return nil
	}

	// Metadata is never applied during a library scan: hold the scan stand-down
	// before the first fetch (each one applies), as maintenance.metadata-refresh
	// and scheduler.metadata-upgrade do. A running scan does not make the op
	// refuse: the acquire asks it to park and waits, and the op fails only if it
	// never parks. RunItems renews the lease per book and stops before the next
	// fetch once it is lost; the hold's ctx, which every fetch runs under, is
	// cancelled then too, so an in-flight fetch stops searching and refuses its
	// write if the loss lands before its apply starts. A loss that lands during
	// the apply itself can still complete that one book's write.
	hold, sdErr := registry.HoldScanStandDown(ctx, p.deps, reporter, "maintenance.resolve-production-authors apply")
	if sdErr != nil {
		return sdErr
	}
	defer func() { retErr = hold.Finish(retErr) }()
	ctx, reporter = hold.Context(), hold.Reporter()

	// Counters are atomics: the fetch callback and the Label closure both run
	// inside RunItems' worker goroutines.
	var attempted, resolved, fetchFailed atomic.Int64
	// Every fetch that ran may have rewritten a book's author credit, so the
	// dedup cache is invalidated whenever one ran, including when the apply
	// stops early (cancel, Timeout, lost lease): the books fetched before the
	// stop keep their new credits.
	defer func() {
		if attempted.Load() > 0 {
			p.deps.InvalidateDedupCache()
		}
	}()

	runErr := registry.RunItems(ctx, reporter, bookIDs, func(itemCtx context.Context, bookID string) error {
		if reporter.IsCanceled() {
			return context.Canceled
		}
		attempted.Add(1)
		newAuthorID, fetchErr := p.deps.FetchMetadataByTitle(itemCtx, bookID)
		if fetchErr != nil {
			if ctx.Err() != nil {
				// The op stopped (cancel, Timeout, lost lease), not this book.
				return context.Cause(ctx)
			}
			fetchFailed.Add(1)
			return nil
		}
		if newAuthorID == nil {
			return nil
		}
		if newAuthor, _ := store.GetAuthorByID(*newAuthorID); newAuthor != nil && !dedup.IsProductionCompany(newAuthor.Name) {
			resolved.Add(1)
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: resolveProductionAuthorsWorkers(),
		Label: func(i, n int) string {
			return fmt.Sprintf("Book %d/%d (%d resolved, %d fetches failed)", i+1, n, resolved.Load(), fetchFailed.Load())
		},
	})

	resultMsg := fmt.Sprintf("Resolved %d books across %d production companies (%d fetches failed, %d companies' book lists unreadable)",
		resolved.Load(), total, fetchFailed.Load(), lookupFailed)
	if runErr != nil {
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("Stopped after %d of %d books: %s: %v", attempted.Load(), len(bookIDs), resultMsg, runErr))
		return runErr
	}
	_ = reporter.Log(slog.LevelInfo, resultMsg)
	prog.Done(resultMsg)
	return nil
}
