// file: internal/metafetch/cache.go
// version: 1.9.0
// guid: a4f33a2e-3b4d-4306-bdce-476758e39120
// last-edited: 2026-09-19
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
	"slices"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"golang.org/x/time/rate"
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

// ValidateCachedIdentityForBook is ValidateCachedIdentity for a caller holding
// the book, and it knows the two input shapes the cache writers use.
//
// The batch fetch (fetchCandidateForBook) hashes (title, author, "", ""): it
// never searched by narrator or series. Recomputing over the book's CURRENT
// narrator and series, as the transcription path does, would fail closed on
// every batch-cached book that has either one, which is most of a library, and
// a bulk-apply gate built on that would read as mass identity drift that never
// happened. So a row passes when its hash matches either the full shape
// (title, author, narrator, series — the UI writer for an untyped search) or
// the batch shape (title, author, "", ""). Both are "the book's current
// fields"; a row matching neither was fetched for a title or author the book
// no longer has, and fails closed with ErrStaleMetadataCache. A legacy row
// with no hash keeps ValidateCachedIdentity's fail-open.
func (mfs *Service) ValidateCachedIdentityForBook(entry *MetadataCandidateCache, book *database.Book) error {
	if entry == nil {
		return nil
	}
	if book == nil {
		return fmt.Errorf("%w: book %s no longer exists", ErrStaleMetadataCache, entry.BookID)
	}
	author, narrator, series := "", "", ""
	if book.Author != nil {
		author = book.Author.Name
	}
	if book.Narrator != nil {
		narrator = *book.Narrator
	}
	if book.Series != nil {
		series = book.Series.Name
	}
	if entry.SourceHash != "" && entry.SourceHash == hashSearchInputs(book.ID, book.Title, author, "", "") {
		return nil
	}
	return mfs.ValidateCachedIdentity(entry, book.ID, book.Title, author, narrator, series)
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
	return mfs.cacheSearchResponse(bookID, query, author, narrator, series, resp), nil
}

// FetchAndCacheLimited is FetchAndCache for batch callers that must throttle
// ACTUAL outbound requests: the limiter is threaded into the search core so each
// live source call (not each book) acquires a token, and ctx is propagated so a
// batch cancel aborts in-flight requests. Cache hits consume no tokens. A nil
// limiter behaves exactly like FetchAndCache.
func (mfs *Service) FetchAndCacheLimited(ctx context.Context, limiter *rate.Limiter, bookID, query, author, narrator, series string, opts SearchOptions) (*MetadataCandidateCache, error) {
	if mfs == nil {
		return nil, fmt.Errorf("FetchAndCacheLimited: nil Service")
	}
	resp, err := mfs.searchMetadataForBook(ctx, limiter, bookID, query, author, narrator, series, opts)
	if err != nil {
		return nil, err
	}
	return mfs.cacheSearchResponse(bookID, query, author, narrator, series, resp), nil
}

