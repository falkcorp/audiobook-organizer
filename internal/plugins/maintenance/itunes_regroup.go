// file: internal/plugins/maintenance/itunes_regroup.go
// version: 1.13.0
// guid: 5e6f7a8b-9c0d-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-10-01

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// CONS-FRAG-HEAL: the iTunes importer historically grouped tracks with an
// artist+album key (PR #1528 fixed it forward). Existing books accreted under the
// old key are both FRAGMENTED (anthologies split per story) and OVER-MERGED
// (empty-album tracks collapsed by artist). This op re-groups them IN PLACE to the
// books the fixed importer would produce — preserving enrichment / version groups
// / manual edits — instead of delete+reimport (which the canary proved tombstones
// PIDs and blocks recreation; see .claude/notes/itunes-heal-canary-findings.md).
//
// It is computed as a frozen, deterministic, exclusive-claim plan (one existing
// book targets at most one group) so dry-run == apply and over-merges actually
// split. Groups whose moves would change a version group's membership or the
// files of a version-group member other than a non-primary iTunes edition
// receiving its own album's tracks are skipped (itunesservice.entanglement
// holds the exact rule).
//
// What the apply writes: book rows (CreateBook for a fresh target, a Title-only
// ModifyBook, DeleteBook for books left with no files and no ext-ids),
// book_file rows (MoveBookFilesToBook* re-points the row's book_id; the row's
// file_path is unchanged), and itunes external-id mappings. It never touches a
// file on disk, never writes tags, and never reads or writes the ITL or iTunes
// XML beyond parsing the XML read-only in Phase 1.

type itunesRegroupParams struct {
	// DryRun defaults to TRUE when omitted (opmode.ResolveDryRun): a request
	// that states no mode is a preview. dry_run is accepted as an alias, and
	// sending both with different values is refused rather than guessed.
	DryRun      *bool  `json:"dryRun,omitempty"`
	DryRunSnake *bool  `json:"dry_run,omitempty"`
	XMLPath     string `json:"xmlPath"`
}

func (p *Plugin) itunesRegroupDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID: "itunes.regroup",
		// Renamed 2026-09-25 (naming audit class 8): every iTunes op lives in itunes.*, whichever Go package implements it.
		FormerIDs:       []string{"maintenance.itunes-regroup"},
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Re-group fragmented/over-merged iTunes books in place",
		Description:     "Re-groups existing iTunes-imported books to match the FIXED importer grouping (CONS-FRAG): consolidates fragmented anthologies/chapter-parts and splits over-merged books, in place via per-PID external-id + BookFile reassignment (database rows only; no file on disk, tag, or ITL is touched), preserving enrichment and version groups. A group is skipped when its moves would take files out of a version-group member or add them to a group's primary or an ambiguous group; a non-primary iTunes edition may receive its own album's ungrouped fragments. Default dry-run reports the plan and how many groups the 2026-10-01 rule change unblocks; set dryRun=false to apply.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "itunes.regroup",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         120 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runITunesRegroup,
	}
}

