// file: internal/server/handlers/review/apply_notices_test.go
// version: 1.0.1
// guid: cf6332c7-7866-4c55-aded-862c8ab7215d
// last-edited: 2026-10-25

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

// approveData approves id with an empty body and returns the response's data
// object, failing the test on anything but a 200.
func approveData(t *testing.T, h *reviewhandler.Handler, id string) map[string]any {
	t.Helper()
	w := doReq(t, h.ApproveReviewItem, http.MethodPost, "/review/items/"+id+"/approve", nil,
		gin.Params{{Key: "id", Value: id}})
	if w.Code != http.StatusOK {
		t.Fatalf("approve: code %d body %s", w.Code, w.Body.String())
	}
	return decodeBody(t, w)["data"].(map[string]any)
}

// An apply that succeeded but left something undone adds a notice
// (database.AddApplyNotice). The item is applied, and the approve response carries
// the line in its note, so the reviewer is told. Before, an apply could only return
// an error (which fails or holds the item) or nil (which says nothing).
func TestApprove_ApplyNoticesReachTheResponseNote(t *testing.T) {
	s := newTestStore(t)
	a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	h := reviewhandler.New(s, func() bool { return true }, nil)
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(ctx context.Context, _ database.ReviewItem) error {
		database.AddApplyNotice(ctx, "combine of /lib/Gotham: path held by 2 live book(s): not reattached")
		database.AddApplyNotice(ctx, "second line")
		return nil
	})

	data := approveData(t, h, a.ID)
	if got, _ := s.GetReviewItem(a.ID); got.Status != database.ReviewStatusApplied {
		t.Fatalf("the item is %q, want applied: the combine committed", got.Status)
	}
	want := "combine of /lib/Gotham: path held by 2 live book(s): not reattached; second line"
	if data["note"] != want {
		t.Fatalf("note = %q, want %q", data["note"], want)
	}
}

func TestApprove_NoNoticesMeansNoNote(t *testing.T) {
	s := newTestStore(t)
	a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	h := reviewhandler.New(s, func() bool { return true }, nil)
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(context.Context, database.ReviewItem) error { return nil })

	if note, present := approveData(t, h, a.ID)["note"]; present {
		t.Fatalf("a clean apply carries no note, got %q", note)
	}
}

// Bulk approve reports the same lines per item, in "notes", beside "applied".
func TestBulkApprove_ApplyNoticesAreListedPerItem(t *testing.T) {
	s := newTestStore(t)
	noisy := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	h := reviewhandler.New(s, func() bool { return true }, nil)
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(ctx context.Context, _ database.ReviewItem) error {
		database.AddApplyNotice(ctx, "left a file unattached")
		return nil
	})

	w := doReq(t, h.BulkReviewAction, http.MethodPost, "/review/bulk",
		map[string]any{"action": "approve", "ids": []string{noisy.ID}}, nil)
	data := decodeBody(t, w)["data"].(map[string]any)
	notes, _ := data["notes"].([]any)
	if len(notes) != 1 {
		t.Fatalf("bulk result = %v, want one note", data)
	}
	n := notes[0].(map[string]any)
	if n["id"] != noisy.ID || n["note"] != "left a file unattached" {
		t.Fatalf("note = %v", n)
	}
	if applied, _ := data["applied"].([]any); len(applied) != 1 {
		t.Fatalf("the item is applied: %v", data)
	}
}
