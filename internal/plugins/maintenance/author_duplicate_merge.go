// file: internal/plugins/maintenance/author_duplicate_merge.go
// version: 1.1.0
// guid: eb7daa4e-b1e4-4a15-92c5-b89e53cc6be3
// last-edited: 2026-09-10

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- author-duplicate-merge ---
//
// Merges duplicate REAL-author rows -- two or more live author rows whose names
// normalize to the same person -- into a single keeper. TODO.md's 2026-08-14
// snapshot names four such groups, but this op deliberately hardcodes NONE of
// them: the names to merge arrive as a runtime `names` parameter and nothing
// else is ever touched.
//
// 🔴 WHY AN EXPLICIT ALLOWLIST AND NOT A CLASSIFIER. The duplicate-author
// population is three different problems wearing one shape. Type 3 is what this
// op fixes: two rows that really are the same person. Types 1 and 2 are book
// titles and disc labels that reached an artist tag and were parsed into the
// author table ("- Edgedancer", "04 - Heir to the Jedi"). Merging those does not
// repair them, it launders them -- an obviously-corrupt row becomes a plausible
// one and stops being findable. TODO.md L3803 says so directly, and
// author_conjunction_repair.go's own header records the same decision for rows
// beginning with "and ". A "does this look like a real name?" heuristic cannot
// tell the three apart, so this op does not have one. It can only act on names
// an operator typed, which is the anti-laundering guard for this task.
//
// The merge primitive is p.mergeAuthorInto (author_conjunction_repair.go) --
// deliberately reused rather than reimplemented, because the link that matters is
// the book_authors junction slice, not Book.AuthorID, and a second merge helper
// would be a second chance to get that wrong.

// Per-name outcomes. Every listed name lands in exactly one bucket and every
// bucket is reported: a summary of "merged 3" that does not account for the
// names it did nothing with is the shape of report that hides a bug.
const (
	authorDupNameMerged    = "group_processed"
	authorDupNameSingleton = "no_duplicate_found"
	authorDupNameNoMatch   = "no_live_author_by_that_name"
	authorDupNameBlank     = "blank_after_normalization"
	authorDupNameDuplicate = "duplicate_of_another_listed_name"
)

// Per-source-row outcomes, counted over the non-canonical rows of every matched
// group.
const (
	authorDupRowMerged     = "merged_into_canonical"
	authorDupRowWouldMerge = "would_merge_into_canonical"
	authorDupRowHeldRefs   = "held_still_referenced"
	authorDupRowFailed     = "failed"
)

// authorDuplicateMergeParams are the JSON parameters accepted by the op.
type authorDuplicateMergeParams struct {
	// DryRun reports what WOULD be merged without writing it. Defaults to TRUE
	// when absent, exactly as author_conjunction_repair.go does, and for the same
	// reason: a merge DELETES an author row, an author's name lives only in that
	// row, and there is no undo handler that puts it back. The default has to be
	// the harmless one.
	//
	// There is deliberately no second `apply` knob. Two flags that both mean
	// "write" have a combination that means both at once, and on an op that
	// deletes rows that ambiguity is the whole hazard.
	DryRun *bool `json:"dry_run,omitempty"`

	// Names is the operator's explicit allowlist, matched after
	// util.NormalizeAuthor so an operator does not have to reproduce a row's
	// exact spacing or casing. It is REQUIRED: an empty or absent Names is a
	// no-op that reads nothing and writes nothing. It never means "all".
	Names []string `json:"names,omitempty"`
}

