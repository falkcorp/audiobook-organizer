// file: internal/server/handlers/split_book_bulk_test.go
// version: 1.0.0
// guid: 2a04b4ca-b833-41e1-99da-0c198846a34b
// last-edited: 2026-08-28

package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/gin-gonic/gin"
)

type splitBookBulkStore struct {
	candidates map[string]*dedup.SplitBookCandidate
}

func (s *splitBookBulkStore) List() ([]dedup.SplitBookCandidate, error) { return nil, nil }
func (s *splitBookBulkStore) Get(id string) (*dedup.SplitBookCandidate, error) {
	return s.candidates[id], nil
}
func (s *splitBookBulkStore) Delete(string) error { return nil }

type splitBookBulkEnqueuer struct {
	called bool
	defID  string
	params any
}

func (e *splitBookBulkEnqueuer) EnqueueOp(_ context.Context, defID string, params any, _ ...registry.EnqueueOption) (string, error) {
	e.called = true
	e.defID = defID
	e.params = params
	return "op-bulk", nil
}

func TestBulkMergeSplitBookCandidatesRejectsOverlapBeforeEnqueue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &splitBookBulkStore{candidates: map[string]*dedup.SplitBookCandidate{
		"first":  {ID: "first", BookIDs: []string{"a", "b"}},
		"second": {ID: "second", BookIDs: []string{"b", "c"}},
	}}
	enqueuer := &splitBookBulkEnqueuer{}
	h := NewSplitBookHandler(enqueuer, store, nil)
	router := gin.New()
	router.POST("/bulk", h.BulkMergeSplitBookCandidates)

	request := httptest.NewRequest(http.MethodPost, "/bulk", strings.NewReader(`{"candidate_ids":["first","second"]}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status: want %d, got %d: %s", http.StatusBadRequest, recorder.Code, recorder.Body.String())
	}
	if enqueuer.called {
		t.Fatal("overlapping candidates must not enqueue a mutation operation")
	}
}

func TestBulkMergeSplitBookCandidatesSnapshotsResolvedCandidate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &splitBookBulkStore{candidates: map[string]*dedup.SplitBookCandidate{
		"candidate": {ID: "candidate", BookIDs: []string{"keep", "source"}, SuggestedTitle: "Recovered title"},
	}}
	enqueuer := &splitBookBulkEnqueuer{}
	h := NewSplitBookHandler(enqueuer, store, nil)
	router := gin.New()
	router.POST("/bulk", h.BulkMergeSplitBookCandidates)

	request := httptest.NewRequest(http.MethodPost, "/bulk", strings.NewReader(`{"candidate_ids":["candidate"],"dry_run":true}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status: want %d, got %d: %s", http.StatusAccepted, recorder.Code, recorder.Body.String())
	}
	if !enqueuer.called || enqueuer.defID != "dedup.split-book-bulk-merge" {
		t.Fatalf("unexpected enqueue: called=%v def=%q", enqueuer.called, enqueuer.defID)
	}
	params, ok := enqueuer.params.(dedup.BulkSplitBookMergeParams)
	if !ok || !params.DryRun || len(params.Items) != 1 {
		t.Fatalf("want one dry-run snapshot, got %#v", enqueuer.params)
	}
	item := params.Items[0]
	if item.KeepID != "keep" || item.SuggestedTitle != "Recovered title" || len(item.BookIDs) != 2 {
		t.Fatalf("snapshot did not contain resolved candidate data: %#v", item)
	}
}
