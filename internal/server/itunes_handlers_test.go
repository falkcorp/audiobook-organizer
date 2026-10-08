// file: internal/server/itunes_handlers_test.go
// version: 1.4.0
// guid: 3a4b5c6d-7e8f-9a0b-1c2d-3e4f5a6b7c8d
// last-edited: 2026-10-08

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// TestITunesDisabled_ReturnsServiceUnavailable proves that with a nil
// itunesSvc every iTunes endpoint returns 503 and setupRoutes never panics.
// Uses a bare &Server{} (no NewServer) to keep itunesSvc nil without
// needing any DB or queue — mirrors the approach in server_queue_test.go.
func TestITunesDisabled_ReturnsServiceUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)

	srv := &Server{router: gin.New()}
	srv.setupRoutes() // must not panic with nil itunesSvc

	cases := []struct {
		method string
		path   string
	}{
		// Handlers that call itunesEnabledOrError at the top
		{http.MethodPost, "/api/v1/itunes/import"},
		{http.MethodGet, "/api/v1/itunes/import-status/fake-op"},
		{http.MethodPost, "/api/v1/itunes/import-status/bulk"},
		// Routes registered via itunesSvcGuard (sub-component method pointers)
		{http.MethodGet, "/api/v1/itunes/library/download"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			srv.router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusServiceUnavailable, w.Code,
				"disabled iTunes must return 503")
			assert.Contains(t, w.Body.String(), "disabled",
				"response body must mention 'disabled'")
		})
	}
}
