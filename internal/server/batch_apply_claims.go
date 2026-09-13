// file: internal/server/batch_apply_claims.go
// version: 1.0.0
// guid: 0c6a9e42-7d1b-4f83-a5e2-9b3f1d7c4e60
// last-edited: 2026-09-13

package server

import (
	"context"
	"encoding/json"
	"runtime"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// claimLoader returns the book and the candidate a bulk apply would put on
// it, or nils when the book would take nothing.
type claimLoader func(id string) (*database.Book, *metafetch.MetadataCandidate)

// buildClaimIndex is the pre-pass every batch path runs BEFORE its first
// apply, so the certainty gate's partial_book check can see a sibling folder
// holding another part of the same book (applygate.ClaimIndex).
//
// All three batch paths call this one helper: the dry-run preview, the cached
// batch op and /metadata/batch-apply-candidates. The preview promises it
// reports the apply's own gate verdict; that holds only while the apply
// builds its index the same way, from the same book list.
//
// It reads only, one DB read (plus a cache read) per book, bounded to
// runtime.NumCPU() workers. A book whose load fails simply claims nothing: it
// is also not applied, so it cannot be the sibling a rename collides with. A
// cancelled ctx stops the pre-pass early; the run is ending anyway.
func buildClaimIndex(ctx context.Context, ids []string, load claimLoader) *applygate.ClaimIndex {
	idx := applygate.NewClaimIndex()
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	for _, id := range ids {
		if gctx.Err() != nil {
			break
		}
		g.Go(func() error {
			if b, c := load(id); b != nil && c != nil {
				idx.Add(b, c)
			}
			return nil
		})
	}
	_ = g.Wait()
	return idx
}

// cachedClaimLoader loads the claim planCachedApply would act on: the top
// cached candidate.
func cachedClaimLoader(svc cachedApplyService, books bookReader) claimLoader {
	return func(id string) (*database.Book, *metafetch.MetadataCandidate) {
		entry, _, err := svc.GetCachedCandidates(id)
		if err != nil || entry == nil || len(entry.Candidates) == 0 {
			return nil, nil
		}
		var cand metafetch.MetadataCandidate
		if json.Unmarshal(entry.Candidates[0], &cand) != nil {
			return nil, nil
		}
		book, err := books.GetBookByID(id)
		if err != nil || book == nil {
			return nil, nil
		}
		return book, &cand
	}
}

// opResultClaimLoader loads the claim planOpResultApply would act on: a
// "matched" candidate-fetch result.
func opResultClaimLoader(books bookReader, get func(id string) (CandidateResult, bool)) claimLoader {
	return func(id string) (*database.Book, *metafetch.MetadataCandidate) {
		cr, ok := get(id)
		if !ok || cr.Status != "matched" || cr.Candidate == nil {
			return nil, nil
		}
		book, err := books.GetBookByID(id)
		if err != nil || book == nil {
			return nil, nil
		}
		return book, cr.Candidate
	}
}
