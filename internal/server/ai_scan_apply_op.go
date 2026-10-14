// file: internal/server/ai_scan_apply_op.go
// version: 1.3.0
// guid: 2d9f4b80-7a13-4c65-8e0b-5f31a6c92d74
// last-edited: 2026-10-14

// ai_scan_apply_op registers ai.author-scan-apply: the op behind "Apply selected"
// in the Authors & series lane's AI scans sub-view. Until 2026-10-13 that button
// only marked the results applied and merged nothing.

package server

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"strings"
	"time"

	ulid "github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/aiscan"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/entities"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// scanApplyStore is what applying a result needs: the shared author merge and the
// shared narrator reclassify.
type scanApplyStore interface {
	authorMergeStore
	entities.ReclassifyStore
}

// scanApplyPlan is what one merge result asks for.
type scanApplyPlan struct {
	KeepID int
	// MergeIDs are folded into KeepID.
	MergeIDs []int
	// NarratorIDs are authors the suggestion flagged as narrators: they are left
	// out of the merge and reclassified, as the Authors view does.
	NarratorIDs []int
}

// planScanMerge turns a stored merge result into a plan, or says why it cannot be
// applied. It is pure so the rules can be tested without a store.
//
// Narrator and publisher flags are acted on only where they can be trusted. A
// groups-scan answer gives them as POSITIONS in the group it was shown, not as
// author ids, so for those rows (and for a flagged publisher, which has no
// reclassify path) the result is refused rather than merged: merging would fold
// the flagged narrator's books into the author. A full-scan row's flags are
// author ids, and so are an agreed (cross_validate) row's when they came from the
// full scan (aiscan.RolesNameAuthorIDs), so those are honoured when each is one
// of the row's authors other than the keep.
func planScanMerge(r database.ScanResult, flags aiscan.RawRoleFlags) (scanApplyPlan, error) {
	sug := r.Suggestion
	if sug.Action != "merge" {
		return scanApplyPlan{}, fmt.Errorf("it is a %q suggestion; only merge suggestions can be applied here", sug.Action)
	}
	if sug.KeepID <= 0 {
		return scanApplyPlan{}, stderrors.New("no keep author could be chosen for it")
	}
	inGroup := map[int]bool{}
	for _, id := range sug.AuthorIDs {
		inGroup[id] = true
	}
	if !inGroup[sug.KeepID] {
		return scanApplyPlan{}, fmt.Errorf("its keep author %d is not one of its authors", sug.KeepID)
	}

	var narrators, publishers []int
	if len(sug.Roles) > 0 {
		var roles ai.SuggestionRoles
		if err := json.Unmarshal(sug.Roles, &roles); err != nil {
			return scanApplyPlan{}, fmt.Errorf("its role flags cannot be read: %w", err)
		}
		if roles.Narrator != nil {
			narrators = roles.Narrator.IDs
		}
		if roles.Publisher != nil {
			publishers = roles.Publisher.IDs
		}
	}
	// The model's flags as the scan stored them. A result whose converter dropped
	// the deprecated is_narrator / is_publisher lists (every scan before the
	// converters kept them) carries no flag, and merging it would fold a flagged
	// narrator into the keep author: refuse what the stored answer flags that the
	// result does not carry.
	carried := map[int]bool{}
	for _, id := range narrators {
		carried[id] = true
	}
	for _, id := range publishers {
		carried[id] = true
	}
	if why := flags.Unaccounted(sug.Source, sug.GroupIndex, sug.AuthorIDs, carried); why != "" {
		return scanApplyPlan{}, stderrors.New(why + "; handle it in the Authors view")
	}
	if len(publishers) > 0 {
		return scanApplyPlan{}, stderrors.New("it flags a publisher, which cannot be reclassified here; handle it in the Authors view")
	}
	narratorSet := map[int]bool{}
	if len(narrators) > 0 {
		if !aiscan.RolesNameAuthorIDs(sug) {
			return scanApplyPlan{}, stderrors.New("it flags a narrator by position, not by author id, so which author is meant is unknown; handle it in the Authors view")
		}
		for _, id := range narrators {
			switch {
			case id == sug.KeepID:
				return scanApplyPlan{}, fmt.Errorf("it flags its own keep author %d as a narrator", id)
			case !inGroup[id]:
				return scanApplyPlan{}, fmt.Errorf("it flags author %d as a narrator, who is not one of its authors", id)
			}
			narratorSet[id] = true
		}
	}

	plan := scanApplyPlan{KeepID: sug.KeepID}
	seen := map[int]bool{sug.KeepID: true}
	for _, id := range sug.AuthorIDs {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		if narratorSet[id] {
			plan.NarratorIDs = append(plan.NarratorIDs, id)
		} else {
			plan.MergeIDs = append(plan.MergeIDs, id)
		}
	}
	if len(plan.MergeIDs) == 0 && len(plan.NarratorIDs) == 0 {
		return scanApplyPlan{}, stderrors.New("it has no author to merge into the keep author")
	}
	return plan, nil
}

