// file: internal/server/startup_steps_test.go
// version: 1.0.0
// guid: 48ec603b-5b58-4cd0-855f-c07bd0cf0b6e
// last-edited: 2026-10-10

package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/startup"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A fatal server step (opRegistrationGate) still aborts Start, runs through
// the runner cmd supplied, and records no /health token.
func TestStart_FatalStepUsesSuppliedRunnerAndRecordsNoToken(t *testing.T) {
	steps := startup.New()
	s := &Server{opRegistrationErrs: []error{errors.New("registry: RegisterOp(x): boom")}}

	err := s.Start(ServerConfig{StartupSteps: steps})
	require.Error(t, err, "a fatal step must stop Start")
	assert.Same(t, steps, s.startupSteps.Load(), "Start must use cmd's runner, not a private one")
	assert.Empty(t, steps.Tokens())
}

// Start without a runner (tests, child mode) gets a fresh one.
func TestStartupStepsFor_DefaultsWhenNil(t *testing.T) {
	s := &Server{}
	got := s.startupStepsFor(ServerConfig{})
	require.NotNil(t, got)
	assert.Same(t, got, s.startupSteps.Load())
}

// Bleve failing to open leaves Index() nil; the searchindex step turns that
// into the search_index token instead of a silent search-less server, and
// does not start the indexing goroutines.
func TestAdoptSearchIndex_MissingIndexIsDegraded(t *testing.T) {
	steps := startup.New()
	s := &Server{}
	s.adoptSearchIndex(steps)

	assert.Equal(t, []string{startup.TokenSearchIndex}, steps.Tokens())
	s.indexQueueMu.RLock()
	defer s.indexQueueMu.RUnlock()
	assert.Nil(t, s.indexQueue, "no index: search indexing must not be switched live")
}

// The degraded set reaches /health through the real route wiring
// (wire_handlers -> system.New -> startupDegradedSource), merged with what
// the handler computes itself.
func TestHealth_ReportsDegradedStartupStepsThroughWiring(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()

	get := func() []string {
		t.Helper()
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
		require.Equal(t, http.StatusOK, w.Code, "a degraded step must leave /health serving 200")
		var resp struct {
			Data struct {
				Degraded []string `json:"degraded"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
		return resp.Data.Degraded
	}

	// Before Start stores a runner: no startup tokens.
	assert.NotContains(t, get(), startup.TokenMetricsDisabled)

	// What cmd does when metrics init fails, then hands the runner to Start.
	steps := startup.New()
	require.NoError(t, steps.Run("InitOTEL", func() error { return errors.New("meter provider") }))
	srv.startupStepsFor(ServerConfig{StartupSteps: steps})

	assert.Contains(t, get(), startup.TokenMetricsDisabled)
}
