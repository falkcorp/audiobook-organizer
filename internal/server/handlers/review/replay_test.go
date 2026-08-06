// file: internal/server/handlers/review/replay_test.go
// version: 1.2.0
// guid: 8e3c5a71-9d24-4b60-af18-2c47e0b96d35
// last-edited: 2026-08-06

package reviewhandler_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	reviewhandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/review"
)

// approveWith drives the real approve path so items reach "approved" exactly the
// way a human would leave them.
func approveWith(t *testing.T, h *reviewhandler.Handler, id string) {
	t.Helper()
	w := doReq(t, h.ApproveReviewItem, http.MethodPost, "/review/items/"+id+"/approve", nil,
		gin.Params{{Key: "id", Value: id}})
	if w.Code != http.StatusOK {
		t.Fatalf("approve %s: code %d body %s", id, w.Code, w.Body.String())
	}
}

// 🔴 THE LOST-WORK BUG. Approving while the global switch is OFF records
// status="approved" and never executes. Nothing else in the codebase read that
// state back, so a human could work through hundreds of holds and have every
// decision silently discarded — flipping the switch later does not revisit them,
// and the regroup scan reports them "already-decided" and skips the folder.
//
// This asserts the decisions survive: approve with apply off, turn apply on,
// replay, and the work actually happens.
func TestReplayApproved_AppliesDecisionsMadeWhileApplyWasOff(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)

	applyOn := false
	h := reviewhandler.New(s, func() bool { return applyOn })

	var appliedIDs []string
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(_ context.Context, item database.ReviewItem) error {
		appliedIDs = append(appliedIDs, item.ID)
		return nil
	})

	// Review-only mode: the decision is recorded but nothing runs.
	approveWith(t, h, it.ID)
	if len(appliedIDs) != 0 {
		t.Fatalf("apply ran while the global switch was off: %v", appliedIDs)
	}

	// Operator flips the switch, then replays.
	applyOn = true
	w := doReq(t, h.ReplayApprovedItems, http.MethodPost, "/review/replay-approved",
		map[string]any{"apply": true}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("replay: code %d body %s", w.Code, w.Body.String())
	}
	body := decodeBody(t, w)
	data, _ := body["data"].(map[string]any)
	if data == nil {
		data = body
	}
	if got := data["applied"]; got != float64(1) {
		t.Fatalf("applied = %v, want 1 (body %s)", got, w.Body.String())
	}
	if len(appliedIDs) != 1 || appliedIDs[0] != it.ID {
		t.Fatalf("apply handler saw %v, want [%s]", appliedIDs, it.ID)
	}

	got, err := s.GetReviewItem(it.ID)
	if err != nil {
		t.Fatalf("GetReviewItem: %v", err)
	}
	if got.Status != database.ReviewStatusApplied {
		t.Fatalf("status = %q, want applied", got.Status)
	}
}

// A dry run must report what WOULD happen and change nothing — the default for
// every destructive path in this codebase.
func TestReplayApproved_DryRunChangesNothing(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	h := reviewhandler.New(s, func() bool { return false })

	ran := 0
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(_ context.Context, _ database.ReviewItem) error {
		ran++
		return nil
	})
	approveWith(t, h, it.ID)

	w := doReq(t, h.ReplayApprovedItems, http.MethodPost, "/review/replay-approved", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("dry run: code %d", w.Code)
	}
	if ran != 0 {
		t.Fatalf("dry run executed %d apply handlers", ran)
	}
	got, _ := s.GetReviewItem(it.ID)
	if got.Status != database.ReviewStatusApproved {
		t.Fatalf("dry run changed status to %q", got.Status)
	}
}

// 🔑 Replaying with the switch off must FAIL LOUDLY, not quietly no-op. Silently
// re-marking items without doing the work is the exact failure this endpoint
// exists to fix.
func TestReplayApproved_RefusesWhenApplyIsGloballyDisabled(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	h := reviewhandler.New(s, func() bool { return false })
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(_ context.Context, _ database.ReviewItem) error {
		return nil
	})
	approveWith(t, h, it.ID)

	w := doReq(t, h.ReplayApprovedItems, http.MethodPost, "/review/replay-approved",
		map[string]any{"apply": true}, nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409 so the operator learns the switch is the reason", w.Code)
	}
}

