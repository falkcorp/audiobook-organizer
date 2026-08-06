// file: internal/server/handlers/review/replay_test.go
// version: 1.3.0
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

// replayApply runs a real (non-dry) replay and fails on anything but 200.
func replayApply(t *testing.T, h *reviewhandler.Handler) map[string]any {
	t.Helper()
	w := doReq(t, h.ReplayApprovedItems, http.MethodPost, "/review/replay-approved",
		map[string]any{"apply": true}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("replay: code %d body %s", w.Code, w.Body.String())
	}
	body := decodeBody(t, w)
	if data, ok := body["data"].(map[string]any); ok {
		return data
	}
	return body
}

// ═══ THE OVERRIDE MUST SURVIVE TO REPLAY (owner item 2) ══════════════════════════
//
// These two are a PAIR and only mean something together. With review_apply_enabled
// off — production's setting — approving executes nothing and replay does the real
// work later, so the whole value of letting a human override the machine rests on the
// choice still being there when replay reads it back. One test alone passes
// vacuously: a replay that skipped everything would satisfy the suppression case, and
// a replay that ran everything would satisfy the execution case.

// 🔴 SUPPRESSION. The classifier said `combine`; the human said `separate`. Replay
// must NOT merge. This is the exact incident the old 409
// REVIEW_OVERRIDE_NOT_PERSISTABLE guard existed to prevent — combine's apply path
// hard-deletes the absorbed Book rows, so a wrong replay here is unrecoverable.
func TestReplayApproved_HonoursOverrideAwayFromCombine(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)

	applyOn := false
	h := reviewhandler.New(s, func() bool { return applyOn })
	combined := 0
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(_ context.Context, _ database.ReviewItem) error {
		combined++
		return nil
	})

	// Reviewer disagrees with the recommendation while apply is off.
	w := doReq(t, h.ApproveReviewItem, http.MethodPost, "/review/items/"+it.ID+"/approve",
		map[string]any{"action": itunesservice.ActionSeparate}, gin.Params{{Key: "id", Value: it.ID}})
	if w.Code != http.StatusOK {
		t.Fatalf("approve with override: code %d body %s", w.Code, w.Body.String())
	}

	// Operator flips the switch and replays.
	applyOn = true
	data := replayApply(t, h)

	if combined != 0 {
		t.Fatalf("combine apply ran %d times — replay used the RECOMMENDATION and merged books "+
			"a human explicitly said to keep apart", combined)
	}
	if got := data["applied"]; got != float64(0) {
		t.Fatalf("applied = %v, want 0", got)
	}
	skipped, _ := data["skipped"].([]any)
	if len(skipped) != 1 {
		t.Fatalf("skipped = %v, want the one separate-decided hold", skipped)
	}
	if reason, _ := skipped[0].(map[string]any)["reason"].(string); !strings.Contains(reason, "separate") {
		t.Fatalf("skip reason = %q, want it to name 'separate' so the operator sees the override worked, "+
			"not that a decision was dropped", reason)
	}
	got, _ := s.GetReviewItem(it.ID)
	if got.Status != database.ReviewStatusApproved {
		t.Fatalf("status = %q, want approved — nothing was applied", got.Status)
	}
	if got.ChosenAction != itunesservice.ActionSeparate {
		t.Fatalf("chosen_action = %q, want separate", got.ChosenAction)
	}
}

// 🔴 EXECUTION, the other direction. The classifier said `separate`; the human looked
// at the folder and said `combine`. Replay must actually MERGE. Without this the
// suppression test above would pass on a replay that had simply stopped working.
func TestReplayApproved_HonoursOverrideTowardsCombine(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionSeparate)

	applyOn := false
	h := reviewhandler.New(s, func() bool { return applyOn })
	var combinedIDs []string
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(_ context.Context, item database.ReviewItem) error {
		combinedIDs = append(combinedIDs, item.ID)
		return nil
	})

	w := doReq(t, h.ApproveReviewItem, http.MethodPost, "/review/items/"+it.ID+"/approve",
		map[string]any{"action": itunesservice.ActionCombine}, gin.Params{{Key: "id", Value: it.ID}})
	if w.Code != http.StatusOK {
		t.Fatalf("approve with override: code %d body %s", w.Code, w.Body.String())
	}
	if len(combinedIDs) != 0 {
		t.Fatalf("apply ran while the switch was off: %v", combinedIDs)
	}

	applyOn = true
	data := replayApply(t, h)

	if len(combinedIDs) != 1 || combinedIDs[0] != it.ID {
		t.Fatalf("combine apply saw %v, want [%s] — replay fell back to the 'separate' recommendation "+
			"and dropped the human's decision", combinedIDs, it.ID)
	}
	if got := data["applied"]; got != float64(1) {
		t.Fatalf("applied = %v, want 1", got)
	}
	got, _ := s.GetReviewItem(it.ID)
	if got.Status != database.ReviewStatusApplied {
		t.Fatalf("status = %q, want applied", got.Status)
	}
	// The approved→applied transition must not have erased the decision it carried out.
	if got.ChosenAction != itunesservice.ActionCombine {
		t.Fatalf("chosen_action = %q after apply, want combine — the status write wiped the decision",
			got.ChosenAction)
	}
}

// 🔴 OLD ROWS KEEP WORKING. A hold approved before ChosenAction existed carries an
// empty one; replay must fall back to the payload's recommendation rather than skip
// it. (Its sibling — an old row whose payload predates recommendations too, so the
// fallback lands on insufficient-evidence — is
// TestReplayApproved_ReportsItemsWithNoApplicableAction.)
func TestReplayApproved_FallsBackToPayloadWhenNoActionWasPersisted(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	// SetReviewItemStatus writes NO chosen action, which is exactly the pre-2026-08-06
	// approved-row shape.
	if _, err := s.SetReviewItemStatus(it.ID, database.ReviewStatusApproved); err != nil {
		t.Fatalf("seed approved: %v", err)
	}
	if got, _ := s.GetReviewItem(it.ID); got.ChosenAction != "" {
		t.Fatalf("chosen_action = %q, want empty — this test models a row that predates the field",
			got.ChosenAction)
	}

	h := reviewhandler.New(s, func() bool { return true })
	combined := 0
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(_ context.Context, _ database.ReviewItem) error {
		combined++
		return nil
	})

	if data := replayApply(t, h); data["applied"] != float64(1) {
		t.Fatalf("applied = %v, want 1", data["applied"])
	}
	if combined != 1 {
		t.Fatalf("combine ran %d times, want 1 — the payload fallback regressed", combined)
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
