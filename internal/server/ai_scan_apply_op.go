// file: internal/server/ai_scan_apply_op.go
// version: 1.5.0
// guid: 2d9f4b80-7a13-4c65-8e0b-5f31a6c92d74
// last-edited: 2026-11-18

// ai_scan_apply_op registers ai.author-scan-apply: the op behind "Apply selected"
// in the Authors & series lane's AI scans sub-view. Until 2026-10-13 that button
// only marked the results applied and merged nothing.

package server

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	ulid "github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/aiscan"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/errhandling"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/entities"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
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

	// aborted is set (and names why) when the scan stand-down lease was lost: the
	// whole run stops, not just this result. Not part of the stored report; the
	// reason is also in Errors.
	aborted string
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
// merged. scan is the stand-down the caller holds under opID (nil for a preview):
// it is renewed before every author's writes, and res.aborted is set when the
// lease is gone, which the caller must treat as the end of the run.
func applyScanResult(
	ctx context.Context,
	store scanApplyStore,
	opID string,
	r database.ScanResult,
	flags aiscan.RawRoleFlags,
	dryRun bool,
	scan scanLeaseRenewer,
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
		Scan:       scan,
	}, canceled, nil)
	if cancelled {
		return res, true
	}
	if out.Aborted != "" {
		res.Errors = append(res.Errors, out.Aborted)
		res.aborted = out.Aborted
		return res, false
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
		// The reclassify rewrites book_authors too, so it is gated like a merge.
		if scan != nil && !scan.RenewScanStandDown(opID) {
			res.aborted = scanStandDownLostMessage(len(res.Merged)+len(res.Reclassified), len(plan.MergeIDs)+len(plan.NarratorIDs))
			res.Errors = append(res.Errors, res.aborted)
			return res, false
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

// scanLeaseFunc adapts a hold's renew function to the scanLeaseRenewer the merge
// loop renews the stand-down through.
type scanLeaseFunc func() bool

func (f scanLeaseFunc) RenewScanStandDown(string) bool { return f() }

// aiScanApplyStore is the reads of the scan apply: the scan and its stored
// results, and the model's stored role flags.
type aiScanApplyStore interface {
	// ScanApplyAvailable reports whether the AI scan store and its pipeline are
	// wired.
	ScanApplyAvailable() bool
	GetScan(id int) (*database.Scan, error)
	GetScanResults(scanID int) ([]database.ScanResult, error)
	// LoadRawRoleFlags is aiscan.LoadRawRoleFlags over the scan store.
	LoadRawRoleFlags(scanID int) (aiscan.RawRoleFlags, error)
}

// aiScanApplyWriter is the writes of the scan apply. The merge of one result is a
// single coarse method: it renews the stand-down before every author's writes
// and journals each move under the op id, on the store it is handed.
type aiScanApplyWriter interface {
	ClaimScanForApply(scanID int, claimID string) error
	ReleaseScanApply(scanID int, claimID string) error
	// EnsureKeepIDs fills in the keep authors the scan stored without one.
	EnsureKeepIDs(scanID int) error
	// ApplyScanResult is applyScanResult.
	ApplyScanResult(ctx context.Context, opID string, r database.ScanResult, flags aiscan.RawRoleFlags, dryRun bool, lease scanLeaseRenewer, canceled func() bool) (scanApplyResult, bool)
	// JournalAppliedResult is journalAppliedResult.
	JournalAppliedResult(opID string, scanID, resultID int) (void func() error, err error)
	MarkResultApplied(scanID, resultID int) error
	// InvalidateAuthorCaches drops the duplicate and author-list caches.
	InvalidateAuthorCaches()
}

// aiScanApplyEnv is what the shared core takes from its shell (rules R2, R10).
type aiScanApplyEnv struct {
	t7Env
	Store     aiScanApplyStore
	Writer    aiScanApplyWriter
	StandDown t7StandDown
}

func (e aiScanApplyEnv) canceled() bool { return e.Canceled != nil && e.Canceled() }

// saveReport stores the per-result outcomes on the run. A failure to store them
// is logged, not returned: the merges have happened and the run's own status
// carries the headline.
func (e aiScanApplyEnv) saveReport(ctx context.Context, report scanApplyReport) {
	if e.SetResult == nil {
		return
	}
	if err := e.SetResult(report); err != nil {
		logger.WarnCtx(ctx, "ai scan apply: could not store the per-result outcomes", "scan_id", report.ScanID, "err", err)
	}
}

// aiScanApplyCore applies the selected results of one scan, one at a time.
//
// SEQUENTIAL ON PURPOSE. Results of one scan can share authors (a full scan
// proposes overlapping groups, and one result's keep can be another's merge id),
// and each merge deletes authors. Two workers on two results could merge the same
// author twice or fold an author into one that a sibling is deleting, so the
// results are disjoint only by being processed in order, with each merge reading
// the store as the previous one left it. The loop is bounded by the selection a
// person made, not the library, so the single core costs nothing.
func aiScanApplyCore(ctx context.Context, p handlers.AIScanApplyOpParams, dryRun bool, env aiScanApplyEnv) (*scanApplyReport, error) {
	if !env.Store.ScanApplyAvailable() {
		return nil, fmt.Errorf("ai.author-scan-apply: the AI scan store is not initialized")
	}
	opID := env.OpID

	// Every exit invalidates: a cancel or a failure part way follows merges that
	// already happened, and a stale cached list keeps offering authors that are gone.
	if !dryRun {
		defer env.Writer.InvalidateAuthorCaches()
	}

	scan, err := env.Store.GetScan(p.ScanID)
	if err != nil {
		return nil, fmt.Errorf("ai.author-scan-apply: read scan %d: %w", p.ScanID, err)
	}
	if scan == nil {
		return nil, fmt.Errorf("ai.author-scan-apply: scan %d not found", p.ScanID)
	}
	if scan.Status == "superseded" {
		return nil, (&database.ScanSupersededError{ScanID: scan.ID, By: scan.SupersededBy})
	}
	// Only a finished scan's results are final. The route checks this too; the run
	// re-checks, because the scan can change between the request and the run.
	if scan.Status != "complete" {
		return nil, fmt.Errorf("ai.author-scan-apply: scan %d is %s; only a finished scan's results can be applied", scan.ID, scan.Status)
	}
	// Stand the library scanner down for the whole apply, before the first write: a
	// concurrent library.scan rewrites book_authors on the rows the merges repoint.
	// ONE hold spans every result (the registry keys a hold by op id, so a hold per
	// result would release the gate between them and re-park the scanner N times).
	// Taken before the claim so a refusal leaves no claim behind. A preview writes
	// nothing and does not park the production scanner for it.
	var lease scanLeaseRenewer
	if !dryRun {
		hold, herr := env.StandDown(ctx, "ai.author-scan-apply")
		if herr != nil {
			return nil, fmt.Errorf("ai.author-scan-apply: %w", herr)
		}
		defer func() {
			errhandling.MustLog(hold.Finish(nil), "scan stand-down lapsed during the run; its lease was lost before release")
		}()
		lease = scanLeaseFunc(hold.Renew)
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
		if err := env.Writer.ClaimScanForApply(p.ScanID, claimID); err != nil {
			return nil, fmt.Errorf("ai.author-scan-apply: %w", err)
		}
		// Released on every exit. A run that dies without reaching here leaves a
		// claim that stops holding the scan as soon as the registry no longer runs
		// this op (AIScanStore.SetApplyRunLiveness), is cleared at the next
		// startup, and expires anyway (database.AIScanApplyClaimTTL).
		defer func() {
			if err := env.Writer.ReleaseScanApply(p.ScanID, claimID); err != nil {
				logger.WarnCtx(ctx, "ai scan apply: could not release the scan claim", "scan_id", p.ScanID, "err", err)
			}
		}()
	}
	if err := env.Writer.EnsureKeepIDs(p.ScanID); err != nil {
		logger.WarnCtx(ctx, "ai scan apply: could not fill in keep authors", "scan_id", p.ScanID, "err", err)
	}
	stored, err := env.Store.GetScanResults(p.ScanID)
	if err != nil {
		return nil, fmt.Errorf("ai.author-scan-apply: read results of scan %d: %w", p.ScanID, err)
	}
	byID := make(map[int]database.ScanResult, len(stored))
	for _, r := range stored {
		byID[r.ID] = r
	}
	// Fails closed: an unreadable stored answer must not read as "nothing flagged".
	flags, err := env.Store.LoadRawRoleFlags(p.ScanID)
	if err != nil {
		return nil, fmt.Errorf("ai.author-scan-apply: %w", err)
	}

	mode := "Applying"
	if dryRun {
		mode = "Previewing"
	}
	total := len(p.ResultIDs)
	env.Log(slog.LevelInfo, fmt.Sprintf("%s %d result(s) of AI scan #%d", mode, total, p.ScanID))

	report := scanApplyReport{ScanID: p.ScanID, DryRun: dryRun}
	applied, failed := 0, 0
	var failures []string
	for i, rid := range p.ResultIDs {
		if env.canceled() {
			env.Log(slog.LevelWarn, "Operation cancelled by user")
			env.saveReport(ctx, report)
			return &report, fmt.Errorf("cancelled")
		}
		env.progress(i, total, fmt.Sprintf("%s result %d/%d", mode, i+1, total))

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

		res, cancelled := env.Writer.ApplyScanResult(ctx, opID, r, flags, dryRun, lease, env.canceled)
		if cancelled {
			env.Log(slog.LevelWarn, "Operation cancelled by user")
			report.Results = append(report.Results, res)
			env.saveReport(ctx, report)
			return &report, fmt.Errorf("cancelled")
		}
		if res.aborted != "" {
			// The lease is gone, so the remaining results are not attempted: the
			// scanner may already be writing. What merged stays merged, the merged
			// results were already marked, and this one is left unapplied.
			env.Log(slog.LevelError, res.aborted)
			report.Results = append(report.Results, res)
			env.saveReport(ctx, report)
			return &report, fmt.Errorf("ai.author-scan-apply aborted after %d of %d result(s): %s", i, total, res.aborted)
		}
		if res.Applied && !dryRun {
			// Mark ONLY a result whose merge fully succeeded. The note that lets an
			// undo of this operation mark the result pending again is written first
			// and voided if the mark is refused, like every other row of the ledger.
			res.Applied = false
			voidNote, nerr := env.Writer.JournalAppliedResult(opID, p.ScanID, rid)
			switch {
			case nerr != nil:
				res.Errors = append(res.Errors, fmt.Sprintf("merged, but the result was not marked applied: its undo note could not be recorded: %v", nerr))
			default:
				if merr := env.Writer.MarkResultApplied(p.ScanID, rid); merr != nil {
					if verr := voidNote(); verr != nil {
						logger.WarnCtx(ctx, "ai scan apply: could not void the undo note of a result that was not marked applied",
							"scan_id", p.ScanID, "result_id", rid, "err", verr)
					}
					if _, ok := stderrors.AsType[*database.ScanSupersededError](merr); ok {
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
			env.Log(slog.LevelInfo, fmt.Sprintf("Result %d: author %d kept, %d merged, %d reclassified", rid, res.KeepID, len(res.Merged), len(res.Reclassified)))
			continue
		}
		failed++
		detail := strings.Join(res.Errors[:min(len(res.Errors), 3)], "; ")
		failures = append(failures, fmt.Sprintf("result %d: %s", rid, detail))
		env.Log(slog.LevelWarn, fmt.Sprintf("Result %d not applied: %s", rid, detail))
	}

	env.saveReport(ctx, report)
	msg := fmt.Sprintf("AI scan #%d: %d of %d result(s) applied, %d not applied", p.ScanID, applied, total, failed)
	if dryRun {
		msg = fmt.Sprintf("AI scan #%d PREVIEW (nothing written): %d of %d result(s) would apply, %d would not", p.ScanID, applied, total, failed)
	}
	env.progress(total, total, msg)
	env.Log(slog.LevelInfo, msg)
	if failed > 0 && !dryRun {
		// Fail rather than complete: the op used to be a mark-only call that
		// completed whatever happened. The results that merged are marked; the
		// rest stay unapplied so the page offers them again, with the reasons here.
		return &report, fmt.Errorf("%s: %s", msg, strings.Join(failures[:min(len(failures), 3)], "; "))
	}
	return &report, nil
}

// headAIScanApply is HEAD's side of both interfaces: the server's AI scan store
// and pipeline manager, the ops store for the merge and the journal, and the two
// caches, each called as HEAD's body called them.
type headAIScanApply struct{ s *Server }

func (h headAIScanApply) ScanApplyAvailable() bool {
	return h.s.aiScanStore != nil && h.s.pipelineManager != nil
}

func (h headAIScanApply) GetScan(id int) (*database.Scan, error) { return h.s.aiScanStore.GetScan(id) }

func (h headAIScanApply) GetScanResults(scanID int) ([]database.ScanResult, error) {
	return h.s.aiScanStore.GetScanResults(scanID)
}

func (h headAIScanApply) LoadRawRoleFlags(scanID int) (aiscan.RawRoleFlags, error) {
	return aiscan.LoadRawRoleFlags(h.s.aiScanStore, scanID)
}

func (h headAIScanApply) ClaimScanForApply(scanID int, claimID string) error {
	return h.s.aiScanStore.ClaimScanForApply(scanID, claimID)
}

func (h headAIScanApply) ReleaseScanApply(scanID int, claimID string) error {
	return h.s.aiScanStore.ReleaseScanApply(scanID, claimID)
}

func (h headAIScanApply) EnsureKeepIDs(scanID int) error {
	return h.s.pipelineManager.EnsureKeepIDs(scanID)
}

func (h headAIScanApply) ApplyScanResult(ctx context.Context, opID string, r database.ScanResult, flags aiscan.RawRoleFlags, dryRun bool, lease scanLeaseRenewer, canceled func() bool) (scanApplyResult, bool) {
	return applyScanResult(ctx, h.s.Ops(), opID, r, flags, dryRun, lease, canceled)
}

func (h headAIScanApply) JournalAppliedResult(opID string, scanID, resultID int) (func() error, error) {
	return journalAppliedResult(h.s.Ops(), opID, scanID, resultID)
}

func (h headAIScanApply) MarkResultApplied(scanID, resultID int) error {
	return h.s.aiScanStore.MarkResultApplied(scanID, resultID)
}

func (h headAIScanApply) InvalidateAuthorCaches() {
	h.s.dedupCache.InvalidateAll()
	h.s.authorsCache.InvalidateAll()
}

// aiScanApplyDef is HEAD's definition. Its Run is the v2 shell.
func (s *Server) aiScanApplyDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:              handlers.AIScanApplyDefID,
		Preview:         opsregistry.PreviewSpec{Params: handlers.AIScanApplyOpParams{}},
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
			h := headAIScanApply{s: s}
			env := aiScanApplyEnv{t7Env: t7HeadEnv(reporter, !dryRun), Store: h, Writer: h, StandDown: s.t7HeadAuthorStandDown(opsregistry.ReporterOpID(reporter))}
			_, err := aiScanApplyCore(ctx, p, dryRun, env)
			return err
		},
	}
}

// RegisterAIScanApplyOp registers the "ai.author-scan-apply" v2 OperationDef.
func (s *Server) RegisterAIScanApplyOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(s.aiScanApplyDef())
}

// aiScanApplyNativeParams are the native kind's params: HEAD's without the mode
// keys, which the start request carries.
type aiScanApplyNativeParams struct {
	ScanID    int   `json:"scan_id"`
	ResultIDs []int `json:"result_ids"`
}

// aiScanApplyNative is the native books.ai.author-scan-apply kind. It merges
// authors, reclassifies narrators, and marks the scan's results applied. HEAD's
// default of a preview when the mode is omitted is the start request's: a
// Preview run writes nothing and takes no stand-down.
func (s *Server) aiScanApplyNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(handlers.AIScanApplyDefID)
	c := t7Common(s.aiScanApplyDef(), sched)
	c.Effects = t7Writes(ops.ResBooks, ops.ResAuthors, ops.ResOperations, t7ResAIScans)
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, scanApplyReport]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (scanApplyReport, error) {
			np, err := t7DecodeNative[aiScanApplyNativeParams](kind, raw)
			if err != nil {
				return scanApplyReport{}, err
			}
			if np.ScanID == 0 {
				return scanApplyReport{}, fmt.Errorf("ai.author-scan-apply: scan_id is required")
			}
			if err := t7NeedStore(kind, deps); err != nil {
				return scanApplyReport{}, err
			}
			w, err := t7NewWriter(kind, deps, rc)
			if err != nil {
				return scanApplyReport{}, err
			}
			p := handlers.AIScanApplyOpParams{ScanID: np.ScanID, ResultIDs: np.ResultIDs}
			dryRun := rc.Mode() != tasks.ModeLive
			env := aiScanApplyEnv{t7Env: t7NativeEnv(rc), Store: deps.Store, Writer: w, StandDown: t7NativeStandDown(rc, deps.StandDownKey)}
			report, err := aiScanApplyCore(rc.Context(), p, dryRun, env)
			if report == nil {
				return scanApplyReport{}, err
			}
			return *report, err
		},
	})
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
