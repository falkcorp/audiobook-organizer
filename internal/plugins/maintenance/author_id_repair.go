// file: internal/plugins/maintenance/author_id_repair.go
// version: 1.0.0
// guid: 6b2f8e19-4d73-4c0a-9e51-a8d7c3f02b64
// last-edited: 2026-09-13

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- author-id-repair ---
//
// Two library-wide author-id repairs, both by REPOINT and never by clearing:
//
//   (a) Dangling scalars. A book whose Book.AuthorID is SET but names no author
//       row (the leftover of DeleteAuthor paths that swept the join rows but not
//       the scalar -- SplitCompositeAuthor / ReclassifyAuthorAsNarrator before
//       this fix) is repointed to its next author by join-row position, or to
//       the Unknown Author placeholder the name index resolves when none is
//       left. The same rule DeleteAuthor now applies itself
//       (database.NextPrimaryAuthorID). A NIL scalar is counted and left alone:
//       sweeping authorless books into Unknown Author would be a library-wide
//       author assignment, which is unknown-author-audit's backlog, not this.
//
//   (b) Duplicate clusters. Author rows whose names normalize the same are
//       merged onto the ONE id the author:name index resolves -- the only row
//       reachable by name, so the only safe canonical choice. Every duplicate's
//       join rows (rewritten in place, de-duplicated) and scalars move onto it,
//       and a duplicate is deleted only after a re-read finds zero credits via
//       BOTH the memdb-backed relink getter AND the Pebble-only durable scan.
//       memdb and Pebble can diverge; a delete must never rest on a count one
//       of them cannot see.
//
// Unlike author-duplicate-merge this acts on EVERY normalized-name cluster, not
// an operator allowlist. That op's allowlist guards against laundering
// title-fragment "authors" into plausible rows by picking a keeper among
// DIFFERENT names; here every member already carries the same normalized name,
// so the merge changes no name any book displays -- it only makes the credits
// reachable through the id the name already resolves to.
//
// dry_run defaults to TRUE. The dry run writes nothing -- no book, no join row,
// no author, no ledger row -- and reports counts, a sample per phase and every
// planned change in the op result.

const (
	authorIDRepairDefaultSample = 25
	authorIDRepairPageSize      = 5000

	authorIDRepairReasonNext    = "next_author_by_position"
	authorIDRepairReasonUnknown = "unknown_author_fallback"
)

// authorIDRepairParams are the op's JSON parameters.
type authorIDRepairParams struct {
	// DryRun defaults to TRUE when absent. dryRun (camelCase) is accepted as an
	// alias; sending both with different values is an error, never a guess.
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
	// SampleLimit bounds the per-phase sample in the summary (the full change
	// list is always in the result). Default 25.
	SampleLimit int `json:"sample_limit,omitempty"`
	// SkipDangling / SkipDuplicates run one phase alone.
	SkipDangling   bool `json:"skip_dangling,omitempty"`
	SkipDuplicates bool `json:"skip_duplicates,omitempty"`
}

// authorIDScalarChange is one phase-(a) repoint, planned or applied.
type authorIDScalarChange struct {
	BookID      string `json:"book_id"`
	OldAuthorID int    `json:"old_author_id"`
	// NewAuthorID is 0 only in a dry run whose Unknown Author row does not
	// exist yet; WouldCreateUnknown says so explicitly.
	NewAuthorID        int    `json:"new_author_id"`
	Reason             string `json:"reason"`
	WouldCreateUnknown bool   `json:"would_create_unknown_author,omitempty"`
	Applied            bool   `json:"applied"`
	Error              string `json:"error,omitempty"`
}

// authorIDMergeChange is one phase-(b) duplicate row's plan or outcome.
type authorIDMergeChange struct {
	NormalizedName string   `json:"normalized_name"`
	DuplicateID    int      `json:"duplicate_id"`
	DuplicateName  string   `json:"duplicate_name"`
	CanonicalID    int      `json:"canonical_id"`
	JoinRewrites   []string `json:"join_rewrites"`
	ScalarRepoints []string `json:"scalar_repoints"`
	Outcome        string   `json:"outcome"`
	// StillCrediting lists the books a re-read still found, when the delete
	// was refused.
	StillCrediting []string `json:"still_crediting,omitempty"`
	Error          string   `json:"error,omitempty"`
}

