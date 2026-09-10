// file: internal/server/activity_handlers_test.go
// version: 5.3.0
// guid: d4e5f6a7-b8c9-0123-defa-234567890123
// last-edited: 2026-09-10

// Updated for Phase 2 handler extraction: tests now use handlers.ActivityHandler
// directly instead of *Server methods.
// NOTE(fable5 T022): Ported NewSQLiteActivityStore → NewNutsActivityStore.

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupActivityTestRouter creates a temporary NutsActivityStore, wraps it in an
// ActivityService, mounts the ListActivity handler on a minimal gin router,
// and returns the router plus a cleanup function.
func setupActivityTestRouter(t *testing.T) (*gin.Engine, func()) {
	t.Helper()

	dir := t.TempDir()

	store, err := database.NewNutsActivityStore(dir)
	require.NoError(t, err)

	svc := activity.NewService(store)

	gin.SetMode(gin.TestMode)
	router := gin.New()

	h := handlers.NewActivityHandler(svc, nil)
	router.GET("/api/v1/activity", h.ListActivity)

	cleanup := func() {
		store.Close()
	}
	return router, cleanup
}

// TestListActivity_Empty verifies that an empty store returns HTTP 200 with
// an entries array (not null) and a total of 0.
func TestListActivity_Empty(t *testing.T) {
	router, cleanup := setupActivityTestRouter(t)
	defer cleanup()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/activity", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data struct {
			Entries []database.ActivityEntry `json:"entries"`
			Total   int                      `json:"total"`
		} `json:"data"`
	}
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, 0, resp.Data.Total)
	// entries must be an array, not null.
	assert.NotNil(t, resp.Data.Entries)
	assert.Empty(t, resp.Data.Entries)
}

// TestListActivity_WithFilters inserts two entries (tiers: change, debug) and
// verifies that filtering by tier=change returns only the one matching entry.
func TestListActivity_WithFilters(t *testing.T) {
	// Use a fresh store so we can seed specific data.
	dir := t.TempDir()
	store, err := database.NewNutsActivityStore(dir)
	require.NoError(t, err)
	defer store.Close()

	svc := activity.NewService(store)
	gin.SetMode(gin.TestMode)
	filterRouter := gin.New()
	h := handlers.NewActivityHandler(svc, nil)
	filterRouter.GET("/api/v1/activity", h.ListActivity)

	now := time.Now().UTC()

	err = svc.Record(database.ActivityEntry{
		Tier:      "change",
		Type:      "metadata_apply",
		Level:     "info",
		Source:    "test",
		Summary:   "metadata applied",
		Timestamp: now,
	})
	require.NoError(t, err)

	err = svc.Record(database.ActivityEntry{
		Tier:      "debug",
		Type:      "isbn_lookup",
		Level:     "debug",
		Source:    "test",
		Summary:   "ISBN lookup",
		Timestamp: now,
	})
	require.NoError(t, err)

	// Filter by tier=change.
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/activity?tier=change", nil)
	filterRouter.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data struct {
			Entries []database.ActivityEntry `json:"entries"`
			Total   int                      `json:"total"`
		} `json:"data"`
	}
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, 1, resp.Data.Total)
	require.Len(t, resp.Data.Entries, 1)
	assert.Equal(t, "change", resp.Data.Entries[0].Tier)
	assert.Equal(t, "metadata_apply", resp.Data.Entries[0].Type)
}

