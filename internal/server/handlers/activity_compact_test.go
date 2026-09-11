// file: internal/server/handlers/activity_compact_test.go
// version: 1.1.0
// guid: e2c7a9f3-4b61-4d0e-8f5a-6c3b9d1e7a48
// last-edited: 2026-09-10

package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

type fakeCompactEnqueuer struct {
	gotDef    string
	gotParams any
	returnID  string
}

func (f *fakeCompactEnqueuer) EnqueueOp(_ context.Context, defID string, params any, _ ...opsregistry.EnqueueOption) (string, error) {
	f.gotDef = defID
	f.gotParams = params
	return f.returnID, nil
}

type fakeActiveLister struct{ rows []database.OperationV2Row }

func (f fakeActiveLister) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	return f.rows, nil
}

func postCompact(t *testing.T, h *handlers.ActivityCompactHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/activity/compact", h.CompactActivity)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/activity/compact", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestCompactActivity_EnqueuesAndReturns202(t *testing.T) {
	enq := &fakeCompactEnqueuer{returnID: "op-123"}
	h := handlers.NewActivityCompactHandler(enq, fakeActiveLister{})

	w := postCompact(t, h, `{"older_than_days": 14}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s; want 202", w.Code, w.Body.String())
	}
	if enq.gotDef != maintenance.CompactActivityLogDefID {
		t.Errorf("enqueued def = %q, want %q", enq.gotDef, maintenance.CompactActivityLogDefID)
	}
	params, ok := enq.gotParams.(maintenance.CompactActivityLogParams)
	if !ok || params.OlderThanDays != 14 {
		t.Errorf("enqueued params = %#v, want OlderThanDays=14", enq.gotParams)
	}

	var body struct {
		Data handlers.CompactStartedResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.OperationID != "op-123" || body.Data.DefID != maintenance.CompactActivityLogDefID || body.Data.Status != "queued" {
		t.Errorf("data = %+v", body.Data)
	}
}

func TestCompactActivity_RejectsNegativeDays(t *testing.T) {
	enq := &fakeCompactEnqueuer{returnID: "op-123"}
	h := handlers.NewActivityCompactHandler(enq, fakeActiveLister{})

	w := postCompact(t, h, `{"older_than_days": -3}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", w.Code)
	}
	if enq.gotDef != "" {
		t.Error("an invalid request must not reach the registry")
	}
}

func TestCompactActivity_409WhenDedupedOntoRunningOp(t *testing.T) {
	// The registry hands back the id of the run already in flight; the handler
	// recognises it from the active rows it saw before enqueueing.
	enq := &fakeCompactEnqueuer{returnID: "op-running"}
	lister := fakeActiveLister{rows: []database.OperationV2Row{
		{ID: "op-running", DefID: maintenance.CompactActivityLogDefID, Status: "running"},
	}}
	h := handlers.NewActivityCompactHandler(enq, lister)

	w := postCompact(t, h, `{"older_than_days": 0}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, body %s; want 409", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "op-running") {
		t.Errorf("409 body should name the running op: %s", w.Body.String())
	}
}

func TestCompactActivity_500WithoutRegistry(t *testing.T) {
	h := handlers.NewActivityCompactHandler(nil, nil)
	w := postCompact(t, h, `{"older_than_days": 0}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500", w.Code)
	}
}

func postRecompact(t *testing.T, h *handlers.ActivityCompactHandler) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/admin/recompact-digests", h.RecompactDigests)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/recompact-digests", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestRecompactDigests_EnqueuesAndReturns202 pins the shape change: the
// endpoint no longer re-derives digests inside the request, it enqueues
// maintenance.recompact-activity-digests and hands back the op id.
func TestRecompactDigests_EnqueuesAndReturns202(t *testing.T) {
	enq := &fakeCompactEnqueuer{returnID: "op-456"}
	h := handlers.NewActivityCompactHandler(enq, fakeActiveLister{})

	w := postRecompact(t, h)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s; want 202", w.Code, w.Body.String())
	}
	if enq.gotDef != maintenance.RecompactActivityDigestsDefID {
		t.Errorf("enqueued def = %q, want %q", enq.gotDef, maintenance.RecompactActivityDigestsDefID)
	}
	var body struct {
		Data handlers.CompactStartedResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.OperationID != "op-456" || body.Data.DefID != maintenance.RecompactActivityDigestsDefID || body.Data.Status != "queued" {
		t.Errorf("data = %+v", body.Data)
	}
}

func TestRecompactDigests_409WhenDedupedOntoRunningOp(t *testing.T) {
	enq := &fakeCompactEnqueuer{returnID: "op-running"}
	lister := fakeActiveLister{rows: []database.OperationV2Row{
		{ID: "op-running", DefID: maintenance.RecompactActivityDigestsDefID, Status: "running"},
	}}
	h := handlers.NewActivityCompactHandler(enq, lister)

	w := postRecompact(t, h)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, body %s; want 409", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "op-running") {
		t.Errorf("409 body should name the running op: %s", w.Body.String())
	}
}

// A live COMPACTION must not make a recompact request read as a duplicate:
// the dedupe is per def id, not per handler.
func TestRecompactDigests_NotDedupedAgainstCompaction(t *testing.T) {
	enq := &fakeCompactEnqueuer{returnID: "op-new"}
	lister := fakeActiveLister{rows: []database.OperationV2Row{
		{ID: "op-new", DefID: maintenance.CompactActivityLogDefID, Status: "running"},
	}}
	h := handlers.NewActivityCompactHandler(enq, lister)

	if w := postRecompact(t, h); w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s; want 202", w.Code, w.Body.String())
	}
}

func TestRecompactDigests_500WithoutRegistry(t *testing.T) {
	h := handlers.NewActivityCompactHandler(nil, nil)
	if w := postRecompact(t, h); w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500", w.Code)
	}
}