// Phase-(b) per-duplicate outcomes.
const (
	authorIDMergeWouldMerge = "would_merge_and_delete"
	authorIDMergeMerged     = "merged_and_deleted"
	authorIDMergeHeld       = "held_still_referenced"
	authorIDMergeFailed     = "failed"
)

// Phase-(b) per-cluster outcomes for clusters that were not merged.
const (
	authorIDClusterIndexUnresolved = "name_index_unresolved"
	authorIDClusterIndexOutside    = "name_index_resolves_outside_cluster"
)

type authorIDRepairResult struct {
	DryRun bool `json:"dry_run"`

	BooksScanned        int                    `json:"books_scanned"`
	NilScalarBooks      int                    `json:"nil_scalar_books_left_alone"`
	DanglingBooks       int                    `json:"dangling_scalar_books"`
	DanglingAuthorIDs   int                    `json:"distinct_dangling_author_ids"`
	DanglingOutcomes    map[string]int         `json:"dangling_outcomes"`
	UnknownAuthorID     int                    `json:"unknown_author_id"`
	DanglingChanges     []authorIDScalarChange `json:"dangling_changes"`
	DuplicateClusters   int                    `json:"duplicate_clusters"`
	DuplicateRows       int                    `json:"duplicate_rows"`
	ClusterSkips        map[string]int         `json:"cluster_skips"`
	DuplicateOutcomes   map[string]int         `json:"duplicate_outcomes"`
	DuplicateChanges    []authorIDMergeChange  `json:"duplicate_changes"`
	UndoLedgerRows      int                    `json:"undo_ledger_rows"`
	PhaseDanglingSkip   bool                   `json:"phase_dangling_skipped,omitempty"`
	PhaseDuplicatesSkip bool                   `json:"phase_duplicates_skipped,omitempty"`
}

func (p *Plugin) authorIDRepairDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.author-id-repair",
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Repair dangling and duplicate author ids",
		Description: "Phase (a): books whose author_id names a deleted author are repointed to their " +
			"next author by credit position, or to the 'Unknown Author' row the name index resolves " +
			"(never cleared). Phase (b): author rows sharing a normalized name are merged onto the id " +
			"the name index resolves; each duplicate is deleted only after a re-read finds zero credits " +
			"in both memdb and Pebble. Defaults to dry_run=true; pass dry_run=false to write. The op " +
			"result lists every planned or applied change.",
		// Idempotent: after an apply there is nothing dangling and one row per
		// cluster, so a restart re-plans from current state.
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.author-id-repair",
		Writes:          []sdk.Resource{sdk.ResBooks, sdk.ResAuthors},
		Reads:           []sdk.Resource{sdk.ResBooks, sdk.ResAuthors},
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runAuthorIDRepair,
	}
}

func (p *Plugin) runAuthorIDRepair(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	var params authorIDRepairParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("maintenance.author-id-repair: decode params: %w", err)
		}
	}
	if params.DryRun != nil && params.DryRunCamel != nil && *params.DryRun != *params.DryRunCamel {
		return fmt.Errorf("maintenance.author-id-repair: dry_run=%v and dryRun=%v disagree; send one", *params.DryRun, *params.DryRunCamel)
	}
	result, err := p.authorIDRepair(ctx, params, reporter)
	if result != nil {
		if serr := registry.ReporterSetResult(reporter, result); serr != nil {
			reporter.Logger().Debug("author-id-repair: result not persisted", "err", serr)
		}
	}
	return err
}

