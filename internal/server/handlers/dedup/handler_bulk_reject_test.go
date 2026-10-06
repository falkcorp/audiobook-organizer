// file: internal/server/handlers/dedup/handler_bulk_reject_test.go
// version: 1.2.0
// guid: 5d65b248-8432-43e0-9a13-2da6e1228ceb
// last-edited: 2026-10-06

package deduphandler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
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
		RejectedIDs []int64 `json:"rejected_ids"`
		Reverted    int     `json:"reverted"`
		RevertedIDs []int64 `json:"reverted_ids"`
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
	allowBooksExist(d)
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

// 40 candidates through the 8-worker pool with the engine wired, so the label
// capture (GetCandidateByID, the scorer, UpsertLabeledExample) really runs
// concurrently. Meaningful under -race.
func TestBulkRejectDedupCandidates_ConcurrentWorkersCaptureEveryLabel(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID(mock.Anything).
		Return(&database.Book{ID: "x", Title: "T"}, nil).Maybe()
	d.store.EXPECT().GetBookFiles(mock.Anything).
		Return([]database.BookFile{{FilePath: "/lib/a.m4b", FileSize: 5 << 20, Duration: 3600}}, nil).Maybe()
	var scored atomic.Int32
	d.engine.EXPECT().ScorePairsForBook(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _ string, _ []dedupengine.RescorePairInput) ([]dedupengine.RescorePairResult, error) {
			scored.Add(1)
			return nil, nil
		}).Maybe()
	const n = 40
	for i := range n {
		insertCandidate(t, d.es, "cc-a"+strconv.Itoa(i), "cc-b"+strconv.Itoa(i))
	}

	w := doReq(t, h.BulkRejectDedupCandidates, http.MethodPost,
		"/api/v1/dedup/candidates/bulk-reject", map[string]any{"expected_total": n}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	r := decodeBulkReject(t, w.Body.Bytes())
	if r.Data.Rejected != n || len(r.Data.RejectedIDs) != n {
		t.Fatalf("rejected=%d ids=%d want %d", r.Data.Rejected, len(r.Data.RejectedIDs), n)
	}
	for _, id := range r.Data.RejectedIDs {
		ex, err := d.es.GetLabeledExample(id)
		if err != nil || ex == nil || ex.Label != "not_dup" {
			t.Fatalf("candidate %d: bulk not_dup label missing (%v, %+v)", id, err, ex)
		}
	}
	if scored.Load() == 0 {
		t.Fatalf("label capture never reached the scorer")
	}
}

// A mistaken bulk dismiss is undone by id: the rows go back to pending and
// the bulk not_dup labels are removed. A row someone re-decided since is
// reported and left alone.
func TestRevertBulkRejectDedupCandidates_RestoresPendingAndDropsBulkLabels(t *testing.T) {
	h, d := newHandler(t)
	allowLabelCaptureReads(d)
	idA, _, _ := insertCandidate(t, d.es, "rv-a", "rv-b")
	idB, _, _ := insertCandidate(t, d.es, "rv-c", "rv-d")

	w := doReq(t, h.BulkRejectDedupCandidates, http.MethodPost,
		"/api/v1/dedup/candidates/bulk-reject", map[string]any{}, nil)
	r := decodeBulkReject(t, w.Body.Bytes())
	if len(r.Data.RejectedIDs) != 2 {
		t.Fatalf("rejected_ids=%v want 2; body=%s", r.Data.RejectedIDs, w.Body.String())
	}
	// Someone merges B before the revert.
	if err := d.es.UpdateCandidateStatus(idB, "merged"); err != nil {
		t.Fatalf("merge B: %v", err)
	}

	w = doReq(t, h.RevertBulkRejectDedupCandidates, http.MethodPost,
		"/api/v1/dedup/candidates/bulk-reject/revert", map[string]any{"candidate_ids": r.Data.RejectedIDs}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", w.Code, w.Body.String())
	}
	rv := decodeBulkReject(t, w.Body.Bytes())
	if rv.Data.Reverted != 1 || rv.Data.Failed != 1 || rv.Data.Failures[0].CandidateID != idB {
		t.Fatalf("want A reverted and B refused; body=%s", w.Body.String())
	}
	if got := candidateStatus(t, d.es, idA); got != "pending" {
		t.Fatalf("A status=%q want pending", got)
	}
	if got := candidateStatus(t, d.es, idB); got != "merged" {
		t.Fatalf("B status=%q want merged (left alone)", got)
	}
	if ex, _ := d.es.GetLabeledExample(idA); ex != nil {
		t.Fatalf("A's bulk not_dup label survived the revert: %+v", ex)
	}
}

// The revert acts only on dismissals a bulk dismiss made, and puts back the
// label the bulk dismiss replaced instead of erasing it.
func TestRevertBulkReject_OnlyBulkDismissalsAndRestoresEarlierLabel(t *testing.T) {
	h, d := newHandler(t)
	allowLabelCaptureReads(d)
	bulkID, _, _ := insertCandidate(t, d.es, "pl-a", "pl-b")
	if err := d.es.UpsertLabeledExample(database.LabeledExample{
		CandidateID: bulkID, EntityAID: "pl-a", EntityBID: "pl-b",
		Label: "unsure", LabelSource: "human", LabelReason: "reviewer_note",
	}); err != nil {
		t.Fatal(err)
	}
	w := doReq(t, h.BulkRejectDedupCandidates, http.MethodPost,
		"/api/v1/dedup/candidates/bulk-reject", map[string]any{}, nil)
	if r := decodeBulkReject(t, w.Body.Bytes()); r.Data.Rejected != 1 {
		t.Fatalf("bulk reject: %s", w.Body.String())
	}
	// A pair dismissed one at a time is someone else's verdict.
	singleID, _, _ := insertCandidate(t, d.es, "sg-a", "sg-b")
	if w := doReq(t, h.RejectDedupCandidate, http.MethodPost,
		"/api/v1/dedup/candidates/"+strconv.FormatInt(singleID, 10)+"/reject", nil,
		gin.Params{{Key: "id", Value: strconv.FormatInt(singleID, 10)}}); w.Code != http.StatusOK {
		t.Fatalf("single reject: %d %s", w.Code, w.Body.String())
	}

	w = doReq(t, h.RevertBulkRejectDedupCandidates, http.MethodPost,
		"/api/v1/dedup/candidates/bulk-reject/revert",
		map[string]any{"candidate_ids": []int64{bulkID, singleID}}, nil)
	rv := decodeBulkReject(t, w.Body.Bytes())
	if rv.Data.Reverted != 1 || rv.Data.Failed != 1 || rv.Data.Failures[0].CandidateID != singleID {
		t.Fatalf("want only the bulk dismissal reverted; body=%s", w.Body.String())
	}
	if got := candidateStatus(t, d.es, singleID); got != "dismissed" {
		t.Fatalf("single dismissal changed: %q", got)
	}
	ex, _ := d.es.GetLabeledExample(bulkID)
	if ex == nil || ex.Label != "unsure" || ex.LabelReason != "reviewer_note" {
		t.Fatalf("earlier label not restored: %+v", ex)
	}
}
