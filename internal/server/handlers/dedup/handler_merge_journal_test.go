// file: internal/server/handlers/dedup/handler_merge_journal_test.go
// version: 1.0.0
// guid: 5f883c97-c8f7-4228-a0d0-6d5838ef6e7b
// last-edited: 2026-09-10

// Regression tests for DA-02: the three bulk/manual merge endpoints
// (merge-series, bulk-merge, merge-cluster) called MergeService.MergeBooks
// directly, so none of them left an undo-ledger entry — while the
// single-candidate endpoint next door had already been changed to refuse the
// merge outright rather than perform it unjournaled. These pin all three to the
// journaled path and to that same refuse-rather-than-merge behaviour.
//
// The mocks carry the assertion: mergeMock has NO MergeBooks expectation in the
// happy-path tests, so a handler that fell back to the unjournaled call would
// fail on an unexpected call.

package deduphandler_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

func TestMergeDedupCandidateSeries_MergesThroughTheUndoJournal(t *testing.T) {
	h, d := newHandler(t)
	insertCandidate(t, d.es, "book-a", "book-b")
	sid := 7
	d.store.EXPECT().GetBookByID(mock.Anything).Return(&database.Book{ID: "x", SeriesID: &sid}, nil).Maybe()
	d.engine.EXPECT().
		MergeBooksJournaled(int64(0), mock.Anything, "", mock.Anything).
		Return(&merge.Result{PrimaryID: "book-a"}, []string{"dedup:automerge:k1"}, nil).
		Once()

	w := doReq(t, h.MergeDedupCandidateSeries, http.MethodPost, "/api/v1/dedup/candidates/merge-series", map[string]int{"series_id": sid}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestMergeDedupCandidateSeries_RefusesWithoutEngine(t *testing.T) {
	h, _ := newHandler(t, noEngine)
	w := doReq(t, h.MergeDedupCandidateSeries, http.MethodPost, "/api/v1/dedup/candidates/merge-series", map[string]int{"series_id": 1}, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 (no engine means no undo journal); body=%s", w.Code, w.Body.String())
	}
}

func TestBulkMergeDedupCandidates_MergesThroughTheUndoJournal(t *testing.T) {
	h, d := newHandler(t)
	allowLabelCaptureReads(d)
	id, aID, bID := insertCandidate(t, d.es, "book-a", "book-b")
	// The bulk lane merges one candidate PAIR at a time, so it routes through
	// the pairwise wrapper and the entry carries the candidate id.
	d.engine.EXPECT().
		MergeJournaled(id, aID, bID, "", mock.Anything).
		Return(&merge.Result{PrimaryID: aID}, "dedup:automerge:k1", nil).
		Once()

	w := doReq(t, h.BulkMergeDedupCandidates, http.MethodPost, "/api/v1/dedup/candidates/bulk-merge", map[string]any{}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestBulkMergeDedupCandidates_RefusesWithoutEngine(t *testing.T) {
	h, _ := newHandler(t, noEngine)
	w := doReq(t, h.BulkMergeDedupCandidates, http.MethodPost, "/api/v1/dedup/candidates/bulk-merge", map[string]any{}, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 (no engine means no undo journal); body=%s", w.Code, w.Body.String())
	}
}

func TestMergeDedupCluster_MergesThroughTheUndoJournal(t *testing.T) {
	h, d := newHandler(t)
	d.engine.EXPECT().
		MergeBooksJournaled(int64(0), []string{"id1", "id2", "id3"}, "id2", mock.Anything).
		Return(&merge.Result{PrimaryID: "id2"}, []string{"dedup:automerge:k1", "dedup:automerge:k2"}, nil).
		Once()

	w := doReq(t, h.MergeDedupCluster, http.MethodPost, "/api/v1/dedup/candidates/merge-cluster",
		map[string]any{"book_ids": []string{"id1", "id2", "id3"}, "primary_book_id": "id2"}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestMergeDedupCluster_RefusesWithoutEngine(t *testing.T) {
	h, _ := newHandler(t, noEngine)
	w := doReq(t, h.MergeDedupCluster, http.MethodPost, "/api/v1/dedup/candidates/merge-cluster",
		map[string][]string{"book_ids": {"id1", "id2"}}, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 (no engine means no undo journal); body=%s", w.Code, w.Body.String())
	}
}
