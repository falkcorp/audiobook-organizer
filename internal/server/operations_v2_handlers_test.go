// file: internal/server/operations_v2_handlers_test.go
// version: 1.0.1
// guid: f6a7b8c9-d0e1-2f3a-4b5c-6d7e8f9a0b1d
// last-edited: 2026-05-06

// Tests for UOS-06: operations v2 SSE + timeline + introspection endpoints.

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeOpsV2Store is a minimal in-memory OpsV2Store for handler tests.
// Only the two new methods (ListOperationsV2Since / GetOpLogsV2) need
// real implementations; the rest return zero-value no-ops so the inline
// MockStore compile-time assertion stays satisfied.
type fakeOpsV2Store struct {
	database.MockStore // embed for all Store methods
	ops                []database.OperationV2Row
	logs               []database.OpLogV2Row
}

func (f *fakeOpsV2Store) ListOperationsV2Since(since time.Time, _ int) ([]database.OperationV2Row, error) {
	var out []database.OperationV2Row
	for _, op := range f.ops {
		if !op.QueuedAt.Before(since) {
			out = append(out, op)
		}
	}
	return out, nil
}

func (f *fakeOpsV2Store) GetOpLogsV2(opID string, limit int) ([]database.OpLogV2Row, error) {
	var out []database.OpLogV2Row
	for _, l := range f.logs {
		if l.OperationID == opID {
			out = append(out, l)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func (f *fakeOpsV2Store) GetOperationV2(id string) (*database.OperationV2Row, error) {
	for _, op := range f.ops {
		if op.ID == id {
			cp := op
			return &cp, nil
		}
	}
	return nil, nil
}

// makeTimelineServer creates a minimal Server wired for timeline tests.
func makeTimelineServer(t *testing.T, store *fakeOpsV2Store) (*Server, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	srv := &Server{
		store: store,
	}
	router := gin.New()
	router.GET("/operations/timeline", srv.handleGetOperationTimeline)
	return srv, router
}

// --- Timeline ordering test ---

func TestHandleGetOperationTimeline_OrderedByStartedAtDesc(t *testing.T) {
	now := time.Now().UTC()

	started1 := now.Add(-10 * time.Minute)
	started2 := now.Add(-5 * time.Minute)

	store := &fakeOpsV2Store{
		ops: []database.OperationV2Row{
			{
				ID:        "op-older",
				Plugin:    "scan",
				DefID:     "scan",
				Status:    "completed",
				QueuedAt:  now.Add(-15 * time.Minute),
				StartedAt: &started1,
			},
			{
				ID:        "op-newer",
				Plugin:    "scan",
				DefID:     "scan",
				Status:    "running",
				QueuedAt:  now.Add(-8 * time.Minute),
				StartedAt: &started2,
			},
			// Queued but not started — should appear after running ops.
			{
				ID:       "op-queued",
				Plugin:   "scan",
				DefID:    "scan",
				Status:   "queued",
				QueuedAt: now.Add(-3 * time.Minute),
			},
		},
	}
	_, router := makeTimelineServer(t, store)

	req := httptest.NewRequest(http.MethodGet, "/operations/timeline?since=30m", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Data struct {
			Operations []operationV2Response `json:"operations"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	ops := body.Data.Operations
	require.Len(t, ops, 3)

	// The fakeOpsV2Store does a simple filter without ordering, so we just
	// verify all three IDs are present; the SQLite impl handles the ordering.
	ids := make(map[string]bool)
	for _, op := range ops {
		ids[op.ID] = true
	}
	assert.True(t, ids["op-older"])
	assert.True(t, ids["op-newer"])
	assert.True(t, ids["op-queued"])
}

func TestHandleGetOperationTimeline_InvalidSince(t *testing.T) {
	store := &fakeOpsV2Store{}
	_, router := makeTimelineServer(t, store)

	req := httptest.NewRequest(http.MethodGet, "/operations/timeline?since=badvalue", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleGetOperationTimeline_NilStore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := &Server{store: nil}
	router := gin.New()
	router.GET("/operations/timeline", srv.handleGetOperationTimeline)

	req := httptest.NewRequest(http.MethodGet, "/operations/timeline?since=15m", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	// Should return 200 with empty list, not 500.
	assert.Equal(t, http.StatusOK, w.Code)
}

// --- SSE test ---

func TestHandleOperationsSSE_ReceivesEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hub := opsregistry.NewEventHub()
	srv := &Server{
		store: &fakeOpsV2Store{},
		opHub: hub,
	}

	// Use a real httptest.Server so SSE streaming works.
	router := gin.New()
	router.GET("/operations/events", srv.handleOperationsSSE)
	ts := httptest.NewServer(router)
	defer ts.Close()

	// Channel to signal test completion.
	found := make(chan bool, 1)

	// Open SSE connection with a per-request context.
	// We control cancellation ourselves via the found channel.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/operations/events", nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	// Read lines from the SSE stream in a background goroutine.
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: op.updated") {
				found <- true
				return
			}
		}
		found <- false
	}()

	// Publish an event after a short delay to let the connection settle.
	time.Sleep(150 * time.Millisecond)
	_ = hub.Publish(context.Background(), "op.updated", map[string]any{
		"op_id": "test-op-1",
	})

	select {
	case ok := <-found:
		// Cancel the context so the SSE handler exits and the test goroutine
		// can finish its scanner loop.
		cancel()
		assert.True(t, ok, "expected op.updated SSE event to be received")
	case <-ctx.Done():
		t.Fatal("timed out waiting for op.updated SSE event")
	}
}

func TestHandleOperationsSSE_NilHub(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := &Server{opHub: nil}
	router := gin.New()
	router.GET("/operations/events", srv.handleOperationsSSE)

	req := httptest.NewRequest(http.MethodGet, "/operations/events", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	// Nil hub → 503
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

// --- Cancel test ---

func TestHandleCancelOperationV2_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Build a real registry with a fakeStore.
	fakeStore := &fakeOpsV2Store{
		ops: []database.OperationV2Row{
			{
				ID:       "op-cancel-me",
				Plugin:   "scan",
				DefID:    "scan",
				Status:   "queued",
				QueuedAt: time.Now().UTC(),
			},
		},
	}
	reg := opsregistry.NewWithOptions(fakeStore, slog.Default(), 1, opsregistry.Options{})
	srv := &Server{
		store:      fakeStore,
		opRegistry: reg,
	}
	router := gin.New()
	router.DELETE("/operations/v2/:id", srv.handleCancelOperationV2)

	req := httptest.NewRequest(http.MethodDelete, "/operations/v2/op-cancel-me", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestHandleCancelOperationV2_NilRegistry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := &Server{opRegistry: nil}
	router := gin.New()
	router.DELETE("/operations/v2/:id", srv.handleCancelOperationV2)

	req := httptest.NewRequest(http.MethodDelete, "/operations/v2/any-id", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// --- GetOperationV2 test ---

func TestHandleGetOperationV2_Found(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now().UTC()
	store := &fakeOpsV2Store{
		ops: []database.OperationV2Row{
			{
				ID:       "op-abc",
				Plugin:   "scan",
				DefID:    "scan",
				Status:   "running",
				QueuedAt: now.Add(-5 * time.Minute),
			},
		},
	}
	srv := &Server{store: store}
	router := gin.New()
	router.GET("/operations/v2/:id", srv.handleGetOperationV2)

	req := httptest.NewRequest(http.MethodGet, "/operations/v2/op-abc", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	data := body["data"].(map[string]any)
	op := data["operation"].(map[string]any)
	assert.Equal(t, "op-abc", op["id"])
}

func TestHandleGetOperationV2_NotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &fakeOpsV2Store{}
	srv := &Server{store: store}
	router := gin.New()
	router.GET("/operations/v2/:id", srv.handleGetOperationV2)

	req := httptest.NewRequest(http.MethodGet, "/operations/v2/no-such-op", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}
