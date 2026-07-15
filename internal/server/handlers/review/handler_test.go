// file: internal/server/handlers/review/handler_test.go
// version: 1.1.0
// guid: 8e4a1c72-3d95-4b60-a7f1-9c2e6b0d5f83
// last-edited: 2026-07-14

// Tests for the universal review-queue handlers. The store is exercised through
// a REAL pebble-backed *database.PebbleStore (which implements the ReviewStore
// interface), matching the dedup handler tests' real-store approach. There is at
// least one test per public endpoint plus the apply-registry and bulk-guard
// paths.

package reviewhandler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	reviewhandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/review"
)

func init() { gin.SetMode(gin.TestMode) }

func newTestStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	s.WaitForWarmup()
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seed(t *testing.T, s *database.PebbleStore, kind, dedupKey string) database.ReviewItem {
	t.Helper()
	it, err := s.UpsertReviewItem(database.ReviewItem{
		Kind:      kind,
		DedupKey:  dedupKey,
		FolderRef: "/f/" + dedupKey,
		Summary:   "s",
		Payload:   "{}",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return it
}

func doReq(t *testing.T, fn gin.HandlerFunc, method, url string, body any, params gin.Params) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, url, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.Request = req
	c.Params = params
	fn(c)
	return w
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
	return m
}

func TestGetReviewCount(t *testing.T) {
	s := newTestStore(t)
	seed(t, s, "regroup.multidisc", "m1")
	seed(t, s, "regroup.multidisc", "m2")
	seed(t, s, "regroup.anthology", "a1")
	h := reviewhandler.New(s, func() bool { return true })

	w := doReq(t, h.GetReviewCount, http.MethodGet, "/review/count", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := decodeBody(t, w)
	data := body["data"].(map[string]any)
	if data["count"].(float64) != 3 {
		t.Fatalf("expected count 3, got %v", data["count"])
	}
	byKind := data["byKind"].(map[string]any)
	if byKind["regroup.multidisc"].(float64) != 2 {
		t.Fatalf("expected 2 multidisc, got %v", byKind["regroup.multidisc"])
	}
}

func TestListReviewItems(t *testing.T) {
	s := newTestStore(t)
	seed(t, s, "regroup.multidisc", "m1")
	seed(t, s, "regroup.anthology", "a1")
	h := reviewhandler.New(s, func() bool { return true })

	// Default (pending) + kind filter.
	w := doReq(t, h.ListReviewItems, http.MethodGet, "/review/items?kind=regroup.multidisc", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := decodeBody(t, w)
	if body["count"].(float64) != 1 {
		t.Fatalf("expected count 1 for multidisc, got %v", body["count"])
	}
	items := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
}

func TestApproveReviewItem_NoHandler_MarksApproved(t *testing.T) {
	s := newTestStore(t)
	it := seed(t, s, "regroup.multidisc", "m1")
	h := reviewhandler.New(s, func() bool { return true }) // no apply handlers registered (A1 state)

	w := doReq(t, h.ApproveReviewItem, http.MethodPost, "/review/items/"+it.ID+"/approve", nil,
		gin.Params{{Key: "id", Value: it.ID}})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := decodeBody(t, w)
	data := body["data"].(map[string]any)
	if data["note"] == nil {
		t.Fatal("expected a note when no apply handler is registered")
	}
	item := data["item"].(map[string]any)
	if item["status"] != database.ReviewStatusApproved {
		t.Fatalf("expected status approved, got %v", item["status"])
	}
}

func TestApproveReviewItem_WithHandler_MarksApplied(t *testing.T) {
	s := newTestStore(t)
	it := seed(t, s, "regroup.multidisc", "m1")
	h := reviewhandler.New(s, func() bool { return true })

	var applied bool
	h.RegisterApplyHandler("regroup.multidisc", func(_ context.Context, item database.ReviewItem) error {
		applied = true
		if item.ID != it.ID {
			t.Errorf("apply handler got wrong item: %s", item.ID)
		}
		return nil
	})

	w := doReq(t, h.ApproveReviewItem, http.MethodPost, "/review/items/"+it.ID+"/approve", nil,
		gin.Params{{Key: "id", Value: it.ID}})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !applied {
		t.Fatal("expected apply handler to be invoked")
	}
	item := decodeBody(t, w)["data"].(map[string]any)["item"].(map[string]any)
	if item["status"] != database.ReviewStatusApplied {
		t.Fatalf("expected status applied, got %v", item["status"])
	}
}

func TestApproveReviewItem_HandlerError_StaysPending(t *testing.T) {
	s := newTestStore(t)
	it := seed(t, s, "regroup.multidisc", "m1")
	h := reviewhandler.New(s, func() bool { return true })
	h.RegisterApplyHandler("regroup.multidisc", func(_ context.Context, _ database.ReviewItem) error {
		return context.DeadlineExceeded // any apply failure
	})

	w := doReq(t, h.ApproveReviewItem, http.MethodPost, "/review/items/"+it.ID+"/approve", nil,
		gin.Params{{Key: "id", Value: it.ID}})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when apply handler fails, got %d: %s", w.Code, w.Body.String())
	}
	// The item must NOT have advanced past pending — approve is not idempotent
	// over a failed apply (B2 relies on this: a failed collapse leaves the hold
	// re-approvable rather than stranded as "applied").
	fetched, _ := s.GetReviewItem(it.ID)
	if fetched.Status != database.ReviewStatusPending {
		t.Fatalf("expected status to stay pending after apply failure, got %q", fetched.Status)
	}
}

// TestApproveReviewItem_ApplyGateOff_ApprovesNotApplies is the core safety test for
// the global "big switch": with the gate OFF, approving a hold that HAS a registered
// apply handler must record the decision as "approved" WITHOUT executing the handler
// (nothing merges), and stay visible/reviewable.
func TestApproveReviewItem_ApplyGateOff_ApprovesNotApplies(t *testing.T) {
	s := newTestStore(t)
	it := seed(t, s, "regroup.multidisc", "m1")
	applied := false
	h := reviewhandler.New(s, func() bool { return false }) // switch OFF
	h.RegisterApplyHandler("regroup.multidisc", func(_ context.Context, _ database.ReviewItem) error {
		applied = true
		return nil
	})

	w := doReq(t, h.ApproveReviewItem, http.MethodPost, "/review/items/"+it.ID+"/approve", nil,
		gin.Params{{Key: "id", Value: it.ID}})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if applied {
		t.Fatal("apply handler MUST NOT run while the global apply switch is off")
	}
	fetched, _ := s.GetReviewItem(it.ID)
	if fetched.Status != database.ReviewStatusApproved {
		t.Fatalf("expected status 'approved' (not applied) with switch off, got %q", fetched.Status)
	}
}

// TestApproveReviewItem_ApplyGateOn_Applies verifies that flipping the switch ON makes
// approve execute the registered handler and mark the item "applied".
func TestApproveReviewItem_ApplyGateOn_Applies(t *testing.T) {
	s := newTestStore(t)
	it := seed(t, s, "regroup.multidisc", "m1")
	applied := false
	h := reviewhandler.New(s, func() bool { return true }) // switch ON
	h.RegisterApplyHandler("regroup.multidisc", func(_ context.Context, _ database.ReviewItem) error {
		applied = true
		return nil
	})

	w := doReq(t, h.ApproveReviewItem, http.MethodPost, "/review/items/"+it.ID+"/approve", nil,
		gin.Params{{Key: "id", Value: it.ID}})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !applied {
		t.Fatal("apply handler must run when the global apply switch is on")
	}
	fetched, _ := s.GetReviewItem(it.ID)
	if fetched.Status != database.ReviewStatusApplied {
		t.Fatalf("expected status 'applied' with switch on, got %q", fetched.Status)
	}
}

func TestApproveReviewItem_NotFound(t *testing.T) {
	s := newTestStore(t)
	h := reviewhandler.New(s, func() bool { return true })
	w := doReq(t, h.ApproveReviewItem, http.MethodPost, "/review/items/missing/approve", nil,
		gin.Params{{Key: "id", Value: "missing"}})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRejectReviewItem(t *testing.T) {
	s := newTestStore(t)
	it := seed(t, s, "regroup.anthology", "a1")
	h := reviewhandler.New(s, func() bool { return true })

	w := doReq(t, h.RejectReviewItem, http.MethodPost, "/review/items/"+it.ID+"/reject", nil,
		gin.Params{{Key: "id", Value: it.ID}})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	fetched, _ := s.GetReviewItem(it.ID)
	if fetched.Status != database.ReviewStatusRejected {
		t.Fatalf("expected persisted rejected status, got %q", fetched.Status)
	}
}

func TestBulkReviewAction_RejectByKind(t *testing.T) {
	s := newTestStore(t)
	seed(t, s, "regroup.multidisc", "m1")
	seed(t, s, "regroup.multidisc", "m2")
	seed(t, s, "regroup.anthology", "a1")
	h := reviewhandler.New(s, func() bool { return true })

	w := doReq(t, h.BulkReviewAction, http.MethodPost, "/review/bulk",
		map[string]any{"action": "reject", "kind": "regroup.multidisc"}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	data := decodeBody(t, w)["data"].(map[string]any)
	if data["processed"].(float64) != 2 {
		t.Fatalf("expected 2 processed, got %v", data["processed"])
	}
	// The anthology item must be untouched.
	if got, _ := s.CountReviewItems(database.ReviewStatusPending); got != 1 {
		t.Fatalf("expected 1 pending remaining, got %d", got)
	}
	if got, _ := s.CountReviewItems(database.ReviewStatusRejected); got != 2 {
		t.Fatalf("expected 2 rejected, got %d", got)
	}
}

func TestBulkReviewAction_ByIDs(t *testing.T) {
	s := newTestStore(t)
	a := seed(t, s, "regroup.multidisc", "m1")
	b := seed(t, s, "regroup.multidisc", "m2")
	h := reviewhandler.New(s, func() bool { return true })

	w := doReq(t, h.BulkReviewAction, http.MethodPost, "/review/bulk",
		map[string]any{"action": "approve", "ids": []string{a.ID, b.ID}}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	data := decodeBody(t, w)["data"].(map[string]any)
	if data["processed"].(float64) != 2 {
		t.Fatalf("expected 2 processed, got %v", data["processed"])
	}
}

func TestBulkReviewAction_RejectsUnscoped(t *testing.T) {
	s := newTestStore(t)
	seed(t, s, "regroup.multidisc", "m1")
	h := reviewhandler.New(s, func() bool { return true })

	// Neither kind nor ids → must be refused, and nothing changed.
	w := doReq(t, h.BulkReviewAction, http.MethodPost, "/review/bulk",
		map[string]any{"action": "reject"}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unscoped bulk, got %d: %s", w.Code, w.Body.String())
	}
	if got, _ := s.CountReviewItems(database.ReviewStatusPending); got != 1 {
		t.Fatalf("unscoped bulk must not mutate; expected 1 pending, got %d", got)
	}
}

func TestBulkReviewAction_InvalidAction(t *testing.T) {
	s := newTestStore(t)
	h := reviewhandler.New(s, func() bool { return true })
	w := doReq(t, h.BulkReviewAction, http.MethodPost, "/review/bulk",
		map[string]any{"action": "delete", "kind": "regroup.multidisc"}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid action, got %d", w.Code)
	}
}
