// file: internal/plugins/maintenance/itunes_regroup.go
// version: 1.3.0
// guid: 5e6f7a8b-9c0d-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-06-20

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
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
// split. Version-entangled groups are skipped in v1 (conservative).

type itunesRegroupParams struct {
	DryRun  bool   `json:"dryRun"`
	XMLPath string `json:"xmlPath"`
}

func (p *Plugin) itunesRegroupDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.itunes-regroup",
		Plugin:          "maintenance",
		DisplayName:     "Re-group fragmented/over-merged iTunes books in place",
		Description:     "Re-groups existing iTunes-imported books to match the FIXED importer grouping (CONS-FRAG): consolidates fragmented anthologies/chapter-parts and splits over-merged books, in place via per-PID external-id + BookFile reassignment, preserving enrichment and version groups. Version-entangled groups are skipped. Default dry-run reports the plan; set dryRun=false to apply.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.itunes-regroup",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         120 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runITunesRegroup,
	}
}

func (p *Plugin) runITunesRegroup(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params := itunesRegroupParams{DryRun: true} // safe default
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	store := p.deps.Store()
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
	if params.DryRun {
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
		"groups=%d already-correct=%d consolidate=%d entangled-skipped=%d fresh-books=%d delete-empty=%d | PIDs resolved=%d unresolved=%d",
		plan.TotalGroups, plan.AlreadyCorrect, plan.Consolidated, plan.EntangledSkipped,
		plan.FreshBooks, len(plan.DeleteBooks), plan.PIDsResolved, plan.PIDsUnresolved)
	_ = reporter.Log(slog.LevelInfo, "PLAN: "+summary)
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"COMPLETENESS: complete-groups=%d partial-groups=%d (missing some tracks) single-file-in-multitrack-album=%d",
		plan.CompleteGroups, plan.PartialGroups, plan.SingleFileChapterBooks))
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"SINGLE-FILE BY DURATION: <15min(true chapter/clip)=%d  15-90min(ambiguous)=%d  >=90min(COMPLETE book, false alarm)=%d | short examples: %s",
		plan.SFCShort, plan.SFCMid, plan.SFCLong, strings.Join(plan.SFCExamples, "; ")))

	if params.DryRun {
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
func (p *Plugin) buildRegroupSnapshot(ctx context.Context, store database.Store, reporter sdk.Reporter) (itunesservice.Snapshot, error) {
	snap := itunesservice.Snapshot{
		PIDLoc: make(map[string]itunesservice.PIDLoc),
		Books:  make(map[string]itunesservice.BookMeta),
	}

	// Pass 1: all books → per-book fields + version-group non-primary membership.
	type partialMeta struct {
		title     string
		isPrimary bool
		enrich    int
		duration  int
		createdAt int64
		vgID      string
	}
	meta := make(map[string]partialMeta)
	vgNonPrimary := make(map[string]bool) // version-group id -> has a non-primary member
	const page = 1000
	off := 0
	for {
		if ctx.Err() != nil {
			return snap, ctx.Err()
		}
		books, err := store.GetAllBooks(page, off)
		if err != nil {
			return snap, fmt.Errorf("GetAllBooks offset=%d: %w", off, err)
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
			isPrimary := b.IsPrimaryVersion != nil && *b.IsPrimaryVersion
			dur := 0
			if b.Duration != nil {
				dur = *b.Duration
			}
			meta[b.ID] = partialMeta{title: b.Title, isPrimary: isPrimary, enrich: enrichScore(b), duration: dur, createdAt: b.CreatedAt.Unix(), vgID: vg}
			if vg != "" && !isPrimary {
				vgNonPrimary[vg] = true
			}
		}
		off += len(books)
		_ = reporter.UpdateProgress(1, 4, fmt.Sprintf("Phase 2/4: scanned %d books…", off))
		if len(books) < page {
			break
		}
	}

	// Pass 2: all book files → PID→location + per-book file counts.
	files, err := store.GetAllBookFiles()
	if err != nil {
		return snap, fmt.Errorf("GetAllBookFiles: %w", err)
	}
	fileCount := make(map[string]int, len(meta))
	for i := range files {
		f := &files[i]
		fileCount[f.BookID]++
		if pid := strings.TrimSpace(f.ITunesPersistentID); pid != "" && f.BookID != "" {
			snap.PIDLoc[pid] = itunesservice.PIDLoc{FileID: f.ID, BookID: f.BookID}
		}
	}

	// Assemble book meta (only books that actually exist; planner only reads the
	// ones referenced by resolved PIDs).
	for id, pm := range meta {
		snap.Books[id] = itunesservice.BookMeta{
			ID:                   id,
			Title:                pm.title,
			IsPrimary:            pm.isPrimary,
			FileCount:            fileCount[id],
			DurationSec:          pm.duration,
			EnrichScore:          pm.enrich,
			CreatedAtUnix:        pm.createdAt,
			VersionGroupID:       pm.vgID,
			HasNonPrimaryMembers: pm.vgID != "" && vgNonPrimary[pm.vgID],
		}
	}
	_ = reporter.UpdateProgress(2, 4, fmt.Sprintf("Phase 2/4: snapshot ready (%d books, %d PID locations)", len(snap.Books), len(snap.PIDLoc)))
	return snap, nil
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
func (p *Plugin) applyRegroupPlan(ctx context.Context, store database.Store, plan itunesservice.RegroupPlan, reporter sdk.Reporter) error {
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

		for _, m := range a.Moves {
			if err := store.MoveBookFilesToBook([]string{m.FileID}, m.From, target); err != nil {
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("move file %s %s->%s failed: %v", m.FileID, m.From, target, err))
				errCount++
				continue
			}
			if err := store.ReassignExternalID("itunes", m.PID, target); err != nil {
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("reassign pid %s->%s failed: %v", m.PID, target, err))
				errCount++
			}
			moved++
		}

		// Set the canonical title (fixes chapter-suffix leaks on survivors too).
		if tb, err := store.GetBookByID(target); err == nil && tb != nil && tb.Title != a.Title {
			tb.Title = a.Title
			if _, err := store.UpdateBook(target, tb); err != nil {
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("set title on %s failed: %v", target, err))
				errCount++
			} else {
				titled++
			}
		}
		touched[target] = true
	}

	// Delete projected-empty books, GUARDED: re-assert no files AND no ext-ids.
	for _, id := range plan.DeleteBooks {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		files, _ := store.GetBookFiles(id)
		exts, _ := store.GetExternalIDsForBook(id)
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
			out = append(out, fmt.Sprintf("SKIP(entangled) %q", a.Title))
		case len(a.Moves) > 0 && a.FreshBook:
			out = append(out, fmt.Sprintf("SPLIT→fresh %q (%d files)", a.Title, len(a.Moves)))
		case len(a.Moves) > 0:
			out = append(out, fmt.Sprintf("MERGE→%s %q (%d files)", a.Target, a.Title, len(a.Moves)))
		}
	}
	return out
}
