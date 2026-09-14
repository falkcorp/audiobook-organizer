// file: internal/server/batch_apply_owner_review_test.go
// version: 1.1.0
// guid: 1a8c5e37-6f02-4d94-b7e3-9c4d2a0f5b81
// last-edited: 2026-09-13
//
// An owner-reviewed apply: the review lane pins the candidate it showed, and
// a matching pin lifts the certainty legs of the gate. A stale pin, no pin,
// and the non-certainty checks all still refuse.

package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// A Google Books candidate for book #1 carrying no volume number and no
// runtime: the shape of the owner's 2026-09-13 refusals.
func ownerReviewFixture() (fakeBooks, metafetch.MetadataCandidate) {
	tenHours := 36000
	books := fakeBooks{"b1": {ID: "b1", Title: "Big Cats 1", FilePath: "/lib/A/Big Cats/Big Cats 1.m4b", Duration: &tenHours}}
	cand := metafetch.MetadataCandidate{Title: "Big Cats", Author: "Ann Author", Source: "Google Books", ISBN13: "9780000000001", Score: 0.95}
	return books, cand
}

func TestOwnerReview_NoPinIsHardGated(t *testing.T) {
	books, cand := ownerReviewFixture()
	svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
	out := applyCachedCandidateForBookTimed(svc, books, nil, "b1", false, nil, metafetch.NewApplyPhaseTimings(), nil, nil)
	if out.Applied || out.Reason != applySkipGateBlocked || out.OwnerReviewed {
		t.Fatalf("no pin: outcome %+v, want gate_blocked", out)
	}
	if len(svc.appliedIDs) != 0 {
		t.Fatalf("no pin: applied %v", svc.appliedIDs)
	}
}

func TestOwnerReview_MatchingPinAppliesAndRecordsOverride(t *testing.T) {
	books, cand := ownerReviewFixture()
	svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
	pin := metafetch.PinOf(cand)
	out := applyCachedCandidateForBookTimed(svc, books, nil, "b1", false, nil, metafetch.NewApplyPhaseTimings(), nil, &pin)
	if !out.Applied || !out.OwnerReviewed {
		t.Fatalf("matching pin: outcome %+v, want applied as owner-reviewed", out)
	}
	if out.Gate == nil || out.Gate.Allowed {
		t.Fatalf("the gate must still run and report its refusal: %+v", out.Gate)
	}
	// The real verdict refuses on two legs at once (no volume number, no
	// runtime); the history must name both, not just the first.
	if len(svc.applyOpts) != 1 {
		t.Fatalf("applies: %+v", svc.applyOpts)
	}
	for _, want := range []string{applygate.ReasonSequenceMissingOnCandidate, applygate.ReasonRuntimeUnknownOverwrite} {
		if !strings.Contains(svc.applyOpts[0].GateOverride, want) {
			t.Errorf("override %q does not name %s", svc.applyOpts[0].GateOverride, want)
		}
	}
}

func TestOwnerReview_StalePinRefusesAndWritesNothing(t *testing.T) {
	books, cand := ownerReviewFixture()
	svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
	shown := metafetch.PinOf(cand)
	shown.Title = "Big Cats (a different record)"
	out := applyCachedCandidateForBookTimed(svc, books, nil, "b1", true, nil, metafetch.NewApplyPhaseTimings(), nil, &shown)
	if out.Applied || out.Reason != applySkipStaleCandidate {
		t.Fatalf("stale pin: outcome %+v, want %s", out, applySkipStaleCandidate)
	}
	if len(svc.appliedIDs)+len(svc.invalidatedID)+len(svc.finishCalls)+len(svc.preflightIDs) != 0 {
		t.Fatalf("stale pin touched the book: applied=%v invalidated=%v finish=%v preflight=%v",
			svc.appliedIDs, svc.invalidatedID, svc.finishCalls, svc.preflightIDs)
	}
}

// identity_stale is not a certainty judgement: the cache row answers a
// question the book no longer asks. A review of the candidate cannot lift it.
func TestOwnerReview_StaleIdentityStillBlocks(t *testing.T) {
	books, cand := ownerReviewFixture()
	svc := &fakeApplySvc{candidates: candidateJSON(t, cand), identityErr: metafetch.ErrStaleMetadataCache}
	pin := metafetch.PinOf(cand)
	out := applyCachedCandidateForBookTimed(svc, books, nil, "b1", false, nil, metafetch.NewApplyPhaseTimings(), nil, &pin)
	if out.Applied || out.Reason != applySkipGateBlocked || out.Gate == nil || out.Gate.Reason != applygate.ReasonIdentityStale {
		t.Fatalf("stale identity with a pin: outcome %+v", out)
	}
}

// The rename preflight is enforced for a reviewed apply exactly as for any
// other: the database and the files must not disagree.
func TestOwnerReview_RenamePreflightStillBlocks(t *testing.T) {
	books, cand := ownerReviewFixture()
	svc := &fakeApplySvc{candidates: candidateJSON(t, cand), preflightErr: metafetch.ErrApplyFileWorkWouldFail}
	pin := metafetch.PinOf(cand)
	out := applyCachedCandidateForBookTimed(svc, books, nil, "b1", true, nil, metafetch.NewApplyPhaseTimings(), nil, &pin)
	if out.Applied || out.Reason != applySkipFileWorkWouldFail || !out.OwnerReviewed {
		t.Fatalf("preflight with a pin: outcome %+v", out)
	}
	if len(svc.appliedIDs) != 0 {
		t.Fatalf("applied despite a failing preflight: %v", svc.appliedIDs)
	}
}

