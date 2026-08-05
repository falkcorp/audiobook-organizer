// file: internal/server/handlers/review/replay.go
// version: 1.0.0
// guid: 4d807d92-72d8-4df6-9da1-80123d2bf6b4
// last-edited: 2026-08-05

package reviewhandler

import (
	"fmt"
	"net/http"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
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
	var replayable []database.ReviewItem
	var noHandler []skipped
	for _, it := range items {
		if _, ok := h.applyHandlerFor(it.Kind); ok {
			replayable = append(replayable, it)
			continue
		}
		noHandler = append(noHandler, skipped{it.ID, it.Kind, "no apply handler registered for this kind"})
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
		fn, ok := h.applyHandlerFor(it.Kind)
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
