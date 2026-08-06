// file: internal/server/handlers/review/replay.go
// version: 1.1.0
// guid: 4d807d92-72d8-4df6-9da1-80123d2bf6b4
// last-edited: 2026-08-06

package reviewhandler

import (
	"fmt"
	"net/http"

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

	items, _, err := h.store.ListReviewItems(database.ReviewFilter{
		Status: database.ReviewStatusApproved,
		Kind:   req.Kind,
		Limit:  0,
	})
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
	// Resolve the ACTION the same way approveOne does (one place owns "empty →
	// insufficient-evidence"), then look the handler up by action. Replay must agree
	// with approve about what a hold means, or the two would carry out different
	// decisions for the same row.
	//
	// Every hold approved before recommendations existed resolves to
	// insufficient-evidence and lands in the skip list — which is why would_replay
	// drops to near zero on a queue of pre-2026-08-06 approvals. That is the
	// fail-closed direction: refusing to replay a hold whose intended action was
	// never recorded beats guessing one.
	//
	// 🔴 THE RECOVERY PATH IS RE-APPROVE, NOT RE-SCAN. A re-scan cannot help here:
	// UpsertReviewItem is a FULL no-op on a non-pending row, so it will not refresh
	// an already-approved hold's payload. What does work is approving the hold again
	// with an explicit {"action": "..."} — approveOne has no status guard, so an
	// approved item can be re-approved and applied. The skip reason says so, because
	// pointing an operator at a re-scan that silently does nothing is worse than
	// saying nothing.
	var replayable []database.ReviewItem
	var noHandler []skipped
	for _, it := range items {
		action := recommendedActionFor(it)
		if _, ok := h.applyHandlerFor(action); ok {
			replayable = append(replayable, it)
			continue
		}
		reason := "no apply handler registered for action " + action
		if action == itunesservice.ActionInsufficientEvidence {
			reason = "hold carries no recommended action (approved before recommendations existed, or the " +
				"classifier could not tell) — approve it again with an explicit {\"action\": \"...\"} to decide it; " +
				"a re-scan will NOT refresh an already-approved hold"
		}
		noHandler = append(noHandler, skipped{it.ID, it.Kind, reason})
	}
	if req.Limit > 0 && len(replayable) > req.Limit {
		replayable = replayable[:req.Limit]
	}

	if !req.Apply {
		httputil.RespondWithOK(c, gin.H{
			"dry_run":        true,
			"approved_total": len(items),
			"would_replay":   len(replayable),
			"skipped":        noHandler,
			"apply_enabled":  h.applyGloballyEnabled(),
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

	ctx := c.Request.Context()
	applied, failed := 0, 0
	var errs []string
	for i := range replayable {
		it := replayable[i]
		fn, ok := h.applyHandlerFor(recommendedActionFor(it))
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
		"approved_total": len(items),
		"applied":        applied,
		"failed":         failed,
		"skipped":        noHandler,
		"errors":         errs,
	})
}
