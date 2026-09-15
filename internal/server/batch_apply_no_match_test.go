// file: internal/server/batch_apply_no_match_test.go
// version: 1.0.0
// guid: 8d4f2a61-0c7b-4e93-b5a2-6f1e9d3c7a08
// last-edited: 2026-09-14

package server

import (
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// noMatchBooks is fakeBooks with b1 marked "no match" by its owner.
func noMatchBooks() fakeBooks {
	dur := 36000
	nm := "no_match"
	return fakeBooks{"b1": {ID: "b1", Title: "A Title", FilePath: "/lib/An Author/A Title/A Title.m4b",
		Duration: &dur, MetadataReviewStatus: &nm}}
}

// The cached batch apply (/batch-apply-cached and the review lane's bulk
// Apply) must not write onto a book the owner rejected: nothing is applied, no
// file work runs, and the book is reported under its own skip reason.
func TestApplyCachedCandidate_SkipsNoMatchBook(t *testing.T) {
	svc := &fakeApplySvc{candidates: oneCandidate(t)}
	out := applyCachedCandidateForBook(svc, noMatchBooks(), &fakeITunes{}, "b1", true, nil)
	if out.Applied || len(svc.appliedIDs) != 0 || len(svc.finishCalls) != 0 {
		t.Fatalf("a no-match book was applied: outcome %+v applied %v finish %d", out, svc.appliedIDs, len(svc.finishCalls))
	}
	if out.Reason != "marked_no_match" {
		t.Errorf("reason = %q, want marked_no_match", out.Reason)
	}
}

// batch-apply-candidates plans its applies with planOpResultApply; a no-match
// book must come back skipped with the same reason, before the gate runs.
func TestPlanOpResultApply_SkipsNoMatchBook(t *testing.T) {
	cand := metafetch.MetadataCandidate{Title: "A Title", Source: "audible", Score: 0.95}
	cr := CandidateResult{Book: CandidateBookInfo{ID: "b1", Title: "A Title"}, Candidate: &cand, Status: "matched"}
	plan := planOpResultApply(noMatchBooks(), "b1", cr, nil)
	if plan.Reason != "marked_no_match" {
		t.Fatalf("reason = %q, want marked_no_match (plan %+v)", plan.Reason, plan)
	}
}

// A review-lane approval (a row pin) is the owner picking this candidate for
// the book: it overrides their own no-match mark, as the single-book dialog
// does, and the apply is not fill-only.
func TestApplyCachedCandidate_ReviewApprovalOverridesNoMatch(t *testing.T) {
	raw := oneCandidate(t)
	svc := &fakeApplySvc{candidates: raw}
	var cand metafetch.MetadataCandidate
	if err := json.Unmarshal(raw[0], &cand); err != nil {
		t.Fatal(err)
	}
	pin := rowPin(cand)
	out := applyCachedCandidateForBookTimed(svc, noMatchBooks(), &fakeITunes{}, "b1", false, nil,
		metafetch.NewApplyPhaseTimings(), nil, pin)
	if !out.Applied || len(svc.appliedIDs) != 1 {
		t.Fatalf("the owner's approval was refused: outcome %+v", out)
	}
	if svc.applyOpts[0].FillOnly {
		t.Errorf("an owner-approved apply must not be fill-only: %+v", svc.applyOpts[0])
	}
}

// The dry-run preview must leave a no-match book out: it is neither a row
// nor counted as would-apply, blocked or skipped.
func TestBulkApplyPreview_ExcludesNoMatchBook(t *testing.T) {
	svc := &fakeApplySvc{candidates: oneCandidate(t)}
	plan := planCachedApply(svc, noMatchBooks(), "b1", nil, nil)
	if !excludedFromPreview(plan) {
		t.Fatalf("a no-match book would be shown in the preview: plan %+v", plan)
	}
	ok := planCachedApply(svc, fakeBooks{}, "b2", nil, nil)
	if excludedFromPreview(ok) {
		t.Fatalf("an ordinary book was excluded from the preview: plan %+v", ok)
	}
}
