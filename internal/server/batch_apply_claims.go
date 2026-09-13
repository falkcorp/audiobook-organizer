// file: internal/server/batch_apply_claims.go
// version: 1.1.0
// guid: 0c6a9e42-7d1b-4f83-a5e2-9b3f1d7c4e60
// last-edited: 2026-09-13

package server

import (
	"context"
	"encoding/json"
	"fmt"
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
// ids must be the whole candidate SOURCE, never the request's own book list:
// every book with cached candidates (cachedClaimIndex) or every row of the
// operation (keysOf on its results). The preview and the apply then build the
// same index whatever subset each is asked about, so a book the preview
// blocks for a sibling is blocked at apply too, on a resumed run as well.
//
// It reads only, one DB read (plus a cache read) per book, bounded to
// runtime.NumCPU() workers. A book whose load fails simply claims nothing. A
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

// cachedCandidateGetter is the one cache read a cached claim needs.
type cachedCandidateGetter interface {
	GetCachedCandidates(bookID string) (*metafetch.MetadataCandidateCache, bool, error)
}

// cachedClaimSource lists every cached book and reads its candidates.
type cachedClaimSource interface {
	cachedCandidateGetter
	ListCachedSummaries(ctx context.Context) ([]metafetch.MetadataCacheSummary, error)
}

// cachedClaimIndex builds the claim index over EVERY book with cached
// candidates, the universe both the cached batch op and the cache-backed
// preview draw from. It takes no book list on purpose: see buildClaimIndex.
// Built once per run. A listing error is returned, not skipped: a smaller
// index would silently let a sibling part through.
func cachedClaimIndex(ctx context.Context, svc cachedClaimSource, books bookReader) (*applygate.ClaimIndex, error) {
	sums, err := svc.ListCachedSummaries(ctx)
	if err != nil {
		return nil, fmt.Errorf("list cached books for the sibling-part index: %w", err)
	}
	ids := make([]string, 0, len(sums))
	for _, sm := range sums {
		if sm.CandidateCount > 0 {
			ids = append(ids, sm.BookID)
		}
	}
	return buildClaimIndex(ctx, ids, cachedClaimLoader(svc, books)), nil
}

// keysOf is the book IDs of an operation's result map: the op-results
// universe for buildClaimIndex.
func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// cachedClaimLoader loads the claim planCachedApply would act on: the top
// cached candidate.
func cachedClaimLoader(svc cachedCandidateGetter, books bookReader) claimLoader {
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