func (p *Plugin) runITunesRegroup(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params itunesRegroupParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	dryRun, err := opmode.ResolveDryRun("itunes.regroup", params.DryRunSnake, params.DryRun)
	if err != nil {
		return err
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	xmlPath := strings.TrimSpace(params.XMLPath)
	if xmlPath == "" {
		xmlPath = strings.TrimSpace(config.AppConfig.ITunes.LibraryReadPath)
	}
	if xmlPath == "" {
		return fmt.Errorf("no iTunes XML path: set params.xmlPath or itunes.library_read_path")
	}
	if dryRun {
		_ = reporter.Log(slog.LevelInfo, "DRY RUN — no changes will be written")
	}

	_ = reporter.UpdateProgress(0, 4, "Phase 1/4: parsing iTunes library…")
	lib, err := itunes.ParseLibrary(xmlPath)
	if err != nil {
		return fmt.Errorf("parse library %q: %w", xmlPath, err)
	}
	groups := itunesservice.GroupLibraryForHeal(lib)

	_ = reporter.UpdateProgress(1, 4, fmt.Sprintf("Phase 2/4: snapshotting DB for %d target groups…", len(groups)))
	snap, err := p.buildRegroupSnapshot(ctx, store, reporter)
	if err != nil {
		return err
	}

	_ = reporter.UpdateProgress(2, 4, "Phase 3/4: planning…")
	plan := itunesservice.PlanRegroup(groups, snap)

	summary := fmt.Sprintf(
		"groups=%d already-correct=%d consolidate=%d entangled-skipped=%d fresh-books=%d delete-empty=%d | PIDs resolved=%d unresolved=%d | %s",
		plan.TotalGroups, plan.AlreadyCorrect, plan.Consolidated, plan.EntangledSkipped,
		plan.FreshBooks, len(plan.DeleteBooks), plan.PIDsResolved, plan.PIDsUnresolved,
		regroupRuleDelta(plan))
	_ = reporter.Log(slog.LevelInfo, "PLAN: "+summary)
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"RULE CHANGE (2026-10-01): %s | legacy-rule-skipped=%d | skips by reason: %s | unblocked examples: %s",
		regroupRuleDelta(plan), plan.LegacyEntangledSkipped, regroupSkipReasons(plan),
		strings.Join(plan.UnblockedExamples, "; ")))
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"COMPLETENESS: complete-groups=%d partial-groups=%d (missing some tracks) single-file-in-multitrack-album=%d",
		plan.CompleteGroups, plan.PartialGroups, plan.SingleFileChapterBooks))
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"SINGLE-FILE BY DURATION: <15min(true chapter/clip)=%d  15-90min(ambiguous)=%d  >=90min(COMPLETE book, false alarm)=%d | short examples: %s",
		plan.SFCShort, plan.SFCMid, plan.SFCLong, strings.Join(plan.SFCExamples, "; ")))

	if dryRun {
		examples := regroupExamples(plan, 8)
		_ = reporter.Log(slog.LevelInfo, "DRY RUN examples: "+strings.Join(examples, " | "))
		_ = reporter.UpdateProgress(4, 4, "DRY RUN — "+summary)
		return nil
	}

	_ = reporter.UpdateProgress(3, 4, "Phase 4/4: applying plan…")
	if err := p.applyRegroupPlan(ctx, store, plan, reporter); err != nil {
		return err
	}
	_ = reporter.UpdateProgress(4, 4, "APPLIED — "+summary)
	return nil
}

