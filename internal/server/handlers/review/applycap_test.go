// file: internal/server/handlers/review/applycap_test.go
// version: 1.0.0
// guid: d7a4c1e8-3b6f-4d29-a5e1-8c0f2b7d4a96
// last-edited: 2026-09-02

package reviewhandler_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	reviewhandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/review"
)

// The bulk apply cap (internal/applycap) on the review handler. The cap is
// injected (third argument to New) so these tests pin it at 2 and drive the
// real store: cap+1 targets are refused with ZERO status changes; exactly cap
// targets are processed. A kind-scoped bulk request is the important case —
// "every pending hold of that kind" is exactly the whole-list shape the cap
// exists to refuse, and its size is only known after the store is read.

func capOf(n int) func() int { return func() int { return n } }

func seedPendingOfKind(t *testing.T, s *database.PebbleStore, kind string, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := range n {
		ids = append(ids, seed(t, s, kind, kind+"-"+strconv.Itoa(i)).ID)
	}
	return ids
}

func TestBulkReviewAction_KindScoped_RefusesOverTheCap(t *testing.T) {
	s := newTestStore(t)
	seedPendingOfKind(t, s, "regroup.multidisc", 3)
	h := reviewhandler.New(s, func() bool { return true }, capOf(2))

	w := doReq(t, h.BulkReviewAction, http.MethodPost, "/review/bulk",
		map[string]any{"action": "reject", "kind": "regroup.multidisc"}, nil)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", w.Code, w.Body.String())
	}
	for _, want := range []string{"BULK_APPLY_CAP_EXCEEDED", "3 items", "cap is 2", "bulk_apply_max_items"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("refusal missing %q: %s", want, w.Body.String())
		}
	}
	if got, _ := s.CountReviewItems(database.ReviewStatusPending); got != 3 {
		t.Fatalf("a refused bulk must change nothing; want 3 pending, got %d", got)
	}
}

func TestBulkReviewAction_KindScoped_ExactlyTheCapIsProcessed(t *testing.T) {
	s := newTestStore(t)
	seedPendingOfKind(t, s, "regroup.multidisc", 2)
	seedPendingOfKind(t, s, "regroup.anthology", 5) // other kind: not a target, must not count
	h := reviewhandler.New(s, func() bool { return true }, capOf(2))

	w := doReq(t, h.BulkReviewAction, http.MethodPost, "/review/bulk",
		map[string]any{"action": "reject", "kind": "regroup.multidisc"}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if got, _ := s.CountReviewItems(database.ReviewStatusRejected); got != 2 {
		t.Fatalf("want 2 rejected, got %d", got)
	}
}

func TestBulkReviewAction_ByIDs_RefusesOverTheCap(t *testing.T) {
	s := newTestStore(t)
	ids := seedPendingOfKind(t, s, "regroup.multidisc", 3)
	h := reviewhandler.New(s, func() bool { return true }, capOf(2))

	w := doReq(t, h.BulkReviewAction, http.MethodPost, "/review/bulk",
		map[string]any{"action": "approve", "ids": ids}, nil)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", w.Code, w.Body.String())
	}
	if got, _ := s.CountReviewItems(database.ReviewStatusPending); got != 3 {
		t.Fatalf("a refused bulk must change nothing; want 3 pending, got %d", got)
	}
}

// A nil cap func (every existing caller/test) and a ≤0 value both mean the
// default — never unlimited.
func TestBulkReviewAction_NilAndZeroCapMeanDefault(t *testing.T) {
	s := newTestStore(t)
	for _, h := range []*reviewhandler.Handler{
		reviewhandler.New(s, func() bool { return true }, nil),
		reviewhandler.New(s, func() bool { return true }, capOf(0)),
	} {
		ids := make([]string, applycap.Default+1)
		for i := range ids {
			ids[i] = "nonexistent-" + strconv.Itoa(i)
		}
		w := doReq(t, h.BulkReviewAction, http.MethodPost, "/review/bulk",
			map[string]any{"action": "reject", "ids": ids}, nil)
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("want 422 at default cap, got %d: %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "cap is "+strconv.Itoa(applycap.Default)) {
			t.Fatalf("want the default cap named: %s", w.Body.String())
		}
	}
}

// --- replay -----------------------------------------------------------------

func approvedCombineItems(t *testing.T, s *database.PebbleStore, h *reviewhandler.Handler, n int) {
	t.Helper()
	for i := range n {
		it := seedAction(t, s, "regroup.multidisc", "m"+strconv.Itoa(i), itunesservice.ActionCombine)
		approveWith(t, h, it.ID)
	}
}

func TestReplayApproved_LiveRefusesOverTheCap(t *testing.T) {
	s := newTestStore(t)
	// Approve with apply OFF so the items park as "approved" and replay has work.
	off := reviewhandler.New(s, func() bool { return false }, capOf(2))
	approvedCombineItems(t, s, off, 3)

	h := reviewhandler.New(s, func() bool { return true }, capOf(2))
	ran := 0
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(_ context.Context, _ database.ReviewItem) error {
		ran++
		return nil
	})
	w := doReq(t, h.ReplayApprovedItems, http.MethodPost, "/review/replay-approved",
		map[string]any{"apply": true}, nil)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", w.Code, w.Body.String())
	}
	if ran != 0 {
		t.Fatalf("a refused replay ran %d apply handlers", ran)
	}
	if got, _ := s.CountReviewItems(database.ReviewStatusApproved); got != 3 {
		t.Fatalf("want all 3 still approved, got %d", got)
	}
}

// The dry run is how an operator sizes a live replay: it must NOT be capped,
// and it must report the cap so a `limit` under it can be chosen.
func TestReplayApproved_DryRunReportsTheCapAndIsNotCapped(t *testing.T) {
	s := newTestStore(t)
	off := reviewhandler.New(s, func() bool { return false }, capOf(2))
	approvedCombineItems(t, s, off, 3)

	h := reviewhandler.New(s, func() bool { return true }, capOf(2))
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(_ context.Context, _ database.ReviewItem) error { return nil })
	w := doReq(t, h.ReplayApprovedItems, http.MethodPost, "/review/replay-approved", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("dry run must not be capped, got %d: %s", w.Code, w.Body.String())
	}
	data := decodeBody(t, w)["data"].(map[string]any)
	if data["would_replay"].(float64) != 3 || data["apply_cap"].(float64) != 2 {
		t.Fatalf("want would_replay=3 apply_cap=2, got %v", data)
	}
}

// `limit` is the operator's knob for staying under the cap: a limit at the cap
// over more approved items than the cap must run exactly cap applies.
func TestReplayApproved_LimitUnderTheCapRuns(t *testing.T) {
	s := newTestStore(t)
	off := reviewhandler.New(s, func() bool { return false }, capOf(2))
	approvedCombineItems(t, s, off, 3)

	h := reviewhandler.New(s, func() bool { return true }, capOf(2))
	ran := 0
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(_ context.Context, _ database.ReviewItem) error {
		ran++
		return nil
	})
	w := doReq(t, h.ReplayApprovedItems, http.MethodPost, "/review/replay-approved",
		map[string]any{"apply": true, "limit": 2}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if ran != 2 {
		t.Fatalf("want exactly 2 applies, got %d", ran)
	}
}
