// file: internal/server/candidate_fallback_round3_test.go
// version: 1.0.0
// guid: 7f057c74-2af0-44a5-bf9a-214d739ab5f0
// last-edited: 2026-10-06

package server

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// Third review pass on #3796 (its second review round). All fixtures are
// synthetic.

// The hashless review_bulk marker is sent for a selected book whose row the
// lane never loaded or held no hash for, so nobody saw its candidate. It must
// not apply a review-only (Open Library / Google Books) candidate: those are
// applied only by an owner who saw them. A hash-bearing pin that matches the
// shown candidate still does.
func TestUnseenOwnerMarker_DoesNotLiftReviewOnlySource(t *testing.T) {
	tenHours := 36000
	books := func() fakeBooks {
		return fakeBooks{"b1": {ID: "b1", Title: "Moon Book", FilePath: "/lib/Zed Quill/Moon Book/Moon Book.m4b", Duration: &tenHours}}
	}
	for _, source := range []string{"Google Books", "Open Library"} {
		cand := metafetch.MetadataCandidate{Title: "Moon Book", Author: "Zed Quill", Source: source, Score: 0.99, DurationSec: 36000}

		svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
		out := applyCachedCandidateForBookTimed(svc, books(), nil, "b1", false, nil, metafetch.NewApplyPhaseTimings(), nil, ownerMarker(), "")
		if out.Applied || out.OwnerReviewed || out.Reason != applySkipGateBlocked ||
			out.Gate == nil || out.Gate.Reason != applygate.ReasonReviewOnlySource {
			t.Fatalf("%s via hashless marker: outcome %+v (gate %+v), want gate_blocked / review_only_source", source, out, out.Gate)
		}
		if len(svc.applyOpts) != 0 {
			t.Fatalf("%s via hashless marker: applied %+v", source, svc.applyOpts)
		}

		for name, pin := range map[string]*metafetch.CandidatePin{"bulk pin": bulkPin(cand), "row pin": rowPin(cand)} {
			svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
			out := applyCachedCandidateForBookTimed(svc, books(), nil, "b1", false, nil, metafetch.NewApplyPhaseTimings(), nil, pin, "")
			if !out.Applied || !out.OwnerReviewed {
				t.Fatalf("%s via %s matching the shown candidate: outcome %+v, want applied as owner-reviewed", source, name, out)
			}
		}
	}
}

// The marker still lifts the certainty legs on a chain (Audible) candidate,
// as the owner ruled for every review-page bulk button.
func TestUnseenOwnerMarker_StillLiftsCertaintyOnChainCandidate(t *testing.T) {
	books, cand := refusedByAuthorAndTranscription()
	svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
	out := applyCachedCandidateForBookTimed(svc, books, nil, "b1", false, nil, metafetch.NewApplyPhaseTimings(), nil, ownerMarker(), "")
	if !out.Applied || !out.OwnerReviewed {
		t.Fatalf("outcome %+v, want the marker to lift the certainty legs on an Audible candidate", out)
	}
}