// buildRegroupSnapshot reads the immutable DB state the planner reasons over via
// TWO bulk in-memory scans — all books once, all book files once — instead of
// tens of thousands of per-PID / per-book point queries (which made the dry-run
// take >10min on a 65K/308K library). The file scan yields PID→location directly
// from BookFile.ITunesPersistentID, so no per-PID lookups are needed. No mutation.
func (p *Plugin) buildRegroupSnapshot(ctx context.Context, store regroupSnapshotReader, reporter sdk.Reporter) (itunesservice.Snapshot, error) {
	snap := itunesservice.Snapshot{
		PIDLoc: make(map[string]itunesservice.PIDLoc),
		Books:  make(map[string]itunesservice.BookMeta),
	}

	// Pass 1: all books → per-book fields + version-group membership.
	type partialMeta struct {
		title     string
		flag      *bool
		enrich    int
		duration  int
		createdAt int64
		vgID      string
	}
	meta := make(map[string]partialMeta)
	live := make(map[string]bool)                   // book id -> scanned and not soft-deleted
	vgMembers := make(map[string][]regroupVGMember) // version-group id -> members, scan order
	vgLegacyNonPrimary := make(map[string]bool)     // version-group id -> has a member not explicitly true (legacy rule)
	const page = 1000
	afterID := ""
	scanned := 0
	for {
		if ctx.Err() != nil {
			return snap, ctx.Err()
		}
		books, err := store.GetAllBooksFullFrom(afterID, page)
		if err != nil {
			return snap, fmt.Errorf("GetAllBooksFullFrom afterID=%q: %w", afterID, err)
		}
		if len(books) == 0 {
			break
		}
		for i := range books {
			b := &books[i]
			vg := ""
			if b.VersionGroupID != nil {
				vg = *b.VersionGroupID
			}
			dur := 0
			if b.Duration != nil {
				dur = *b.Duration
			}
			// An unknown creation time ranks as newest, so it never wins the
			// older-is-better tiebreak over a row whose age is known. (A nil
			// CreatedAt used to panic the whole snapshot.)
			created := int64(math.MaxInt64)
			if b.CreatedAt != nil {
				created = b.CreatedAt.Unix()
			}
			meta[b.ID] = partialMeta{title: b.Title, flag: b.IsPrimaryVersion, enrich: enrichScore(b), duration: dur, createdAt: created, vgID: vg}
			live[b.ID] = !b.IsSoftDeleted()
			if vg != "" {
				vgMembers[vg] = append(vgMembers[vg], regroupVGMember{
					id: b.ID, flag: b.IsPrimaryVersion, softDeleted: b.IsSoftDeleted(), mergedInto: b.MergedIntoBookID,
				})
				if b.IsPrimaryVersion == nil || !*b.IsPrimaryVersion {
					vgLegacyNonPrimary[vg] = true
				}
			}
		}
		scanned += len(books)
		afterID = books[len(books)-1].ID
		_ = reporter.UpdateProgress(1, 4, fmt.Sprintf("Phase 2/4: scanned %d books…", scanned))
		if len(books) < page {
			break
		}
	}

	// Pass 2: all book files → PID→location + per-book file counts.
	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return snap, fmt.Errorf("GetAllBookFilesCore: %w", err)
	}
	fileCount := make(map[string]int, len(meta))
	for i := range files {
		f := &files[i]
		fileCount[f.BookID]++
		if pid := strings.TrimSpace(f.ITunesPersistentID); pid != "" && f.BookID != "" {
			snap.PIDLoc[pid] = itunesservice.PIDLoc{FileID: f.ID, BookID: f.BookID}
		}
	}

	// Every version group's incumbent primary, over ALL its members (a library
	// copy holding no iTunes PID still decides which member is primary).
	alive := func(id string) bool { return live[id] }
	incumbent := make(map[string]string, len(vgMembers))
	for vg, members := range vgMembers {
		incumbent[vg] = regroupIncumbent(members, alive)
	}

	// Assemble book meta (only books that actually exist; planner only reads the
	// ones referenced by resolved PIDs).
	for id, pm := range meta {
		isPrimary := database.EffectiveIsPrimaryVersion(pm.flag)
		if pm.vgID != "" {
			isPrimary = incumbent[pm.vgID] == id
		}
		snap.Books[id] = itunesservice.BookMeta{
			ID:                  id,
			Title:               pm.title,
			IsPrimary:           isPrimary,
			FileCount:           fileCount[id],
			DurationSec:         pm.duration,
			EnrichScore:         pm.enrich,
			CreatedAtUnix:       pm.createdAt,
			VersionGroupID:      pm.vgID,
			GroupHasNoIncumbent: pm.vgID != "" && incumbent[pm.vgID] == "",
			LegacyEntangled:     pm.vgID != "" && vgLegacyNonPrimary[pm.vgID],
		}
	}
	_ = reporter.UpdateProgress(2, 4, fmt.Sprintf("Phase 2/4: snapshot ready (%d books, %d PID locations)", len(snap.Books), len(snap.PIDLoc)))
	return snap, nil
}

// regroupVGMember is the slice of a book row the incumbent rule reads.
type regroupVGMember struct {
	id          string
	flag        *bool
	softDeleted bool
	mergedInto  *string
}

// regroupIncumbent returns the ID of the member that currently acts as its
// version group's primary, or "" when none can be told apart. It is the
// incumbent rule (read-only, never elects):
//
//  1. the first Electable member whose flag is explicitly true;
//  2. otherwise the ONE Electable member whose flag is unset, since every
//     visibility path reads an unset flag as primary
//     (database.EffectiveIsPrimaryVersion); two or more unset members are
//     ambiguous and return "".
//
// Electable (versionprimary.ElectableRow) excludes soft-deleted members and
// merge losers whose survivor is alive. Members are in book-ID scan order, so
// rule 1's "first" is deterministic.
//
// TODO(#3649): replace with versionprimary.Incumbent once PR #3649
// (fix/visibility-hotfix-w-1) merges; this mirrors its rule on the narrow
// member rows the snapshot keeps instead of full database.Book values.
func regroupIncumbent(members []regroupVGMember, alive func(id string) bool) string {
	nilID, nilCount := "", 0
	for _, m := range members {
		if !versionprimary.ElectableRow(m.softDeleted, m.mergedInto, alive) {
			continue
		}
		if m.flag != nil && *m.flag {
			return m.id
		}
		if m.flag == nil {
			nilCount++
			nilID = m.id
		}
	}
	if nilCount == 1 {
		return nilID
	}
	return ""
}

