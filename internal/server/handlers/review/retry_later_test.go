// file: internal/server/handlers/review/retry_later_test.go
// version: 1.0.0
// guid: 2d8f5a63-91c4-4e07-b6a2-c3e1f40d7b95
// last-edited: 2026-10-25

package reviewhandler_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	reviewhandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/review"
)

// warmupRefusingApply models a combine refused before its journal because the
// library index is still loading: nothing was written, so the apply marks it
// NothingWritten (regroup's refusedForWarmup does).
func warmupRefusingApply(_ context.Context, _ database.ReviewItem) error {
	return database.NothingWritten(fmt.Errorf("regroup multidisc apply: combine: the row of book Hush was not taken: %w",
		database.ErrOwnershipLookupIncomplete))
}

// A single approve of a hold whose combine was refused while the index loads says
// so: 503 with its own code and the reason, and the hold stays pending. It used to
// be the generic "failed to approve review item" 500, with the reason only in the log.
func TestApproveReviewItem_WarmupRefusal_Is503RetryLaterAndStaysPending(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	h := reviewhandler.New(s, func() bool { return true }, nil)
	h.RegisterApplyHandler(itunesservice.ActionCombine, warmupRefusingApply)

	w := doReq(t, h.ApproveReviewItem, http.MethodPost, "/review/items/"+it.ID+"/approve", nil,
		gin.Params{{Key: "id", Value: it.ID}})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "REVIEW_RETRY_LATER") || !strings.Contains(w.Body.String(), "was not taken") {
		t.Fatalf("expected the code and the reason, got %s", w.Body.String())
	}
	if got, _ := s.GetReviewItem(it.ID); got.Status != database.ReviewStatusPending {
		t.Fatalf("the refused hold is %q, want pending", got.Status)
	}
}

// Bulk approve lists it under skipped (the code says it is still pending), not under
// failed.
func TestBulkApprove_WarmupRefusal_IsSkippedNotFailed(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	h := reviewhandler.New(s, func() bool { return true }, nil)
	h.RegisterApplyHandler(itunesservice.ActionCombine, warmupRefusingApply)

	w := doReq(t, h.BulkReviewAction, http.MethodPost, "/review/bulk",
		map[string]any{"action": "approve", "ids": []string{it.ID}}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	data := decodeBody(t, w)["data"].(map[string]any)
	skipped, _ := data["skipped"].([]any)
	if len(skipped) != 1 || skipped[0].(map[string]any)["code"] != "REVIEW_RETRY_LATER" {
		t.Fatalf("expected one skip coded REVIEW_RETRY_LATER, got %s", w.Body.String())
	}
	if failed, _ := data["failed"].([]any); len(failed) != 0 {
		t.Fatalf("a warmup refusal is not a failure: %s", w.Body.String())
	}
}

// The same error from an apply that DID write (not marked NothingWritten) holds the
// item as interrupted and keeps its 409: retrying later is not what that needs.
func TestApproveReviewItem_LookupIncompleteAfterWriting_StaysHeld409(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	h := reviewhandler.New(s, func() bool { return true }, nil)
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(context.Context, database.ReviewItem) error {
		return fmt.Errorf("combine stopped part-way: %w", database.ErrOwnershipLookupIncomplete)
	})

	w := doReq(t, h.ApproveReviewItem, http.MethodPost, "/review/items/"+it.ID+"/approve", nil,
		gin.Params{{Key: "id", Value: it.ID}})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "REVIEW_APPLY_PARTIAL") {
		t.Fatalf("expected 409 REVIEW_APPLY_PARTIAL, got %d: %s", w.Code, w.Body.String())
	}
}