// cacheSearchResponse writes the top-N candidates from a search response to the
// candidate cache and returns the resulting entry. Shared by FetchAndCache and
// FetchAndCacheLimited so both persist results identically.
//
// A search that comes back EMPTY does not erase candidates an earlier search
// found. This write was unconditional until 2026-09-08 — the doc comment here
// said "always replaces" — so a single empty provider response overwrote a good
// entry with `Candidates: []` and a fresh FetchedAt. That silently moved a book
// out of the review queue and into the "unreviewable" bucket, and because a
// book's review verdict is stored separately from its candidates, the verdict
// outlived the evidence it was based on. Production on 2026-09-08 held 212 books
// carrying a matched/no_match/audio_confirmed verdict with zero candidates to
// justify it, and 8,714 zero-candidate entries stamped "fresh" — an empty
// refetch marks itself current on the way past.
//
// Dropping candidates because the book changed underneath us is a real need, but
// it is not this function's job: InvalidateCachedCandidates already does exactly
// that, from the paths that know it happened (manual edit, metadata apply,
// organize rename).
//
// SourceHash is the discriminator, and this is its first non-diagnostic use.
// Same inputs + zero results means the providers had nothing to say this time
// and the stored candidates are still the best answer anyone has, so they stay
// and only LastEmptyFetchAt moves. Different inputs means the title/author/
// series the candidates answer to no longer exists, so replacing them is right.
func (mfs *Service) cacheSearchResponse(bookID, query, author, narrator, series string, resp *SearchMetadataResponse) *MetadataCandidateCache {
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

	sourceHash := hashSearchInputs(bookID, query, author, narrator, series)
	entry := &MetadataCandidateCache{
		BookID:            bookID,
		Candidates:        raw,
		FetchedAt:         nowUTC(),
		SourceHash:        sourceHash,
		SearchFingerprint: resp.InputFingerprint,
	}

	// Preserve-on-empty. A search WITH results always replaces, exactly as
	// before, and leaves LastEmptyFetchAt nil -- the invariant is "the last time
	// a search for these inputs came back with nothing", so a search that found
	// something clears it rather than carrying a stale one forward.
	if len(raw) == 0 {
		now := nowUTC()
		entry.LastEmptyFetchAt = &now
		entry.EmptyAnswers = emptyAnswers(resp, now)
		if mfs.db != nil {
			if prev, perr := mfs.db.GetMetadataCache(bookID); perr == nil && prev != nil && prev.SourceHash == sourceHash {
				if len(prev.Candidates) > 0 {
					entry.Candidates = prev.Candidates
					// NOT bumped: FetchedAt dates the CANDIDATES, and these are the
					// ones the previous search returned. Moving it would relabel
					// month-old candidates as freshly fetched.
					entry.FetchedAt = prev.FetchedAt
				}
				// Same questions (fingerprint): a source that answered "nothing"
				// earlier and was not asked, or could not be asked, this time still
				// answered "nothing" -- keep its answer with ITS timestamp, so each
				// answer ages on its own. A different fingerprint means different
				// questions, and the old answers say nothing about them.
				if prev.SearchFingerprint != "" && prev.SearchFingerprint == entry.SearchFingerprint {
					entry.EmptyAnswers = mergeEmptyAnswers(prev.EmptyAnswers, entry.EmptyAnswers, now)
				}
			}
		}
	}

	if mfs.db != nil {
		if err := mfs.db.PutMetadataCache(entry); err != nil {
			// Cache failure should not break the user's fetch; log and
			// continue (callers can still consume the in-memory entry).
			slog.Warn("metafetch FetchAndCache write", "id", logger.SanitizeLogValue(bookID), "error", logger.SanitizeLogValue(err.Error()))
			return entry
		}
	}
	return entry
}

// emptyAnswers records, at now, every source in resp that answered: its
// ladder completed without error (SearchMetadataResponse.SourcesAnswered).
// Only meaningful for a response with no results, where every such source
// answered "nothing".
func emptyAnswers(resp *SearchMetadataResponse, now time.Time) map[string]time.Time {
	if resp == nil || len(resp.SourcesAnswered) == 0 {
		return nil
	}
	out := make(map[string]time.Time, len(resp.SourcesAnswered))
	for _, name := range resp.SourcesAnswered {
		out[name] = now
	}
	return out
}