// regroupRuleDelta formats the per-group rule-change delta for the summary.
func regroupRuleDelta(plan itunesservice.RegroupPlan) string {
	return fmt.Sprintf("rule-delta: unblocked=%d (legacy skipped, now consolidate) newly-blocked=%d (legacy consolidated, now skipped)",
		plan.Unblocked, plan.NewlyBlocked)
}

// regroupSkipReasons formats EntangledByReason deterministically.
func regroupSkipReasons(plan itunesservice.RegroupPlan) string {
	if len(plan.EntangledByReason) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(plan.EntangledByReason))
	for k := range plan.EntangledByReason {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, plan.EntangledByReason[k]))
	}
	return strings.Join(parts, " ")
}

// enrichScore counts populated enrichment fields so the planner prefers the
// richest existing book as the survivor.
func enrichScore(b *database.Book) int {
	score := 0
	nonEmpty := func(s *string) bool { return s != nil && strings.TrimSpace(*s) != "" }
	if nonEmpty(b.ISBN13) {
		score++
	}
	if nonEmpty(b.ISBN10) {
		score++
	}
	if nonEmpty(b.ASIN) {
		score++
	}
	if nonEmpty(b.Description) {
		score++
	}
	if nonEmpty(b.Narrator) {
		score++
	}
	if nonEmpty(b.Publisher) {
		score++
	}
	return score
}