func (p *Plugin) authorIDRepair(ctx context.Context, params authorIDRepairParams, reporter sdk.Reporter) (*authorIDRepairResult, error) {
	store := p.deps.OpsStore()
	if store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	dryRun := true
	if params.DryRun != nil {
		dryRun = *params.DryRun
	} else if params.DryRunCamel != nil {
		dryRun = *params.DryRunCamel
	}
	sample := params.SampleLimit
	if sample <= 0 {
		sample = authorIDRepairDefaultSample
	}
	log := reporter.Logger()

	res := &authorIDRepairResult{
		DryRun:              dryRun,
		DanglingOutcomes:    map[string]int{},
		ClusterSkips:        map[string]int{},
		DuplicateOutcomes:   map[string]int{},
		DanglingChanges:     []authorIDScalarChange{},
		DuplicateChanges:    []authorIDMergeChange{},
		PhaseDanglingSkip:   params.SkipDangling,
		PhaseDuplicatesSkip: params.SkipDuplicates,
	}

	// Stand-down for the write phase only, as author-duplicate-merge does: a
	// running scan rewrites book rows underneath an apply. A dry run parks nothing.
	var standDownHolder string
	var standDownHeld bool
	if !dryRun {
		holderID, held, release, sdErr := acquireScanStandDownForApply(ctx, p.deps, reporter, "author-id-repair apply")
		if sdErr != nil {
			return res, fmt.Errorf("author-id-repair: acquire scan stand-down: %w", sdErr)
		}
		defer release()
		standDownHolder, standDownHeld = holderID, held
	}
	lost := func() bool { return scanStandDownLostForApply(p.deps, standDownHolder, standDownHeld) }

	opID := registry.ReporterOpID(reporter)
	wrote := false

	if !params.SkipDangling {
		w, err := p.authorIDRepairDangling(ctx, store, reporter, dryRun, opID, lost, res)
		wrote = wrote || w
		if err != nil {
			return res, err
		}
	}
	if !params.SkipDuplicates {
		w, err := p.authorIDRepairDuplicates(ctx, store, reporter, dryRun, opID, lost, res)
		wrote = wrote || w
		if err != nil {
			return res, err
		}
	}

	if wrote {
		p.deps.InvalidateAuthorsCache()
		p.deps.InvalidateDedupCache()
	}

	summary := fmt.Sprintf(
		"author-id-repair complete (dry_run=%v): books_scanned=%d, nil_scalar_left_alone=%d, "+
			"dangling_books=%d (distinct ids %d) outcomes=%v; duplicate_clusters=%d rows=%d "+
			"cluster_skips=%v outcomes=%v; undo_rows=%d",
		dryRun, res.BooksScanned, res.NilScalarBooks, res.DanglingBooks, res.DanglingAuthorIDs,
		res.DanglingOutcomes, res.DuplicateClusters, res.DuplicateRows, res.ClusterSkips,
		res.DuplicateOutcomes, res.UndoLedgerRows)
	log.Info("author-id-repair: done", "dry_run", dryRun,
		"dangling_sample", firstN(res.DanglingChanges, sample),
		"duplicate_sample", firstN(res.DuplicateChanges, sample))
	_ = reporter.UpdateProgress(1, 1, summary)
	return res, nil
}

