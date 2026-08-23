// file: internal/server/handlers/operations_v2_test.go
// version: 1.2.0
// guid: b2c3d4e5-f6a7-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-08-23

package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	databasemocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// newOpsV2Ctx builds a gin context with the given path params and an optional
// JSON request body.
func newOpsV2Ctx(method, path, body string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = params
	return c, w
}

// ── GetOperationTimeline ──────────────────────────────────────────────────

func TestOperationsV2Handler_GetOperationTimeline_NilStore(t *testing.T) {
	registry := handlersmocks.NewMockOperationsRegistry(t)
	h := handlers.NewOperationsV2Handler(nil, registry, nil, false)

	c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline", "", nil)
	h.GetOperationTimeline(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"operations"`)
}

func TestOperationsV2Handler_GetOperationTimeline_Success(t *testing.T) {
	store := databasemocks.NewMockOpsV2Store(t)
	registry := handlersmocks.NewMockOperationsRegistry(t)
	store.EXPECT().ListOperationsV2Since(mock.Anything, 200).Return([]database.OperationV2Row{
		{ID: "op1", DefID: "library.scan", Status: "queued"},
	}, nil)
	// Timeline calls displayNameFor + notifyLevelFor → ActiveDefs (status != running, so no GetCurrentItem).
	registry.EXPECT().ActiveDefs().Return([]opsregistry.OperationDef{
		{ID: "library.scan", DisplayName: "Library Scan"},
	})

	h := handlers.NewOperationsV2Handler(store, registry, nil, false)
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=15m", "", nil)
	h.GetOperationTimeline(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "op1")
}

func TestOperationsV2Handler_GetOperationTimeline_BadSince(t *testing.T) {
	h := handlers.NewOperationsV2Handler(nil, nil, nil, false)
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=notaduration", "", nil)
	h.GetOperationTimeline(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── GetOperationV2 ────────────────────────────────────────────────────────

func TestOperationsV2Handler_GetOperationV2_NilStore(t *testing.T) {
	h := handlers.NewOperationsV2Handler(nil, nil, nil, false)
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/v2/op1", "", gin.Params{{Key: "id", Value: "op1"}})
	h.GetOperationV2(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestOperationsV2Handler_GetOperationV2_Success(t *testing.T) {
	store := databasemocks.NewMockOpsV2Store(t)
	registry := handlersmocks.NewMockOperationsRegistry(t)
	store.EXPECT().GetOperationV2("op1").Return(&database.OperationV2Row{ID: "op1", DefID: "library.scan", Status: "completed"}, nil)
	store.EXPECT().GetOpLogsV2("op1", 50).Return([]database.OpLogV2Row{
		{OperationID: "op1", Level: "info", Message: "done", Attrs: "{}"},
	}, nil)
	registry.EXPECT().ActiveDefs().Return([]opsregistry.OperationDef{
		{ID: "library.scan", DisplayName: "Library Scan"},
	})

	h := handlers.NewOperationsV2Handler(store, registry, nil, false)
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/v2/op1", "", gin.Params{{Key: "id", Value: "op1"}})
	h.GetOperationV2(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "op1")
}

func TestOperationsV2Handler_GetOperationV2_NotFound(t *testing.T) {
	store := databasemocks.NewMockOpsV2Store(t)
	store.EXPECT().GetOperationV2("missing").Return(nil, nil)

	h := handlers.NewOperationsV2Handler(store, nil, nil, false)
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/v2/missing", "", gin.Params{{Key: "id", Value: "missing"}})
	h.GetOperationV2(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ── CancelOperationV2 ─────────────────────────────────────────────────────

func TestOperationsV2Handler_CancelOperationV2_NilRegistry(t *testing.T) {
	h := handlers.NewOperationsV2Handler(nil, nil, nil, false)
	c, w := newOpsV2Ctx(http.MethodDelete, "/operations/v2/op1", "", gin.Params{{Key: "id", Value: "op1"}})
	h.CancelOperationV2(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestOperationsV2Handler_CancelOperationV2_Success(t *testing.T) {
	registry := handlersmocks.NewMockOperationsRegistry(t)
	registry.EXPECT().Cancel("op1").Return(nil)

	h := handlers.NewOperationsV2Handler(nil, registry, nil, false)
	c, w := newOpsV2Ctx(http.MethodDelete, "/operations/v2/op1", "", gin.Params{{Key: "id", Value: "op1"}})
	h.CancelOperationV2(c)

	assert.Equal(t, http.StatusNoContent, w.Code)
}

// TestOperationsV2Handler_CancelOperationV2_UnknownID_Returns404 pins
// TASK-115: when the registry reports there was nothing to cancel
// (opsregistry.ErrOpNotFound), the handler must answer 404, not the old
// 204 — a caller asking to cancel an id the registry never heard of must
// be able to tell that apart from an id it actually cancelled. Paired
// with the _Success test above (nil -> 204) so the two outcomes are
// proven to genuinely differ under the same handler.
func TestOperationsV2Handler_CancelOperationV2_UnknownID_Returns404(t *testing.T) {
	registry := handlersmocks.NewMockOperationsRegistry(t)
	registry.EXPECT().Cancel("bad-id").Return(opsregistry.ErrOpNotFound)

	h := handlers.NewOperationsV2Handler(nil, registry, nil, false)
	c, w := newOpsV2Ctx(http.MethodDelete, "/operations/v2/bad-id", "", gin.Params{{Key: "id", Value: "bad-id"}})
	h.CancelOperationV2(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ── TriggerOperationV2 ────────────────────────────────────────────────────

func TestOperationsV2Handler_TriggerOperationV2_NilRegistry(t *testing.T) {
	h := handlers.NewOperationsV2Handler(nil, nil, nil, false)
	c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2", `{"def_id":"library.scan"}`, nil)
	h.TriggerOperationV2(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestOperationsV2Handler_TriggerOperationV2_MissingDefID(t *testing.T) {
	registry := handlersmocks.NewMockOperationsRegistry(t)
	// EnqueueOp must never be reached.
	h := handlers.NewOperationsV2Handler(nil, registry, nil, false)
	c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2", `{}`, nil)
	h.TriggerOperationV2(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestOperationsV2Handler_TriggerOperationV2_Success(t *testing.T) {
	registry := handlersmocks.NewMockOperationsRegistry(t)
	registry.EXPECT().EnqueueOp(mock.Anything, "library.scan", mock.Anything).Return("op42", nil)

	h := handlers.NewOperationsV2Handler(nil, registry, nil, false)
	c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2", `{"def_id":"library.scan","params":{"foo":"bar"}}`, nil)
	h.TriggerOperationV2(c)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Contains(t, w.Body.String(), "op42")
}

// ── ListOpDefs ────────────────────────────────────────────────────────────

func TestOperationsV2Handler_ListOpDefs_NilRegistry(t *testing.T) {
	h := handlers.NewOperationsV2Handler(nil, nil, nil, false)
	c, w := newOpsV2Ctx(http.MethodGet, "/op-defs", "", nil)
	h.ListOpDefs(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"defs"`)
}

