// file: internal/plugins/maintenance/author_conjunction_repair.go
// version: 1.1.0
// guid: 2f8a41c6-9d73-4e05-b18a-6c4f2e93d70b
// last-edited: 2026-08-14

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- author-conjunction-repair ---
//
// Repairs author rows left named "& Conrad Westmaas" by the ordering bug in
// dedup.SplitCompositeAuthorName (fixed 2026-08-14). The source is an
// Oxford-comma credit list; the real album_artist tag on The Creed of the Kromon
// reads "Paul McGann, India Fisher, & Conrad Westmaas". The comma branch ran
// before the " & " branch and accepted "& Conrad Westmaas" because its only
// validity test was "contains a space".
//
// This op repairs the rows that bug already created. The forward fix does not
// touch them: nothing re-normalizes an author name after creation.

// authorConjunctionRe deliberately matches "&" ONLY, and is therefore NARROWER
// than the pattern in dedup.NormalizeAuthorName, which also strips "and".
//
// That difference is the whole point and must not be "tidied up" into a shared
// constant. Three production rows begin with "and " — "and Thanks for All the
// Fish" (from So Long, and Thanks for All the Fish), "and the Farm Boy (DBY)",
// and "and Make Better Decisions". They are not stranded conjunctions from a
// credit list; they are book TITLES that reached an artist tag and were then
// comma-split. Stripping "and" from them yields "Thanks for All the Fish",
// which is still not an author — it merely stops looking broken. Laundering an
// obviously-corrupt row into a plausible one is worse than leaving it visible,
// so those rows are left alone for a separate fix that addresses the real
// defect (the comma branch cannot tell a person from a title clause).
//
// Stripping "and" IS correct in the forward path, where "A, B, and C" is a
// genuine credit list. The two pattern widths are correct for their two jobs.
var authorConjunctionRe = regexp.MustCompile(`^&\s+`)

// authorConjunctionRepairParams controls the repair.
type authorConjunctionRepairParams struct {
	// DryRun reports what WOULD be written without writing it. Defaults to
	// TRUE. The population is small enough (46 rows on 2026-08-14) that the
	// full per-row plan is readable, and merges delete author rows — a
	// destructive step that deserves to be read before it runs.
	DryRun *bool `json:"dry_run,omitempty"`

	// SkipAuthorIDs excludes specific author rows from the run, reported as
	// their own outcome bucket rather than silently dropped.
	//
	// This exists because a merge DELETES an author row, and the op can only
	// relink the books it can see. On 2026-08-14 two dry runs of this same op
	// disagreed: the first ran four seconds after a restart and fell through to
	// the Pebble junction scan, reporting books_relinked=86; the second ran
	// against a warm memdb and reported 84. The whole difference was author
	// 46627 ("& Nicholas Courtney"), where Pebble holds two book links memdb
	// does not — and memdb had been freshly loaded, so its loader drops them
	// rather than merely lagging.
	//
	// Merging such a row via the memdb path would relink zero books and then
	// delete the author, leaving those Pebble junction rows pointing at an
	// author id that no longer exists. That is the same orphaning hazard H8
	// documents on the author split scan. Until the divergence is understood,
	// the affected row is excluded by id rather than by a clever heuristic, so
	// the exclusion is visible in the op's params and in its summary.
	SkipAuthorIDs []int `json:"skip_author_ids,omitempty"`
}

// Repair outcomes — every matched author lands in exactly one bucket and all
// buckets are reported. A summary of "scanned 9350, repaired 46" that does not
// account for the rest is the shape of report that hides a bug.
const (
	conjRepairMerged      = "merged_into_existing"
	conjRepairRenamed     = "renamed_in_place"
	conjRepairWouldMerge  = "would_merge_into_existing"
	conjRepairWouldRename = "would_rename_in_place"
	conjRepairSkipNoop    = "skip_strip_changed_nothing"
	conjRepairSkipListed  = "skip_explicitly_excluded"
	conjRepairFailed      = "failed"
)

func (p *Plugin) authorConjunctionRepairDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.author-conjunction-repair",
		Plugin:      "maintenance",
		DisplayName: "Repair author names with a stranded ampersand",
		Description: "Repairs author rows named like '& Conrad Westmaas', created when an " +
			"Oxford-comma credit list ('A, B, & C') was split on the comma before the ampersand " +
			"branch could run. Merges each row into the correctly-named author when one already " +
			"exists, otherwise renames it in place. Matches '&' only — rows beginning with 'and ' " +
			"are book-title fragments and are deliberately left alone. Defaults to dry_run=true; " +
			"pass dry_run=false to write. skip_author_ids excludes specific rows, reported as " +
			"skip_explicitly_excluded rather than dropped from the totals.",
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.author-conjunction-repair",
		Writes:          []sdk.Resource{sdk.ResBooks},
		Reads:           []sdk.Resource{sdk.ResBooks},
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runAuthorConjunctionRepair,
	}
}

