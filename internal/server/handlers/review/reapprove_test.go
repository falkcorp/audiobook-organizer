// file: internal/server/handlers/review/reapprove_test.go
// version: 1.0.0
// guid: d2e4e573-9060-4296-b021-6de20390337a
// last-edited: 2026-10-18

package reviewhandler_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	reviewhandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/review"
)

// reApprove approves an item again with an empty body: no explicit action, so the
// handler must resolve the decision the human already recorded.
func reApprove(t *testing.T, h *reviewhandler.Handler, id string) {
	t.Helper()
	w := doReq(t, h.ApproveReviewItem, http.MethodPost, "/review/items/"+id+"/approve", nil,
		gin.Params{{Key: "id", Value: id}})
	if w.Code != http.StatusOK {
		t.Fatalf("re-approve %s: code %d body %s", id, w.Code, w.Body.String())
	}
}

// ═══ THE OVERRIDE MUST SURVIVE UNTIL APPLY (owner item 2) ═══════════════════════
//
// These two are a PAIR and only mean something together. With review_apply_enabled
// off, approving executes nothing and the hold is approved again once the switch is
// on (POST /review/replay-approved, which used to do this in one call, was retired by
// 01-P81j). The value of letting a human override the machine rests on the choice
// still being there when that second approve reads it back with an EMPTY body. One
// test alone passes vacuously: a handler that skipped everything would satisfy the
// suppression case, and one that ran everything would satisfy the execution case.

// 🔴 SUPPRESSION. The classifier said `combine`; the human said `separate`. The
// second approve must NOT merge: combine's apply path hard-deletes the absorbed Book
// rows, so a wrong merge here is unrecoverable.
func TestReApprove_HonoursOverrideAwayFromCombine(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)

	applyOn := false
	h := reviewhandler.New(s, func() bool { return applyOn }, nil)
	combined := 0
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(_ context.Context, _ database.ReviewItem) error {
		combined++
		return nil
	})

	w := doReq(t, h.ApproveReviewItem, http.MethodPost, "/review/items/"+it.ID+"/approve",
		map[string]any{"action": itunesservice.ActionSeparate}, gin.Params{{Key: "id", Value: it.ID}})
	if w.Code != http.StatusOK {
		t.Fatalf("approve with override: code %d body %s", w.Code, w.Body.String())
	}

	applyOn = true
	reApprove(t, h, it.ID)

	if combined != 0 {
		t.Fatalf("combine apply ran %d times - the re-approve used the RECOMMENDATION and merged "+
			"books a human explicitly said to keep apart", combined)
	}
	got, _ := s.GetReviewItem(it.ID)
	if got.Status != database.ReviewStatusApproved {
		t.Fatalf("status = %q, want approved - separate has nothing to apply", got.Status)
	}
	if got.ChosenAction != itunesservice.ActionSeparate {
		t.Fatalf("chosen_action = %q, want separate", got.ChosenAction)
	}
}

// 🔴 EXECUTION, the other direction. The classifier said `separate`; the human said
// `combine`. The second approve must actually MERGE. Without this the suppression
// test above would pass on a handler that had simply stopped working.
func TestReApprove_HonoursOverrideTowardsCombine(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionSeparate)

	applyOn := false
	h := reviewhandler.New(s, func() bool { return applyOn }, nil)
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
	reApprove(t, h, it.ID)

	if len(combinedIDs) != 1 || combinedIDs[0] != it.ID {
		t.Fatalf("combine apply saw %v, want [%s] - the re-approve fell back to the 'separate' "+
			"recommendation and dropped the human's decision", combinedIDs, it.ID)
	}
	got, _ := s.GetReviewItem(it.ID)
	if got.Status != database.ReviewStatusApplied {
		t.Fatalf("status = %q, want applied", got.Status)
	}
	// The approved-to-applied transition must not have erased the decision it carried out.
	if got.ChosenAction != itunesservice.ActionCombine {
		t.Fatalf("chosen_action = %q after apply, want combine - the status write wiped the decision",
			got.ChosenAction)
	}
}

// 🔴 RE-APPROVE IS THE ONLY WAY TO EXECUTE A HOLD APPROVED WHILE APPLY WAS OFF.
// POST /review/replay-approved was retired by 01-P81j, and a re-scan cannot unstick
// an approved hold — UpsertReviewItem is a full no-op on a non-pending row. The only
// way out is approving again, so this asserts an ALREADY-APPROVED hold can be approved
// again with an explicit action and applied. Without this such holds are in a dead end.
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

// 🔴 THE REPLACEMENT THE RETIRED ROUTE NAMES MUST WORK. The 410 body for
// POST /review/replay-approved points at POST /review/bulk with explicit ids, so this
// pins that approving already-approved ids in bulk executes each one with the action a
// human recorded (not the payload's recommendation).
func TestBulkApproveByIDs_AppliesItemsApprovedWhileApplyWasOff(t *testing.T) {
	s := newTestStore(t)
	toCombine := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionSeparate)
	keepApart := seedAction(t, s, "regroup.multidisc", "m2", itunesservice.ActionCombine)

	applyOn := false
	h := reviewhandler.New(s, func() bool { return applyOn }, nil)
	var combinedIDs []string
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(_ context.Context, item database.ReviewItem) error {
		combinedIDs = append(combinedIDs, item.ID)
		return nil
	})
	for id, action := range map[string]string{toCombine.ID: itunesservice.ActionCombine, keepApart.ID: itunesservice.ActionSeparate} {
		w := doReq(t, h.ApproveReviewItem, http.MethodPost, "/review/items/"+id+"/approve",
			map[string]any{"action": action}, gin.Params{{Key: "id", Value: id}})
		if w.Code != http.StatusOK {
			t.Fatalf("approve %s: code %d body %s", id, w.Code, w.Body.String())
		}
	}

	applyOn = true
	w := doReq(t, h.BulkReviewAction, http.MethodPost, "/review/bulk",
		map[string]any{"action": "approve", "ids": []string{toCombine.ID, keepApart.ID}}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("bulk re-approve: code %d body %s", w.Code, w.Body.String())
	}
	if len(combinedIDs) != 1 || combinedIDs[0] != toCombine.ID {
		t.Fatalf("combine apply saw %v, want only [%s]", combinedIDs, toCombine.ID)
	}
	if got, _ := s.GetReviewItem(toCombine.ID); got.Status != database.ReviewStatusApplied {
		t.Fatalf("combine-decided status = %q, want applied", got.Status)
	}
	if got, _ := s.GetReviewItem(keepApart.ID); got.Status != database.ReviewStatusApproved || got.ChosenAction != itunesservice.ActionSeparate {
		t.Fatalf("separate-decided item = %q/%q, want approved/separate", got.Status, got.ChosenAction)
	}
}
