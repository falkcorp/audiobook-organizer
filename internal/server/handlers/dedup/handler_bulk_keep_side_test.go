// file: internal/server/handlers/dedup/handler_bulk_keep_side_test.go
// version: 1.0.0
// guid: 1f6b3a8e-92c4-4d07-b5e1-7a0c9d2e4f63
// last-edited: 2026-10-06

// The filter-scoped bulk endpoints as the cross-page selection uses them:
// keep_side (the Acoustic tab's Keep A / Keep B over every matching pair) goes
// through bulk-link's review-queue-only guard like any other bulk link;
// expected_total refuses a filter that moved since the reviewer confirmed;
// and a filtered bulk action never overturns a merged/dismissed verdict.

package deduphandler_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

func TestBulkLink_KeepSidePicksTheSurvivorPerPair(t *testing.T) {
	h, d := newHandler(t)
	wireGuardBooks(d, map[string]*database.Book{
		"ka-1": {ID: "ka-1", FilePath: "/lib/1a.m4b"}, "kb-1": {ID: "kb-1", FilePath: "/lib/1b.m4b"},
		"ka-2": {ID: "ka-2", FilePath: "/lib/2a.m4b"}, "kb-2": {ID: "kb-2", FilePath: "/lib/2b.m4b"},
	})
	id1, a1, _ := insertCandidate(t, d.es, "ka-1", "kb-1")
	id2, a2, _ := insertCandidate(t, d.es, "ka-2", "kb-2")
	d.engine.EXPECT().MergeJournaled(id1, mock.Anything, mock.Anything, a1, mock.Anything).
		Return(&merge.Result{PrimaryID: a1}, "k", nil).Once()
	d.engine.EXPECT().MergeJournaled(id2, mock.Anything, mock.Anything, a2, mock.Anything).
		Return(&merge.Result{PrimaryID: a2}, "k", nil).Once()

	w := doReq(t, h.BulkLinkDedupCandidates, http.MethodPost, "/api/v1/dedup/candidates/bulk-link",
		map[string]any{"keep_side": "a", "expected_total": 2}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	if r := decodeBulk(t, w.Body.Bytes()); r.Data.Merged != 2 {
		t.Fatalf("merged=%d want 2; body=%s", r.Data.Merged, w.Body.String())
	}
}

// keep_side does not bypass the guard: a pinned (manual) pair, a same-path
// pair, and a pair that would chain two same-path books into one group are
// all refused; only the clean pair links.
func TestBulkLink_KeepSideStillRefusesPinnedSamePathAndChain(t *testing.T) {
	h, d := newHandler(t)
	same := "/lib/Author/Book/32.m4b"
	chain := "/lib/Author/Other/07.m4b"
	wireGuardBooks(d, map[string]*database.Book{
		"sp-a": {ID: "sp-a", FilePath: same}, "sp-b": {ID: "sp-b", FilePath: same},
		"ch-a": {ID: "ch-a", FilePath: chain}, "ch-b": {ID: "ch-b", FilePath: chain},
		"ch-c":  {ID: "ch-c", FilePath: "/lib/c.m4b"},
		"man-a": {ID: "man-a", FilePath: "/lib/ma.m4b"}, "man-b": {ID: "man-b", FilePath: "/lib/mb.m4b"},
		"ok-a": {ID: "ok-a", FilePath: "/lib/oa.m4b"}, "ok-b": {ID: "ok-b", FilePath: "/lib/ob.m4b"},
	})
	insertCandidate(t, d.es, "sp-a", "sp-b")
	insertCandidate(t, d.es, "ch-a", "ch-c")
	insertCandidate(t, d.es, "ch-b", "ch-c")
	okID, okA, okB := insertCandidate(t, d.es, "ok-a", "ok-b")
	if _, err := d.es.EnqueueManualCandidate("book", "man-a", "man-b", "pinned"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// Exactly two merges may run: the clean pair, and ONE of the two chain
	// links (the second would join ch-a and ch-b). Any other merge fails the mock.
	d.engine.EXPECT().MergeJournaled(okID, okA, okB, okB, mock.Anything).
		Return(&merge.Result{PrimaryID: okB}, "k", nil).Once()
	d.engine.EXPECT().MergeJournaled(mock.Anything, mock.Anything, "ch-c", "ch-c", mock.Anything).
		Return(&merge.Result{PrimaryID: "ch-c"}, "k", nil).Once()

	w := doReq(t, h.BulkLinkDedupCandidates, http.MethodPost, "/api/v1/dedup/candidates/bulk-link",
		map[string]any{"keep_side": "b"}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	r := decodeBulk(t, w.Body.Bytes())
	if r.Data.Merged != 2 || r.Data.Failed != 3 {
		t.Fatalf("want 2 linked, 3 refused (same-path, chain, manual); body=%s", w.Body.String())
	}
	reasons := w.Body.String()
	for _, want := range []string{"same_path", "manual"} {
		if !strings.Contains(reasons, want) {
			t.Fatalf("refusals must name %q; body=%s", want, reasons)
		}
	}
}

// A pair another session decided after the list was taken is skipped, not
// merged: the per-pair recheck re-reads the row before each merge.
func TestBulkLink_KeepSideSkipsPairDecidedMidRun(t *testing.T) {
	h, d := newHandler(t)
	wireGuardBooks(d, map[string]*database.Book{
		"m1-a": {ID: "m1-a", FilePath: "/lib/m1a.m4b"}, "m1-b": {ID: "m1-b", FilePath: "/lib/m1b.m4b"},
		"m2-a": {ID: "m2-a", FilePath: "/lib/m2a.m4b"}, "m2-b": {ID: "m2-b", FilePath: "/lib/m2b.m4b"},
	})
	id1, _, _ := insertCandidate(t, d.es, "m1-a", "m1-b")
	id2, _, _ := insertCandidate(t, d.es, "m2-a", "m2-b")
	// The first merge "happens while" someone dismisses the second pair.
	d.engine.EXPECT().MergeJournaled(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(id int64, a, _ string, _ string, _ string) (*merge.Result, string, error) {
			other := id1
			if id == id1 {
				other = id2
			}
			if err := d.es.UpdateCandidateStatus(other, "dismissed"); err != nil {
				t.Fatalf("dismiss: %v", err)
			}
			return &merge.Result{PrimaryID: a}, "k", nil
		}).Once()

	w := doReq(t, h.BulkLinkDedupCandidates, http.MethodPost, "/api/v1/dedup/candidates/bulk-link",
		map[string]any{"keep_side": "a"}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	r := decodeBulk(t, w.Body.Bytes())
	if r.Data.Merged != 1 || r.Data.Failed != 1 {
		t.Fatalf("want 1 merged, 1 skipped as decided since; body=%s", w.Body.String())
	}
}

func TestBulkEndpoints_ExpectedTotalMismatchIs409AndWritesNothing(t *testing.T) {
	h, d := newHandler(t)
	id, _, _ := insertCandidate(t, d.es, "et-a", "et-b")
	insertCandidate(t, d.es, "et-c", "et-d")
	// No MergeJournaled expectation: any merge fails the mock.
	w := doReq(t, h.BulkLinkDedupCandidates, http.MethodPost, "/api/v1/dedup/candidates/bulk-link",
		map[string]any{"expected_total": 1}, nil)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "FILTER_CHANGED") {
		t.Fatalf("bulk-link: want 409 FILTER_CHANGED; got %d %s", w.Code, w.Body.String())
	}
	w = doReq(t, h.BulkRejectDedupCandidates, http.MethodPost, "/api/v1/dedup/candidates/bulk-reject",
		map[string]any{"expected_total": 3}, nil)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"matched":2`) {
		t.Fatalf("bulk-reject: want 409 with matched=2; got %d %s", w.Code, w.Body.String())
	}
	if got := candidateStatus(t, d.es, id); got != "pending" {
		t.Fatalf("a refused request wrote anyway: status=%q", got)
	}
}

// status=dismissed + "merge everything matching" used to link every pair a
// human had marked not-a-duplicate.
func TestBulkLink_NonPendingStatusRefused(t *testing.T) {
	h, d := newHandler(t)
	id, _, _ := insertCandidate(t, d.es, "np-a", "np-b")
	if err := d.es.UpdateCandidateStatus(id, "dismissed"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	for _, status := range []string{"dismissed", "merged"} {
		w := doReq(t, h.BulkLinkDedupCandidates, http.MethodPost, "/api/v1/dedup/candidates/bulk-link",
			map[string]any{"status": status}, nil)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status=%s: got %d want 400; body=%s", status, w.Code, w.Body.String())
		}
	}
	if got := candidateStatus(t, d.es, id); got != "dismissed" {
		t.Fatalf("dismissed pair changed: %q", got)
	}
}

func TestBulkLink_BadKeepSideRefused(t *testing.T) {
	h, d := newHandler(t)
	insertCandidate(t, d.es, "bk-a", "bk-b")
	w := doReq(t, h.BulkLinkDedupCandidates, http.MethodPost, "/api/v1/dedup/candidates/bulk-link",
		map[string]any{"keep_side": "c"}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400; body=%s", w.Code, w.Body.String())
	}
	w = doReq(t, h.BulkRejectDedupCandidates, http.MethodPost, "/api/v1/dedup/candidates/bulk-reject",
		map[string]any{"keep_side": "a"}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("reject with keep_side: got %d want 400; body=%s", w.Code, w.Body.String())
	}
}
