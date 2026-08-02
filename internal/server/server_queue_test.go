// file: internal/server/server_queue_test.go
// version: 2.0.1
// guid: b1c2d3e4-f5a6-7890-bcde-f01234567890
// last-edited: 2026-05-11

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// TestCancelOperation_NilRegistry verifies that when opRegistry is nil the cancel
// handler falls back to a DB force-update and returns 204.
func TestCancelOperation_NilRegistry(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockStore := dbmocks.NewMockStore(t)
	mockStore.EXPECT().
		UpdateOperationStatus("test-op-789", "canceled", 0, 0, "force canceled (stale operation)").
		Return(nil).Once()

	srv := &Server{router: gin.New(), store: mockStore} // opRegistry left nil
	srv.setupRoutes()

	req := httptest.NewRequest("DELETE", "/api/v1/operations/test-op-789", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
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
