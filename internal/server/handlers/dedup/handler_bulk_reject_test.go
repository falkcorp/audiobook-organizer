// file: internal/server/handlers/dedup/handler_bulk_reject_test.go
// version: 1.0.0
// guid: 5d65b248-8432-43e0-9a13-2da6e1228ceb
// last-edited: 2026-10-06

package deduphandler_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// POST /dedup/candidates/bulk-reject backs the review UI's "Select all N
// matching" + Dismiss. The filter is re-evaluated server-side with the same
// binder bulk-link uses, the writes are guarded (pending -> dismissed only,
// manual candidates refused), and the response reports what happened.

type bulkRejectResp struct {
	Data struct {
		Attempted int `json:"attempted"`
		Rejected  int `json:"rejected"`
		Failed    int `json:"failed"`
		Failures  []struct {
			CandidateID int64  `json:"candidate_id"`
			Reason      string `json:"reason"`
		} `json:"failures"`
	} `json:"data"`
}

func decodeBulkReject(t *testing.T, body []byte) bulkRejectResp {
	t.Helper()
	var r bulkRejectResp
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("decode: %v; body=%s", err, body)
	}
	return r
}

func candidateStatus(t *testing.T, es *database.EmbeddingStore, id int64) string {
	t.Helper()
	c, err := es.GetCandidateByID(id)
	if err != nil || c == nil {
		t.Fatalf("GetCandidateByID(%d): %v", id, err)
	}
	return c.Status
}

func TestBulkRejectDedupCandidates_ScopedByFilter(t *testing.T) {
	h, d := newHandler(t)
	allowLabelCaptureReads(d)
	revID, _, _ := insertCandidateWithBand(t, d.es, "rev-a", "rev-b", "REVIEW")
	rev2ID, _, _ := insertCandidateWithBand(t, d.es, "rev-c", "rev-d", "REVIEW")
	certID, _, _ := insertCandidateWithBand(t, d.es, "cert-a", "cert-b", "CERTAIN")

	w := doReq(t, h.BulkRejectDedupCandidates, http.MethodPost,
		"/api/v1/dedup/candidates/bulk-reject", map[string]any{"band": "REVIEW"}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	r := decodeBulkReject(t, w.Body.Bytes())
	if r.Data.Attempted != 2 || r.Data.Rejected != 2 || r.Data.Failed != 0 {
		t.Fatalf("counts = %+v, want attempted=2 rejected=2 failed=0", r.Data)
	}
	for _, id := range []int64{revID, rev2ID} {
		if got := candidateStatus(t, d.es, id); got != "dismissed" {
			t.Fatalf("candidate %d status=%q want dismissed", id, got)
		}
	}
	// The band filter must narrow the WRITE, not just the count.
	if got := candidateStatus(t, d.es, certID); got != "pending" {
		t.Fatalf("out-of-filter candidate status=%q want pending", got)
	}
}

func TestBulkRejectDedupCandidates_RefusesOverTheBulkApplyCap(t *testing.T) {
	withBulkApplyCap(t, 3)
	h, d := newHandler(t)
	id, _, _ := insertCandidate(t, d.es, "cap-a", "cap-b")
	insertNCandidates(t, d, 3)
	w := doReq(t, h.BulkRejectDedupCandidates, http.MethodPost,
		"/api/v1/dedup/candidates/bulk-reject", map[string]any{}, nil)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "BULK_APPLY_CAP_EXCEEDED") {
		t.Fatalf("missing cap code: %s", w.Body.String())
	}
	if got := candidateStatus(t, d.es, id); got != "pending" {
		t.Fatalf("a refused bulk reject wrote anyway: status=%q", got)
	}
}

func TestBulkRejectDedupCandidates_ManualCandidateRefusedAndReported(t *testing.T) {
	h, d := newHandler(t)
	allowLabelCaptureReads(d)
	normalID, _, _ := insertCandidate(t, d.es, "norm-a", "norm-b")
	res, err := d.es.EnqueueManualCandidate("book", "man-a", "man-b", "pinned by hand")
	if err != nil {
		t.Fatalf("EnqueueManualCandidate: %v", err)
	}
	manualID := res.Candidate.ID

	w := doReq(t, h.BulkRejectDedupCandidates, http.MethodPost,
		"/api/v1/dedup/candidates/bulk-reject", map[string]any{}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	r := decodeBulkReject(t, w.Body.Bytes())
	if r.Data.Attempted != 2 || r.Data.Rejected != 1 || r.Data.Failed != 1 {
		t.Fatalf("counts = %+v, want attempted=2 rejected=1 failed=1", r.Data)
	}
	if len(r.Data.Failures) != 1 || r.Data.Failures[0].CandidateID != manualID ||
		!strings.Contains(r.Data.Failures[0].Reason, "manual") {
		t.Fatalf("manual candidate not reported: %+v", r.Data.Failures)
	}
	if got := candidateStatus(t, d.es, manualID); got != "pending" {
		t.Fatalf("manual candidate status=%q want pending", got)
	}
	if got := candidateStatus(t, d.es, normalID); got != "dismissed" {
		t.Fatalf("normal candidate status=%q want dismissed", got)
	}
}

func TestBulkRejectDedupCandidates_RefusesWhatItCannotScope(t *testing.T) {
	h, d := newHandler(t)
	id, _, _ := insertCandidate(t, d.es, "keep-a", "keep-b")
	for name, body := range map[string]any{
		"non-pending status": map[string]any{"status": "merged"},
		"both_unmatched":     map[string]any{"both_unmatched": true},
		"non-book entity":    map[string]any{"entity_type": "author"},
		"malformed body":     map[string]any{"band": 7},
	} {
		w := doReq(t, h.BulkRejectDedupCandidates, http.MethodPost,
			"/api/v1/dedup/candidates/bulk-reject", body, nil)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d want 400; body=%s", name, w.Code, w.Body.String())
		}
	}
	if got := candidateStatus(t, d.es, id); got != "pending" {
		t.Fatalf("a refused request wrote anyway: status=%q", got)
	}
}
