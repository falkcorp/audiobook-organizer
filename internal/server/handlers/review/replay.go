// file: internal/server/handlers/review/replay.go
// version: 1.4.0
// guid: 4d807d92-72d8-4df6-9da1-80123d2bf6b4
// last-edited: 2026-09-02

package reviewhandler

import (
	"fmt"
	"net/http"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/gin-gonic/gin"
)

// replayRequest is the POST /api/v1/review/replay-approved body.
type replayRequest struct {
	// Apply must be true to execute. Default false — dry run, matching every other
	// destructive operation in this codebase.
	Apply bool `json:"apply"`
	// Kind narrows the replay to one review kind ("" = all kinds).
	Kind string `json:"kind,omitempty"`
	// Limit caps how many items are replayed (0 = all). Useful for a canary.
	Limit int `json:"limit,omitempty"`
}

// replayPageSize is how many approved items one store call fetches while replay
// collects the approved set. It is a page size, not a cap: collectApprovedItems
// walks Offset forward until the store's total is reached, so every approved
// item is seen exactly once no matter how many there are.
//
// 🔴 WHY REPLAY PAGES AT ALL. The store treats Limit<=0 as "default page of 50",
// not as "no limit". The previous version of this handler passed Limit: 0 in
// the belief it meant unbounded, so a queue with 300 approved holds replayed 50
// of them, reported approved_total=50, and an operator reading that saw a queue
// that was done. Nothing errored. The other 250 decisions sat approved forever.
const replayPageSize = 100

// collectApprovedItems gathers EVERY approved item for kind by paging the store,
// and returns the store's own total alongside. The items are collected in full
// BEFORE anything is applied, because applying moves an item out of the approved
// set and would shift every later page under a live walk.
//
// The store re-materialises and sorts the matching set on each page, so this is
// O(total * pages) at the store; with approved counts in the hundreds to low
// thousands that is milliseconds, and correctness of the count is worth it.
//
// Items are de-duplicated by ID: a hold approved between two page reads lands
// at the head of the store's newest-first order and shifts the window, so a
// page can repeat a row. Repeating an apply would be far worse than the cost
// of a set.
func (h *Handler) collectApprovedItems(kind string) ([]database.ReviewItem, int, error) {
	var out []database.ReviewItem
	seen := map[string]bool{}
	total := 0
	for offset := 0; ; offset += replayPageSize {
		page, t, err := h.store.ListReviewItems(database.ReviewFilter{
			Status: database.ReviewStatusApproved,
			Kind:   kind,
			Limit:  replayPageSize,
			Offset: offset,
		})
		if err != nil {
			return nil, 0, err
		}
		total = t
		for _, it := range page {
			if seen[it.ID] {
				continue
			}
			seen[it.ID] = true
			out = append(out, it)
		}
		// Short page: the store ran out. Reached total: nothing left past here.
		// Either alone would do on a quiet store; both together also end the walk
		// if the store's total and its pages disagree, rather than spinning.
		if len(page) < replayPageSize || offset+len(page) >= t {
			break
		}
	}
	return out, total, nil
}