// scanApplyResult is the outcome for one result.
type scanApplyResult struct {
	ResultID     int      `json:"result_id"`
	KeepID       int      `json:"keep_id,omitempty"`
	Merged       []int    `json:"merged,omitempty"`
	Reclassified []int    `json:"reclassified,omitempty"`
	Applied      bool     `json:"applied"`
	Skipped      string   `json:"skipped,omitempty"`
	Errors       []string `json:"errors,omitempty"`
}

// scanApplyReport is the op's result payload.
type scanApplyReport struct {
	ScanID  int               `json:"scan_id"`
	DryRun  bool              `json:"dry_run"`
	Results []scanApplyResult `json:"results"`
}

// applyScanResult merges one result's authors (and reclassifies its flagged
// narrators), and reports whether every step succeeded. It marks nothing: the
// caller marks a result applied only when Applied is true and the run is live.
//
// cancelled is true when the run was canceled part way; what was merged stays
// merged.
func applyScanResult(
	ctx context.Context,
	store scanApplyStore,
	opID string,
	r database.ScanResult,
	flags aiscan.RawRoleFlags,
	dryRun bool,
	canceled func() bool,
) (res scanApplyResult, cancelled bool) {
	res.ResultID = r.ID
	plan, err := planScanMerge(r, flags)
	if err != nil {
		res.Errors = []string{fmt.Sprintf("result %d refused: %v", r.ID, err)}
		return res, false
	}
	res.KeepID = plan.KeepID

	out, cancelled := mergeAuthorsInto(ctx, store, opID, authorMergeSpec{
		KeepID:   plan.KeepID,
		MergeIDs: plan.MergeIDs,
		// A scan applies many groups from one click, so locks are always honoured.
		HonorLocks: true,
		DryRun:     dryRun,
	}, canceled, nil)
	if cancelled {
		return res, true
	}
	for _, id := range plan.MergeIDs {
		if out.MergedIDs[id] {
			res.Merged = append(res.Merged, id)
		}
	}
	res.Errors = append(res.Errors, out.Errors...)
	if left := unmergedAuthorIDs(plan.MergeIDs, plan.KeepID, out.MergedIDs); len(left) > 0 {
		res.Errors = append(res.Errors, fmt.Sprintf("authors %s were not merged into author %d", joinInts(left), plan.KeepID))
		return res, false
	}

	// Narrators flagged by the suggestion, after the merge (the Authors view's
	// order). The same code as POST /authors/:id/reclassify-as-narrator, told to
	// leave a book whose author_name is locked on its author.
	for _, id := range plan.NarratorIDs {
		if canceled != nil && canceled() {
			return res, true
		}
		// GetAuthorByID follows the author tombstone. Nothing left of the author
		// means a reclassify already happened; an answer under another id means it
		// was MERGED into that author, so its books are credited to an author and
		// not to a narrator, and calling it reclassified would be false.
		cur, gerr := store.GetAuthorByID(id)
		switch {
		case gerr != nil:
			res.Errors = append(res.Errors, fmt.Sprintf("author %d could not be read before reclassifying it: %v", id, gerr))
			return res, false
		case cur == nil:
			res.Reclassified = append(res.Reclassified, id)
			continue
		case cur.ID != id:
			res.Errors = append(res.Errors, fmt.Sprintf(
				"author %d was already merged into author %d, so it cannot be reclassified as a narrator", id, cur.ID))
			return res, false
		}
		if dryRun {
			res.Reclassified = append(res.Reclassified, id)
			continue
		}
		ro, rerr := entities.ReclassifyAuthorAsNarrator(store, id, entities.ReclassifyOptions{
			// Journaled under this operation, so a revert of it undoes the reclassify too.
			Journal: &entities.ReclassifyJournal{OperationID: opID, Store: store},
			// A reclassify takes the author credit off the book AND puts a narrator
			// on it: either lock stops it.
			SkipBook: func(bookID string) (bool, string, error) {
				locks, lerr := database.LoadFieldLocks(store, bookID)
				if lerr != nil {
					return false, "", lerr
				}
				if locks.Locked(database.FieldKeyAuthorName) {
					return true, "author name is user-locked; it keeps this author", nil
				}
				if locks.Locked(database.FieldKeyNarrator) {
					return true, "narrator is user-locked; no narrator is added", nil
				}
				return false, "", nil
			},
		})
		switch {
		case rerr == nil:
			res.Reclassified = append(res.Reclassified, id)
		default:
			res.Errors = append(res.Errors, fmt.Sprintf("reclassifying author %d as a narrator failed: %v", id, rerr))
			for _, sk := range ro.Skipped {
				res.Errors = append(res.Errors, fmt.Sprintf("author %d: %s", id, sk))
			}
			return res, false
		}
	}
	res.Applied = len(res.Errors) == 0
	return res, false
}