func (p *Plugin) runAuthorConjunctionRepair(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	var params authorConjunctionRepairParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("maintenance.author-conjunction-repair: decode params: %w", err)
		}
	}
	dryRun := params.DryRun == nil || *params.DryRun
	skip := make(map[int]struct{}, len(params.SkipAuthorIDs))
	for _, id := range params.SkipAuthorIDs {
		skip[id] = struct{}{}
	}

	log := reporter.Logger()

	authors, err := store.GetAllAuthors()
	if err != nil {
		return fmt.Errorf("get all authors: %w", err)
	}

	// Select first, then act. The matched set is tiny relative to the table
	// (46 of 9,350 on 2026-08-14 — 48 author names begin with '&', but two are
	// the '&#169' HTML-entity rows this op correctly does not match), and
	// reporting the selection size separately
	// from the table size is what makes a zero-match run readable as "nothing
	// to do" rather than "the scan did not run".
	var matched []database.Author
	for _, a := range authors {
		if authorConjunctionRe.MatchString(strings.TrimSpace(a.Name)) {
			matched = append(matched, a)
		}
	}

	log.Info("author-conjunction-repair: starting",
		"dry_run", dryRun, "authors_total", len(authors), "authors_matched", len(matched),
		"skip_author_ids", params.SkipAuthorIDs)

	outcomes := map[string]int{}
	booksRelinked := 0

	prog := sdk.NewProgress(reporter, len(matched))
	prog.Start(fmt.Sprintf("Repairing %d author rows with a stranded ampersand (dry_run=%v)", len(matched), dryRun))

	// Deliberately SEQUENTIAL, against the repo's default preference for a
	// bounded worker pool on library-scale loops.
	//
	// Two reasons, both correctness rather than convenience. First, the merge
	// path is a read-modify-write of a book's author slice (GetBookAuthors →
	// SetBookAuthors); two workers repairing two different "&" rows that appear
	// on the SAME book would lose one another's update. Partitioning by author
	// does not make the work disjoint, because the unit actually mutated is the
	// book. Second, the matched set is 46 rows carrying 145 books — the whole
	// run is seconds, so a pool would add a race window to buy nothing.
	for i, author := range matched {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Excluded rows stay in `matched` and get their own bucket, so the
		// summary still adds up to the number of rows the pattern selected. A
		// row filtered out of the selection instead would make a partial run
		// indistinguishable from a smaller library.
		if _, ok := skip[author.ID]; ok {
			outcomes[conjRepairSkipListed]++
			log.Info("author-conjunction-repair: skipped by request",
				"author_id", author.ID, "name", author.Name)
			prog.StepN(i+1, fmt.Sprintf("%d/%d", i+1, len(matched)))
			continue
		}

		// Use the same normalizer the forward fix installed, so a repaired name
		// is byte-identical to what a re-import would now produce rather than
		// merely similar. For the current population this equals a plain strip:
		// none of the 46 rows contain collapsed initials for it to expand.
		cleaned := dedup.NormalizeAuthorName(author.Name)
		if cleaned == "" || cleaned == author.Name {
			outcomes[conjRepairSkipNoop]++
			prog.StepN(i+1, fmt.Sprintf("%d/%d", i+1, len(matched)))
			continue
		}

		twin, err := store.GetAuthorByName(cleaned)
		if err != nil {
			outcomes[conjRepairFailed]++
			log.Warn("author-conjunction-repair: twin lookup failed",
				"author_id", author.ID, "name", author.Name, "cleaned", cleaned, "err", err)
			prog.StepN(i+1, fmt.Sprintf("%d/%d", i+1, len(matched)))
			continue
		}

		if twin != nil && twin.ID != author.ID {
			relinked, err := p.mergeAuthorInto(ctx, author, *twin, dryRun, log)
			if err != nil {
				outcomes[conjRepairFailed]++
				log.Warn("author-conjunction-repair: merge failed",
					"from_id", author.ID, "from", author.Name,
					"into_id", twin.ID, "into", twin.Name, "err", err)
			} else {
				booksRelinked += relinked
				if dryRun {
					outcomes[conjRepairWouldMerge]++
				} else {
					outcomes[conjRepairMerged]++
				}
				log.Info("author-conjunction-repair: merge",
					"dry_run", dryRun, "from_id", author.ID, "from", author.Name,
					"into_id", twin.ID, "into", twin.Name, "books", relinked)
			}
			prog.StepN(i+1, fmt.Sprintf("%d/%d", i+1, len(matched)))
			continue
		}

		// No existing author by the cleaned name — rename in place. This keeps
		// the row's id, so every book already linked to it stays linked and no
		// book rows are touched at all.
		if dryRun {
			outcomes[conjRepairWouldRename]++
			log.Info("author-conjunction-repair: rename",
				"dry_run", true, "author_id", author.ID, "from", author.Name, "to", cleaned)
		} else if err := store.UpdateAuthorName(author.ID, cleaned); err != nil {
			outcomes[conjRepairFailed]++
			log.Warn("author-conjunction-repair: rename failed",
				"author_id", author.ID, "from", author.Name, "to", cleaned, "err", err)
		} else {
			outcomes[conjRepairRenamed]++
			log.Info("author-conjunction-repair: rename",
				"dry_run", false, "author_id", author.ID, "from", author.Name, "to", cleaned)
		}
		prog.StepN(i+1, fmt.Sprintf("%d/%d", i+1, len(matched)))
	}

	summary := fmt.Sprintf(
		"author-conjunction-repair complete (dry_run=%v): matched=%d of %d authors, books_relinked=%d, outcomes=%v",
		dryRun, len(matched), len(authors), booksRelinked, outcomes)
	log.Info("author-conjunction-repair: done",
		"dry_run", dryRun, "authors_total", len(authors), "authors_matched", len(matched),
		"books_relinked", booksRelinked, "outcomes", outcomes)
	prog.Done(summary)
	return nil
}

