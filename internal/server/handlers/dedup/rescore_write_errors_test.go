// file: internal/server/handlers/dedup/rescore_write_errors_test.go
// version: 1.0.0
// guid: 3f1a86c4-92de-4b70-a5e3-c07f4d29b118
// last-edited: 2026-09-02

package deduphandler_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/stretchr/testify/mock"
)

// TestRescoreEndpoint_PartialWriteBackIsNotA200 closes the third gate on
// RescoreResult.WriteErrors.
//
// PR #3058 made a partly-failed re-band reportable: Rescore returns a nil error
// with WriteErrors > 0, and the rows it could not write still carry the
// PREVIOUS ladder's band, which AutoResolveCertain acts on. Two of the three
// callers were taught to treat that as failure (the dedup.rescore op, and
// ReloadScoreConfig). This endpoint was the third — and it is the one every
// partial-re-band message tells the operator to run, so a 200 here means the
// remedy reports success having done half the job.
//
// Mutation check: delete the `if result.WriteErrors > 0` block in
// RescoreDedupCandidates and this fails with a 200.
func TestRescoreEndpoint_PartialWriteBackIsNotA200(t *testing.T) {
	h, d := newHandler(t)
	d.engine.EXPECT().Rescore(mock.Anything, true).
		Return(dedup.RescoreResult{
			Inspected: 10, Changed: 6, Written: 2, WriteErrors: 4, Applied: true,
		}, nil).Once()

	w := w2rawReq(t, h.RescoreDedupCandidates, "/api/v1/dedup/rescore", `{"apply":true}`, true)

	if w.Code == http.StatusOK {
		t.Fatalf("a re-band that failed to write 4 of 6 changed rows returned 200; the operator following a partial-re-band remedy would believe it finished. body=%s", w.Body.String())
	}
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "4") {
		t.Errorf("the response should name how many rows were not re-banded; body=%s", body)
	}
}

// TestRescoreEndpoint_CleanRunStillReturns200 is the control: the guard above
// must not turn every rescore into a 500.
func TestRescoreEndpoint_CleanRunStillReturns200(t *testing.T) {
	h, d := newHandler(t)
	d.engine.EXPECT().Rescore(mock.Anything, true).
		Return(dedup.RescoreResult{Inspected: 10, Changed: 6, Written: 6, Applied: true}, nil).Once()

	w := w2rawReq(t, h.RescoreDedupCandidates, "/api/v1/dedup/rescore", `{"apply":true}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("a fully-written re-band returned %d, want 200; body=%s", w.Code, w.Body.String())
	}
}
