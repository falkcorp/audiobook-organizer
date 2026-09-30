// file: internal/server/metadata_batch_fetch_stale_test.go
// version: 1.0.0
// guid: 94517ca9-634c-4760-83e5-5b98eecf127a
// last-edited: 2026-09-30

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// TestBatchFetchCandidates_StaleResolvesServerSide pins the fix for the review
// rail reading "3,511 stale" while its refetch button refetched 10: {stale:true}
// is resolved on the server to exactly the set the summary counts, reviewable
// or not, and forced so a known-empty row is actually re-asked.
func TestBatchFetchCandidates_StaleResolvesServerSide(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	store := s.storeForWiring()

	// No live provider: the enqueued op may start running in the background,
	// and it must not reach the network.
	mfs := metafetch.NewService(store)
	mfs.SetOverrideSources([]metadata.MetadataSource{&countingSource{name: "SrcA"}})
	s.metadataFetchService = mfs

	old := time.Now().Add(-2 * database.MetadataCacheTTL)
	cand, err := json.Marshal(map[string]any{"title": "T", "score": 0.9})
	if err != nil {
		t.Fatal(err)
	}
	noMatch := "no_match"
	mk := func(title string, reviewStatus *string, candidates []json.RawMessage, fetchedAt time.Time) string {
		t.Helper()
		b, err := store.CreateBook(&database.Book{Title: title, FilePath: "/lib/" + title + "/b.m4b", MetadataReviewStatus: reviewStatus})
		if err != nil {
			t.Fatalf("CreateBook %s: %v", title, err)
		}
		if err := store.PutMetadataCache(&database.MetadataCandidateCache{
			BookID: b.ID, Candidates: candidates, FetchedAt: fetchedAt, SourceHash: "h", SearchFingerprint: "f",
		}); err != nil {
			t.Fatalf("PutMetadataCache %s: %v", title, err)
		}
		return b.ID
	}
	reviewableStale := mk("ReviewableStale", nil, []json.RawMessage{cand}, old)
	emptyStale := mk("EmptyStale", nil, nil, old)
	mk("Fresh", nil, []json.RawMessage{cand}, time.Now())
	mk("Rejected", &noMatch, []json.RawMessage{cand}, old)

	want, err := handlers.StaleCachedBookIDs(t.Context(), store, mfs)
	if err != nil {
		t.Fatalf("StaleCachedBookIDs: %v", err)
	}
	if len(want) != 2 {
		t.Fatalf("fixture resolves %d stale books (%v), want 2: reviewable + no_candidates", len(want), want)
	}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/metadata/batch-fetch-candidates", bytes.NewBufferString(`{"stale":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	s.handleBatchFetchCandidates(c)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			OperationID string `json:"operation_id"`
			BookCount   int    `json:"book_count"`
			TotalBooks  int    `json:"total_books"`
			Skipped     int    `json:"skipped"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.BookCount != 2 || resp.Data.TotalBooks != 2 || resp.Data.Skipped != 0 {
		t.Fatalf("response = %+v, want book_count=2 total_books=2 skipped=0", resp.Data)
	}

	row, err := store.GetOperationV2(resp.Data.OperationID)
	if err != nil || row == nil {
		t.Fatalf("GetOperationV2(%s): %v", resp.Data.OperationID, err)
	}
	var params metadataCandidateFetchOpParams
	if err := json.Unmarshal([]byte(row.Params), &params); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if !params.Force {
		t.Error("a stale refetch must be forced: a known-empty row is otherwise served from the cache and stays stale")
	}
	got := map[string]bool{}
	for _, id := range params.BookIDs {
		got[id] = true
	}
	if len(params.BookIDs) != 2 || !got[reviewableStale] || !got[emptyStale] {
		t.Fatalf("enqueued %v, want exactly [%s %s]", params.BookIDs, reviewableStale, emptyStale)
	}
}

// An empty stale set is a clear backlog, not a bad request.
func TestBatchFetchCandidates_StaleWithNothingStaleIsOK(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	s.metadataFetchService = metafetch.NewService(s.storeForWiring())

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/metadata/batch-fetch-candidates", bytes.NewBufferString(`{"stale":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	s.handleBatchFetchCandidates(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"book_count":0`)) {
		t.Fatalf("body %s, want book_count 0", w.Body.String())
	}
}