// ReplayApprovedItems re-runs the apply handler for items already marked approved.
//
// 🔴 WHY THIS EXISTS — approved decisions were being silently discarded.
//
// approveOne applies an item ONLY inside the approve request, and only when the
// global switch is on. With the switch off it records status="approved" and
// returns a note. Nothing ever read that state back: before this handler,
// ReviewStatusApproved appeared in exactly two places in the codebase — the
// constant, and the one line that sets it.
//
// So a human could work through hundreds of holds in review-only mode, and every
// one of those decisions would evaporate:
//   - flipping review_apply_enabled later does NOT revisit them, because apply
//     only ever happens inside approve; and
//   - the regroup scan reports them as "already-decided" and SKIPS the folder, so
//     they are never even re-offered.
//
// This closes that gap: the queue can be reviewed before apply is enabled, and the
// decisions still count afterwards.
//
// Dry-run by default. Refuses to execute while the global switch is off rather
// than silently doing nothing — an operator asking to replay deserves to be told
// the switch is the reason, not left guessing.
func (h *Handler) ReplayApprovedItems(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithServiceUnavailable(c, "review store not available")
		return
	}
	var req replayRequest
	// A body is optional: no body means a dry run over every kind.
	_ = c.ShouldBindJSON(&req)

	// approvedTotal is the STORE's count of approved items for this kind, not the
	// length of what was materialised — the two only agree because the walk is
	// complete, and reporting the store's number is what makes a short walk
	// visible instead of self-consistent.
	items, approvedTotal, err := h.collectApprovedItems(req.Kind)
	if err != nil {
		httputil.InternalError(c, "failed to list approved review items", err)
		return
	}

	// Partition BEFORE doing anything, so a dry run reports exactly what an apply
	// would touch — including the items it would have to skip.
	type skipped struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Reason string `json:"reason"`
	}
	// 🔴 REPLAY RUNS WHAT THE HUMAN CHOSE, NOT WHAT THE MACHINE RECOMMENDED.
	//
	// effectiveActionFor prefers the PERSISTED ReviewItem.ChosenAction and only falls
	// back to the payload's `recommendedAction` when none was recorded. That ordering
	// is the entire point of owner item 2: with review_apply_enabled off, approving is
	// the only moment a human is in the loop and replay is where the work actually
	// happens, so a hold recommending `combine` that a reviewer approved as `separate`
	// must arrive here as `separate`. Reading the payload first would merge the books
	// they said to keep apart, and the combine apply path hard-deletes absorbed rows.
	//
	// approveOne resolves through the SAME helper, so approve and replay can never
	// carry out different decisions for one row.
	//
	// Holds approved before ChosenAction existed fall through to the payload; a
	// pre-recommendation hold resolves to insufficient-evidence and lands in the skip
	// list. That is the fail-closed direction: refusing to replay a hold whose intended
	// action was never recorded beats guessing one.
	//
	// 🔴 THE RECOVERY PATH IS RE-APPROVE, NOT RE-SCAN. A re-scan cannot help here:
	// UpsertReviewItem is a FULL no-op on a non-pending row, so it will not refresh
	// an already-approved hold's payload. What does work is approving the hold again
	// with an explicit {"action": "..."} — approveOne has no status guard, so an
	// approved item can be re-approved, and it now RECORDS the action, so a later
	// replay honours it. The skip reason says so, because pointing an operator at a
	// re-scan that silently does nothing is worse than saying nothing.
	var replayable []database.ReviewItem
	var noHandler []skipped
	for _, it := range items {
		action := effectiveActionFor(it)
		if _, ok := h.applyHandlerFor(action); ok {
			replayable = append(replayable, it)
			continue
		}
		reason := "no apply handler registered for action " + action
		switch action {
		case itunesservice.ActionInsufficientEvidence:
			reason = "hold carries no recommended action (approved before recommendations existed, or the " +
				"classifier could not tell) — approve it again with an explicit {\"action\": \"...\"} to decide it; " +
				"a re-scan will NOT refresh an already-approved hold"
		case itunesservice.ActionSeparate:
			// Not a gap — a completed decision. Every member is already its own book,
			// so there is nothing left to execute. Saying so matters when the reviewer
			// OVERRODE a `combine` recommendation to get here: the skip is the override
			// working, and an operator reading a bare "no apply handler" line could
			// easily mistake it for the decision having been dropped.
			reason = "action 'separate' needs no apply step — every member is already its own book, " +
				"so this decision is complete"
		}
		noHandler = append(noHandler, skipped{it.ID, it.Kind, reason})
	}
	if req.Limit > 0 && len(replayable) > req.Limit {
		replayable = replayable[:req.Limit]
	}

	if !req.Apply {
		httputil.RespondWithOK(c, gin.H{
			"dry_run":        true,
			"approved_total": approvedTotal,
			"would_replay":   len(replayable),
			"skipped":        noHandler,
			"apply_enabled":  h.applyGloballyEnabled(),
			"apply_cap":      applycap.Effective(h.configuredApplyCap()),
			"note":           "dry run — nothing applied. Pass {\"apply\": true} to execute.",
		})
		return
	}

	// Fail loudly rather than quietly no-op'ing: replaying with the switch off would
	// re-mark items without doing the work, which is the exact failure this handler
	// exists to fix.
	if !h.applyGloballyEnabled() {
		httputil.RespondWithError(c, http.StatusConflict, "REVIEW_APPLY_DISABLED",
			"review apply is globally disabled; enable review_apply_enabled before replaying approved items")
		return
	}

	// Fail-safe cap (internal/applycap): the dry run above reports the cap so an
	// operator can pass a `limit` under it; an over-cap live replay is refused
	// before the first apply handler runs.
	if h.refuseIfOverCap(c, "review/replay-approved", len(replayable)) {
		return
	}

	ctx := c.Request.Context()
	applied, failed := 0, 0
	var errs []string
	for i := range replayable {
		it := replayable[i]
		action := effectiveActionFor(it)
		fn, ok := h.applyHandlerFor(action)
		if !ok {
			continue
		}
		if err := fn(ctx, it); err != nil {
			failed++
			if len(errs) < 10 {
				errs = append(errs, fmt.Sprintf("%s (%s): %v", it.ID, it.Kind, err))
			}
			// Leave the item "approved" so a later replay retries it. Marking it
			// applied after a failure would strand the work exactly as before.
			continue
		}
		if _, serr := h.store.SetReviewItemStatus(it.ID, database.ReviewStatusApplied); serr != nil {
			failed++
			if len(errs) < 10 {
				errs = append(errs, fmt.Sprintf("%s: applied but status write failed: %v", it.ID, serr))
			}
			continue
		}
		applied++
	}

	httputil.RespondWithOK(c, gin.H{
		"dry_run":        false,
		"approved_total": approvedTotal,
		"applied":        applied,
		"failed":         failed,
		"skipped":        noHandler,
		"errors":         errs,
	})
}