// 🔴 A failing apply must leave the item APPROVED so a later replay retries it.
// Marking it applied after a failure would strand the work exactly as before.
func TestReplayApproved_LeavesFailedItemsRetryable(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	h := reviewhandler.New(s, func() bool { return true })

	fail := true
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(_ context.Context, _ database.ReviewItem) error {
		if fail {
			return context.DeadlineExceeded
		}
		return nil
	})

	// Approve with apply ON would apply immediately, so force the approved state
	// directly to model an item left over from review-only mode.
	if _, err := s.SetReviewItemStatus(it.ID, database.ReviewStatusApproved); err != nil {
		t.Fatalf("seed approved: %v", err)
	}

	w := doReq(t, h.ReplayApprovedItems, http.MethodPost, "/review/replay-approved",
		map[string]any{"apply": true}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("code %d", w.Code)
	}
	got, _ := s.GetReviewItem(it.ID)
	if got.Status != database.ReviewStatusApproved {
		t.Fatalf("status = %q after a failed apply, want approved so a retry can pick it up", got.Status)
	}

	// A later replay, once the cause is fixed, must succeed.
	fail = false
	w = doReq(t, h.ReplayApprovedItems, http.MethodPost, "/review/replay-approved",
		map[string]any{"apply": true}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("retry code %d", w.Code)
	}
	got, _ = s.GetReviewItem(it.ID)
	if got.Status != database.ReviewStatusApplied {
		t.Fatalf("status = %q after retry, want applied", got.Status)
	}
}

// Holds whose ACTION has no registered handler are reported as skipped, never
// silently dropped. This one is seeded with the OLD payload shape (no
// recommendedAction), which is every hold approved before 2026-08-06: it resolves
// to insufficient-evidence and must be skipped with a reason that says why, rather
// than replayed on a guessed action.
func TestReplayApproved_ReportsItemsWithNoApplicableAction(t *testing.T) {
	s := newTestStore(t)
	it := seed(t, s, "regroup.ambiguous", "a1")
	h := reviewhandler.New(s, func() bool { return true })
	if _, err := s.SetReviewItemStatus(it.ID, database.ReviewStatusApproved); err != nil {
		t.Fatalf("seed approved: %v", err)
	}

	w := doReq(t, h.ReplayApprovedItems, http.MethodPost, "/review/replay-approved", nil, nil)
	body := decodeBody(t, w)
	data, _ := body["data"].(map[string]any)
	if data == nil {
		data = body
	}
	skipped, _ := data["skipped"].([]any)
	if len(skipped) != 1 {
		t.Fatalf("skipped = %v, want the one handler-less item (body %s)", skipped, w.Body.String())
	}
	reason, _ := skipped[0].(map[string]any)["reason"].(string)
	if !strings.Contains(reason, "no recommended action") {
		t.Fatalf("skip reason = %q, want it to name the missing recommendation so an operator "+
			"knows a re-scan is the fix", reason)
	}
}

// 🔴 THE RECOVERY PATH THE SKIP REASON ADVERTISES MUST ACTUALLY WORK. Replay skips
// every hold approved before recommendations existed, and a re-scan cannot unstick
// them — UpsertReviewItem is a full no-op on a non-pending row. The only way out is
// re-approving with an explicit action, so this asserts an ALREADY-APPROVED hold can
// be approved again and applied. Without this the skipped items are in a dead end.
func TestApprove_AlreadyApprovedItem_CanBeReApprovedWithAnExplicitAction(t *testing.T) {
	s := newTestStore(t)
	it := seed(t, s, "regroup.ambiguous", "a1") // old payload → insufficient-evidence
	if _, err := s.SetReviewItemStatus(it.ID, database.ReviewStatusApproved); err != nil {
		t.Fatalf("seed approved: %v", err)
	}
	h, c := newActionHandler(s, true)

	// A re-scan is a no-op on a non-pending row, which is why the skip reason must
	// not recommend one.
	again, err := s.UpsertReviewItem(database.ReviewItem{
		Kind: it.Kind, DedupKey: it.DedupKey, FolderRef: it.FolderRef, Summary: "s",
		Payload: `{"recommendedAction":"combine"}`,
	})
	if err != nil {
		t.Fatalf("re-scan upsert: %v", err)
	}
	if again.Payload != "{}" {
		t.Fatalf("payload = %q; a re-scan was expected to leave an approved hold untouched", again.Payload)
	}

	w, _ := approveBody(t, h, it.ID, itunesservice.ActionCombine)
	if w.Code != http.StatusOK {
		t.Fatalf("re-approve: code %d body %s", w.Code, w.Body.String())
	}
	if c.combine != 1 {
		t.Fatalf("combine ran %d times, want 1", c.combine)
	}
	got, _ := s.GetReviewItem(it.ID)
	if got.Status != database.ReviewStatusApplied {
		t.Fatalf("status = %q, want applied", got.Status)
	}
}