// mergeEmptyAnswers overlays fresh on prev, keeping the newer time per source
// and dropping answers already past MetadataKnownEmptyTTL at now.
func mergeEmptyAnswers(prev, fresh map[string]time.Time, now time.Time) map[string]time.Time {
	out := make(map[string]time.Time, len(prev)+len(fresh))
	for _, m := range []map[string]time.Time{prev, fresh} {
		for name, at := range m {
			if now.Sub(at) >= database.MetadataKnownEmptyTTL {
				continue
			}
			if cur, ok := out[name]; !ok || at.After(cur) {
				out[name] = at
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// mergeSourceNames returns the sorted, de-duplicated union of a and b, or nil
// when both are empty.
func mergeSourceNames(a, b []string) []string {
	if len(a)+len(b) == 0 {
		return nil
	}
	out := make([]string, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	slices.Sort(out)
	return slices.Compact(out)
}

// ActiveSourceNames returns the names of the metadata sources a search would
// ask right now (the test override, or the configured chain), sorted.
func (mfs *Service) ActiveSourceNames() []string {
	if mfs == nil {
		return nil
	}
	sources := mfs.overrideSources
	if len(sources) == 0 {
		sources = mfs.BuildSourceChain()
	}
	names := make([]string, 0, len(sources))
	for _, src := range sources {
		names = append(names, src.Name())
	}
	return mergeSourceNames(nil, names)
}

// BatchVerdict is what the candidate cache already knows about a book, as far
// as the batch candidate fetch is concerned.
type BatchVerdict int

const (
	// BatchVerdictNone: the cache cannot answer for the book as it is now
	// (no row, a legacy row, inputs changed, stale candidates, or an empty
	// result some enabled provider has no valid answer for). Ask the
	// providers CachedBatchVerdict names -- all of them when it names none.
	BatchVerdictNone BatchVerdict = iota
	// BatchVerdictFreshCandidates: the row holds candidates fetched for the
	// book's current inputs within MetadataCacheTTL. Serve them.
	BatchVerdictFreshCandidates
	// BatchVerdictKnownEmpty: every currently enabled provider has answered
	// the book's current inputs with nothing within MetadataKnownEmptyTTL.
	// Asking again is the same question to the same providers. Re-opened by
	// an input change (anything in the search fingerprint, including an
	// author rename), a newly enabled provider, an answer ageing past the
	// TTL, or an explicit force.
	BatchVerdictKnownEmpty
)

// CachedBatchVerdict decides whether the batch candidate fetch may answer
// book (searched as query/author, the hints the fetch passes) from the
// candidate cache without a provider call. It returns the entry it decided
// on, the verdict, and -- for BatchVerdictNone on an empty entry whose
// questions still match -- the providers that lack a valid answer, which are
// the only ones the fetch needs to ask. A nil list means "ask every provider".
//
// The entry is trusted only when BOTH identities hold: the cache's SourceHash
// rule (ValidateCachedIdentityForBook, with a missing hash never trusted) and
// the search fingerprint, which binds it to the questions actually asked
// (resolved author included). An entry with no fingerprint predates
// 2026-09-19; the path that wrote it recorded an empty result even when every
// provider had failed, so it proves nothing and is re-asked in full.
func (mfs *Service) CachedBatchVerdict(book *database.Book, query, author string) (*MetadataCandidateCache, BatchVerdict, []string) {
	if mfs == nil || mfs.db == nil || book == nil {
		return nil, BatchVerdictNone, nil
	}
	entry, err := mfs.db.GetMetadataCache(book.ID)
	if err != nil || entry == nil || entry.SourceHash == "" || entry.SearchFingerprint == "" {
		return entry, BatchVerdictNone, nil
	}
	if mfs.ValidateCachedIdentityForBook(entry, book) != nil {
		return entry, BatchVerdictNone, nil
	}
	if entry.SearchFingerprint != mfs.SearchFingerprintFor(book, query, author, "") {
		return entry, BatchVerdictNone, nil
	}
	now := nowUTC()
	if len(entry.Candidates) > 0 {
		// Fresh by "last checked", the same rule the review list uses for its
		// is_fresh flag (and so for the stale-refetch button): a search that
		// came back empty an hour ago kept these candidates and re-dated the
		// look, not the candidates.
		if entry.IsFresh() ||
			(entry.LastEmptyFetchAt != nil && now.Sub(*entry.LastEmptyFetchAt) < database.MetadataCacheTTL) {
			return entry, BatchVerdictFreshCandidates, nil
		}
		return entry, BatchVerdictNone, nil
	}
	active := mfs.ActiveSourceNames()
	if len(active) == 0 {
		return entry, BatchVerdictNone, nil
	}
	var ask []string
	for _, name := range active {
		at, ok := entry.EmptyAnswers[name]
		if !ok || now.Sub(at) >= database.MetadataKnownEmptyTTL {
			ask = append(ask, name)
		}
	}
	if len(ask) == 0 {
		return entry, BatchVerdictKnownEmpty, nil
	}
	return entry, BatchVerdictNone, ask
}

// ListCachedSummaries returns one summary per cached entry, ordered
// by FetchedAt descending.
func (mfs *Service) ListCachedSummaries(_ context.Context) ([]MetadataCacheSummary, error) {
	if mfs == nil || mfs.db == nil {
		return nil, nil
	}
	return mfs.db.ListMetadataCacheKeys()
}

// InvalidateCachedCandidates removes the cached candidates for bookID. Used
// when book metadata changes underneath us (manual edit, metadata
// apply, undo, revert, organize rename) so the next read fetches fresh.
//
// It deliberately does NOT touch the per-provider fetch cache. Every caller
// is an apply/edit/undo/revert path. Each fetch row carries the
// SearchIdentity it was fetched for, built from five fields: title, author,
// ASIN, ISBN-13 and ISBN-10. Any change that writes one of those five
// (including an apply that fills an empty ASIN or ISBN) makes the row miss
// and the next fetch re-queries. Only a change that touches none of the five
// (narrator, series, description and the like) keeps its rows, and those rows
// are still valid for the unchanged identity, so wiping them would only force
// a provider refetch. A wipe here was tried and reverted in review of #3421.
// For a result that is wrong for an UNCHANGED identity (the user rejects the
// match), use InvalidateFetchCacheForBook: the stamp cannot catch that case.
func (mfs *Service) InvalidateCachedCandidates(bookID string) error {
	if mfs == nil || mfs.db == nil {
		return nil
	}
	return mfs.db.DeleteMetadataCache(bookID)
}

// InvalidateFetchCacheForBook deletes every cached metadata result for
// bookID: every provider's fetch-cache row AND the book's candidate cache.
// Call it when the cached results are wrong for the book as it is NOW, i.e.
// the user rejected the match ("no match") while the five identity fields
// stay the same. Neither cache would notice on its own: the fetch rows'
// SearchIdentity stamp still matches, and the candidate cache is keyed on a
// hash of the same unchanged search inputs, so without both deletes the
// rejected result would replay on the next fetch and the review UI would keep
// offering the rejected candidates. Both deletes are always attempted; their
// errors are joined. Identity changes do not need this (see
// InvalidateCachedCandidates).
//
// This only forces a fresh result. It does not stop a later fetch from
// matching the book again: only FetchMetadataForBook refuses books whose
// MetadataReviewStatus is "no_match".
func (mfs *Service) InvalidateFetchCacheForBook(bookID string) error {
	if mfs == nil || mfs.db == nil {
		return nil
	}
	var errs []error
	if err := database.InvalidateAllCachedMetadataFetchesForBook(mfs.db, bookID); err != nil {
		errs = append(errs, fmt.Errorf("invalidate metadata fetch cache: %w", err))
	}
	if err := mfs.db.DeleteMetadataCache(bookID); err != nil {
		errs = append(errs, fmt.Errorf("invalidate metadata candidate cache: %w", err))
	}
	return errors.Join(errs...)
}

// fetchCacheIdentity returns the database.MetadataSearchIdentity for a book
// row, resolving the author name the same way the bulk paths do (the stored
// author's name, no garbage filtering). Every metafetch cache read and write
// goes through this so the fetch, search and bulk paths agree on the stamp.
func (mfs *Service) fetchCacheIdentity(book *database.Book) string {
	if book == nil {
		return ""
	}
	author := ""
	if book.Author != nil {
		author = book.Author.Name
	} else if book.AuthorID != nil && mfs != nil && mfs.db != nil {
		if a, err := mfs.db.GetAuthorByID(*book.AuthorID); err == nil && a != nil {
			author = a.Name
		}
	}
	return database.MetadataSearchIdentity(book.Title, author, book.ASIN, book.ISBN13, book.ISBN10)
}

// hashSearchInputs builds a short stable digest of the search inputs
// so v2 can compare against the inputs the cached entry came from.
func hashSearchInputs(bookID, query, author, narrator, series string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s", bookID, query, author, narrator, series)
	return hex.EncodeToString(h.Sum(nil))[:16]
}