func (p *Plugin) authorDuplicateMergeDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.author-duplicate-merge",
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Merge an operator-confirmed list of duplicate authors",
		Description: "Merges duplicate author rows into one keeper, but ONLY for the author names " +
			"passed in `names` (matched after normalization). There is no heuristic and no " +
			"'merge everything that looks similar' mode: an empty `names` is a no-op. Within each " +
			"listed group the row with the most books wins (ties go to the lowest id) and every " +
			"other row's books are relinked onto it before it is deleted. A row still referenced " +
			"by books the merge cannot move (trashed, non-primary, or junction-only credits) is " +
			"held back rather than deleted. Defaults to dry_run=true; pass dry_run=false to write.",
		// ResumeRestart, matching author-conjunction-repair: the run is short and
		// idempotent (after a successful merge the group has one live row, so a
		// re-run reports nothing to do), so restarting beats resuming a half-known
		// position through a deletion.
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.author-duplicate-merge",
		Writes:          []sdk.Resource{sdk.ResBooks},
		Reads:           []sdk.Resource{sdk.ResBooks},
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runAuthorDuplicateMerge,
	}
}

func (p *Plugin) runAuthorDuplicateMerge(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	var params authorDuplicateMergeParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("maintenance.author-duplicate-merge: decode params: %w", err)
		}
	}
	dryRun := params.DryRun == nil || *params.DryRun
	log := reporter.Logger()

	// Build the allowlist FIRST, before any store read. Two listed names that
	// normalize to the same group are folded here rather than processed twice --
	// the second pass would find a group of one and report "no duplicate found"
	// about a merge that had just succeeded.
	nameOutcomes := map[string]int{}
	wanted := map[string]string{} // normalized form -> the spelling the operator typed
	var wantedOrder []string
	for _, n := range params.Names {
		norm := util.NormalizeAuthor(n)
		if norm == "" {
			nameOutcomes[authorDupNameBlank]++
			log.Warn("author-duplicate-merge: listed name is blank after normalization, ignored", "name", n)
			continue
		}
		if _, seen := wanted[norm]; seen {
			nameOutcomes[authorDupNameDuplicate]++
			log.Info("author-duplicate-merge: listed name folds into an earlier one", "name", n, "normalized", norm)
			continue
		}
		wanted[norm] = strings.TrimSpace(n)
		wantedOrder = append(wantedOrder, norm)
	}
	// Deterministic order: two runs of the same params process the same groups in
	// the same sequence, so their reports can be diffed.
	sort.Strings(wantedOrder)

	// 🔴 NO NAMES IS A NO-OP, NOT "EVERYTHING". Returned before the first store
	// read so the inertness is structural rather than a branch that has to stay
	// correct: with no names there is nothing to read and nothing to write.
	if len(wantedOrder) == 0 {
		summary := fmt.Sprintf(
			"author-duplicate-merge complete (dry_run=%v): names_listed=%d, names_usable=0, nothing to do "+
				"(this op only merges names it is explicitly given)", dryRun, len(params.Names))
		log.Info("author-duplicate-merge: no usable names supplied, nothing to do",
			"dry_run", dryRun, "names_listed", len(params.Names), "name_outcomes", nameOutcomes)
		prog := sdk.NewProgress(reporter, 0)
		prog.Start(summary)
		prog.Done(summary)
		return nil
	}

	authors, err := store.GetAllAuthors()
	if err != nil {
		return fmt.Errorf("get all authors: %w", err)
	}
	bookCounts, err := store.GetAllAuthorBookCounts()
	if err != nil {
		return fmt.Errorf("author book counts: %w", err)
	}

	// THE DELETE GUARD USES THE UNFILTERED COUNT, NOT bookCounts ABOVE.
	//
	// GetAllAuthorBookCounts is a DISPLAY counter: it skips trashed and
	// non-primary books and never sees junction-only co-author credits. Every one
	// of those books still holds the author id, so merging a row whose books the
	// merge cannot see and then deleting it strands them behind an id that no
	// longer exists -- and the author's NAME lives only in the deleted row, so it
	// is not recoverable afterwards. Same guard family as
	// author_purge_empty.go:184. It fails CLOSED: a store that cannot answer the
	// unfiltered question aborts the op rather than falling back to the filtered
	// count, because that fallback IS the bug.
	refCounts, err := database.AuthorRefCounts(store)
	if err != nil {
		return fmt.Errorf("author-duplicate-merge: %w", err)
	}

	groups := map[string][]database.Author{}
	for _, a := range authors {
		norm := util.NormalizeAuthor(a.Name)
		if _, ok := wanted[norm]; !ok {
			continue
		}
		groups[norm] = append(groups[norm], a)
	}

	log.Info("author-duplicate-merge: starting",
		"dry_run", dryRun, "authors_total", len(authors),
		"names_listed", len(params.Names), "names_usable", len(wantedOrder))

	// The scan stand-down is acquired ONCE for the whole write phase, matching
	// mark_missing_files.go / missing_file_repoint.go / recover_missing_files.go.
	// A running library.scan rewrites book rows underneath an apply, so the writes
	// below must not race it. A dry run writes nothing and therefore takes no gate:
	// parking the scanner to read would be a real cost for no reason.
	var standDownHolder string
	var standDownHeld bool
	if !dryRun {
		holderID, held, releaseStandDown, sdErr := acquireScanStandDownForApply(ctx, p.deps, reporter, "author-duplicate-merge apply")
		if sdErr != nil {
			return fmt.Errorf("author-duplicate-merge: acquire scan stand-down: %w", sdErr)
		}
		defer releaseStandDown()
		standDownHolder, standDownHeld = holderID, held
	}

	opID := registry.ReporterOpID(reporter)
	if opID == "" {
		// Matches series_dedup.go's precedent: journal anyway rather than refuse to
		// write. A ledger row with no operation id is still a record of what moved;
		// refusing the write because the record is imperfect would be worse.
		log.Warn("author-duplicate-merge: no operation id on the reporter, undo-ledger rows will be unattributed")
	}

	rowOutcomes := map[string]int{}
	booksRelinked := 0
	ledgerRows := 0

	prog := sdk.NewProgress(reporter, len(wantedOrder))
	prog.Start(fmt.Sprintf("Merging %d operator-listed author group(s) (dry_run=%v)", len(wantedOrder), dryRun))

	// Deliberately SEQUENTIAL, against the repo's default preference for a worker
	// pool. The merge path is a read-modify-write of a book's author slice
	// (GetBookAuthors -> SetBookAuthors), and two workers merging two different
	// author rows that appear on the SAME book would lose one another's update.
	// Partitioning by author does not make the work disjoint, because the unit
	// actually mutated is the book. The same reasoning is written out at
	// author_conjunction_repair.go:168-177. The listed set is a handful of groups,
	// so a pool would buy nothing and add a race window.
	for i, norm := range wantedOrder {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		listed := wanted[norm]
		group := groups[norm]
		sort.Slice(group, func(a, b int) bool { return group[a].ID < group[b].ID })

		if len(group) == 0 {
			nameOutcomes[authorDupNameNoMatch]++
			log.Info("author-duplicate-merge: no live author by that name", "name", listed, "normalized", norm)
			prog.StepN(i+1, fmt.Sprintf("%d/%d", i+1, len(wantedOrder)))
			continue
		}
		if len(group) == 1 {
			nameOutcomes[authorDupNameSingleton]++
			log.Info("author-duplicate-merge: no duplicate found",
				"name", listed, "normalized", norm, "author_id", group[0].ID)
			prog.StepN(i+1, fmt.Sprintf("%d/%d", i+1, len(wantedOrder)))
			continue
		}
		nameOutcomes[authorDupNameMerged]++

		// Canonical = most books, ties to the lowest id. Most books means the
		// fewest links to rewrite and the row users have actually been seeing.
		canonical := group[0]
		for _, a := range group[1:] {
			if bookCounts[a.ID] > bookCounts[canonical.ID] {
				canonical = a
			}
		}

		for _, src := range group {
			if src.ID == canonical.ID {
				continue
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}

			// movable is what mergeAuthorInto will iterate. It is fetched here as
			// well as inside the primitive so the guard below and the undo-ledger
			// rows further down can both name the specific books -- which assumes
			// the two reads agree within the run, true for a sequential op holding
			// the scan stand-down.
			movable, mErr := store.GetBooksByAuthorIDWithRoleCore(src.ID)
			if mErr != nil {
				rowOutcomes[authorDupRowFailed]++
				log.Warn("author-duplicate-merge: cannot list books for source row, not merging",
					"author_id", src.ID, "name", src.Name, "err", mErr)
				continue
			}
			// Deduped by book id before it is counted. A book carrying two role
			// rows for the same author comes back twice here, while
			// AuthorRefCounts counts it once; an inflated movable count would mask
			// a real stranding.
			distinct := make(map[string]struct{}, len(movable))
			for _, b := range movable {
				distinct[b.ID] = struct{}{}
			}

			// 🔴 THE GUARD, evaluated before the dry-run branch so a dry run and an
			// apply hold back exactly the same rows. A guard applied only on the
			// write path would make the dry run a lie (author_purge_empty.go:189).
			//
			// It is a PRE-flight check rather than a post-move re-count because
			// mergeAuthorInto's DeleteAuthor is its own last statement -- there is
			// no seam between the move and the delete to check in, and prising one
			// open would change a primitive two other ops already depend on. The
			// invariant is the same either way: refuse when the unfiltered
			// reference count exceeds what the merge is actually able to move,
			// because the difference is exactly the set that would be stranded.
			if refCounts[src.ID] > len(distinct) {
				rowOutcomes[authorDupRowHeldRefs]++
				log.Warn("author-duplicate-merge: source row still referenced by books the merge cannot move, held back",
					"author_id", src.ID, "name", src.Name,
					"unfiltered_refs", refCounts[src.ID], "movable_books", len(distinct))
				continue
			}

			// The per-item half of the stand-down contract: renew the lease and
			// treat losing it as a hard abort of the REMAINING writes. A lapsed
			// lease means the scanner has resumed, and a scan running alongside
			// these writes rewrites the very book rows being relinked. Inert when
			// the gate is not held (a dry run, or a direct call with no op id), so
			// an op with no interlock is never spuriously aborted.
			if scanStandDownLostForApply(p.deps, standDownHolder, standDownHeld) {
				return fmt.Errorf("author-duplicate-merge: scan stand-down lease lost after %d merge(s), "+
					"refusing to keep writing while the scanner is running", rowOutcomes[authorDupRowMerged])
			}

			relinked, err := p.mergeAuthorInto(ctx, src, canonical, dryRun, log)
			// Journal FIRST, and on the error path too: mergeAuthorInto can fail
			// partway through, and the books it moved before failing are moved. A
			// mutation without a ledger row is a defect, so the partial case is
			// precisely the one that must not be skipped. relinked counts the
			// primitive's own iterations over the same ordered slice, so
			// movable[:relinked] names exactly the books it rewrote.
			if !dryRun {
				ledgerRows += p.journalAuthorMerge(store, opID, src, canonical, movable, relinked, err == nil, log)
			}
			if err != nil {
				rowOutcomes[authorDupRowFailed]++
				log.Warn("author-duplicate-merge: merge failed",
					"from_id", src.ID, "from", src.Name,
					"into_id", canonical.ID, "into", canonical.Name, "err", err)
				continue
			}
			booksRelinked += relinked
			if dryRun {
				rowOutcomes[authorDupRowWouldMerge]++
			} else {
				rowOutcomes[authorDupRowMerged]++
			}
			log.Info("author-duplicate-merge: merge",
				"dry_run", dryRun, "from_id", src.ID, "from", src.Name,
				"into_id", canonical.ID, "into", canonical.Name, "books", relinked)
		}
		prog.StepN(i+1, fmt.Sprintf("%d/%d", i+1, len(wantedOrder)))
	}

	// Drop the cached author list, but only when this run actually wrote. A dry
	// run changed nothing and dropping a warm cache costs something for nothing --
	// the same guard as author_conjunction_repair.go:263.
	if !dryRun && rowOutcomes[authorDupRowMerged] > 0 {
		p.deps.InvalidateAuthorsCache()
		p.deps.InvalidateDedupCache()
		log.Info("author-duplicate-merge: invalidated author caches", "rows_written", rowOutcomes[authorDupRowMerged])
	}

	summary := fmt.Sprintf(
		"author-duplicate-merge complete (dry_run=%v): names_listed=%d, names_usable=%d, name_outcomes=%v, "+
			"row_outcomes=%v, books_relinked=%d, undo_rows=%d, authors_total=%d",
		dryRun, len(params.Names), len(wantedOrder), nameOutcomes, rowOutcomes, booksRelinked, ledgerRows, len(authors))
	log.Info("author-duplicate-merge: done",
		"dry_run", dryRun, "name_outcomes", nameOutcomes, "row_outcomes", rowOutcomes,
		"books_relinked", booksRelinked, "undo_rows", ledgerRows, "authors_total", len(authors))
	prog.Done(summary)
	return nil
}

