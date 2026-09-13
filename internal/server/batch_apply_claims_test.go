// file: internal/server/batch_apply_claims_test.go
// version: 1.1.0
// guid: e4b9c7a2-1f36-4d80-b5c9-8a0d2e6f3b71
// last-edited: 2026-09-13

package server

import (
	"context"
	"errors"
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

// listingSvc adds the cache listing to fakeApplySvc: the books with cached
// candidates, the universe cachedClaimIndex draws from.
type listingSvc struct {
	*fakeApplySvc
	ids     []string
	listErr error
}

func (l listingSvc) ListCachedSummaries(context.Context) ([]metafetch.MetadataCacheSummary, error) {
	out := make([]metafetch.MetadataCacheSummary, 0, len(l.ids))
	for _, id := range l.ids {
		out = append(out, metafetch.MetadataCacheSummary{BookID: id, CandidateCount: 1})
	}
	return out, l.listErr
}

// TestClaimIndex_SubsetApplySeesSibling is review F4: the apply is asked
// about b1 alone, yet the sibling folder b2 holding Part 2 of the same
// candidate must still block it, as it does in a preview of both. The index
// is built from the whole source (every cached book, every op row), never the
// request's list, so the two cannot disagree.
func TestClaimIndex_SubsetApplySeesSibling(t *testing.T) {
	cand := metafetch.MetadataCandidate{Title: "A Fall of Moondust", Author: "Arthur C. Clarke", ASIN: "B0TESTMOON", Score: 0.99}
	books := fakeBooks{
		"b1": {ID: "b1", Title: "A Fall of Moondust", FilePath: "/lib/Arthur C. Clarke/A Fall of Moondust/A Fall of Moondust.m4b", Author: &database.Author{Name: "Arthur C. Clarke"}},
		"b2": {ID: "b2", Title: "A Fall of Moondust", FilePath: "/lib/Arthur C. Clarke/A Fall of Moondust/Part 2", Author: &database.Author{Name: "Arthur C. Clarke"}},
	}
	svc := listingSvc{fakeApplySvc: &fakeApplySvc{candidates: candidateJSON(t, cand)}, ids: []string{"b1", "b2"}}

	// Cached op and cache-backed preview: the request names only b1.
	claims, err := cachedClaimIndex(context.Background(), svc, books)
	if err != nil {
		t.Fatal(err)
	}
	p := planCachedApply(svc, books, "b1", claims)
	if p.Reason != applySkipGateBlocked || p.Gate == nil || p.Gate.Evidence.Reason != applygate.ReasonPartialBook {
		t.Fatalf("cached subset apply of b1: reason=%q gate=%+v, want blocked %q", p.Reason, p.Gate, applygate.ReasonPartialBook)
	}

	// batch-apply-candidates: the op has rows for both, the request only b1.
	rows := map[string]CandidateResult{"b1": {Status: "matched", Candidate: &cand}, "b2": {Status: "matched", Candidate: &cand}}
	opClaims := buildClaimIndex(context.Background(), keysOf(rows), opResultClaimLoader(books, func(id string) (CandidateResult, bool) {
		cr, ok := rows[id]
		return cr, ok
	}))
	if p := planOpResultApply(books, "b1", rows["b1"], opClaims); p.Reason != applySkipGateBlocked || p.Gate.Evidence.Reason != applygate.ReasonPartialBook {
		t.Fatalf("op-result subset apply of b1: reason=%q gate=%+v", p.Reason, p.Gate)
	}

	// A failed listing is an error, never a smaller index.
	if _, err := cachedClaimIndex(context.Background(), listingSvc{fakeApplySvc: svc.fakeApplySvc, listErr: errors.New("boom")}, books); err == nil {
		t.Fatal("listing error was swallowed")
	}
}
