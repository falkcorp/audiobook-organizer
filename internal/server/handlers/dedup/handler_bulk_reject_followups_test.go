// file: internal/server/handlers/dedup/handler_bulk_reject_followups_test.go
// version: 1.0.0
// guid: d1363515-1b2b-4dc3-86c0-614ebe421362
// last-edited: 2026-10-06

package deduphandler_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	deduphandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/dedup"
)

// Follow-ups to the #3783 review: the undo record decides what a revert may
// touch, a failed save never leaves an un-undoable dismissal, a book read
// error is not a dead book, and a failed restore can be retried.

// S1 + N1: the undo record cannot be saved (the row's current label is a
// bulk-dismiss label with no record behind it, so saving it as "the earlier
// verdict" is refused). The row is reported as failed, its dismiss is rolled
// back and the label it had is left exactly as it was.
func TestBulkReject_UndoRecordSaveFailureLeavesTheRowAndReportsIt(t *testing.T) {
	h, d := newHandler(t)
	allowLabelCaptureReads(d)
	stuckID, _, _ := insertCandidate(t, d.es, "sv-a", "sv-b")
	okID, _, _ := insertCandidate(t, d.es, "sv-c", "sv-d")
	stale := database.LabeledExample{
		CandidateID: stuckID, EntityAID: "sv-a", EntityBID: "sv-b",
		Label: "not_dup", LabelSource: "human", LabelReason: database.LabelReasonUserBulkDismiss,
		DecidedAt: "2026-01-01T00:00:00Z",
	}
	if err := d.es.UpsertLabeledExample(stale); err != nil {
		t.Fatal(err)
	}

	w := doReq(t, h.BulkRejectDedupCandidates, http.MethodPost,
		"/api/v1/dedup/candidates/bulk-reject", map[string]any{}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; body=%s", w.Code, w.Body.String())
	}
	r := decodeBulkReject(t, w.Body.Bytes())
	if r.Data.Attempted != 2 || r.Data.Rejected != 1 || r.Data.Failed != 1 {
		t.Fatalf("counts=%+v want attempted=2 rejected=1 failed=1", r.Data)
	}
	if r.Data.Rejected+r.Data.Failed != r.Data.Attempted {
		t.Fatalf("rows unaccounted for: %+v", r.Data)
	}
	if len(r.Data.Failures) != 1 || r.Data.Failures[0].CandidateID != stuckID ||
		!strings.Contains(r.Data.Failures[0].Reason, "undo record not saved") {
		t.Fatalf("save failure not reported: %+v", r.Data.Failures)
	}
	if len(r.Data.RejectedIDs) != 1 || r.Data.RejectedIDs[0] != okID {
		t.Fatalf("rejected_ids=%v want only %d", r.Data.RejectedIDs, okID)
	}
	if got := candidateStatus(t, d.es, stuckID); got != "pending" {
		t.Fatalf("failed row status=%q want pending (rolled back)", got)
	}
	got, _ := d.es.GetLabeledExample(stuckID)
	if got == nil || got.DecidedAt != stale.DecidedAt || got.LabelReason != stale.LabelReason {
		t.Fatalf("failed row's label was overwritten: %+v", got)
	}
}

// S2: the not_dup capture fails (the feature snapshot cannot read the book's
// files), so the dismissed row carries no bulk label. It is still undoable,
// because the revert is keyed off the undo record, not the label reason.
func TestRevertBulkReject_WorksWhenTheCaptureFailed(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookFiles(mock.Anything).Return(nil, errors.New("files unreadable")).Maybe()
	allowLabelCaptureReads(d)
	id, _, _ := insertCandidate(t, d.es, "cf-a", "cf-b")

	w := doReq(t, h.BulkRejectDedupCandidates, http.MethodPost,
		"/api/v1/dedup/candidates/bulk-reject", map[string]any{}, nil)
	r := decodeBulkReject(t, w.Body.Bytes())
	if len(r.Data.RejectedIDs) != 1 {
		t.Fatalf("bulk reject: %s", w.Body.String())
	}
	if ex, _ := d.es.GetLabeledExample(id); ex != nil {
		t.Fatalf("test premise: the capture should have failed, got %+v", ex)
	}

	w = doReq(t, h.RevertBulkRejectDedupCandidates, http.MethodPost,
		"/api/v1/dedup/candidates/bulk-reject/revert", map[string]any{"candidate_ids": r.Data.RejectedIDs}, nil)
	rv := decodeBulkReject(t, w.Body.Bytes())
	if rv.Data.Reverted != 1 || rv.Data.Failed != 0 {
		t.Fatalf("want the row reverted; body=%s", w.Body.String())
	}
	if got := candidateStatus(t, d.es, id); got != "pending" {
		t.Fatalf("status=%q want pending", got)
	}
}

