// file: internal/server/batch_apply_claims.go
// version: 1.2.0
// guid: 0c6a9e42-7d1b-4f83-a5e2-9b3f1d7c4e60
// last-edited: 2026-09-13

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// claimLoader returns the book and the candidate a bulk apply would put on
// it, nils when the book claims nothing, or an error when it could not be
// read.
type claimLoader func(id string) (*database.Book, *metafetch.MetadataCandidate, error)

// claimBookReader reads a book and its files: a sibling claim needs both.
type claimBookReader interface {
	bookReader
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

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
// It reads only, bounded to runtime.NumCPU() workers. It fails CLOSED: a book
// that could not be read, or a cancelled ctx, returns an error and no index,
// because a smaller index would silently let a sibling part through. The
// caller fails the op (or the request) before any write.
func buildClaimIndex(ctx context.Context, ids []string, load claimLoader) (*applygate.ClaimIndex, error) {
	idx := applygate.NewClaimIndex()
	var failed atomic.Int64
	var firstMu sync.Mutex
	var first error
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	for _, id := range ids {
		if gctx.Err() != nil {
			break
		}
		g.Go(func() error {
			b, c, err := load(id)
			if err != nil {
				failed.Add(1)
				firstMu.Lock()
				if first == nil {
					first = fmt.Errorf("book %s: %w", id, err)
				}
				firstMu.Unlock()
				return nil
			}
			if b != nil && c != nil {
				idx.Add(b, c)
			}
			return nil
		})
	}
	_ = g.Wait()
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("sibling-part index: %w", err)
	}
	if n := failed.Load(); n > 0 {
		return nil, fmt.Errorf("sibling-part index: %d of %d books could not be read; first: %w", n, len(ids), first)
	}
	return idx, nil
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
// Built once per run. A listing error is returned, not skipped.
func cachedClaimIndex(ctx context.Context, svc cachedClaimSource, books claimBookReader) (*applygate.ClaimIndex, error) {
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
	return buildClaimIndex(ctx, ids, cachedClaimLoader(svc, books))
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

// claimBook reads the book behind a claim, or nil when it must not claim a
// part:
//   - gone (GetBookByID returns nil for a missing row);
//   - soft-deleted (marked for deletion): its files are on their way out;
//   - a non-primary member of a version group: another copy of the same book
//     (a second rip, a different edition), not a PART of it;
//   - every one of its file rows is missing: there is nothing on disk to be
//     a sibling part. A book with no file rows at all still claims: nothing
//     says its files are gone.
func claimBook(books claimBookReader, id string) (*database.Book, error) {
	book, err := books.GetBookByID(id)
	if err != nil {
		return nil, fmt.Errorf("read book: %w", err)
	}
	if book == nil || book.IsSoftDeleted() || (book.IsPrimaryVersion != nil && !*book.IsPrimaryVersion) {
		return nil, nil
	}
	files, err := books.GetBookFiles(id)
	if err != nil {
		return nil, fmt.Errorf("read book files: %w", err)
	}
	if len(files) > 0 {
		for _, f := range files {
			if !f.Missing {
				return book, nil
			}
		}
		return nil, nil
	}
	return book, nil
}

// cachedClaimLoader loads the claim planCachedApply would act on: the top
// cached candidate.
func cachedClaimLoader(svc cachedCandidateGetter, books claimBookReader) claimLoader {
	return func(id string) (*database.Book, *metafetch.MetadataCandidate, error) {
		entry, _, err := svc.GetCachedCandidates(id)
		if err != nil {
			return nil, nil, fmt.Errorf("read cached candidates: %w", err)
		}
		if entry == nil || len(entry.Candidates) == 0 {
			return nil, nil, nil
		}
		var cand metafetch.MetadataCandidate
		if err := json.Unmarshal(entry.Candidates[0], &cand); err != nil {
			return nil, nil, fmt.Errorf("decode cached candidate: %w", err)
		}
		book, err := claimBook(books, id)
		if err != nil || book == nil {
			return nil, nil, err
		}
		return book, &cand, nil
	}
}

// opResultClaimLoader loads the claim planOpResultApply would act on: a
// "matched" candidate-fetch result. get reports a row it could not decode as
// an error.
func opResultClaimLoader(books claimBookReader, get func(id string) (CandidateResult, bool, error)) claimLoader {
	return func(id string) (*database.Book, *metafetch.MetadataCandidate, error) {
		cr, ok, err := get(id)
		if err != nil {
			return nil, nil, err
		}
		if !ok || cr.Status != "matched" || cr.Candidate == nil {
			return nil, nil, nil
		}
		book, err := claimBook(books, id)
		if err != nil || book == nil {
			return nil, nil, err
		}
		return book, cr.Candidate, nil
	}
}