// TestListActivity_SearchParam verifies that the search query param filters
// entries by substring match on summary.
func TestListActivity_SearchParam(t *testing.T) {
	dir := t.TempDir()
	store, err := database.NewNutsActivityStore(dir)
	require.NoError(t, err)
	defer store.Close()

	svc := activity.NewService(store)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := handlers.NewActivityHandler(svc, nil)
	r.GET("/api/v1/activity", h.ListActivity)

	now := time.Now().UTC()

	require.NoError(t, svc.Record(database.ActivityEntry{
		Tier:      "info",
		Type:      "scanner",
		Level:     "info",
		Source:    "scanner",
		Summary:   "Found: Project Hail Mary",
		Timestamp: now,
	}))
	require.NoError(t, svc.Record(database.ActivityEntry{
		Tier:      "info",
		Type:      "scanner",
		Level:     "info",
		Source:    "scanner",
		Summary:   "Found: The Martian",
		Timestamp: now,
	}))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/activity?search=Hail+Mary", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data struct {
			Entries []database.ActivityEntry `json:"entries"`
			Total   int                      `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 1, resp.Data.Total)
	require.Len(t, resp.Data.Entries, 1)
	assert.Contains(t, resp.Data.Entries[0].Summary, "Hail Mary")
}

// TestListActivitySources verifies that the sources endpoint returns distinct
// source names with counts, ordered by count descending.
func TestListActivitySources(t *testing.T) {
	dir := t.TempDir()
	store, err := database.NewNutsActivityStore(dir)
	require.NoError(t, err)
	defer store.Close()

	svc := activity.NewService(store)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := handlers.NewActivityHandler(svc, nil)
	r.GET("/api/v1/activity/sources", h.ListActivitySources)

	now := time.Now().UTC()

	// Record 2 gin entries and 1 scanner entry.
	for range 2 {
		require.NoError(t, svc.Record(database.ActivityEntry{
			Tier:      "info",
			Type:      "request",
			Level:     "info",
			Source:    "gin",
			Summary:   "HTTP request",
			Timestamp: now,
		}))
	}
	require.NoError(t, svc.Record(database.ActivityEntry{
		Tier:      "change",
		Type:      "scan",
		Level:     "info",
		Source:    "scanner",
		Summary:   "scan complete",
		Timestamp: now,
	}))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/activity/sources", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data struct {
			Sources []database.SourceCount `json:"sources"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data.Sources, 2)
	// Ordered by count DESC: gin (2) first, scanner (1) second.
	assert.Equal(t, "gin", resp.Data.Sources[0].Source)
	assert.Equal(t, 2, resp.Data.Sources[0].Count)
	assert.Equal(t, "scanner", resp.Data.Sources[1].Source)
	assert.Equal(t, 1, resp.Data.Sources[1].Count)
}

// operationActivityEntry mirrors the handler-package type for JSON unmarshaling in tests.
type operationActivityEntry = handlers.OperationActivityEntry

