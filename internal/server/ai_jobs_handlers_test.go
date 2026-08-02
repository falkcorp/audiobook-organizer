// file: internal/server/ai_jobs_handlers_test.go
// version: 2.0.0
// guid: 136d5ad0-d226-471a-8c2c-64992ba3882d
// last-edited: 2026-06-10

// NOTE(fable5 T022): Ported from SQLiteStore to PebbleStore.

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupAIJobsTestServer creates a PebbleStore with migrations applied and sets it
// as the global store for the Server to use. Returns the Server and a cleanup function.
func setupAIJobsTestServer(t *testing.T) (*Server, *database.PebbleStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, database.RunMigrations(store))

	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(origStore)
		store.Close()
	})

	srv := NewServer(store)
	return srv, store
}

func TestListAIJobsHandler_ReturnsRowsFiltered(t *testing.T) {
	srv, store := setupAIJobsTestServer(t)

	// Verify the global store implements AIJobsStore
	globalStore := database.GetGlobalStore()
	_, ok := globalStore.(database.AIJobsStore)
	require.True(t, ok, "global store does not implement AIJobsStore")

	// Insert test jobs
	now := time.Now()
	require.NoError(t, store.CreateAIJob(database.AIJob{
		ID: "j1", Type: "dedup_review", CustomIDPrefix: "x", Status: "completed",
		ItemCount: 1, CreatedAt: now,
	}, []byte("[]")))
	require.NoError(t, store.CreateAIJob(database.AIJob{
		ID: "j2", Type: "metadata_review", CustomIDPrefix: "x", Status: "submitted",
		ItemCount: 1, CreatedAt: now.Add(1 * time.Second),
	}, []byte("[]")))

	// Verify jobs exist in store directly
	jobs, err := store.ListAIJobs("dedup_review", "", 100, 0)
	require.NoError(t, err)
	require.Len(t, jobs, 1, "Expected 1 job in store")

	// Test: filter by type
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ai-jobs?type=dedup_review", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	t.Logf("Response body: %s", w.Body.String())

	var resp struct {
		Data struct {
			Jobs []database.AIJob `json:"jobs"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Len(t, resp.Data.Jobs, 1)
	assert.Equal(t, "j1", resp.Data.Jobs[0].ID)
	assert.Equal(t, "dedup_review", resp.Data.Jobs[0].Type)
}

func TestListAIJobsHandler_FilterByStatus(t *testing.T) {
	srv, store := setupAIJobsTestServer(t)

	// Insert test jobs with different statuses
	now := time.Now()
	require.NoError(t, store.CreateAIJob(database.AIJob{
		ID: "j1", Type: "dedup_review", CustomIDPrefix: "x", Status: "completed",
		ItemCount: 1, CreatedAt: now,
	}, []byte("[]")))
	require.NoError(t, store.CreateAIJob(database.AIJob{
		ID: "j2", Type: "dedup_review", CustomIDPrefix: "x", Status: "submitted",
		ItemCount: 1, CreatedAt: now.Add(1 * time.Second),
	}, []byte("[]")))

	// Test: filter by status
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ai-jobs?status=submitted", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			Jobs []database.AIJob `json:"jobs"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Len(t, resp.Data.Jobs, 1)
	assert.Equal(t, "j2", resp.Data.Jobs[0].ID)
	assert.Equal(t, "submitted", resp.Data.Jobs[0].Status)
}

func TestListAIJobsHandler_Pagination(t *testing.T) {
	srv, store := setupAIJobsTestServer(t)

	// Insert 5 test jobs
	now := time.Now()
	for i := 0; i < 5; i++ {
		id := "j" + string(rune('1'+i))
		require.NoError(t, store.CreateAIJob(database.AIJob{
			ID: id, Type: "dedup_review", CustomIDPrefix: "x", Status: "completed",
			ItemCount: 1, CreatedAt: now.Add(time.Duration(i) * time.Second),
		}, []byte("[]")))
	}

	// Test: limit and offset
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ai-jobs?limit=2&offset=1", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			Jobs []database.AIJob `json:"jobs"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Len(t, resp.Data.Jobs, 2)
}

func TestListAIJobsHandler_Empty(t *testing.T) {
	srv, _ := setupAIJobsTestServer(t)

	// Test: empty store returns empty jobs array
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ai-jobs", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			Jobs []database.AIJob `json:"jobs"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Len(t, resp.Data.Jobs, 0)
}

func TestListAIJobsHandler_LimitClamping(t *testing.T) {
	srv, _ := setupAIJobsTestServer(t)

	// Test: limit > 500 is clamped to 500
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ai-jobs?limit=1000", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	// Just verify it doesn't error; actual clamping is tested in handler logic
	var resp struct {
		Data struct {
			Jobs []database.AIJob `json:"jobs"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
}
