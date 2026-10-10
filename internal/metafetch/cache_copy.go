// file: internal/metafetch/cache_copy.go
// version: 1.0.1
// guid: 5b0f6c2e-9a41-4d7e-8c13-2e7a4f9d0b68
// last-edited: 2026-10-09

package metafetch

import (
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ErrNoCandidatesToCopy is CopyCandidateCache's refusal when the source book
// holds no cached candidates (none was fetched, or they were deleted since).
var ErrNoCandidatesToCopy = errors.New("metadata cache copy: the source book holds no cached candidates")

// CopyCandidateCache copies the candidates cached for book fromID onto book
// toID, re-keyed so the identity checks judge the copy as a row fetched FOR
// toID: a byte-for-byte copy could never pass ValidateCachedIdentityForBook,
// because every SourceHash binds the book id it was fetched for.
//
// It is for two versions of one book whose search identity is the same (the
// caller proves that; maintenance.version-twin-metadata requires the same
// normalised title and author). The copy is fetch state only: nothing is
// applied, and the target's book row is not written.
//
//   - Candidates and FetchedAt are the source's (FetchedAt dates the
//     candidates, and these are not fresher for having been copied).
//   - SourceHash is the batch shape (BatchSourceHash) for the target's title
//     and live primary author, the shape the batch fetch writes.
//   - SearchFingerprint keeps the source row's standing: a row current for
//     the source gets the target's current fingerprint, a version "1" row
//     the target's legacy one, and a row stale for the source (or with none)
//     gets none, which no fetch trusts as a verdict.
//   - FetchedForASIN is the source's, so ASINReplaced / CandidateIdentityStale
//     still refuse a candidate on a target identified by another ASIN.
//
// The copy is checked against the target before it is written: it must pass
// ValidateCachedIdentityForBook for the target, or nothing is written.
//
// check runs last, on the target book and its cache row as re-read
// immediately before the write; a non-nil error refuses the write and is
// returned as is. The store offers no compare-and-set on a cache row, so a
// candidate fetch for the target that lands between that re-read and the Put
// is overwritten -- by candidates for the same search identity, which is the
// only case the caller copies.
func (mfs *Service) CopyCandidateCache(fromID, toID string, check func(target *database.Book, cur *MetadataCandidateCache) error) (*MetadataCandidateCache, error) {
	if mfs == nil || mfs.db == nil {
		return nil, fmt.Errorf("metadata cache copy: service not initialized")
	}
	if fromID == toID {
		return nil, fmt.Errorf("metadata cache copy: source and target are the same book %s", fromID)
	}
	src, err := mfs.db.GetMetadataCache(fromID)
	if err != nil {
		return nil, fmt.Errorf("read the candidate cache of %s: %w", fromID, err)
	}
	if src == nil || len(src.Candidates) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoCandidatesToCopy, fromID)
	}
	from, err := mfs.db.GetBookByID(fromID)
	if err != nil {
		return nil, fmt.Errorf("read source book %s: %w", fromID, err)
	}
	if from == nil {
		return nil, fmt.Errorf("source book %s not found", fromID)
	}
	fromLive, err := database.LiveBookAuthorNames(mfs.db, from)
	if err != nil {
		return nil, fmt.Errorf("read live authors of %s: %w", fromID, err)
	}
	target, err := mfs.db.GetBookByID(toID)
	if err != nil {
		return nil, fmt.Errorf("read target book %s: %w", toID, err)
	}
	if target == nil {
		return nil, fmt.Errorf("target book %s not found", toID)
	}
	targetLive, err := database.LiveBookAuthorNames(mfs.db, target)
	if err != nil {
		return nil, fmt.Errorf("read live authors of %s: %w", toID, err)
	}

	cp := &MetadataCandidateCache{
		BookID:         toID,
		Candidates:     append(src.Candidates[:0:0], src.Candidates...),
		FetchedAt:      src.FetchedAt,
		SourceHash:     BatchSourceHash(toID, target.Title, firstAuthorHint(targetLive)),
		FetchedForASIN: src.FetchedForASIN,
	}
	if src.SearchFingerprint != "" {
		in := mfs.resolveSearchInputs(target, target.Title, firstAuthorHint(targetLive), "")
		switch mfs.matchSearchFingerprint(src.SearchFingerprint, from, from.Title, firstAuthorHint(fromLive), "") {
		case fingerprintCurrent:
			cp.SearchFingerprint = in.fingerprint(target.Title)
		case fingerprintLegacy:
			cp.SearchFingerprint = in.legacyFingerprint(target.Title)
		}
	}
	if verr := mfs.ValidateCachedIdentityForBook(cp, target, targetLive); verr != nil {
		return nil, fmt.Errorf("metadata cache copy %s -> %s: the copy does not pass the target's identity check: %w", fromID, toID, verr)
	}

	if check != nil {
		fresh, ferr := mfs.db.GetBookByID(toID)
		if ferr != nil {
			return nil, fmt.Errorf("re-read target book %s: %w", toID, ferr)
		}
		if fresh == nil {
			return nil, fmt.Errorf("target book %s vanished before the copy", toID)
		}
		cur, cerr := mfs.db.GetMetadataCache(toID)
		if cerr != nil {
			return nil, fmt.Errorf("re-read the candidate cache of %s: %w", toID, cerr)
		}
		if err := check(fresh, cur); err != nil {
			return nil, err
		}
	}
	if err := mfs.db.PutMetadataCache(cp); err != nil {
		return nil, fmt.Errorf("write the candidate cache of %s: %w", toID, err)
	}
	return cp, nil
}

// firstAuthorHint is the author hint the batch fetch searches a book by: its
// live primary author, with a placeholder dropped to "" (SearchAuthorHint).
func firstAuthorHint(live []string) string {
	if len(live) == 0 {
		return ""
	}
	return SearchAuthorHint(live[0])
}