func TestOperationsV2Handler_ListOpDefs_Success(t *testing.T) {
	registry := handlersmocks.NewMockOperationsRegistry(t)
	registry.EXPECT().ActiveDefs().Return([]opsregistry.OperationDef{
		{ID: "library.scan", DisplayName: "Library Scan"},
	})

	h := handlers.NewOperationsV2Handler(nil, registry, nil, false)
	c, w := newOpsV2Ctx(http.MethodGet, "/op-defs", "", nil)
	h.ListOpDefs(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "library.scan")
}

// ── GetOpDef ──────────────────────────────────────────────────────────────

func TestOperationsV2Handler_GetOpDef_NilRegistry(t *testing.T) {
	h := handlers.NewOperationsV2Handler(nil, nil, nil, false)
	c, w := newOpsV2Ctx(http.MethodGet, "/op-defs/library.scan", "", gin.Params{{Key: "id", Value: "library.scan"}})
	h.GetOpDef(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestOperationsV2Handler_GetOpDef_Found(t *testing.T) {
	registry := handlersmocks.NewMockOperationsRegistry(t)
	registry.EXPECT().ActiveDefs().Return([]opsregistry.OperationDef{
		{ID: "library.scan", DisplayName: "Library Scan"},
	})

	h := handlers.NewOperationsV2Handler(nil, registry, nil, false)
	c, w := newOpsV2Ctx(http.MethodGet, "/op-defs/library.scan", "", gin.Params{{Key: "id", Value: "library.scan"}})
	h.GetOpDef(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "library.scan")
}

func TestOperationsV2Handler_GetOpDef_NotFound(t *testing.T) {
	registry := handlersmocks.NewMockOperationsRegistry(t)
	registry.EXPECT().ActiveDefs().Return([]opsregistry.OperationDef{
		{ID: "library.scan", DisplayName: "Library Scan"},
	})

	h := handlers.NewOperationsV2Handler(nil, registry, nil, false)
	c, w := newOpsV2Ctx(http.MethodGet, "/op-defs/missing", "", gin.Params{{Key: "id", Value: "missing"}})
	h.GetOpDef(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ── OperationsSSE ─────────────────────────────────────────────────────────

func TestOperationsV2Handler_OperationsSSE_NilHub(t *testing.T) {
	h := handlers.NewOperationsV2Handler(nil, nil, nil, false)
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/events", "", nil)
	h.OperationsSSE(c)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestOperationsV2Handler_OperationsSSE_StreamThenDisconnect(t *testing.T) {
	hub := handlersmocks.NewMockOperationsEventHub(t)
	ch := make(chan opsregistry.Event, 1)
	ch <- opsregistry.Event{Name: "op.created", Payload: map[string]any{"id": "op1"}}
	close(ch)
	var roChan <-chan opsregistry.Event = ch
	hub.EXPECT().Subscribe().Return(roChan, func() {})

	h := handlers.NewOperationsV2Handler(nil, nil, hub, false)
	// The channel is closed, so the SSE loop drains the one queued event then
	// exits when the receive reports !ok (no need to cancel the request context).
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/events", "", nil)
	h.OperationsSSE(c)

	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, ": heartbeat")
	assert.Contains(t, body, "event: op.created")
}

// TestTriggerOperationV2_ParamsArriveDecodable carries forward the contract that
// launch_params_test.go used to protect for the retired /operations/{scan,
// organize,transcode} shims.
//
// Those shims read the raw request body as []byte and handed it to
// EnqueueOp(params any), which json.Marshal's its argument — and marshalling a
// []byte BASE64-ENCODES it. A body of {"book_ids":["b1"]} was stored as the JSON
// string "eyJib29rX2lkcyI6...", every op decoding it hit "cannot unmarshal string
// into Go value of type ...", and before wave 3 that error was discarded, leaving
// the params struct zero — so an organize with an explicit book_ids list ran with
// BookIDs nil, which downstream means the WHOLE LIBRARY.
//
// Deleting the shims removes that specific defect by construction: params arrive
// here already decoded into `any`, so there is no []byte to mis-marshal. This test
// exists so "by construction" is asserted rather than assumed, on the one path
// that now carries every trigger.
//
// It deliberately CAPTURES the enqueued params instead of matching mock.Anything.
// The sibling success test above passes mock.Anything for this argument, which
// means it would go green against the base64 bug too — matching on Anything is
// how a payload defect hides from a test that appears to cover it.
func TestTriggerOperationV2_ParamsArriveDecodable(t *testing.T) {
	var captured any
	registry := handlersmocks.NewMockOperationsRegistry(t)
	registry.EXPECT().EnqueueOp(mock.Anything, "library.organize", mock.Anything).
		RunAndReturn(func(_ context.Context, _ string, params any, _ ...opsregistry.EnqueueOption) (string, error) {
			captured = params
			return "op-1", nil
		})

	h := handlers.NewOperationsV2Handler(nil, registry, nil, false)
	c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2",
		`{"def_id":"library.organize","params":{"book_ids":["b1","b2"],"fetch_metadata_first":true}}`, nil)
	h.TriggerOperationV2(c)
	require.Equal(t, http.StatusAccepted, w.Code)

	// Round-trip exactly as the registry does before handing params to the op.
	raw, err := json.Marshal(captured)
	require.NoError(t, err)
	require.False(t, bytes.HasPrefix(bytes.TrimSpace(raw), []byte(`"`)),
		"params marshalled to a JSON STRING (%s) — that is the base64 shape the "+
			"retired shims produced, and every op decoding it silently got zero values", raw)

	var got struct {
		BookIDs            []string `json:"book_ids"`
		FetchMetadataFirst bool     `json:"fetch_metadata_first"`
	}
	require.NoError(t, json.Unmarshal(raw, &got),
		"the op must be able to decode what the trigger enqueued")
	require.Equal(t, []string{"b1", "b2"}, got.BookIDs,
		"an explicit selection must survive the trigger; nil here means WHOLE LIBRARY downstream")
	require.True(t, got.FetchMetadataFirst, "flags must survive the trigger too")
}

// fakeScanCanceler / fakeScanLister are hand-written because the interfaces are
// two methods between them and the assertion is about WHICH scan id is passed.
type fakeScanCanceler struct{ canceled []int }

func (f *fakeScanCanceler) CancelScan(scanID int) error {
	f.canceled = append(f.canceled, scanID)
	return nil
}

type fakeScanLister struct {
	scans []database.Scan
	err   error
}

func (f *fakeScanLister) ListScans() ([]database.Scan, error) { return f.scans, f.err }

// TestCancelOperationV2_CancelsAnAIScanThroughThePipeline covers the branch
// ported from the legacy DELETE /operations/:id.
//
// An AI scan is driven by the pipeline manager, not the ops registry. Only the
// legacy route knew that; CancelOperationV2 went straight to registry.Cancel.
// Retiring the legacy route without carrying this over would have left the
// cancel button answering 204 while the scan kept running — a silent break, and
// the reason the port had to land BEFORE the deletion rather than after.
//
// The registry mock has NO Cancel expectation: mockery fails the test if the
// handler falls through to it, which is the half that proves the AI-scan branch
// actually took priority rather than merely also running.
func TestCancelOperationV2_CancelsAnAIScanThroughThePipeline(t *testing.T) {
	canceler := &fakeScanCanceler{}
	lister := &fakeScanLister{scans: []database.Scan{
		{ID: 7, OperationID: "other-op"},
		{ID: 42, OperationID: "op-abc"},
	}}
	registry := handlersmocks.NewMockOperationsRegistry(t)

	h := handlers.NewOperationsV2Handler(nil, registry, nil, false,
		handlers.WithAIScanCancellation(canceler, lister))
	c, w := newOpsV2Ctx(http.MethodDelete, "/operations/v2/op-abc", "", nil)
	c.Params = gin.Params{{Key: "id", Value: "op-abc"}}
	h.CancelOperationV2(c)

	require.Equal(t, http.StatusNoContent, w.Code)
	require.Equal(t, []int{42}, canceler.canceled,
		"the scan matched by OperationID must be the one cancelled — OperationID is "+
			"the only link between an operation and its AI scan")
}

// TestCancelOperationV2_FallsThroughToTheRegistryForAnOrdinaryOp is the other
// half: the AI-scan branch must not swallow every cancel. An op with no matching
// scan still has to reach the registry.
func TestCancelOperationV2_FallsThroughToTheRegistryForAnOrdinaryOp(t *testing.T) {
	canceler := &fakeScanCanceler{}
	lister := &fakeScanLister{scans: []database.Scan{{ID: 7, OperationID: "some-other-op"}}}
	registry := handlersmocks.NewMockOperationsRegistry(t)
	registry.EXPECT().Cancel("op-xyz").Return(nil).Once()

	h := handlers.NewOperationsV2Handler(nil, registry, nil, false,
		handlers.WithAIScanCancellation(canceler, lister))
	c, w := newOpsV2Ctx(http.MethodDelete, "/operations/v2/op-xyz", "", nil)
	c.Params = gin.Params{{Key: "id", Value: "op-xyz"}}
	h.CancelOperationV2(c)

	require.Equal(t, http.StatusNoContent, w.Code)
	require.Empty(t, canceler.canceled, "no scan matched; the pipeline must not be touched")
}