// N2: the status step succeeds and the label restore fails. The row is
// reported as failed (not in reverted_ids), and sending it again finishes the
// restore.
func TestRevertBulkReject_FailedRestoreIsReportedAndRetryable(t *testing.T) {
	h, d := newHandler(t)
	allowLabelCaptureReads(d)
	id, _, _ := insertCandidate(t, d.es, "rr-a", "rr-b")
	earlier := database.LabeledExample{
		CandidateID: id, EntityAID: "rr-a", EntityBID: "rr-b",
		Label: "unsure", LabelSource: "human", LabelReason: "reviewer_note",
	}
	if err := d.es.UpsertLabeledExample(earlier); err != nil {
		t.Fatal(err)
	}
	w := doReq(t, h.BulkRejectDedupCandidates, http.MethodPost,
		"/api/v1/dedup/candidates/bulk-reject", map[string]any{}, nil)
	if r := decodeBulkReject(t, w.Body.Bytes()); r.Data.Rejected != 1 {
		t.Fatalf("bulk reject: %s", w.Body.String())
	}

	deduphandler.SetRestoreBulkLabelForTest(h, func(int64) (bool, error) {
		return false, errors.New("disk full")
	})
	body := map[string]any{"candidate_ids": []int64{id}}
	w = doReq(t, h.RevertBulkRejectDedupCandidates, http.MethodPost,
		"/api/v1/dedup/candidates/bulk-reject/revert", body, nil)
	rv := decodeBulkReject(t, w.Body.Bytes())
	if rv.Data.Reverted != 0 || rv.Data.Failed != 1 ||
		!strings.Contains(rv.Data.Failures[0].Reason, "revert this id again") {
		t.Fatalf("restore failure not reported; body=%s", w.Body.String())
	}
	if got := candidateStatus(t, d.es, id); got != "pending" {
		t.Fatalf("status=%q want pending", got)
	}

	deduphandler.SetRestoreBulkLabelForTest(h, d.es.RestoreLabelAfterBulkRevert)
	w = doReq(t, h.RevertBulkRejectDedupCandidates, http.MethodPost,
		"/api/v1/dedup/candidates/bulk-reject/revert", body, nil)
	rv = decodeBulkReject(t, w.Body.Bytes())
	if rv.Data.Reverted != 1 || rv.Data.Failed != 0 {
		t.Fatalf("retry did not finish the revert; body=%s", w.Body.String())
	}
	ex, _ := d.es.GetLabeledExample(id)
	if ex == nil || ex.LabelReason != "reviewer_note" {
		t.Fatalf("earlier label not restored on retry: %+v", ex)
	}
	// Done: a third send finds nothing to undo.
	w = doReq(t, h.RevertBulkRejectDedupCandidates, http.MethodPost,
		"/api/v1/dedup/candidates/bulk-reject/revert", body, nil)
	if rv = decodeBulkReject(t, w.Body.Bytes()); rv.Data.Reverted != 0 || rv.Data.Failed != 1 {
		t.Fatalf("finished revert ran again; body=%s", w.Body.String())
	}
}

// S3: a book that cannot be read is not a dead book. bulk-count, bulk-reject
// and the list refuse the request instead of silently dropping the row.
func TestBookReadErrorRefusesBulkAndList(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("rd-b").Return(nil, errors.New("hydrate book_sig: corrupt")).Maybe()
	d.store.EXPECT().GetBookByID(mock.Anything).Return(&database.Book{ID: "x"}, nil).Maybe()
	insertCandidate(t, d.es, "rd-c", "rd-d")
	id, _, _ := insertCandidate(t, d.es, "rd-a", "rd-b")

	for name, fn := range map[string]func() int{
		"bulk-count": func() int {
			return doReq(t, h.BulkCountDedupCandidates, http.MethodPost,
				"/api/v1/dedup/candidates/bulk-count", map[string]any{}, nil).Code
		},
		"bulk-reject": func() int {
			return doReq(t, h.BulkRejectDedupCandidates, http.MethodPost,
				"/api/v1/dedup/candidates/bulk-reject", map[string]any{}, nil).Code
		},
		"list": func() int {
			return doReq(t, h.ListDedupCandidates, http.MethodGet,
				"/api/v1/dedup/candidates?entity_type=book&status=pending", nil, nil).Code
		},
		"list include_books": func() int {
			return doReq(t, h.ListDedupCandidates, http.MethodGet,
				"/api/v1/dedup/candidates?include_books=true", nil, nil).Code
		},
	} {
		if code := fn(); code != http.StatusInternalServerError {
			t.Fatalf("%s: status=%d want 500", name, code)
		}
	}
	if got := candidateStatus(t, d.es, id); got != "pending" {
		t.Fatalf("a refused bulk reject wrote anyway: status=%q", got)
	}
}