func firstN[T any](s []T, n int) []T {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// authorIDRepairDangling is phase (a). It reports whether it wrote anything.
//
// Parallel via registry.RunItems, partitioned BY BOOK: each item is one book
// and each book appears once, and the only row an item writes is its own book
// row, so no two workers can touch the same row.
func (p *Plugin) authorIDRepairDangling(
	ctx context.Context,
	store OpsStore,
	reporter sdk.Reporter,
	dryRun bool,
	opID string,
	lost func() bool,
	res *authorIDRepairResult,
) (bool, error) {
	log := reporter.Logger()

	authors, err := store.GetAllAuthors()
	if err != nil {
		return false, fmt.Errorf("author-id-repair: list authors: %w", err)
	}
	live := make(map[int]bool, len(authors))
	for _, a := range authors {
		live[a.ID] = true
	}

	// Candidate discovery reads the fail-closed complete list; every candidate is
	// then re-verified against Pebble before anything is written, so a list that
	// is stale cannot cause a wrong write -- only a missed one, which the next
	// run finds.
	var candidates []database.BookCore
	for offset := 0; ; offset += authorIDRepairPageSize {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		page, pErr := store.GetAllBooksCoreComplete(authorIDRepairPageSize, offset)
		if pErr != nil {
			return false, fmt.Errorf("author-id-repair: list books at offset %d: %w", offset, pErr)
		}
		for _, b := range page {
			res.BooksScanned++
			if b.AuthorID == nil {
				res.NilScalarBooks++
				continue
			}
			if !live[*b.AuthorID] {
				candidates = append(candidates, b)
			}
		}
		_ = reporter.UpdateProgress(res.BooksScanned, 0,
			fmt.Sprintf("Phase (a): scanned %d books, %d dangling candidates", res.BooksScanned, len(candidates)))
		if len(page) < authorIDRepairPageSize {
			break
		}
	}
	res.DanglingBooks = len(candidates)
	ids := map[int]struct{}{}
	for _, c := range candidates {
		ids[*c.AuthorID] = struct{}{}
	}
	res.DanglingAuthorIDs = len(ids)
	if len(candidates) == 0 {
		return false, nil
	}

	// The placeholder is resolved ONCE, through the name index. A dry run never
	// creates it; it reports that the apply would.
	unknownID := 0
	if u, uErr := store.GetAuthorByName(database.UnknownAuthorName); uErr != nil {
		return false, fmt.Errorf("author-id-repair: resolve %q: %w", database.UnknownAuthorName, uErr)
	} else if u != nil {
		unknownID = u.ID
	}
	resolveUnknown := func() (int, error) {
		if unknownID != 0 || dryRun {
			return unknownID, nil
		}
		u, cErr := store.CreateAuthor(database.UnknownAuthorName)
		if cErr != nil || u == nil || u.ID <= 0 {
			return 0, fmt.Errorf("create %q: %v", database.UnknownAuthorName, cErr)
		}
		unknownID = u.ID
		return unknownID, nil
	}
	var unknownMu sync.Mutex
	resolveLive := func(id int) (int, bool) {
		a, gErr := store.GetAuthorByID(id)
		if gErr != nil || a == nil || a.ID <= 0 {
			return 0, false
		}
		return a.ID, true
	}

	var mu sync.Mutex
	done := 0
	anyWrite := false
	record := func(ch authorIDScalarChange, outcome string) {
		mu.Lock()
		defer mu.Unlock()
		done++
		res.DanglingOutcomes[outcome]++
		if ch.BookID != "" {
			res.DanglingChanges = append(res.DanglingChanges, ch)
		}
		if ch.Applied {
			anyWrite = true
		}
	}

	runErr := registry.RunItems(ctx, reporter, candidates, func(ctx context.Context, c database.BookCore) error {
		old := *c.AuthorID
		// Pebble re-read: the row decides, not the projection.
		full, gErr := store.GetBookByID(c.ID)
		if gErr != nil {
			record(authorIDScalarChange{BookID: c.ID, OldAuthorID: old, Error: gErr.Error()}, "failed_read")
			return nil
		}
		if full == nil || full.AuthorID == nil || *full.AuthorID != old {
			record(authorIDScalarChange{}, "changed_since_scan")
			return nil
		}
		if _, ok := resolveLive(old); ok {
			record(authorIDScalarChange{}, "resolves_on_reread")
			return nil
		}
		joins, jErr := store.GetBookAuthors(c.ID)
		if jErr != nil {
			record(authorIDScalarChange{BookID: c.ID, OldAuthorID: old, Error: jErr.Error()}, "failed_read")
			return nil
		}
		ch := authorIDScalarChange{BookID: c.ID, OldAuthorID: old, Reason: authorIDRepairReasonNext}
		next, ok := database.NextPrimaryAuthorID(joins, old, resolveLive)
		if !ok {
			ch.Reason = authorIDRepairReasonUnknown
			unknownMu.Lock()
			uid, uErr := resolveUnknown()
			unknownMu.Unlock()
			if uErr != nil {
				ch.Error = uErr.Error()
				record(ch, "failed")
				return nil
			}
			if uid == 0 {
				ch.WouldCreateUnknown = true
			}
			next = uid
		}
		ch.NewAuthorID = next
		if dryRun {
			record(ch, "would_repoint")
			return nil
		}
		if lost() {
			return fmt.Errorf("author-id-repair: scan stand-down lease lost; refusing to keep writing")
		}
		target, tErr := store.GetAuthorByID(next)
		if tErr != nil || target == nil {
			ch.Error = fmt.Sprintf("successor %d did not resolve: %v", next, tErr)
			record(ch, "failed")
			return nil
		}
		full.AuthorID = &target.ID
		full.Author = target
		if _, uErr := store.UpdateBook(c.ID, full); uErr != nil {
			ch.Error = uErr.Error()
			record(ch, "failed")
			return nil
		}
		ch.Applied = true
		if lErr := store.CreateOperationChange(&database.OperationChange{
			ID:          ulid.Make().String(),
			OperationID: opID,
			BookID:      c.ID,
			ChangeType:  "metadata_update",
			FieldName:   "author_id",
			OldValue:    strconv.Itoa(old),
			NewValue:    strconv.Itoa(target.ID),
		}); lErr != nil {
			log.Warn("author-id-repair: undo-ledger write failed for a repointed book",
				"book_id", c.ID, "from_id", old, "into_id", target.ID, "err", lErr)
		} else {
			mu.Lock()
			res.UndoLedgerRows++
			mu.Unlock()
		}
		record(ch, "repointed")
		return nil
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(),
		// Label runs inside each worker goroutine, so it reads done under mu.
		Label: func(i, total int) string {
			mu.Lock()
			d := done
			mu.Unlock()
			return fmt.Sprintf("Phase (a): dangling author ids %d/%d (processed %d)", i+1, total, d)
		},
	})
	sort.Slice(res.DanglingChanges, func(i, j int) bool { return res.DanglingChanges[i].BookID < res.DanglingChanges[j].BookID })
	res.UnknownAuthorID = unknownID
	if runErr != nil {
		return anyWrite, runErr
	}
	return anyWrite, nil
}

// authorIDRepairDuplicates is phase (b). It reports whether it wrote anything.
//
// Deliberately SEQUENTIAL. The unit mutated is a book's author slice
// (GetBookAuthors -> SetBookAuthors), and a book co-credited to two duplicated
// authors belongs to two clusters, so partitioning by author id does not make
// the work disjoint and a pool would lose one worker's update to the other --
// the reasoning author_duplicate_merge.go and author_conjunction_repair.go
// already record. The population is small (a few dozen clusters on prod).
func (p *Plugin) authorIDRepairDuplicates(
	ctx context.Context,
	store OpsStore,
	reporter sdk.Reporter,
	dryRun bool,
	opID string,
	lost func() bool,
	res *authorIDRepairResult,
) (bool, error) {
	log := reporter.Logger()
	authors, err := store.GetAllAuthors()
	if err != nil {
		return false, fmt.Errorf("author-id-repair: list authors: %w", err)
	}
	groups := map[string][]database.Author{}
	for _, a := range authors {
		norm := util.NormalizeAuthor(a.Name)
		if norm == "" {
			continue
		}
		groups[norm] = append(groups[norm], a)
	}
	var names []string
	for norm, g := range groups {
		if len(g) > 1 {
			names = append(names, norm)
		}
	}
	sort.Strings(names)
	res.DuplicateClusters = len(names)
	for _, n := range names {
		res.DuplicateRows += len(groups[n]) - 1
	}

	wrote := false
	step := 0
	for _, norm := range names {
		if ctx.Err() != nil {
			return wrote, ctx.Err()
		}
		group := groups[norm]
		sort.Slice(group, func(i, j int) bool { return group[i].ID < group[j].ID })

		canonical, skip := authorIDRepairCanonical(store, group)
		if skip != "" {
			res.ClusterSkips[skip]++
			log.Warn("author-id-repair: cluster skipped", "normalized", norm, "reason", skip, "ids", authorIDs(group))
			continue
		}

		for _, dup := range group {
			if dup.ID == canonical.ID {
				continue
			}
			if ctx.Err() != nil {
				return wrote, ctx.Err()
			}
			step++
			_ = reporter.UpdateProgress(step, res.DuplicateRows,
				fmt.Sprintf("Phase (b): duplicate %d/%d (%q -> %d)", step, res.DuplicateRows, dup.Name, canonical.ID))
			if !dryRun && lost() {
				return wrote, fmt.Errorf("author-id-repair: scan stand-down lease lost; refusing to keep writing")
			}
			ch, w, ledger := p.authorIDMergeOne(store, dryRun, opID, norm, dup, *canonical, log)
			wrote = wrote || w
			res.UndoLedgerRows += ledger
			res.DuplicateOutcomes[ch.Outcome]++
			res.DuplicateChanges = append(res.DuplicateChanges, ch)
		}
	}
	return wrote, nil
}

// authorIDRepairCanonical picks the cluster's canonical row: the id the
// author:name index resolves for the cluster's name. Never "most books" or
// "lowest id" -- only the indexed row is reachable by name, so any other choice
// leaves the survivor unreachable. A cluster whose index resolves to nothing,
// or to a row outside the cluster, is skipped rather than guessed at.
func authorIDRepairCanonical(store OpsStore, group []database.Author) (*database.Author, string) {
	resolved, err := store.GetAuthorByName(group[0].Name)
	if err != nil || resolved == nil {
		return nil, authorIDClusterIndexUnresolved
	}
	for i := range group {
		if group[i].ID == resolved.ID {
			return &group[i], ""
		}
	}
	return nil, authorIDClusterIndexOutside
}

func authorIDs(g []database.Author) []int {
	out := make([]int, len(g))
	for i, a := range g {
		out[i] = a.ID
	}
	return out
}

// rewriteJoinsOnto replaces from with into in joins, in place (position kept),
// drops later duplicates of into, and renumbers positions densely in order.
// It reports whether anything changed.
func rewriteJoinsOnto(joins []database.BookAuthor, from, into int) ([]database.BookAuthor, bool) {
	sorted := make([]database.BookAuthor, len(joins))
	copy(sorted, joins)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Position < sorted[j].Position })
	out := make([]database.BookAuthor, 0, len(sorted))
	changed := false
	intoSeen := false
	for _, ba := range sorted {
		if ba.AuthorID == from {
			ba.AuthorID = into
			changed = true
		}
		if ba.AuthorID == into {
			if intoSeen {
				changed = true
				continue
			}
			intoSeen = true
		}
		out = append(out, ba)
	}
	for i := range out {
		if out[i].Position != i {
			out[i].Position = i
		}
	}
	return out, changed
}