// TestListOperationActivity_FallbackToOpLogs verifies that when the activity
// store has no rows for an operation, the handler falls back to op_logs_v2 and
// returns those entries with the correct shape and tags.
func TestListOperationActivity_FallbackToOpLogs(t *testing.T) {
	dir := t.TempDir()

	// Main store: PebbleStore implements OpsV2Store (has op_logs_v2 in Pebble).
	sqlStore, err := database.NewPebbleStoreInMemory(filepath.Join(dir, "main.pebble"))
	require.NoError(t, err)
	require.NoError(t, database.RunMigrations(sqlStore))
	defer sqlStore.Close()

	// Activity service backed by a fresh empty store — no entries for the op.
	actDir := t.TempDir()
	actStore, err := database.NewNutsActivityStore(actDir)
	require.NoError(t, err)
	defer actStore.Close()
	actSvc := activity.NewService(actStore)

	opID := "test-fallback-op-001"
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, sqlStore.AppendOpLogsV2([]database.OpLogV2Row{
		{OperationID: opID, Level: "info", Message: "started processing", CreatedAt: now},
		{OperationID: opID, Level: "debug", Message: "processing item 1", CreatedAt: now.Add(time.Second)},
	}))

	gin.SetMode(gin.TestMode)
	h := handlers.NewActivityHandler(actSvc, sqlStore)
	r := gin.New()
	r.GET("/api/v1/operations/:id/activity", h.ListOperationActivity)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/operations/"+opID+"/activity", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data struct {
			OperationID string                   `json:"operation_id"`
			Entries     []operationActivityEntry `json:"entries"`
			Total       int                      `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	assert.Equal(t, opID, resp.Data.OperationID)
	require.Len(t, resp.Data.Entries, 2)
	assert.Equal(t, "started processing", resp.Data.Entries[0].Message)
	assert.Equal(t, "info", resp.Data.Entries[0].Level)

	// Tags should include an op: tag for the operation ID.
	hasOpTag := false
	for _, tag := range resp.Data.Entries[0].Tags {
		if strings.HasPrefix(tag, "op:") {
			hasOpTag = true
		}
	}
	assert.True(t, hasOpTag, "expected op: tag in fallback entry tags")
	assert.Equal(t, 2, resp.Data.Total)
}

func TestListOperationActivity_WithRecordedEntries(t *testing.T) {
	dir := t.TempDir()
	store, err := database.NewNutsActivityStore(dir)
	require.NoError(t, err)
	defer store.Close()

	svc := activity.NewService(store)
	gin.SetMode(gin.TestMode)
	h := handlers.NewActivityHandler(svc, nil)
	r := gin.New()
	r.GET("/api/v1/operations/:id/activity", h.ListOperationActivity)

	opID := "metadata-fetch-op-001"
	baseTime := time.Now().UTC().Truncate(time.Second)

	records := []database.ActivityEntry{
		{
			Timestamp:   baseTime,
			Tier:        "change",
			Type:        "metadata-fetch",
			Level:       "info",
			Source:      "scheduler",
			OperationID: opID,
			Summary:     "metadata fetch queued",
			Details: map[string]any{
				"stage": "queued",
			},
		},
		{
			Timestamp:   baseTime.Add(time.Minute),
			Tier:        "change",
			Type:        "metadata-fetch",
			Level:       "info",
			Source:      "scheduler",
			OperationID: opID,
			Summary:     "metadata fetch running",
			Details: map[string]any{
				"stage": "running",
			},
		},
		{
			Timestamp:   baseTime.Add(2 * time.Minute),
			Tier:        "change",
			Type:        "metadata-fetch",
			Level:       "info",
			Source:      "scheduler",
			OperationID: opID,
			Summary:     "metadata fetch complete",
			Details: map[string]any{
				"stage": "complete",
			},
		},
	}

	for _, record := range records {
		require.NoError(t, svc.Record(record))
	}

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/operations/%s/activity", opID), nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data struct {
			OperationID string                   `json:"operation_id"`
			Entries     []operationActivityEntry `json:"entries"`
			Total       int                      `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	assert.Equal(t, opID, resp.Data.OperationID)
	assert.Equal(t, len(records), resp.Data.Total)
	require.Len(t, resp.Data.Entries, len(records))

	for idx, record := range records {
		got := resp.Data.Entries[idx]
		assert.Equal(t, record.Summary, got.Message)
		assert.Equal(t, record.Level, got.Level)
		assert.Equal(t, record.Type, got.OperationType)
		assert.True(t, record.Timestamp.Equal(got.Timestamp))
	}

	hasOpTag := slices.Contains(resp.Data.Entries[0].Tags, "op:"+opID)
	assert.True(t, hasOpTag)
}

// mergedActivityFixture builds a handler whose activity log holds entries for
// opA only and whose op-log v2 store holds rows for opB only, so a merged read
// has to take the activity path for one member and the fallback for the other.
// Timestamps interleave on purpose: A0 < B1 < A2.
func mergedActivityFixture(t *testing.T) (*gin.Engine, string, string, time.Time) {
	t.Helper()
	dir := t.TempDir()
	mainStore, err := database.NewPebbleStoreInMemory(filepath.Join(dir, "main.pebble"))
	require.NoError(t, err)
	require.NoError(t, database.RunMigrations(mainStore))
	t.Cleanup(func() { mainStore.Close() })

	actStore, err := database.NewNutsActivityStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { actStore.Close() })
	actSvc := activity.NewService(actStore)

	opA, opB := "merged-op-a", "merged-op-b"
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	_, err = actStore.Record(database.ActivityEntry{
		Timestamp: base, Tier: "change", Type: "ai-parse", Level: "info",
		Source: "scanner", OperationID: opA, Summary: "A first",
	})
	require.NoError(t, err)
	_, err = actStore.Record(database.ActivityEntry{
		Timestamp: base.Add(2 * time.Second), Tier: "change", Type: "ai-parse", Level: "info",
		Source: "scanner", OperationID: opA, Summary: "A last",
	})
	require.NoError(t, err)
	require.NoError(t, mainStore.AppendOpLogsV2([]database.OpLogV2Row{
		{OperationID: opB, Level: "info", Message: "B only", CreatedAt: base.Add(time.Second)},
	}))

	gin.SetMode(gin.TestMode)
	h := handlers.NewActivityHandler(actSvc, mainStore)
	r := gin.New()
	r.POST("/api/v1/operations/activity/merged", h.ListMergedOperationActivity)
	return r, opA, opB, base
}