// RegisterAIScanApplyOp registers the "ai.author-scan-apply" v2 OperationDef.
func (s *Server) RegisterAIScanApplyOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              handlers.AIScanApplyDefID,
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "ai",
		DisplayName:     "AI Scan Apply",
		Description:     "Merges the authors of the selected AI scan results into their keep author, honouring locked author names, and marks the results that merged as applied.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		// Interactive: a person pressed Apply and is waiting on it.
		NoWarmupWait: true,
		Timeout:      2 * time.Hour,
		// RESUME AUDIT 2026-10-13: ResumeDrop, the same as entities.author-merge
		// whose merge loop this runs. A restart that re-ran the op would re-issue
		// merges for authors already deleted, a path the author-merge audit could
		// not prove is a no-op; the interrupted run is surfaced for the user to
		// re-issue, and the results it had already marked applied are skipped.
		ResumePolicy: opsregistry.ResumeDrop,
		// The SAME key as entities.author-merge: both rewrite book_authors and
		// delete authors, and the registry serializes runs that share a key, so an
		// Authors-view merge cannot interleave with this one on the same rows.
		ConcurrencyKey: "entities.author-merge",
		Permissions:    []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:   []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p handlers.AIScanApplyOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("ai.author-scan-apply: decode params: %w", err)
				}
			}
			// Omitted means a preview (owner rule 2026-09-25); the HTTP handler
			// sends dry_run=false for a real apply.
			dryRun, modeErr := opmode.ResolveDryRun(handlers.AIScanApplyDefID, p.DryRun, p.DryRunCamel)
			if modeErr != nil {
				return modeErr
			}
			if p.ScanID == 0 {
				return fmt.Errorf("ai.author-scan-apply: scan_id is required")
			}
			if s.aiScanStore == nil || s.pipelineManager == nil {
				return fmt.Errorf("ai.author-scan-apply: the AI scan store is not initialized")
			}
			return s.runAIScanApply(ctx, p, dryRun, reporter)
		},
	})
}

