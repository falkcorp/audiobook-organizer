// file: internal/server/activity_integration_test.go
// version: 3.0.0
// guid: f8a3b2c1-d4e5-6f7a-8b9c-0d1e2f3a4b5c
// last-edited: 2026-06-10

// NOTE(fable5 T022): Ported NewSQLiteActivityStore → NewNutsActivityStore.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/gin-gonic/gin"
)

func TestActivity_Integration_RecordAndHTTPQuery(t *testing.T) {
	// Setup: create temp store, service, and router
	store, err := database.NewNutsActivityStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewNutsActivityStore: %v", err)
	}
	defer store.Close()

	svc := activity.NewService(store)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := handlers.NewActivityHandler(svc, nil)
	r.GET("/api/v1/activity", h.ListActivity)

	// Simulate an iTunes sync writing activity
	_ = svc.Record(database.ActivityEntry{
		Tier:        "change",
		Type:        "itunes_sync",
		Source:      "scheduler",
		OperationID: "op-sync-1",
		Summary:     "Sync: 312 updated, 39 new",
		Details:     map[string]any{"updated": 312, "new": 39},
		Tags:        []string{"scheduled", "itunes"},
	})

	// Simulate a metadata apply
	_ = svc.Record(database.ActivityEntry{
		Tier:    "change",
		Type:    "metadata_apply",
		Source:  "api",
		BookID:  "book-1",
		Summary: "Applied title: old → new",
	})

	// Simulate debug progress
	_ = svc.Record(database.ActivityEntry{
		Tier:        "debug",
		Type:        "progress",
		Source:      "background",
		OperationID: "op-sync-1",
		Summary:     "Processing book 45 of 312...",
	})

	type activityResp struct {
		Data struct {
			Entries []database.ActivityEntry `json:"entries"`
			Total   int                      `json:"total"`
		} `json:"data"`
	}

	// Query all
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/activity?limit=100", nil)
	r.ServeHTTP(w, req)

	var resp activityResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Data.Total != 3 {
		t.Fatalf("expected 3, got %d", resp.Data.Total)
	}

	// Query by operation
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/activity?operation_id=op-sync-1", nil)
	r.ServeHTTP(w, req)
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.Total != 2 {
		t.Errorf("expected 2 entries for op-sync-1, got %d", resp.Data.Total)
	}

	// Query by book
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/activity?book_id=book-1&tier=change", nil)
	r.ServeHTTP(w, req)
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.Total != 1 {
		t.Errorf("expected 1 entry for book-1, got %d", resp.Data.Total)
	}

	// Query by tags
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/activity?tags=itunes", nil)
	r.ServeHTTP(w, req)
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.Total != 1 {
		t.Errorf("expected 1 entry with tag=itunes, got %d", resp.Data.Total)
	}

	// Verify details round-trip
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/activity?type=itunes_sync", nil)
	r.ServeHTTP(w, req)
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.Total != 1 {
		t.Fatalf("expected 1 itunes_sync, got %d", resp.Data.Total)
	}
	if v, ok := resp.Data.Entries[0].Details["updated"]; !ok || v != float64(312) {
		t.Errorf("details.updated mismatch: %v", resp.Data.Entries[0].Details)
	}

	// Search filter
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/activity?search=312+updated", nil)
	r.ServeHTTP(w, req)
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.Total != 1 {
		t.Errorf("search '312 updated': expected 1, got %d", resp.Data.Total)
	}

	// Exclude sources
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/activity?exclude_sources=background", nil)
	r.ServeHTTP(w, req)
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.Total != 2 {
		t.Errorf("exclude background: expected 2, got %d", resp.Data.Total)
	}
}

func TestActivity_Integration_TeeWriterCapture(t *testing.T) {
	store, err := database.NewNutsActivityStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewNutsActivityStore: %v", err)
	}
	defer store.Close()

	w := activity.NewWriter(store, 1000)
	// Disable the default gin source skip so all 3 lines (including [GIN]) are stored
	// and source-filtering assertions work correctly.
	w.SetSkipSources()
	w.Start(context.Background()) //nolint:errcheck

	fmt.Fprintln(w, "2026/03/25 17:35:08 logger.go:103: [info] scheduler: iTunes sync started")
	fmt.Fprintln(w, "[GIN] 2026/03/25 - 17:35:11 | 200 |    1.44s |    192.0.2.164 | GET      \"/api/v1/health\"")
	fmt.Fprintln(w, "2026/03/25 17:35:08 logger.go:103: [warn] server: No params found for scan")

	_ = w.Stop(context.Background())

	entries, total, err := store.Query(database.ActivityFilter{Limit: 100})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if total < 3 {
		t.Fatalf("expected at least 3 entries, got %d", total)
	}

	// Source filtering
	_, total, _ = store.Query(database.ActivityFilter{Source: "gin"})
	if total != 1 {
		t.Errorf("expected 1 gin entry, got %d", total)
	}

	_, total, _ = store.Query(database.ActivityFilter{ExcludeSources: []string{"gin"}})
	if total < 2 {
		t.Errorf("expected at least 2 non-gin entries, got %d", total)
	}

	// Search
	_, total, _ = store.Query(database.ActivityFilter{Search: "iTunes"})
	if total != 1 {
		t.Errorf("expected 1 entry matching 'iTunes', got %d", total)
	}
	_ = entries
}
