// file: internal/server/handlers/review/applycap_test.go
// version: 1.1.0
// guid: d7a4c1e8-3b6f-4d29-a5e1-8c0f2b7d4a96
// last-edited: 2026-10-18

package reviewhandler_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	"github.com/falkcorp/audiobook-organizer/internal/database"
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
