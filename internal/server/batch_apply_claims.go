// file: internal/server/batch_apply_claims.go
// version: 1.3.0
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
// it, nils when the book claims nothing, or an error when it could not be
// read. With an error it still returns whatever it did read (the book, for
// its folder; the candidate, for its ASIN), so the index can block that
// book's look-alikes.
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
// same index whatever subset each is asked about.
//
// A book that cannot be read is not skipped and does not fail the run (owner
// decision, 2026-09-13): it is recorded in the index with whatever is still
// known (folder, ASIN), every row that looks like it (a related folder or the
// same ASIN) is blocked for manual review, and every other row proceeds.
// ClaimIndex.Unreadable is the count the caller reports. A book with nothing
// known blocks nothing. A cancelled ctx still returns an error: the index is
// then incomplete in an unknown way.
//
// It reads only, bounded to runtime.NumCPU() workers.
func buildClaimIndex(ctx context.Context, ids []string, load claimLoader) (*applygate.ClaimIndex, error) {
	idx := applygate.NewClaimIndex()
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	for _, id := range ids {
		if gctx.Err() != nil {
			break
		}
		g.Go(func() error {
			b, c, err := load(id)
			if err != nil {
				var path, asin string
				if b != nil {
					path = b.FilePath
				}
				if c != nil {
					asin = c.ASIN
				}
				idx.AddUnreadable(id, path, asin)
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
// Built once per run. A LISTING error fails the run: without the list there
// is no universe to index at all.
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
//
// When the files cannot be read it returns the book WITH the error, so the
// caller still knows the folder.
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
		return book, fmt.Errorf("read book files: %w", err)
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

// bestEffortBook reads the book for its folder when another read failed.
func bestEffortBook(books claimBookReader, id string) *database.Book {
	b, err := books.GetBookByID(id)
	if err != nil {
		return nil
	}
	return b
}

// cachedClaimLoader loads the claim planCachedApply would act on: the top
// cached candidate.
func cachedClaimLoader(svc cachedCandidateGetter, books claimBookReader) claimLoader {
	return func(id string) (*database.Book, *metafetch.MetadataCandidate, error) {
		entry, _, err := svc.GetCachedCandidates(id)
		if err != nil {
			return bestEffortBook(books, id), nil, fmt.Errorf("read cached candidates: %w", err)
		}
		if entry == nil || len(entry.Candidates) == 0 {
			return nil, nil, nil
		}
		var cand metafetch.MetadataCandidate
		if err := json.Unmarshal(entry.Candidates[0], &cand); err != nil {
			return bestEffortBook(books, id), nil, fmt.Errorf("decode cached candidate: %w", err)
		}
		book, err := claimBook(books, id)
		if err != nil {
			return book, &cand, err
		}
		if book == nil {
			return nil, nil, nil
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
			return bestEffortBook(books, id), nil, err
		}
		if !ok || cr.Status != "matched" || cr.Candidate == nil {
			return nil, nil, nil
		}
		book, err := claimBook(books, id)
		if err != nil {
			return book, cr.Candidate, err
		}
		if book == nil {
			return nil, nil, nil
		}
		return book, cr.Candidate, nil
	}
}
