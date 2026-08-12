// file: internal/server/handlers/activity_cancel_test.go
// version: 1.0.0
// guid: 5c48f1a9-2d76-4b03-8e91-6a3fd027b8ce
// last-edited: 2026-08-11

// Package handlers_test — the activity handlers' behaviour when the client
// disconnects mid-scan.
//
// The store-level guarantee (the scan stops) is proved in
// internal/database/pebble_activity_cancel_test.go. What is proved HERE is the
// other half: that the handler translates a cancelled scan into a response
// that cannot be mistaken for real data.
//
// The specific trap these guard is Gin's default status of 200. A handler that
// notices the cancellation and simply stops — a bare c.Abort(), or a plain
// return — still emits 200. A caller receiving 200 with an empty entries array
// has no way to tell "the log really is empty" from "your scan was killed",
// and will happily render or cache the emptiness as fact. So the assertion is
// on the status code, not merely on the absence of a payload.
package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// statusClientClosedRequest mirrors the handler's unexported constant (nginx's
// 499). Spelled out rather than exported from the production package: the wire
// contract is what the test should pin, so that renaming or changing the
// constant cannot quietly move the expectation with it.
const statusClientClosedRequest = 499

// newActivityCancelCtx builds a gin context whose request context is already
// cancelled, standing in for a client that has disconnected.
//
// The cancelled context must be attached to the REQUEST, not merely created:
// httptest.NewRequest leaves Request.ctx nil, so Request.Context() would
// otherwise hand back context.Background() and the cancelledCtx matcher below
// would have nothing to distinguish.
func newActivityCancelCtx(method, path string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(method, path, nil).WithContext(ctx)
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	return c, w
}

// cancelledCtx matches only a context that is actually cancelled.
//
// This is the load-bearing half of every expectation below. Using
// mock.Anything for the context argument would let a handler that passed
// context.Background() — precisely the bug this work exists to remove — go on
// satisfying the mock forever, which is how a test on the exact endpoint can
// pass for the entire life of the bug.
func cancelledCtx() any {
	return mock.MatchedBy(func(ctx context.Context) bool {
		return ctx != nil && ctx.Err() != nil
	})
}

// TestListActivity_CancelledScanDoesNotReturn200 is the endpoint that produced
// the production heap profile: GET /api/v1/activity.
func TestListActivity_CancelledScanDoesNotReturn200(t *testing.T) {
	svc := handlersmocks.NewMockActivityService(t)

	// The filter matcher stays exact. Widening it to mock.Anything would let
	// the test keep passing if the handler silently stopped honouring the
	// caller's paging or filters.
	wantFilter := database.ActivityFilter{Limit: 5, Offset: 0}
	svc.EXPECT().
		Query(cancelledCtx(), wantFilter).
		Return(nil, 0, context.Canceled).
		Once()

	h := handlers.NewActivityHandler(svc, nil)
	c, w := newActivityCancelCtx(http.MethodGet, "/api/v1/activity?limit=5")
	h.ListActivity(c)

	assert.Equal(t, statusClientClosedRequest, w.Code,
		"a cancelled scan must not be reported as success")
	assert.NotEqual(t, http.StatusOK, w.Code)
	assert.Empty(t, w.Body.String(), "no body should be written for an abandoned request")
	assert.True(t, c.IsAborted(), "the handler chain must be aborted")
}

// TestListActivitySources_CancelledScanDoesNotReturn200 covers the sources
// endpoint, which ran concurrently with the query on every page load and
// contributed 3.21 GB of its own to the OOM heap.
func TestListActivitySources_CancelledScanDoesNotReturn200(t *testing.T) {
	svc := handlersmocks.NewMockActivityService(t)

	wantFilter := database.ActivityFilter{}
	svc.EXPECT().
		GetDistinctSources(cancelledCtx(), wantFilter).
		Return(nil, context.Canceled).
		Once()

	h := handlers.NewActivityHandler(svc, nil)
	c, w := newActivityCancelCtx(http.MethodGet, "/api/v1/activity/sources")
	h.ListActivitySources(c)

	assert.Equal(t, statusClientClosedRequest, w.Code)
	assert.Empty(t, w.Body.String())
	assert.True(t, c.IsAborted())
}

// TestListOperationActivity_CancelledScanDoesNotReturn200 covers the
// op-transcript endpoint. This one matters disproportionately: it takes the
// queryByIndexPrefix fast path rather than the bounded merge scan, so it is
// the endpoint most likely to be left behind by a partial fix.
//
// It also pins that a cancelled scan must NOT fall through to the op-log
// fallback. The fallback triggers on len(entries) == 0, and a cancelled query
// returns zero entries — so a handler that checked the length before the error
// would answer an abandoned request with a full, wrong, 200 transcript
// assembled from a different data source.
func TestListOperationActivity_CancelledScanDoesNotReturn200(t *testing.T) {
	svc := handlersmocks.NewMockActivityService(t)

	wantFilter := database.ActivityFilter{OperationID: "op-123", Limit: 1000}
	svc.EXPECT().
		Query(cancelledCtx(), wantFilter).
		Return(nil, 0, context.Canceled).
		Once()

	// opsStore is deliberately nil: if the handler ever reached the fallback,
	// that is a behaviour change this test should surface rather than mask.
	h := handlers.NewActivityHandler(svc, nil)
	c, w := newActivityCancelCtx(http.MethodGet, "/api/v1/operations/op-123/activity")
	c.Params = gin.Params{{Key: "id", Value: "op-123"}}
	h.ListOperationActivity(c)

	assert.Equal(t, statusClientClosedRequest, w.Code)
	assert.Empty(t, w.Body.String())
	assert.True(t, c.IsAborted())
}

// TestListActivity_RealErrorStillReturns500 is the counterweight to the tests
// above: only cancellation gets the 499 treatment. A genuine store failure must
// still surface as a server error, or the fix would convert every activity-log
// fault into a silent non-response.
func TestListActivity_RealErrorStillReturns500(t *testing.T) {
	svc := handlersmocks.NewMockActivityService(t)

	wantFilter := database.ActivityFilter{Limit: 5, Offset: 0}
	svc.EXPECT().
		Query(cancelledCtx(), wantFilter).
		Return(nil, 0, assert.AnError).
		Once()

	h := handlers.NewActivityHandler(svc, nil)
	c, w := newActivityCancelCtx(http.MethodGet, "/api/v1/activity?limit=5")
	h.ListActivity(c)

	require.NotEqual(t, statusClientClosedRequest, w.Code,
		"a real failure must not be disguised as a client disconnect")
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}
