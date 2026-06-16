// file: internal/server/maintenance_window_handlers_test.go
// version: 1.3.0
// guid: d5e6f7a8-b9c0-1234-efab-456789012345
// last-edited: 2026-06-16

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/scheduler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupMaintenanceTestServer creates a minimal Server with a real PebbleStore
// and a wired TaskScheduler, then returns it ready for HTTP testing.
func setupMaintenanceTestServer(t *testing.T) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)

	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStore(pebblePath)
	require.NoError(t, err, "open pebble store")

	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(origStore)
		store.Close()
	})

	srv := NewServer(store)
	srv.scheduler = scheduler.NewTaskScheduler(scheduler.SchedulerDeps{
		Store:               srv.Store,
		OpRegistry:          srv.opRegistry,
		HasDedupEngine:      func() bool { return false },
		HasMetadataFetchSvc: func() bool { return false },
		HasActivitySvc:      func() bool { return false },
		HasBatchPoller:      func() bool { return false },
	})
	return srv
}

func TestListTasksHasIsRunning(t *testing.T) {
	srv := setupMaintenanceTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "expected 200 OK")

	var resp struct {
		Data []struct {
			Name      string `json:"name"`
			IsRunning bool   `json:"is_running"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Data, "expected at least one task")

	for _, task := range resp.Data {
		// is_running should be present (false for all tasks in a fresh test server)
		assert.False(t, task.IsRunning, "task %q should not be running", task.Name)
	}
}

func TestGetMaintenanceWindowStatus(t *testing.T) {
	// Set up known config values.
	orig := config.AppConfig
	defer func() { config.AppConfig = orig }()

	config.AppConfig.Maintenance.Enabled = true
	config.AppConfig.Maintenance.WindowStart = 2
	config.AppConfig.Maintenance.WindowEnd = 5

	srv := setupMaintenanceTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/maintenance-window/status", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "expected 200 OK")

	var resp struct {
		Data struct {
			Enabled          bool   `json:"enabled"`
			WindowStart      int    `json:"window_start"`
			WindowEnd        int    `json:"window_end"`
			LastRunDate      string `json:"last_run_date"`
			NextRunEstimate  string `json:"next_run_estimate"`
			CurrentlyRunning bool   `json:"currently_running"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	assert.True(t, resp.Data.Enabled, "enabled should be true")
	assert.Equal(t, 2, resp.Data.WindowStart)
	assert.Equal(t, 5, resp.Data.WindowEnd)
	assert.NotEmpty(t, resp.Data.NextRunEstimate, "next_run_estimate should be non-empty")
	assert.False(t, resp.Data.CurrentlyRunning, "no maintenance should be running")
}

func TestUpdateMaintenanceWindowConfig_Valid(t *testing.T) {
	orig := config.AppConfig
	defer func() { config.AppConfig = orig }()

	srv := setupMaintenanceTestServer(t)

	body, err := json.Marshal(map[string]interface{}{
		"enabled":      true,
		"window_start": 3,
		"window_end":   5,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/maintenance-window/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "expected 200 OK: %s", w.Body.String())

	// Verify config was updated.
	assert.True(t, config.AppConfig.Maintenance.Enabled)
	assert.Equal(t, 3, config.AppConfig.Maintenance.WindowStart)
	assert.Equal(t, 5, config.AppConfig.Maintenance.WindowEnd)

	// GET status and confirm round-trip.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/maintenance-window/status", nil)
	w2 := httptest.NewRecorder()
	srv.router.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusOK, w2.Code)

	var resp struct {
		Data struct {
			Enabled     bool `json:"enabled"`
			WindowStart int  `json:"window_start"`
			WindowEnd   int  `json:"window_end"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &resp))
	assert.True(t, resp.Data.Enabled)
	assert.Equal(t, 3, resp.Data.WindowStart)
	assert.Equal(t, 5, resp.Data.WindowEnd)
}

func TestUpdateMaintenanceWindowConfig_InvalidHour(t *testing.T) {
	srv := setupMaintenanceTestServer(t)

	body, err := json.Marshal(map[string]interface{}{
		"enabled":      true,
		"window_start": 24,
		"window_end":   5,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/maintenance-window/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code, "hour 24 should return 400")
}

// NOTE: calculateNextWindowRun moved to the handlers/operations sub-package as
// an unexported helper. It is covered indirectly there via the
// GetMaintenanceWindowStatus handler test (which asserts a non-empty
// next_run_estimate); an external operations_test package cannot reference the
// unexported helper directly.
