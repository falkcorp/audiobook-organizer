// file: internal/server/server_queue_test.go
// version: 2.1.0
// guid: b1c2d3e4-f5a6-7890-bcde-f01234567890
// last-edited: 2026-08-16

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// TestCancelOperationV2_NilRegistry verifies that cancelling with no registry
// wired reports an error rather than claiming success.
//
// This replaces TestCancelOperation_NilRegistry, which asserted the legacy
// DELETE /operations/:id behaviour: with opRegistry nil it force-updated the
// legacy `operations` row to "canceled" and returned 204. That was a lie in the
// one case it was reached — nothing had been asked to stop, and the row it
// rewrote is the same table that never transitions rows out of `pending`. The
// route is retired; the v2 route answers 500, which is honest.
func TestCancelOperationV2_NilRegistry(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// The store is still wired so the failure can only come from the nil
	// registry — a mock with no EXPECT calls fails the test if anything touches
	// it, so this also asserts the legacy force-update is really gone.
	mockStore := dbmocks.NewMockStore(t)

	srv := &Server{router: gin.New(), store: mockStore} // opRegistry left nil
	srv.setupRoutes()

	req := httptest.NewRequest("DELETE", "/api/v1/operations/v2/test-op-789", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// TestGetOperationsActive verifies GET /operations/active returns 410 Gone (UOS-14 removal).
func TestGetOperationsActive(t *testing.T) {
	gin.SetMode(gin.TestMode)

	srv := &Server{router: gin.New()}
	srv.setupRoutes()

	req := httptest.NewRequest("GET", "/api/v1/operations/active", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusGone, w.Code)
	assert.Contains(t, w.Body.String(), "gone")
}