// The dry run takes no pin: it reports the hard gate, and says what a
// reviewed Apply would do.
func TestOwnerReview_PreviewReportsWouldApply(t *testing.T) {
	books, cand := ownerReviewFixture()
	svc := fakePreviewSvc{&fakeApplySvc{candidates: candidateJSON(t, cand)}}
	plan := planCachedApply(svc, books, "b1", nil, nil)
	row := previewBulkApplyRow(svc, "b1", plan, false)
	if row.Verdict != previewVerdictBlocked || !row.OwnerReviewedWouldApply {
		t.Fatalf("preview row: verdict %q reason %q owner_reviewed_would_apply %v", row.Verdict, row.Reason, row.OwnerReviewedWouldApply)
	}

	stale := fakePreviewSvc{&fakeApplySvc{candidates: candidateJSON(t, cand), identityErr: metafetch.ErrStaleMetadataCache}}
	row = previewBulkApplyRow(stale, "b1", planCachedApply(stale, books, "b1", nil, nil), false)
	if row.OwnerReviewedWouldApply {
		t.Fatalf("identity_stale must not read as reviewable: %+v", row)
	}

	// The op-results path takes no pin, so its dry run never claims it.
	cr := CandidateResult{Status: "matched", Candidate: &cand}
	cr.Book.Title = "Big Cats 1"
	opPlan := planOpResultApply(books, "b1", cr, nil)
	if opPlan.Reason != applySkipGateBlocked {
		t.Fatalf("op-results plan: reason %q, want gate_blocked", opPlan.Reason)
	}
	if row = previewBulkApplyRow(svc, "b1", opPlan, false); row.OwnerReviewedWouldApply {
		t.Fatalf("op-results preview claimed an owner review would apply: %+v", row)
	}
}

type fakePreviewSvc struct{ *fakeApplySvc }

func (fakePreviewSvc) PreviewMetadataCandidate(string, metafetch.MetadataCandidate, bool) (*metafetch.ApplyPreview, error) {
	return &metafetch.ApplyPreview{}, nil
}

// A queued run that absorbs a second request keeps both requests' pins; a
// dropped pin would silently hard-gate a book the owner reviewed.
func TestOwnerReview_QueuedMergeKeepsPins(t *testing.T) {
	a := metafetch.CandidatePin{Source: "Audible", Title: "A"}
	b1 := metafetch.CandidatePin{Source: "Audible", Title: "B old"}
	b2 := metafetch.CandidatePin{Source: "Audible", Title: "B new"}
	cur, _ := json.Marshal(batchApplyOpParams{BookIDs: []string{"a", "b"}, WriteBack: true, Pins: map[string]metafetch.CandidatePin{"a": a, "b": b1}})
	next, _ := json.Marshal(batchApplyOpParams{BookIDs: []string{"b", "c"}, WriteBack: true, Pins: map[string]metafetch.CandidatePin{"b": b2}})
	raw, ok, err := mergeBatchApplyQueuedParams(cur, next)
	if err != nil || !ok {
		t.Fatalf("merge: ok=%v err=%v", ok, err)
	}
	var got batchApplyOpParams
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Pins["a"] != a || got.Pins["b"] != b2 || len(got.Pins) != 2 {
		t.Fatalf("merged pins %+v, want a kept and b from the newer request", got.Pins)
	}
}

// The checkpoint carries the pins of the books still owed, so a resumed run
// is still owner-reviewed.
func TestOwnerReview_CheckpointCarriesPins(t *testing.T) {
	pins := map[string]metafetch.CandidatePin{"a": {Title: "A"}, "c": {Title: "C"}}
	st := batchApplyCheckpointState([]string{"a", "b", "c"}, true, 3, 1, nil)
	st.Pins = pinsFor(st.BookIDs, pins)
	raw, _ := json.Marshal(st)
	var back batchApplyOpParams
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Pins) != 1 || back.Pins["c"].Title != "C" {
		t.Fatalf("checkpoint pins %+v, want only c (a is done)", back.Pins)
	}
	if back.pinOf("c") == nil || back.pinOf("b") != nil {
		t.Fatalf("pinOf: c=%v b=%v", back.pinOf("c"), back.pinOf("b"))
	}
}

// PinOf/Matches: every identity field must match.
func TestCandidatePin_Matches(t *testing.T) {
	c := metafetch.MetadataCandidate{Source: "Audible", Title: "T", Author: "A", ASIN: "B00X"}
	if !metafetch.PinOf(c).Matches(c) {
		t.Fatal("a pin must match the candidate it was taken from")
	}
	other := c
	other.ASIN = "B00Y"
	if metafetch.PinOf(c).Matches(other) {
		t.Fatal("a different ASIN is a different record")
	}
}