// runAIScanApply applies the selected results of one scan, one at a time.
//
// SEQUENTIAL ON PURPOSE. Results of one scan can share authors (a full scan
// proposes overlapping groups, and one result's keep can be another's merge id),
// and each merge deletes authors. Two workers on two results could merge the same
// author twice or fold an author into one that a sibling is deleting, so the
// results are disjoint only by being processed in order, with each merge reading
// the store as the previous one left it. The loop is bounded by the selection a
// person made, not the library, so the single core costs nothing.
func (s *Server) runAIScanApply(ctx context.Context, p handlers.AIScanApplyOpParams, dryRun bool, reporter opsregistry.Reporter) error {
	progress := registryProgressAdapter{r: reporter}
	store := s.Ops()
	opID := opsregistry.ReporterOpID(reporter)

	// Every exit invalidates: a cancel or a failure part way follows merges that
	// already happened, and a stale cached list keeps offering authors that are gone.
	if !dryRun {
		defer func() {
			s.dedupCache.InvalidateAll()
			s.authorsCache.InvalidateAll()
		}()
	}

	scan, err := s.aiScanStore.GetScan(p.ScanID)
	if err != nil {
		return fmt.Errorf("ai.author-scan-apply: read scan %d: %w", p.ScanID, err)
	}
	if scan == nil {
		return fmt.Errorf("ai.author-scan-apply: scan %d not found", p.ScanID)
	}
	if scan.Status == "superseded" {
		return (&database.ScanSupersededError{ScanID: scan.ID, By: scan.SupersededBy})
	}
	// Only a finished scan's results are final. The route checks this too; the run
	// re-checks, because the scan can change between the request and the run.
	if scan.Status != "complete" {
		return fmt.Errorf("ai.author-scan-apply: scan %d is %s; only a finished scan's results can be applied", scan.ID, scan.Status)
	}
	// Claim the scan before the first merge: a nightly run that supersedes it
	// meanwhile is refused (database.AIScanStore.SupersedeIfUnapplied), where an
	// unclaimed scan was hidden under a run that then merged its results and could
	// not mark them. The claim is the authoritative status check too (it re-reads
	// under the store's apply lock). A preview writes nothing and claims nothing.
	if !dryRun {
		claimID := opID
		if claimID == "" {
			claimID = database.UnattributedApplyClaimPrefix + ulid.Make().String()
		}
		if err := s.aiScanStore.ClaimScanForApply(p.ScanID, claimID); err != nil {
			return fmt.Errorf("ai.author-scan-apply: %w", err)
		}
		// Released on every exit. A run that dies without reaching here leaves a
		// claim that stops holding the scan as soon as the registry no longer runs
		// this op (AIScanStore.SetApplyRunLiveness), is cleared at the next
		// startup, and expires anyway (database.AIScanApplyClaimTTL).
		defer func() {
			if err := s.aiScanStore.ReleaseScanApply(p.ScanID, claimID); err != nil {
				logging.Warn(ctx, "ai scan apply: could not release the scan claim", "scan_id", p.ScanID, "err", err)
			}
		}()
	}
	if err := s.pipelineManager.EnsureKeepIDs(p.ScanID); err != nil {
		logging.Warn(ctx, "ai scan apply: could not fill in keep authors", "scan_id", p.ScanID, "err", err)
	}
	stored, err := s.aiScanStore.GetScanResults(p.ScanID)
	if err != nil {
		return fmt.Errorf("ai.author-scan-apply: read results of scan %d: %w", p.ScanID, err)
	}
	byID := make(map[int]database.ScanResult, len(stored))
	for _, r := range stored {
		byID[r.ID] = r
	}
	// Fails closed: an unreadable stored answer must not read as "nothing flagged".
	flags, err := aiscan.LoadRawRoleFlags(s.aiScanStore, p.ScanID)
	if err != nil {
		return fmt.Errorf("ai.author-scan-apply: %w", err)
	}

	mode := "Applying"
	if dryRun {
		mode = "Previewing"
	}
	total := len(p.ResultIDs)
	_ = progress.Log("info", fmt.Sprintf("%s %d result(s) of AI scan #%d", mode, total, p.ScanID), nil)

	report := scanApplyReport{ScanID: p.ScanID, DryRun: dryRun}
	applied, failed := 0, 0
	var failures []string
	for i, rid := range p.ResultIDs {
		if progress.IsCanceled() {
			_ = progress.Log("warn", "Operation cancelled by user", nil)
			s.saveScanApplyReport(ctx, reporter, report)
			return fmt.Errorf("cancelled")
		}
		_ = progress.UpdateProgress(i, total, fmt.Sprintf("%s result %d/%d", mode, i+1, total))

		r, ok := byID[rid]
		if !ok {
			report.Results = append(report.Results, scanApplyResult{ResultID: rid,
				Errors: []string{fmt.Sprintf("result %d is not in scan %d", rid, p.ScanID)}})
			failed++
			failures = append(failures, report.Results[len(report.Results)-1].Errors[0])
			continue
		}
		if r.Applied {
			// Already merged by an earlier apply (a repeated request, or the run
			// this one replaced): counted, not redone.
			report.Results = append(report.Results, scanApplyResult{ResultID: rid, Applied: true, Skipped: "already applied"})
			applied++
			continue
		}

		res, cancelled := applyScanResult(ctx, store, opID, r, flags, dryRun, progress.IsCanceled)
		if cancelled {
			_ = progress.Log("warn", "Operation cancelled by user", nil)
			report.Results = append(report.Results, res)
			s.saveScanApplyReport(ctx, reporter, report)
			return fmt.Errorf("cancelled")
		}
		if res.Applied && !dryRun {
			// Mark ONLY a result whose merge fully succeeded. The note that lets an
			// undo of this operation mark the result pending again is written first
			// and voided if the mark is refused, like every other row of the ledger.
			res.Applied = false
			voidNote, nerr := journalAppliedResult(store, opID, p.ScanID, rid)
			switch {
			case nerr != nil:
				res.Errors = append(res.Errors, fmt.Sprintf("merged, but the result was not marked applied: its undo note could not be recorded: %v", nerr))
			default:
				if merr := s.aiScanStore.MarkResultApplied(p.ScanID, rid); merr != nil {
					if verr := voidNote(); verr != nil {
						logging.Warn(ctx, "ai scan apply: could not void the undo note of a result that was not marked applied",
							"scan_id", p.ScanID, "result_id", rid, "err", verr)
					}
					var sup *database.ScanSupersededError
					if stderrors.As(merr, &sup) {
						res.Errors = append(res.Errors, fmt.Sprintf("merged, but scan %d was superseded meanwhile, so the result was not marked applied: %v", p.ScanID, merr))
					} else {
						res.Errors = append(res.Errors, fmt.Sprintf("merged, but the result could not be marked applied: %v", merr))
					}
				} else {
					res.Applied = true
				}
			}
		}
		report.Results = append(report.Results, res)
		if res.Applied {
			applied++
			_ = progress.Log("info", fmt.Sprintf("Result %d: author %d kept, %d merged, %d reclassified", rid, res.KeepID, len(res.Merged), len(res.Reclassified)), nil)
			continue
		}
		failed++
		detail := strings.Join(res.Errors[:min(len(res.Errors), 3)], "; ")
		failures = append(failures, fmt.Sprintf("result %d: %s", rid, detail))
		_ = progress.Log("warn", fmt.Sprintf("Result %d not applied: %s", rid, detail), nil)
	}

	s.saveScanApplyReport(ctx, reporter, report)
	msg := fmt.Sprintf("AI scan #%d: %d of %d result(s) applied, %d not applied", p.ScanID, applied, total, failed)
	if dryRun {
		msg = fmt.Sprintf("AI scan #%d PREVIEW (nothing written): %d of %d result(s) would apply, %d would not", p.ScanID, applied, total, failed)
	}
	_ = progress.UpdateProgress(total, total, msg)
	_ = progress.Log("info", msg, nil)
	if failed > 0 && !dryRun {
		// Fail rather than complete: the op used to be a mark-only call that
		// completed whatever happened. The results that merged are marked; the
		// rest stay unapplied so the page offers them again, with the reasons here.
		return fmt.Errorf("%s: %s", msg, strings.Join(failures[:min(len(failures), 3)], "; "))
	}
	return nil
}

// saveScanApplyReport stores the per-result outcomes on the run. A failure to
// store them is logged, not returned: the merges have happened and the run's own
// status carries the headline.
func (s *Server) saveScanApplyReport(ctx context.Context, reporter opsregistry.Reporter, report scanApplyReport) {
	if err := opsregistry.ReporterSetResult(reporter, report); err != nil {
		logging.Warn(ctx, "ai scan apply: could not store the per-result outcomes", "scan_id", report.ScanID, "err", err)
	}
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterAIScanApplyOp(reg) })
}

// journalAppliedResult writes the ledger note that ties an applied scan result to
// the operation, before the result is marked, and returns the function that voids it
// when the mark is refused.
func journalAppliedResult(store authorMergeJournalStore, opID string, scanID, resultID int) (void func() error, err error) {
	row, err := undo.NewAIScanResultAppliedRow(scanID, resultID)
	if err != nil {
		return nil, err
	}
	return journalStep(store, opID, row)
}