// applyRegroupPlan executes the frozen plan: gather each group's files onto its
// target (creating a fresh book when the target was contested), set the canonical
// title, then delete books that end empty — re-asserting no files AND no ext-id
// mappings before each delete (the canary lesson: zero files ≠ zero PID mappings).
func (p *Plugin) applyRegroupPlan(ctx context.Context, store itunesRegroupStore, plan itunesservice.RegroupPlan, reporter sdk.Reporter) error {
	touched := make(map[string]bool)
	var moved, titled, created, deleted, deleteSkipped, errCount int

	for _, a := range plan.Groups {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if a.Entangled || (a.Target == "" && !a.FreshBook) {
			continue // skipped or nothing-in-DB
		}

		target := a.Target
		if a.FreshBook {
			nb, err := store.CreateBook(&database.Book{Title: a.Title})
			if err != nil || nb == nil {
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("create fresh book for %q failed: %v", a.Title, err))
				errCount++
				continue
			}
			target = nb.ID
			created++
		}

		// Move this group's files in ONE batch. Each MoveBookFilesToBook call
		// recomputes both of its books, so the previous per-file loop cost two
		// full re-reads of the target's growing file set per file — an O(N^2)
		// shape on a plan that can carry thousands of moves. The bulk form pays
		// one recompute per distinct book for the whole group.
		movedOK := a.Moves
		if len(a.Moves) > 0 {
			bulk := make([]database.BookFileMove, 0, len(a.Moves))
			for _, m := range a.Moves {
				bulk = append(bulk, database.BookFileMove{FileIDs: []string{m.FileID}, SourceBookID: m.From})
			}
			bulkErr := store.MoveBookFilesToBookBulk(bulk, target)
			if errors.Is(bulkErr, database.ErrBookFileDurabilityUnknown) {
				// Everything moved (visible); only the fsync failed. The
				// per-file retry below would find the rows gone from their
				// sources and count every move as failed.
				_ = reporter.Log(slog.LevelError, fmt.Sprintf("bulk move of %d files -> %s was written but its fsync failed (%v): durability unknown; treating it as applied", len(bulk), target, bulkErr))
				bulkErr = nil
			}
			if err := bulkErr; err != nil {
				// The bulk form is atomic, so NOTHING moved. Fall back to the
				// per-file loop rather than failing the whole group: a plan is
				// frozen ahead of the apply, so a single file that vanished in
				// between must not block every other move in the group. This is
				// the resilience the per-file loop always had; the batch is only
				// the fast path.
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
					"bulk move of %d files -> %s failed (%v); retrying per file", len(bulk), target, err))
				// Fresh slice, NOT movedOK[:0] — movedOK aliases a.Moves here, and
				// truncate-then-append would rewrite the frozen plan's own backing
				// array underneath the caller.
				movedOK = make([]itunesservice.FileMove, 0, len(a.Moves))
				for _, m := range a.Moves {
					if err := store.MoveBookFilesToBook([]string{m.FileID}, m.From, target); err != nil {
						_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("move file %s %s->%s failed: %v", m.FileID, m.From, target, err))
						errCount++
						continue
					}
					movedOK = append(movedOK, m)
				}
			}
		}

		for _, m := range movedOK {
			if err := store.ReassignExternalID("itunes", m.PID, target); err != nil {
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("reassign pid %s->%s failed: %v", m.PID, target, err))
				errCount++
			}
			moved++
		}

		// Set the canonical title (fixes chapter-suffix leaks on survivors too).
		// Write only Title, under the book's write lock (ModifyBook), so a
		// column another writer commits meanwhile is not reverted (audit
		// A1#15); a survivor already carrying the canonical title is skipped.
		retitled := false
		if written, err := store.ModifyBook(target, func(tb *database.Book) error {
			if tb.Title == a.Title {
				return database.ErrSkipBookWrite
			}
			tb.Title = a.Title
			retitled = true
			return nil
		}); err != nil {
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("set title on %s failed: %v", target, err))
			errCount++
		} else if written != nil && retitled {
			titled++
		}
		touched[target] = true
	}

	// Delete projected-empty books, GUARDED: re-assert no files AND no ext-ids.
	for _, id := range plan.DeleteBooks {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		files, ferr := store.GetBookFiles(id)
		exts, eerr := store.GetExternalIDsForBook(id)
		// Fail CLOSED: if either read errored we cannot prove the book is empty,
		// so both slices may be a misleading nil (len 0). Skip the delete rather
		// than risk removing a book that still owns files or iTunes PID ext-ids —
		// the exact canary this guard exists to prevent.
		if ferr != nil || eerr != nil {
			deleteSkipped++
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("skip delete %s: could not verify empty (files err=%v, ext-ids err=%v)", id, ferr, eerr))
			continue
		}
		if len(files) != 0 || len(exts) != 0 {
			deleteSkipped++
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("skip delete %s: %d files, %d ext-ids remain", id, len(files), len(exts)))
			continue
		}
		if err := store.DeleteBook(id); err != nil {
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("delete %s failed: %v", id, err))
			errCount++
			continue
		}
		deleted++
	}

	// Recompute aggregates for every touched target.
	for id := range touched {
		if err := store.RecomputeBookAggregates(id); err != nil {
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("recompute %s failed: %v", id, err))
			errCount++
		}
	}

	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"APPLIED: moved=%d titled=%d fresh=%d deleted=%d delete-skipped=%d errors=%d",
		moved, titled, created, deleted, deleteSkipped, errCount))
	if errCount > 0 {
		return fmt.Errorf("%d errors during itunes-regroup (see op log)", errCount)
	}
	return nil
}

// regroupExamples returns a few human-readable sample actions for the dry-run log.
func regroupExamples(plan itunesservice.RegroupPlan, n int) []string {
	out := make([]string, 0, n)
	for _, a := range plan.Groups {
		if len(out) >= n {
			break
		}
		switch {
		case a.Entangled:
			out = append(out, fmt.Sprintf("SKIP(entangled:%s) %q", a.EntangleReason, a.Title))
		case len(a.Moves) > 0 && a.FreshBook:
			out = append(out, fmt.Sprintf("SPLIT→fresh %q (%d files)", a.Title, len(a.Moves)))
		case len(a.Moves) > 0:
			out = append(out, fmt.Sprintf("MERGE→%s %q (%d files)", a.Target, a.Title, len(a.Moves)))
		}
	}
	return out
}
