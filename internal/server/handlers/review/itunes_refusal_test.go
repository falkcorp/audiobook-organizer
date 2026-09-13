// file: internal/server/handlers/review/itunes_refusal_test.go
// version: 1.0.0
// guid: 9b4d2e61-7c3a-4f85-b0e9-1d6a8c5f2e34
// last-edited: 2026-09-13

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
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	reviewhandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/review"
)

func itunesRefusingApply(_ context.Context, _ database.ReviewItem) error {
	return fmt.Errorf("regroup multidisc apply: combine: %w",
		&merge.ITunesProtectedError{BookID: "B1", Path: "/srv/media/Audiobooks/x.m4b"})
}

// An apply refused by the iTunes guard is a 409 and the hold stays pending.
func TestApproveReviewItem_ITunesRefusal_Is409AndStaysPending(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	h := reviewhandler.New(s, func() bool { return true }, nil)
	h.RegisterApplyHandler(itunesservice.ActionCombine, itunesRefusingApply)

	w := doReq(t, h.ApproveReviewItem, http.MethodPost, "/review/items/"+it.ID+"/approve", nil,
		gin.Params{{Key: "id", Value: it.ID}})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for an iTunes refusal, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "REVIEW_ITUNES_PROTECTED") {
		t.Fatalf("expected the REVIEW_ITUNES_PROTECTED code, got %s", w.Body.String())
	}
	fetched, _ := s.GetReviewItem(it.ID)
	if fetched.Status != database.ReviewStatusPending {
		t.Fatalf("expected status to stay pending after an iTunes refusal, got %q", fetched.Status)
	}
}

// Bulk approve skips the refused hold instead of aborting the batch with a 500.
func TestBulkApprove_ITunesRefusal_SkipsAndStaysPending(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	h := reviewhandler.New(s, func() bool { return true }, nil)
	h.RegisterApplyHandler(itunesservice.ActionCombine, itunesRefusingApply)

	w := doReq(t, h.BulkReviewAction, http.MethodPost, "/review/bulk",
		map[string]any{"action": "approve", "ids": []string{it.ID}}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with the item skipped, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), it.ID) {
		t.Fatalf("expected the refused item in the skipped list, got %s", w.Body.String())
	}
	fetched, _ := s.GetReviewItem(it.ID)
	if fetched.Status != database.ReviewStatusPending {
		t.Fatalf("expected status to stay pending, got %q", fetched.Status)
	}
}
