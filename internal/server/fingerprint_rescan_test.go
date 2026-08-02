// file: internal/server/fingerprint_rescan_test.go
// version: 1.0.0
// guid: 3a8c1b59-7e6f-4d12-9b1a-2c4f5d8a9e6b

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newRescanTestServer wires a Server with the bare minimum to exercise
// the request-validation paths of triggerFingerprintRescan. We do NOT wire
// up an operations queue here — the validation guard rejects requests
// before the queue is touched, so the queue-nil path is what we assert on
// happy bodies. (End-to-end queue exercise is covered by the larger
// integration suites.)
func newRescanTestServer(t *testing.T) (*Server, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	mockStore := mocks.NewMockStore(t)
	srv := &Server{store: mockStore}
	router := gin.New()
	router.POST("/rescan", srv.triggerFingerprintRescan)
	return srv, router
}

func postRescanJSON(t *testing.T, router *gin.Engine, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest("POST", "/rescan", &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestFingerprintRescan_RejectsBooksScopeWithoutIDs(t *testing.T) {
	_, router := newRescanTestServer(t)
	w := postRescanJSON(t, router, FingerprintRescanRequest{Scope: scopeBooks})
	assert.Equal(t, http.StatusBadRequest, w.Code, "body=%s", w.Body.String())
}

func TestFingerprintRescan_RejectsUnknownScope(t *testing.T) {
	_, router := newRescanTestServer(t)
	w := postRescanJSON(t, router, FingerprintRescanRequest{Scope: "garbage"})
	assert.Equal(t, http.StatusBadRequest, w.Code, "body=%s", w.Body.String())
}

func TestFingerprintRescan_DefaultsToMissingScope(t *testing.T) {
	// Empty body should default to scope=missing and pass validation.
	// The handler then trips on a missing operation queue (500), which
	// proves we got past validation. Either way, validation must not
	// reject with 400.
	_, router := newRescanTestServer(t)
	w := postRescanJSON(t, router, struct{}{})
	assert.NotEqual(t, http.StatusBadRequest, w.Code,
		"empty body should default scope=missing, not bad-request; got %d body=%s",
		w.Code, w.Body.String())
}