type mergedActivityResponse struct {
	Data struct {
		OperationIDs []string                 `json:"operation_ids"`
		Entries      []operationActivityEntry `json:"entries"`
		Total        int                      `json:"total"`
		Truncated    bool                     `json:"truncated"`
	} `json:"data"`
}

func postMergedActivity(t *testing.T, r *gin.Engine, body string) (*httptest.ResponseRecorder, mergedActivityResponse) {
	t.Helper()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/operations/activity/merged", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	var resp mergedActivityResponse
	if w.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	}
	return w, resp
}

// TestListMergedOperationActivity_ChronologicalAcrossMembers proves the merged
// feed is one timeline: entries from the activity log and from the op-log
// fallback interleave by timestamp, each still labelled with its own op id.
func TestListMergedOperationActivity_ChronologicalAcrossMembers(t *testing.T) {
	r, opA, opB, _ := mergedActivityFixture(t)

	w, resp := postMergedActivity(t, r, fmt.Sprintf(`{"ids":[%q,%q]}`, opA, opB))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	require.Len(t, resp.Data.Entries, 3)
	assert.Equal(t, []string{"A first", "B only", "A last"},
		[]string{resp.Data.Entries[0].Message, resp.Data.Entries[1].Message, resp.Data.Entries[2].Message})
	assert.Equal(t, []string{opA, opB, opA},
		[]string{resp.Data.Entries[0].OperationID, resp.Data.Entries[1].OperationID, resp.Data.Entries[2].OperationID})
	assert.Equal(t, 3, resp.Data.Total)
	assert.False(t, resp.Data.Truncated)
	assert.Equal(t, []string{opA, opB}, resp.Data.OperationIDs)
}

// TestListMergedOperationActivity_LimitKeepsNewest: the cap is over the merged
// timeline, not per member, and it drops the OLDEST entries — the reader
// opening a group wants to see how it ended.
func TestListMergedOperationActivity_LimitKeepsNewest(t *testing.T) {
	r, opA, opB, _ := mergedActivityFixture(t)

	w, resp := postMergedActivity(t, r, fmt.Sprintf(`{"ids":[%q,%q],"limit":2}`, opA, opB))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	require.Len(t, resp.Data.Entries, 2)
	assert.Equal(t, "B only", resp.Data.Entries[0].Message)
	assert.Equal(t, "A last", resp.Data.Entries[1].Message)
	assert.Equal(t, 3, resp.Data.Total, "total counts what exists, not what was returned")
	assert.True(t, resp.Data.Truncated)
}

// TestListMergedOperationActivity_BadInput: no ids is a client error, and a
// repeated id is read once — a duplicated member must not duplicate its lines.
func TestListMergedOperationActivity_BadInput(t *testing.T) {
	r, opA, _, _ := mergedActivityFixture(t)

	w, _ := postMergedActivity(t, r, `{"ids":[]}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	w, _ = postMergedActivity(t, r, `{"ids":[" ",""]}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	w, _ = postMergedActivity(t, r, `not json`)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	w, resp := postMergedActivity(t, r, fmt.Sprintf(`{"ids":[%q,%q]}`, opA, opA))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, []string{opA}, resp.Data.OperationIDs)
	assert.Len(t, resp.Data.Entries, 2)
	assert.Equal(t, 2, resp.Data.Total)
}
