// file: internal/plugins/maintenance/author_duplicate_merge.go
// version: 1.4.3
// guid: eb7daa4e-b1e4-4a15-92c5-b89e53cc6be3
// last-edited: 2026-10-30

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
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
	// dryRun (camelCase) is accepted as an alias; sending both with different
	// values is refused by opmode.ResolveDryRun, never guessed at.
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`

	// Names is the operator's explicit allowlist, matched after
	// util.NormalizeAuthor so an operator does not have to reproduce a row's
	// exact spacing or casing. It is REQUIRED: an empty or absent Names is a
	// no-op that reads nothing and writes nothing. It never means "all".
	Names []string `json:"names,omitempty"`
}

func (p *Plugin) authorDuplicateMergeDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.author-duplicate-merge",
		Preview:     sdk.PreviewSpec{Params: authorDuplicateMergeParams{}},
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
	dryRun, err := opmode.ResolveDryRun("maintenance.author-duplicate-merge", params.DryRun, params.DryRunCamel)
	if err != nil {
		return err
	}
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
	//
	// Bucketed (2026-09-12): the guard compares only the references a relink can
	// see -- live and trashed books -- against what it will move. A junction row
	// whose book row no longer exists (dangling) used to count too, and held
	// authors back for a reference that DeleteAuthor's sweep removes anyway.
	refBuckets, err := database.AuthorRefBucketCounts(store)
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
	if !dryRun && opID == "" {
		// Every merge is journaled under this id so the revert engine can undo it
		// (mergeAuthorInto). Rows under no id belong to no operation a revert can
		// name, so an apply without one would be a merge nobody can undo.
		return fmt.Errorf("author-duplicate-merge: no operation id on the reporter to journal the undo rows under; refusing to merge")
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
			// well as inside the primitive so the guard below can count the books
			// the merge is able to move -- which assumes the two reads agree
			// within the run, true for a sequential op holding the scan
			// stand-down. Both reads use GetBooksByAuthorIDForRelinkCore, which
			// includes the trash; they must stay the same getter, or the guard
			// would judge a different set than the merge moves.
			movable, mErr := store.GetBooksByAuthorIDForRelinkCore(src.ID)
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
			// It is a PRE-flight check: mergeAuthorInto re-checks after its moves
			// (VerifyAuthorUnlinked) only against what the relink view can see,
			// and this compares the UNFILTERED reference count with what the merge
			// is actually able to move, because the difference is exactly the set
			// that would be stranded.
			//
			// Dangling refs are excluded from the comparison (see refBuckets above)
			// but still logged, so a held-back line accounts for every reference.
			if rb := refBuckets[src.ID]; rb.Resolvable() > len(distinct) {
				rowOutcomes[authorDupRowHeldRefs]++
				log.Warn("author-duplicate-merge: source row still referenced by books the merge cannot move, held back",
					"author_id", src.ID, "name", src.Name,
					"live_refs", rb.Live, "trashed_refs", rb.Trashed, "dangling_refs", rb.Dangling,
					"movable_books", len(distinct))
				continue
			}

			// The per-item half of the stand-down contract: renew the lease and
			// treat losing it as a hard abort of the REMAINING writes. A lapsed
			// lease means the scanner has resumed, and a scan running alongside
			// these writes rewrites the very book rows being relinked. Inert when
			// the gate is not held (a dry run, or a stand-down controller that is
			// not wired), so an op with no interlock is never spuriously aborted.
			// An apply with no op id never gets here: it is refused above.
			if scanStandDownLostForApply(p.deps, standDownHolder, standDownHeld) {
				return fmt.Errorf("author-duplicate-merge: scan stand-down lease lost after %d merge(s), "+
					"refusing to keep writing while the scanner is running", rowOutcomes[authorDupRowMerged])
			}

			// mergeAuthorInto journals each write before making it (one
			// author_merge_credits row per book, one author_merge_delete row for
			// the source), so a merge that fails partway leaves exactly the
			// books it moved journaled, and the revert engine can undo the lot.
			relinked, undoRows, err := p.mergeAuthorInto(ctx, src, canonical, dryRun, opID, log)
			ledgerRows += undoRows
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
