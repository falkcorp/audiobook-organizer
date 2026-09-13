// file: internal/server/batch_apply_claims_test.go
// version: 1.0.0
// guid: e4b9c7a2-1f36-4d80-b5c9-8a0d2e6f3b71
// last-edited: 2026-09-13

package server

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// TestBuildClaimIndex_FeedsPlan: two folders of one book take the same
// cached candidate. With the batch's claim index the gate refuses the whole-
// book folder (its sibling is CD 2); without one (nil) that test is skipped.
func TestBuildClaimIndex_FeedsPlan(t *testing.T) {
	cand := metafetch.MetadataCandidate{Title: "The Long Road", Author: "Jane Roe", ASIN: "B0TESTROAD", Score: 0.99}
	books := fakeBooks{
		"b1": {ID: "b1", Title: "The Long Road", FilePath: "/lib/Jane Roe/The Long Road/The Long Road.m4b", Author: &database.Author{Name: "Jane Roe"}},
		"b2": {ID: "b2", Title: "The Long Road", FilePath: "/lib/Jane Roe/The Long Road/CD 2", Author: &database.Author{Name: "Jane Roe"}},
	}
	svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}

	claims := buildClaimIndex(context.Background(), []string{"b1", "b2"}, cachedClaimLoader(svc, books))
	if claims.Len() != 2 {
		t.Fatalf("claims = %d, want 2", claims.Len())
	}
	// A book with no cached candidate claims nothing.
	if n := buildClaimIndex(context.Background(), []string{"b1"}, cachedClaimLoader(&fakeApplySvc{}, books)).Len(); n != 0 {
		t.Fatalf("no-candidate book claimed %d", n)
	}
	p := planCachedApply(svc, books, "b1", claims)
	if p.Reason != applySkipGateBlocked || p.Gate == nil || p.Gate.Evidence.Reason != applygate.ReasonPartialBook {
		t.Fatalf("with claims: reason=%q gate=%+v", p.Reason, p.Gate)
	}
	if p := planCachedApply(svc, books, "b1", nil); p.Gate != nil && p.Gate.Evidence.Reason == applygate.ReasonPartialBook {
		t.Fatalf("without claims the sibling test must be skipped: %+v", p.Gate.Evidence)
	}

	crs := map[string]CandidateResult{"b1": {Status: "matched", Candidate: &cand}, "b2": {Status: "matched", Candidate: &cand}}
	opClaims := buildClaimIndex(context.Background(), []string{"b1", "b2"}, opResultClaimLoader(books, func(id string) (CandidateResult, bool) {
		cr, ok := crs[id]
		return cr, ok
	}))
	if opClaims.Len() != claims.Len() {
		t.Fatalf("op-result index %d claims, cache index %d: the two paths must agree", opClaims.Len(), claims.Len())
	}
}
