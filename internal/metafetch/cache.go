// file: internal/metafetch/cache.go
// version: 1.2.0
//
// Cache-layer on top of metafetch.Service. The persisted record type
// lives in internal/database (MetadataCandidateCache) — re-exported
// here via a type alias so existing metafetch callers keep their
// import path. The forbidden direction (database → metafetch) is
// preserved: metafetch imports database, never the other way.

package metafetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ErrStaleMetadataCache is returned by ValidateCachedIdentity when the cache
// entry's stored SourceHash does not match a hash recomputed over the book's
// CURRENT search inputs — i.e. the book's identity drifted since the cache was
// written. Callers treat a non-nil error as "skip + log" (fail-closed).
var ErrStaleMetadataCache = errors.New("metadata cache stale: source hash mismatch")

// MetadataCandidateCache is a re-export of the persistence type so
// metafetch callers don't need to know about internal/database.
type MetadataCandidateCache = database.MetadataCandidateCache

// MetadataCacheSummary is the lightweight enumeration record.
type MetadataCacheSummary = database.MetadataCacheSummary

// metadataCacheTopN caps how many candidates we persist per book.
// Matches the existing default response size.
const metadataCacheTopN = 10

// nowUTC is overridable for tests.
var nowUTC = func() time.Time { return time.Now().UTC() }

// GetCachedCandidates returns the cached entry for bookID plus a
// freshness flag (entry.IsFresh()). Returns (nil, false, nil) for
// cache-miss. Errors are real I/O failures.
func (mfs *Service) GetCachedCandidates(bookID string) (*MetadataCandidateCache, bool, error) {
	if mfs == nil || mfs.db == nil {
		return nil, false, nil
	}
	entry, err := mfs.db.GetMetadataCache(bookID)
	if err != nil {
		return nil, false, err
	}
	if entry == nil {
		return nil, false, nil
	}
	return entry, entry.IsFresh(), nil
}

// ValidateCachedIdentity closes the metadata-cache TOCTOU window (INIT-3-T5):
// the cache is keyed only by book ID, so an entry can be refreshed between the
// gate read and the apply. This recomputes the existing hashSearchInputs over
// the book's CURRENT fields and compares it to the SourceHash stored at write
// time. It reuses hashSearchInputs exactly — no second hashing scheme — and
// does not touch mfs.db, so it is safe on a minimal Service.
//
// Three-case semantics (mirrored in the tests):
//   - stored hash EMPTY (legacy row predating the field being load-bearing) →
//     fail-OPEN: slog.Warn + nil, so an UNCHANGED book still applies via the
//     existing slot-0 identity guard;
//   - hash MISMATCH → fail-CLOSED: ErrStaleMetadataCache (wrapped with book ID);
//   - hash MATCH → nil.
//
// Note: the two production cache writers hash different input shapes — the batch
// path (metadata_batch_candidates.go) passes narrator/series as empty strings;
// the UI handler path passes user-typed values. Recomputing over the book's
// current fields therefore fails CLOSED for a row whose stored hash came from
// inputs that differ from the book's fields (e.g. a book with a narrator cached
// via the batch path). That is intentional and conservative: refusing an apply
// never mutates data — it only declines to reuse a cache row whose provenance
// no longer matches the book.
func (mfs *Service) ValidateCachedIdentity(entry *MetadataCandidateCache, bookID, query, author, narrator, series string) error {
	if entry == nil {
		return nil
	}
	if entry.SourceHash == "" {
		// Legacy row written before SourceHash was load-bearing. Fail open so
		// an unchanged book still applies; the slot-0 identity guard remains.
		slog.Warn("metafetch ValidateCachedIdentity: legacy cache row has empty SourceHash, applying (fail-open)", "id", bookID)
		return nil
	}
	want := hashSearchInputs(bookID, query, author, narrator, series)
	if entry.SourceHash != want {
		return fmt.Errorf("%w: book %s (stored %s, current %s)", ErrStaleMetadataCache, bookID, entry.SourceHash, want)
	}
	return nil
}

// FetchAndCache runs the existing search pipeline, writes top-N to
// the cache (always replaces), and returns the resulting entry.
//
// This is the "manual = invalidate" path — every call overwrites
// whatever was there. Use GetCachedCandidates for cache-respecting
// reads.
func (mfs *Service) FetchAndCache(ctx context.Context, bookID, query, author, narrator, series string, opts SearchOptions) (*MetadataCandidateCache, error) {
	if mfs == nil {
		return nil, fmt.Errorf("FetchAndCache: nil Service")
	}
	resp, err := mfs.SearchMetadataForBookWithOptions(bookID, query, author, narrator, series, opts)
	if err != nil {
		return nil, err
	}
	candidates := resp.Results
	if len(candidates) > metadataCacheTopN {
		candidates = candidates[:metadataCacheTopN]
	}
	raw := make([]json.RawMessage, 0, len(candidates))
	for _, c := range candidates {
		b, jerr := json.Marshal(c)
		if jerr != nil {
			// Skip a single corrupt candidate rather than fail.
			continue
		}
		raw = append(raw, b)
	}

	entry := &MetadataCandidateCache{
		BookID:     bookID,
		Candidates: raw,
		FetchedAt:  nowUTC(),
		SourceHash: hashSearchInputs(bookID, query, author, narrator, series),
	}
	if mfs.db != nil {
		if err := mfs.db.PutMetadataCache(entry); err != nil {
			// Cache failure should not break the user's fetch; log and
			// continue (callers can still consume the in-memory entry).
						slog.Warn("metafetch FetchAndCache write", "id", bookID, "error", err)
			return entry, nil
		}
	}
	return entry, nil
}

// ListCachedSummaries returns one summary per cached entry, ordered
// by FetchedAt descending.
func (mfs *Service) ListCachedSummaries(_ context.Context) ([]MetadataCacheSummary, error) {
	if mfs == nil || mfs.db == nil {
		return nil, nil
	}
	return mfs.db.ListMetadataCacheKeys()
}

// InvalidateCachedCandidates removes the cache entry for bookID. Used
// when book metadata changes underneath us (manual edit, metadata
// apply, organize rename) so the next read fetches fresh.
func (mfs *Service) InvalidateCachedCandidates(bookID string) error {
	if mfs == nil || mfs.db == nil {
		return nil
	}
	return mfs.db.DeleteMetadataCache(bookID)
}

// hashSearchInputs builds a short stable digest of the search inputs
// so v2 can compare against the inputs the cached entry came from.
func hashSearchInputs(bookID, query, author, narrator, series string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s", bookID, query, author, narrator, series)
	return hex.EncodeToString(h.Sum(nil))[:16]
}
