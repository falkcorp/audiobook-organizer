// file: internal/server/handlers/operations_v2_test.go
// version: 1.0.0
// guid: b2c3d4e5-f6a7-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-06-03

package handlers_test

import (
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
	h := handlers.NewOperationsV2Handler(nil, registry, nil)

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

	h := handlers.NewOperationsV2Handler(store, registry, nil)
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=15m", "", nil)
	h.GetOperationTimeline(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "op1")
}

func TestOperationsV2Handler_GetOperationTimeline_BadSince(t *testing.T) {
	h := handlers.NewOperationsV2Handler(nil, nil, nil)
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=notaduration", "", nil)
	h.GetOperationTimeline(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ── GetOperationV2 ────────────────────────────────────────────────────────

func TestOperationsV2Handler_GetOperationV2_NilStore(t *testing.T) {
	h := handlers.NewOperationsV2Handler(nil, nil, nil)
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

	h := handlers.NewOperationsV2Handler(store, registry, nil)
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/v2/op1", "", gin.Params{{Key: "id", Value: "op1"}})
	h.GetOperationV2(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "op1")
}

func TestOperationsV2Handler_GetOperationV2_NotFound(t *testing.T) {
	store := databasemocks.NewMockOpsV2Store(t)
	store.EXPECT().GetOperationV2("missing").Return(nil, nil)

	h := handlers.NewOperationsV2Handler(store, nil, nil)
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/v2/missing", "", gin.Params{{Key: "id", Value: "missing"}})
	h.GetOperationV2(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ── CancelOperationV2 ─────────────────────────────────────────────────────

func TestOperationsV2Handler_CancelOperationV2_NilRegistry(t *testing.T) {
	h := handlers.NewOperationsV2Handler(nil, nil, nil)
	c, w := newOpsV2Ctx(http.MethodDelete, "/operations/v2/op1", "", gin.Params{{Key: "id", Value: "op1"}})
	h.CancelOperationV2(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestOperationsV2Handler_CancelOperationV2_Success(t *testing.T) {
	registry := handlersmocks.NewMockOperationsRegistry(t)
	registry.EXPECT().Cancel("op1").Return(nil)

	h := handlers.NewOperationsV2Handler(nil, registry, nil)
	c, w := newOpsV2Ctx(http.MethodDelete, "/operations/v2/op1", "", gin.Params{{Key: "id", Value: "op1"}})
	h.CancelOperationV2(c)

	assert.Equal(t, http.StatusNoContent, w.Code)
}

// ── TriggerOperationV2 ────────────────────────────────────────────────────

func TestOperationsV2Handler_TriggerOperationV2_NilRegistry(t *testing.T) {
	h := handlers.NewOperationsV2Handler(nil, nil, nil)
	c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2", `{"def_id":"library.scan"}`, nil)
	h.TriggerOperationV2(c)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestOperationsV2Handler_TriggerOperationV2_MissingDefID(t *testing.T) {
	registry := handlersmocks.NewMockOperationsRegistry(t)
	// EnqueueOp must never be reached.
	h := handlers.NewOperationsV2Handler(nil, registry, nil)
	c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2", `{}`, nil)
	h.TriggerOperationV2(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestOperationsV2Handler_TriggerOperationV2_Success(t *testing.T) {
	registry := handlersmocks.NewMockOperationsRegistry(t)
	registry.EXPECT().EnqueueOp(mock.Anything, "library.scan", mock.Anything).Return("op42", nil)

	h := handlers.NewOperationsV2Handler(nil, registry, nil)
	c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2", `{"def_id":"library.scan","params":{"foo":"bar"}}`, nil)
	h.TriggerOperationV2(c)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Contains(t, w.Body.String(), "op42")
}

// ── ListOpDefs ────────────────────────────────────────────────────────────

func TestOperationsV2Handler_ListOpDefs_NilRegistry(t *testing.T) {
	h := handlers.NewOperationsV2Handler(nil, nil, nil)
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

	h := handlers.NewOperationsV2Handler(nil, registry, nil)
	c, w := newOpsV2Ctx(http.MethodGet, "/op-defs", "", nil)
	h.ListOpDefs(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "library.scan")
}

// ── GetOpDef ──────────────────────────────────────────────────────────────

func TestOperationsV2Handler_GetOpDef_NilRegistry(t *testing.T) {
	h := handlers.NewOperationsV2Handler(nil, nil, nil)
	c, w := newOpsV2Ctx(http.MethodGet, "/op-defs/library.scan", "", gin.Params{{Key: "id", Value: "library.scan"}})
	h.GetOpDef(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestOperationsV2Handler_GetOpDef_Found(t *testing.T) {
	registry := handlersmocks.NewMockOperationsRegistry(t)
	registry.EXPECT().ActiveDefs().Return([]opsregistry.OperationDef{
		{ID: "library.scan", DisplayName: "Library Scan"},
	})

	h := handlers.NewOperationsV2Handler(nil, registry, nil)
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

	h := handlers.NewOperationsV2Handler(nil, registry, nil)
	c, w := newOpsV2Ctx(http.MethodGet, "/op-defs/missing", "", gin.Params{{Key: "id", Value: "missing"}})
	h.GetOpDef(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ── OperationsSSE ─────────────────────────────────────────────────────────

func TestOperationsV2Handler_OperationsSSE_NilHub(t *testing.T) {
	h := handlers.NewOperationsV2Handler(nil, nil, nil)
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

	h := handlers.NewOperationsV2Handler(nil, nil, hub)
	// The channel is closed, so the SSE loop drains the one queued event then
	// exits when the receive reports !ok (no need to cancel the request context).
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/events", "", nil)
	h.OperationsSSE(c)

	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, ": heartbeat")
	assert.Contains(t, body, "event: op.created")
}