// mergeAuthorInto moves every book link from `from` onto `into` and then deletes
// the `from` row. It returns the number of books whose author slice was
// rewritten.
//
// The link that matters is the BookAuthor join slice, not Book.AuthorID: all 46
// stranded rows sit at position 1+ of a credit list, which is why every one of
// them reports file_count=0 while carrying books. A merge that only rewrote
// AuthorID would report success and change nothing.
func (p *Plugin) mergeAuthorInto(ctx context.Context, from, into database.Author, dryRun bool, log *slog.Logger) (int, error) {
	store := p.deps.Store()

	books, err := store.GetBooksByAuthorIDWithRoleCore(from.ID)
	if err != nil {
		return 0, fmt.Errorf("get books for author %d: %w", from.ID, err)
	}

	relinked := 0
	for _, book := range books {
		if ctx.Err() != nil {
			return relinked, ctx.Err()
		}

		bookAuthors, err := store.GetBookAuthors(book.ID)
		if err != nil {
			// Do NOT fall through to DeleteAuthor after this: dropping the row
			// while a book still points at it orphans that book's author
			// reference. Same failure H8 documented on the split scan.
			return relinked, fmt.Errorf("get book authors for %s: %w", book.ID, err)
		}

		// Preserve the role the stranded row carried, and note whether the
		// target is already on this book — a credit list can name both.
		role := "author"
		targetAlreadyLinked := false
		for _, ba := range bookAuthors {
			if ba.AuthorID == from.ID {
				role = ba.Role
			}
			if ba.AuthorID == into.ID {
				targetAlreadyLinked = true
			}
		}

		var updated []database.BookAuthor
		for _, ba := range bookAuthors {
			if ba.AuthorID == from.ID {
				continue
			}
			updated = append(updated, ba)
		}
		if !targetAlreadyLinked {
			updated = append(updated, database.BookAuthor{
				BookID:   book.ID,
				AuthorID: into.ID,
				Role:     role,
				Position: len(updated),
			})
		}

		if dryRun {
			relinked++
			continue
		}

		if err := store.SetBookAuthors(book.ID, updated); err != nil {
			return relinked, fmt.Errorf("set book authors for %s: %w", book.ID, err)
		}

		// If the stranded row was somehow also the denormalized primary, move
		// that too. Hydrate the full row rather than writing the BookCore
		// projection: BookCore has heavy fields nil, and its guard-preserved
		// Author would still name the row being deleted (STOREFID W5d-1).
		if book.AuthorID != nil && *book.AuthorID == from.ID {
			if full, err := store.GetBookByID(book.ID); err == nil && full != nil {
				target := into
				full.AuthorID = &into.ID
				full.Author = &target
				if _, err := store.UpdateBook(book.ID, full); err != nil {
					log.Warn("author-conjunction-repair: primary author rewrite failed",
						"book_id", book.ID, "err", err)
				}
			} else {
				log.Warn("author-conjunction-repair: hydrate failed, primary author left stale",
					"book_id", book.ID, "err", err)
			}
		}
		relinked++
	}

	if dryRun {
		return relinked, nil
	}
	if err := store.DeleteAuthor(from.ID); err != nil {
		return relinked, fmt.Errorf("delete author %d: %w", from.ID, err)
	}
	return relinked, nil
}
