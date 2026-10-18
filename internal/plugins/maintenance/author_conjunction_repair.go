// file: internal/plugins/maintenance/author_conjunction_repair.go
// version: 1.12.1
// guid: 2f8a41c6-9d73-4e05-b18a-6c4f2e93d70b
// last-edited: 2026-10-18

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/authormerge"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
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
	// conjRepairSkipNameTaken: the cleaned name was free when the twin lookup
	// ran but another live author held it at the rename (renameAuthorJournaled
	// checks under the author-name lock). Renaming would have taken that
	// author's name entry over; the row is left as it is and its undo row voided.
	conjRepairSkipNameTaken = "skip_name_taken"
	conjRepairFailed        = "failed"
)

func (p *Plugin) authorConjunctionRepairDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.author-conjunction-repair",
		Preview:     sdk.PreviewSpec{Key: sdk.PreviewDryRun, Params: authorConjunctionRepairParams{}},
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Repair author names with a stranded ampersand",
		Description: "Repairs author rows named like '& Conrad Westmaas', created when an " +
			"Oxford-comma credit list ('A, B, & C') was split on the comma before the ampersand " +
			"branch could run. Merges each row into the correctly-named author when one already " +
			"exists, otherwise renames it in place. Matches '&' only — rows beginning with 'and ' " +
			"are book-title fragments and are deliberately left alone. Defaults to dry_run=true; " +
			"pass dry_run=false to write. skip_author_ids excludes specific rows, reported as " +
			"skip_explicitly_excluded rather than dropped from the totals.",
		// RESUME AUDIT 2026-09-11 (c): kept, with no checkpoint, because a
		// from-zero restart is idempotent and cheap by construction. The work
		// list is selected from CURRENT author names (authorConjunctionRe on
		// GetAllAuthors), and every write removes its row from that selection:
		// a rename changes the name so it no longer matches, and a merge deletes
		// the row. A resumed run therefore recomputes the set, finds only what
		// the first attempt did not reach, and re-does nothing. The whole set is
		// tens of rows and runs in seconds, and dry_run and skip_author_ids
		// travel in params untouched.
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
	store := p.deps.OpsStore()
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
	// The undo ledger keys each merge's rows by this id (mergeAuthorInto).
	opID := registry.ReporterOpID(reporter)

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
			relinked, _, err := p.mergeAuthorInto(ctx, author, *twin, dryRun, opID, log)
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
		} else if err := renameAuthorJournaled(store, opID, author, cleaned); errors.Is(err, errRenameNameTaken) {
			outcomes[conjRepairSkipNameTaken]++
			log.Warn("author-conjunction-repair: rename skipped, another author holds the name",
				"author_id", author.ID, "from", author.Name, "to", cleaned, "err", err)
		} else if err != nil {
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

	// Drop the cached author list, but only when this run actually wrote.
	// The cache has a 24-hour TTL and was previously invalidated only by the
	// entities API, so the 2026-08-14 apply landed correctly in the store and
	// in all 145 book records while the author list kept serving the old "&"
	// names -- the one page anyone would check to confirm the repair. A dry
	// run must NOT invalidate: it changed nothing, and dropping a warm cache
	// is a real cost to pay for no reason.
	wrote := outcomes[conjRepairMerged] + outcomes[conjRepairRenamed]
	if !dryRun && wrote > 0 {
		p.deps.InvalidateAuthorsCache()
		p.deps.InvalidateDedupCache()
		log.Info("author-conjunction-repair: invalidated author caches", "rows_written", wrote)
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
// rewritten and the number of undo-ledger rows it wrote.
//
// The link that matters is the BookAuthor join slice, not Book.AuthorID: all 46
// stranded rows sit at position 1+ of a credit list, which is why every one of
// them reports file_count=0 while carrying books. A merge that only rewrote
// AuthorID would report success and change nothing.
//
// The list comes from GetBooksByAuthorIDForRelinkCore, which includes books in
// the trash: DeleteAuthor sweeps the author out of every junction row, trashed
// books included, so a trashed book this loop skipped used to lose its credit
// and keep an AuthorID naming the deleted row. The delete is gated on
// database.VerifyAuthorUnlinked, so a link this loop could not move (including
// a primary-author rewrite that only logged its failure) keeps the author row
// instead of being destroyed with it.
//
// Every write is journaled under opID BEFORE it is made, through the same
// authormerge path the Authors view merge uses: one author_merge_credits row
// per book and one author_merge_delete row for the author, which the revert
// engine reverses (credits, primary, the author, its aliases and redirects). An
// apply with no operation id is refused: its merge could never be undone.
func (p *Plugin) mergeAuthorInto(ctx context.Context, from, into database.Author, dryRun bool, opID string, log *slog.Logger) (relinked, undoRows int, err error) {
	store := p.deps.OpsStore()
	// The delete at the end is the store's merge-delete capability. Without it
	// the merged author could only be deleted with its aliases, so refuse before
	// any credit moves (a preview too: it must not promise a merge the apply
	// refuses). No DeleteAuthor fallback.
	if _, ok := database.AsCapability[database.AuthorMergeDeleter](store); !ok {
		return 0, 0, fmt.Errorf("merge author %d into %d: %w", from.ID, into.ID, database.ErrAuthorMergeDeleteUnsupported)
	}
	if !dryRun && opID == "" {
		return 0, 0, fmt.Errorf("merge author %d into %d: no operation id to journal its undo rows under, so it could not be undone", from.ID, into.ID)
	}

	books, err := store.GetBooksByAuthorIDForRelinkCore(from.ID)
	if err != nil {
		return 0, 0, fmt.Errorf("get books for author %d: %w", from.ID, err)
	}

	// A book carrying two role rows for the author comes back once per row; its
	// credits move (and are journaled) once.
	seen := make(map[string]struct{}, len(books))
	for _, book := range books {
		if ctx.Err() != nil {
			return relinked, undoRows, ctx.Err()
		}
		if _, dup := seen[book.ID]; dup {
			continue
		}
		seen[book.ID] = struct{}{}

		before, err := store.GetBookAuthors(book.ID)
		if err != nil {
			return relinked, undoRows, fmt.Errorf("get book authors for %s: %w", book.ID, err)
		}
		if dryRun {
			// The junction read above still ran, so a dry run surfaces the same
			// read failure the apply would hit.
			relinked++
			continue
		}
		into := into
		moved, err := authormerge.MoveCredits(store, opID, authormerge.CreditMove{
			BookID: book.ID, FromID: from.ID, IntoID: into.ID, Into: &into,
			Before: before, After: creditsMovedOnto(book.ID, before, from.ID, into.ID),
			PrimaryBefore: book.AuthorID,
		})
		if err != nil {
			// Do NOT fall through to the delete after this: dropping the row
			// while a book still points at it orphans that book's author
			// reference. Same failure H8 documented on the split scan.
			return relinked, undoRows, fmt.Errorf("book %s: %w", book.ID, err)
		}
		undoRows++
		for _, problem := range moved.Problems {
			// Logged, not returned: VerifyAuthorUnlinked below still refuses
			// the delete while the primary names the row.
			log.Warn("author merge: book credits moved with a problem",
				"book_id", book.ID, "from_id", from.ID, "into_id", into.ID, "problem", problem)
		}
		relinked++
	}

	if dryRun {
		return relinked, undoRows, nil
	}
	if err := database.VerifyAuthorUnlinked(store, from.ID); err != nil {
		return relinked, undoRows, err
	}
	// MergeDeleteAuthor, not DeleteAuthor: the merged author's aliases (pen
	// names) move to the survivor and the tombstone redirecting the old id is
	// written in the same batch. DeleteAuthor cascades the aliases away, so a pen
	// name of a merged author resolved to nobody.
	del, err := authormerge.MergeDelete(store, opID, from, into)
	if err != nil {
		return relinked, undoRows, fmt.Errorf("delete author %d: %w", from.ID, err)
	}
	undoRows++
	for _, problem := range del.Problems {
		log.Warn("author merge: author deleted with a problem", "author_id", from.ID, "problem", problem)
	}
	return relinked, undoRows, nil
}

// errRenameNameTaken: another live author holds the name a rename would write.
var errRenameNameTaken = errors.New("another author holds the name")

// renameAuthorJournaled renames author to name in place, journaled BEFORE the
// write as the author_rename row the revert engine reverses (it renames the
// author back while it still carries name). A row that cannot be recorded
// means no rename; a refused rename voids its row.
//
// The rename is checked and written under the author-name lock
// (database.AuthorCheckedRenamer): the row must still be author, live under its
// own id with the name the caller read, and no other live author may hold name
// (errRenameNameTaken). UpdateAuthorName would instead take a held name entry
// over and leave its holder unresolvable by name. A store without the
// capability is refused rather than renamed unchecked.
func renameAuthorJournaled(store OpsStore, opID string, author database.Author, name string) error {
	renamer, ok := database.AsCapability[database.AuthorCheckedRenamer](store)
	if !ok {
		return fmt.Errorf("this store cannot rename author %d under a name check", author.ID)
	}
	row, err := undo.NewAuthorRenameRow(author.ID, author.Name, name)
	if err != nil {
		return fmt.Errorf("undo row not built: %w", err)
	}
	void, err := authormerge.Step(store, opID, row)
	if err != nil {
		return fmt.Errorf("undo row not recorded: %w", err)
	}
	if err := renamer.RenameAuthorChecked(author.ID, name, func(cur *database.Author, holder int) error {
		switch {
		case cur == nil || cur.ID != author.ID:
			return fmt.Errorf("author %d was deleted or merged since it was read", author.ID)
		case cur.Name != author.Name:
			return fmt.Errorf("author %d was renamed since it was read (now %q)", author.ID, cur.Name)
		case holder != 0 && holder != author.ID:
			return fmt.Errorf("%w: %q is held by author %d", errRenameNameTaken, name, holder)
		}
		return nil
	}); err != nil {
		if verr := void(); verr != nil {
			return fmt.Errorf("%w (and its undo row could not be voided: %v)", err, verr)
		}
		return err
	}
	return nil
}

// creditsMovedOnto is bookAuthors with fromID's credit moved onto intoID: the
// role the from row carried is kept, and intoID is not duplicated when the book
// already credits it.
func creditsMovedOnto(bookID string, bookAuthors []database.BookAuthor, fromID, intoID int) []database.BookAuthor {
	// Preserve the role the from row carried, and note whether the target is
	// already on this book — a credit list can name both.
	role := "author"
	targetAlreadyLinked := false
	for _, ba := range bookAuthors {
		if ba.AuthorID == fromID {
			role = ba.Role
		}
		if ba.AuthorID == intoID {
			targetAlreadyLinked = true
		}
	}

	var updated []database.BookAuthor
	for _, ba := range bookAuthors {
		if ba.AuthorID == fromID {
			continue
		}
		updated = append(updated, ba)
	}
	if !targetAlreadyLinked {
		updated = append(updated, database.BookAuthor{
			BookID:   bookID,
			AuthorID: intoID,
			Role:     role,
			Position: len(updated),
		})
	}
	return updated
}

// relinkBookCredit moves one book's credit from `from` to `into`: the
// book_authors junction (keeping the role the from row carried, and not
// duplicating into when the book already credits it) and, when it named from,
// the denormalized primary AuthorID.
//
// The link that matters is the junction, not Book.AuthorID (see
// mergeAuthorInto). The primary is rewritten under the book's write lock
// (ModifyBook, audit A1#15) on the hydrated row, not the BookCore projection
// (STOREFID W5d-1), and a primary already moved off from meanwhile is left
// alone.
//
// dryRun reads the junction and writes nothing.
//
// err is a junction failure: nothing about the credit changed that a re-run
// would not redo. primaryErr is a failed primary rewrite AFTER the junction
// was written; the caller decides whether that is fatal. Its caller is
// author-strip-merge's title-as-author relink (one book at a time), which
// journals the move itself (titleRelinkJournal). mergeAuthorInto computes the
// same credits (creditsMovedOnto) but writes them through authormerge, which
// journals each book before its write.
func relinkBookCredit(store interface {
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
}, book database.BookCore, from, into database.Author, dryRun bool) (primaryErr, err error) {
	bookAuthors, err := store.GetBookAuthors(book.ID)
	if err != nil {
		return nil, fmt.Errorf("get book authors for %s: %w", book.ID, err)
	}

	updated := creditsMovedOnto(book.ID, bookAuthors, from.ID, into.ID)
	if dryRun {
		// The junction read above still ran, so a dry run surfaces the same
		// read failure the apply would hit.
		return nil, nil
	}
	if err := store.SetBookAuthors(book.ID, updated); err != nil {
		return nil, fmt.Errorf("set book authors for %s: %w", book.ID, err)
	}

	if book.AuthorID == nil || *book.AuthorID != from.ID {
		return nil, nil
	}
	target := into
	written, mErr := store.ModifyBook(book.ID, func(full *database.Book) error {
		if full.AuthorID == nil || *full.AuthorID != from.ID {
			return database.ErrSkipBookWrite
		}
		full.AuthorID = &target.ID
		full.Author = &target
		return nil
	})
	switch {
	case mErr != nil:
		return fmt.Errorf("rewrite primary author of %s: %w", book.ID, mErr), nil
	case written == nil:
		return fmt.Errorf("rewrite primary author of %s: book gone", book.ID), nil
	}
	return nil, nil
}