// journalAuthorMerge writes the undo-ledger rows for one source row's merge and
// returns how many it wrote. One row per book whose author link actually moved,
// plus one for the deleted author row when the merge completed.
//
// 🔴 WHAT THE LEDGER CAN AND CANNOT UNDO. internal/undo/engine.go replays
// change_type "metadata_update" (engine.go:141), so the per-book rows are in the
// vocabulary it understands, though "author_id" is not one of the field names its
// metadata path switches on -- these rows are a durable record of what moved, not
// a one-click restore. The author-delete row uses change_type "author_delete",
// mirroring series_dedup.go's "series_delete", which the engine likewise does not
// replay: an author's name lives only in the deleted row, so the ledger records
// it precisely because nothing else will. This is why dry_run defaults to true.
func (p *Plugin) journalAuthorMerge(
	store OpsStore,
	opID string,
	src, canonical database.Author,
	movable []database.BookCore,
	relinked int,
	deleted bool,
	log logWarner,
) int {
	if relinked > len(movable) {
		relinked = len(movable)
	}
	written := 0
	journaled := make(map[string]struct{}, relinked)
	for _, b := range movable[:relinked] {
		if _, dup := journaled[b.ID]; dup {
			continue
		}
		journaled[b.ID] = struct{}{}
		if err := store.CreateOperationChange(&database.OperationChange{
			ID:          ulid.Make().String(),
			OperationID: opID,
			BookID:      b.ID,
			ChangeType:  "metadata_update",
			FieldName:   "author_id",
			OldValue:    strconv.Itoa(src.ID),
			NewValue:    strconv.Itoa(canonical.ID),
		}); err != nil {
			// Logged, never swallowed: an unjournaled mutation is the defect this
			// call exists to prevent, so a failure to record one has to be visible.
			log.Warn("author-duplicate-merge: undo-ledger write failed for a moved book",
				"book_id", b.ID, "from_id", src.ID, "into_id", canonical.ID, "err", err)
			continue
		}
		written++
	}
	if !deleted {
		return written
	}
	if err := store.CreateOperationChange(&database.OperationChange{
		ID:          ulid.Make().String(),
		OperationID: opID,
		ChangeType:  "author_delete",
		FieldName:   "author",
		OldValue:    fmt.Sprintf("%d:%s", src.ID, src.Name),
		NewValue:    fmt.Sprintf("merged_into:%d", canonical.ID),
	}); err != nil {
		log.Warn("author-duplicate-merge: undo-ledger write failed for the deleted author row",
			"author_id", src.ID, "name", src.Name, "err", err)
		return written
	}
	return written + 1
}

// logWarner is the one method journalAuthorMerge needs from the reporter's
// logger, declared rather than taking *slog.Logger so the helper's dependency is
// the method it calls.
type logWarner interface {
	Warn(msg string, args ...any)
}