// authorIDMergeOne moves one duplicate's credits onto canonical and deletes it
// when -- and only when -- a re-read through BOTH stores finds nothing left.
// Returns the change record, whether it wrote, and undo-ledger rows written.
func (p *Plugin) authorIDMergeOne(
	store OpsStore,
	dryRun bool,
	opID, norm string,
	dup, canonical database.Author,
	log logWarner,
) (authorIDMergeChange, bool, int) {
	ch := authorIDMergeChange{
		NormalizedName: norm, DuplicateID: dup.ID, DuplicateName: dup.Name, CanonicalID: canonical.ID,
		JoinRewrites: []string{}, ScalarRepoints: []string{},
	}

	// The book set is the union of the memdb-or-Pebble relink view and the
	// Pebble-only durable scan, so a credit only one store can see still moves.
	bookSet := map[string]struct{}{}
	relink, rErr := store.GetBooksByAuthorIDForRelinkCore(dup.ID)
	if rErr != nil {
		ch.Outcome, ch.Error = authorIDMergeFailed, rErr.Error()
		return ch, false, 0
	}
	for _, b := range relink {
		bookSet[b.ID] = struct{}{}
	}
	durable, dErr := store.GetBookIDsCreditingAuthorDurable(dup.ID)
	if dErr != nil {
		ch.Outcome, ch.Error = authorIDMergeFailed, dErr.Error()
		return ch, false, 0
	}
	for _, id := range durable {
		bookSet[id] = struct{}{}
	}
	bookIDs := make([]string, 0, len(bookSet))
	for id := range bookSet {
		bookIDs = append(bookIDs, id)
	}
	sort.Strings(bookIDs)

	wrote := false
	var moved []database.BookCore
	for _, bookID := range bookIDs {
		joins, jErr := store.GetBookAuthors(bookID)
		if jErr != nil {
			ch.Error = jErr.Error()
			continue
		}
		newJoins, joinChanged := rewriteJoinsOnto(joins, dup.ID, canonical.ID)
		full, bErr := store.GetBookByID(bookID)
		if bErr != nil {
			ch.Error = bErr.Error()
			continue
		}
		scalarHit := full != nil && full.AuthorID != nil && *full.AuthorID == dup.ID
		if joinChanged {
			ch.JoinRewrites = append(ch.JoinRewrites, bookID)
		}
		if scalarHit {
			ch.ScalarRepoints = append(ch.ScalarRepoints, bookID)
		}
		if dryRun || (!joinChanged && !scalarHit) {
			continue
		}
		bookMoved := false
		if joinChanged {
			if sErr := store.SetBookAuthors(bookID, newJoins); sErr != nil {
				ch.Error = sErr.Error()
				log.Warn("author-id-repair: join rewrite failed", "book_id", bookID, "err", sErr)
			} else {
				wrote, bookMoved = true, true
			}
		}
		if scalarHit {
			target := canonical
			full.AuthorID = &target.ID
			full.Author = &target
			if _, uErr := store.UpdateBook(bookID, full); uErr != nil {
				ch.Error = uErr.Error()
				log.Warn("author-id-repair: scalar repoint failed", "book_id", bookID, "err", uErr)
			} else {
				wrote, bookMoved = true, true
			}
		}
		if bookMoved {
			moved = append(moved, database.BookCore{ID: bookID})
		}
	}

	if dryRun {
		ch.Outcome = authorIDMergeWouldMerge
		return ch, false, 0
	}

	// 🔴 DELETE ONLY WHEN EMPTY, re-read through BOTH stores after the moves.
	// Either one still seeing a credit -- a failed write above, a book written
	// concurrently, memdb and Pebble disagreeing -- holds the row back.
	deleted := false
	var still []string
	if vErr := database.VerifyAuthorUnlinked(store, dup.ID); vErr != nil {
		still = append(still, "relink-view: "+vErr.Error())
	}
	remaining, drErr := store.GetBookIDsCreditingAuthorDurable(dup.ID)
	if drErr != nil {
		still = append(still, "durable-scan: "+drErr.Error())
	}
	still = append(still, remaining...)
	if len(still) > 0 {
		ch.Outcome, ch.StillCrediting = authorIDMergeHeld, still
		log.Warn("author-id-repair: duplicate still credited after moves, NOT deleted",
			"author_id", dup.ID, "name", dup.Name, "canonical_id", canonical.ID, "still", still)
	} else if delErr := store.DeleteAuthor(dup.ID); delErr != nil {
		ch.Outcome, ch.Error = authorIDMergeFailed, delErr.Error()
	} else {
		deleted, wrote = true, true
		ch.Outcome = authorIDMergeMerged
	}
	// Journal every book that moved, on every path, plus the author delete when
	// it happened. Same ledger shape as author-duplicate-merge.
	ledger := p.journalAuthorMerge(store, opID, dup, canonical, moved, len(moved), deleted, log)
	return ch, wrote, ledger
}
