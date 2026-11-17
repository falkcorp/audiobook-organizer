// file: internal/server/batch_apply_opresult_already_applied_test.go
// version: 1.0.0
// guid: 7c3e9a15-2b84-4d60-9f1a-d5e8b4c06a72
// last-edited: 2026-11-17

package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// opResultFixture is a "matched" candidate-fetch result for b1, as the fetch
// recorded it before anything applied the book.
func opResultFixture() CandidateResult {
	cand := metafetch.MetadataCandidate{Title: "A Title", Source: "audible", Score: 0.95}
	return CandidateResult{Book: CandidateBookInfo{ID: "b1", Title: "A Title"}, Candidate: &cand, Status: "matched"}
}

// batch-apply-candidates plans each book with planOpResultApply. The op
// result's status flips to "applied" only when this path applies the book, so
// a book applied since the fetch through any other path (the cached bulk
// apply, the review lane, the single-book dialog) still has a "matched"
// result. It must come back already_applied, as planCachedApply does, not be
// gated and applied again.
func TestPlanOpResultApply_SkipsAnAppliedBook(t *testing.T) {
	for _, status := range []string{"matched", "audio_confirmed"} {
		t.Run(status, func(t *testing.T) {
			plan := planOpResultApply(appliedBooks(status), "b1", opResultFixture(), nil)
			require.Equal(t, applySkipAlreadyApplied, plan.Reason, "plan %+v", plan)
			require.Error(t, plan.Err)
			require.Nil(t, plan.Gate, "an applied book must be left out before the gate runs")
		})
	}
}

// The skip keys on the applied statuses only: a book nobody applied (no
// status, pending) keeps its old path through the gate.
func TestPlanOpResultApply_AlreadyAppliedOnlyForAppliedStatuses(t *testing.T) {
	for _, status := range []string{"", "pending", "no_match"} {
		books := appliedBooks(status)
		if status == "" {
			books["b1"].MetadataReviewStatus = nil
		}
		p := planOpResultApply(books, "b1", opResultFixture(), nil)
		require.NotEqual(t, applySkipAlreadyApplied, p.Reason, "status %q was skipped as already applied", status)
	}
}

// The dry run uses the same plan, so it reports an applied book as skipped /
// already_applied and never as "would apply".
func TestOpResultPreview_ReportsAnAppliedBookAsSkipped(t *testing.T) {
	plan := planOpResultApply(appliedBooks("matched"), "b1", opResultFixture(), nil)
	require.False(t, excludedFromPreview(plan), "an applied book must get a row")
	svc := fakePreviewSvc{&fakeApplySvc{candidates: oneCandidate(t)}}
	row := previewBulkApplyRow(svc, "b1", plan, true, false)
	require.Equal(t, previewVerdictSkipped, row.Verdict)
	require.Equal(t, applySkipAlreadyApplied, row.Reason)
}

// The handler counts an already-applied book as skipped, not as an error: the
// cached bulk apply does the same (batch_apply_op.go). It returns before it
// touches any service, so the zero Server is enough, and an apply that did run
// would panic on the nil metadata service.
func TestApplyOpResultCandidateLocked_AnAppliedBookIsSkippedNotAnError(t *testing.T) {
	raw, err := json.Marshal(opResultFixture())
	require.NoError(t, err)
	byBook := map[string]database.OperationResult{"b1": {OperationID: "op1", BookID: "b1", ResultJSON: string(raw), Status: "matched"}}
	s := &Server{}
	out := s.applyOpResultCandidateLocked(context.Background(), appliedBooks("audio_confirmed"), "op1", "b1", byBook, nil)
	require.True(t, out.skipped, "outcome %+v", out)
	require.True(t, out.alreadyApplied, "the queued apply logs this skip only when it was for an applied match")
	require.False(t, out.applied)
	require.False(t, out.blocked)
	require.Empty(t, out.errMsg)
}

// A book marked "no match" is skipped too, but it is not an applied match, so
// the queued apply's already-applied log line must not claim it is.
func TestApplyOpResultCandidateLocked_ANoMatchSkipIsNotAnAppliedMatch(t *testing.T) {
	raw, err := json.Marshal(opResultFixture())
	require.NoError(t, err)
	byBook := map[string]database.OperationResult{"b1": {OperationID: "op1", BookID: "b1", ResultJSON: string(raw), Status: "matched"}}
	out := (&Server{}).applyOpResultCandidateLocked(context.Background(), appliedBooks("no_match"), "op1", "b1", byBook, nil)
	require.True(t, out.skipped, "outcome %+v", out)
	require.False(t, out.alreadyApplied)
}
