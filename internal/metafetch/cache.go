// file: internal/metafetch/cache.go
// version: 1.27.0
// guid: a4f33a2e-3b4d-4306-bdce-476758e39120
// last-edited: 2026-10-06
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
	"strings"
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

// ErrCandidateASINReplaced is CandidateASINStale's refusal: the book's ASIN
// was replaced or cleared after its cached candidates were fetched, and the
// candidate does not carry the book's current ASIN. It deliberately does NOT
// wrap ErrStaleMetadataCache: the gate's transcription lift explains a stale
// QUERY (cachedTranscribedSearch keys on that sentinel), and nothing about a
// transcription explains a book now identified by another record.
var ErrCandidateASINReplaced = errors.New("metadata cache stale: the book's ASIN changed after these candidates were fetched")

// CandidateASINStale reports whether candidate c, cached in entry, was
// fetched for an ASIN book no longer carries (MetadataCandidateCache.
// ASINReplaced) and does not carry the book's current one. A candidate whose
// ASIN equals the book's current ASIN is current whatever the row says: it
// is the record the book now holds, typically the one just applied. A
// candidate naming a different ASIN is stale here too; the gate's
// asin_conflict check refuses it as well.
//
// nil when the row records no replacement. Callers: the bulk-apply planner
// (identity leg, never lifted by a transcription), the transcription
// auto-apply, the single-book dialog's candidate flags.
func CandidateASINStale(entry *MetadataCandidateCache, book *database.Book, c *MetadataCandidate) error {
	if entry == nil || book == nil || c == nil {
		return nil
	}
	was, replaced := entry.ASINReplaced(book.ASIN)
	if !replaced {
		return nil
	}
	cur := trimmedASIN(book)
	if cur != "" && strings.EqualFold(strings.TrimSpace(c.ASIN), cur) {
		return nil
	}
	if cur == "" {
		return fmt.Errorf("%w: book %s: fetched for ASIN %s, which the book no longer carries", ErrCandidateASINReplaced, book.ID, was)
	}
	return fmt.Errorf("%w: book %s: fetched for ASIN %s, the book now carries %s", ErrCandidateASINReplaced, book.ID, was, cur)
}

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
//
// A row an earlier search version wrote comes back with its candidates
// filtered by this version's position rules (filterLegacyCandidates): the
// apply paths read candidates here, and a sibling the old ladder pooled must
// not be applyable. The stored row is not rewritten, and nothing is
// refetched. Only such a row costs more than the cache read: a row this
// version wrote is recognized by its stamp (isCurrentFingerprint) and
// returned as stored, with no book read.
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
	if len(entry.Candidates) > 0 && !isCurrentFingerprint(entry.SearchFingerprint) {
		book, berr := mfs.db.GetBookByID(bookID)
		if berr != nil {
			return nil, false, berr
		}
		if book != nil {
			filtered, ferr := mfs.filterLegacyForBook(entry, book, mfs.db, mfs.bookRuntimeSec(book))
			if ferr != nil {
				return nil, false, ferr
			}
			entry = filtered
		}
	}
	return entry, entry.IsFresh(), nil
}

// GetCachedCandidatesPreloaded is GetCachedCandidates for a caller that has
// already read the book (nil when it no longer resolves) and holds its file
// rows: a listing over the whole cache (the review page's loader) reads every
// book in one batch and every book's files in one batch, and the per-row
// GetBookByID plus the per-book GetBookFiles range scan behind the legacy
// filter's runtime were 38% of a 119 s review request on production.
//
// The result is identical to GetCachedCandidates for the same store state:
// the same stamp gate, the same live-author hint (read through authors), and
// the same runtime rule (database.LoadBookRuntime over files).
func (mfs *Service) GetCachedCandidatesPreloaded(bookID string, book *database.Book, files database.BookFilesGetter, authors database.BookAuthorReader) (*MetadataCandidateCache, bool, error) {
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
	if len(entry.Candidates) > 0 && !isCurrentFingerprint(entry.SearchFingerprint) && book != nil {
		rt, rerr := database.LoadBookRuntime(files, book)
		if rerr != nil {
			cachePreloadLog.Warn("book files unreadable; runtime treated as unknown: book_id=%s err=%v", book.ID, rerr)
		}
		sec, _ := rt.KnownSeconds()
		filtered, ferr := mfs.filterLegacyForBook(entry, book, authors, sec)
		if ferr != nil {
			return nil, false, ferr
		}
		entry = filtered
	}
	return entry, entry.IsFresh(), nil
}

var cachePreloadLog = logger.New("metafetch.cache")

// filterLegacyForBook is the GetCachedCandidates legacy filter for a resolved
// book: the search author hint is the book's first live author, read through
// authors, and runtimeSec is the book's known runtime (bookRuntimeSec).
func (mfs *Service) filterLegacyForBook(entry *MetadataCandidateCache, book *database.Book, authors database.BookAuthorReader, runtimeSec int) (*MetadataCandidateCache, error) {
	live, err := database.LiveBookAuthorNames(authors, book)
	if err != nil {
		return nil, err
	}
	author := ""
	if len(live) > 0 {
		author = SearchAuthorHint(live[0])
	}
	return mfs.filterLegacyCandidatesRuntime(entry, book, book.Title, author, runtimeSec), nil
}

// filterLegacyCandidates returns entry with its candidates filtered by this
// version's position rules when an earlier search version wrote it (its
// stamp is not isCurrentFingerprint: a version "1" fingerprint, or none),
// else entry itself. The criteria are the ones a search for query builds now
// (newStrongCriteria), so a legacy sibling ("Rogue Ascension 7" pooled for
// book 8) is dropped exactly as a fresh search would drop it. A copy is
// returned; the stored row is untouched.
//
// The gate is the stamp, not a fingerprint match, on purpose: a row a
// user-typed query wrote carries the legacy fingerprint of THAT query, which
// the book's title never reproduces, so a match-based gate let it through
// unfiltered. Such a row is filtered by the book's own title, the identity
// the apply paths write to. If the typed query named another position on
// purpose (a mislabeled book), its candidate is held back until the user
// searches again under this version, whose row is not re-filtered.
func (mfs *Service) filterLegacyCandidates(entry *MetadataCandidateCache, book *database.Book, query, author string) *MetadataCandidateCache {
	if entry == nil || len(entry.Candidates) == 0 || isCurrentFingerprint(entry.SearchFingerprint) {
		return entry
	}
	return mfs.filterLegacyCandidatesRuntime(entry, book, query, author, mfs.bookRuntimeSec(book))
}

// filterLegacyCandidatesRuntime is filterLegacyCandidates with the book's
// runtime already known, for a caller that holds the book's file rows.
func (mfs *Service) filterLegacyCandidatesRuntime(entry *MetadataCandidateCache, book *database.Book, query, author string, runtimeSec int) *MetadataCandidateCache {
	if entry == nil || len(entry.Candidates) == 0 || isCurrentFingerprint(entry.SearchFingerprint) {
		return entry
	}
	in := mfs.resolveSearchInputs(book, query, author, "")
	c := newStrongCriteria(in.parsed, in.title, in.literal, in.asin, in.author, runtimeSec)
	cp := *entry
	cp.Candidates = c.filterCarried(entry.Candidates)
	return &cp
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
//
// "The book's current author" is any form a writer could have hashed
// (CurrentAuthorForms): the Book.Author snapshot and the live author
// (liveAuthors, database.LiveBookAuthorNames), which the batch fetch hashes
// since 2026-09-28 and the certainty gate judges by. A row fetched for the
// live author was fetched for the book as it is, so it is not stale.
//
// A row the batch fetch wrote BEFORE that change was hashed with no author:
// GetBookByID leaves Book.Author unhydrated, so the hint was "" even for a
// book with an author. Such a row passes only through cachedQueryMatches'
// fingerprint leg -- its stored search fingerprint, which bound the author
// resolved from AuthorID, must equal the fingerprint of a search for the
// live author now. A no-author row with no fingerprint stays stale, and so
// does one whose author has since changed.
//
// CachedBatchVerdict validates with this same function and the same live
// authors, so a row the batch fetch reuses is a row the apply gate accepts.
func (mfs *Service) ValidateCachedIdentityForBook(entry *MetadataCandidateCache, book *database.Book, liveAuthors []string) error {
	if entry == nil {
		return nil
	}
	if book == nil {
		return fmt.Errorf("%w: book %s no longer exists", ErrStaleMetadataCache, entry.BookID)
	}
	narrator, series := "", ""
	if book.Narrator != nil {
		narrator = *book.Narrator
	}
	if book.Series != nil {
		series = book.Series.Name
	}
	forms := CurrentAuthorForms(book, liveAuthors)
	if entry.SourceHash != "" {
		if mfs.cachedQueryMatches(entry, book, liveAuthors, forms, book.Title) {
			return nil
		}
		for _, author := range forms[1:] {
			if mfs.ValidateCachedIdentity(entry, book.ID, book.Title, author, narrator, series) == nil {
				return nil
			}
		}
	}
	// The first form's error is the one reported: it names the snapshot, the
	// input every earlier refusal named.
	return mfs.ValidateCachedIdentity(entry, book.ID, book.Title, forms[0], narrator, series)
}

// CachedQueryMatchesIdentity reports whether entry was written by a batch
// fetch that searched book by query, with the book's CURRENT author: its
// SourceHash equals the batch shape (query, author, "", "") for some current
// author form (CurrentAuthorForms, plus the stripped "" hint when a form is a
// placeholder -- see SearchAuthorHint), or it is a pre-2026-09-28 no-author
// row whose search fingerprint proves the live author (cachedQueryMatches). A
// legacy row with no hash never matches.
//
// It is the proof the certainty gate needs before it accepts a candidate
// found by searching a stand-in title (the book's transcribed title): the
// row's identity differs from the book's in the query ONLY. A row fetched for
// another author, or for any other title, does not match and stays
// identity_stale.
func (mfs *Service) CachedQueryMatchesIdentity(entry *MetadataCandidateCache, book *database.Book, liveAuthors []string, query string) bool {
	if entry == nil || book == nil || entry.SourceHash == "" || strings.TrimSpace(query) == "" {
		return false
	}
	return mfs.cachedQueryMatches(entry, book, liveAuthors, CurrentAuthorForms(book, liveAuthors), query)
}

// cachedQueryMatches is the hash comparison behind CachedQueryMatchesIdentity
// and the batch-shape leg of ValidateCachedIdentityForBook. A placeholder
// author form also matches as "": the batch fetch sends (and hashes) no author
// hint for a placeholder author, so without this every such book's row would
// read as identity drift at apply time.
//
// For a book with a real author, a row hashed with no author matches only by
// its search fingerprint. The batch fetch hashed "" for every book until
// 2026-09-28 (GetBookByID leaves Book.Author unhydrated), so the hash alone
// says nothing about the author -- but the fingerprint it stored bound the
// author the search resolved from AuthorID. When that fingerprint equals the
// one a search by the live primary author would record now, the row asked
// exactly the questions a fresh fetch would, and it is the book's. A row with
// no fingerprint, or one fetched before the author changed, does not match.
func (mfs *Service) cachedQueryMatches(entry *MetadataCandidateCache, book *database.Book, liveAuthors, forms []string, query string) bool {
	noAuthor := hashSearchInputs(book.ID, query, "", "", "")
	for _, author := range forms {
		// The batch fetch sends and hashes SearchAuthorHint(author), which
		// cleans a credit ("zzJane Example" -> "Jane Example"); a row written
		// before that cleaning hashed the raw form. Both are the book's
		// current author, so both match.
		if entry.SourceHash == hashSearchInputs(book.ID, query, author, "", "") {
			return true
		}
		if hint := SearchAuthorHint(author); hint != "" && hint != author &&
			entry.SourceHash == hashSearchInputs(book.ID, query, hint, "", "") {
			return true
		}
		// A placeholder credit has always been sent and hashed as no
		// author. A real credit that only cleans to "" ("[XYZ]") is not
		// proof of the author: it proves itself through the fingerprint
		// leg below, which binds the author the search resolved.
		if author != "" && isPlaceholderCredit(author) && entry.SourceHash == noAuthor {
			return true
		}
	}
	if entry.SourceHash != noAuthor || entry.SearchFingerprint == "" || len(liveAuthors) == 0 {
		return false
	}
	// A row fetched by the version "1" ladder for the same inputs vouches
	// for its candidates as well as a current one (legacyFingerprint): the
	// searchInputVersion bump changed which questions are asked, not which
	// book the row's candidates were fetched for.
	return mfs.matchSearchFingerprint(entry.SearchFingerprint, book, query, SearchAuthorHint(liveAuthors[0]), "") != fingerprintStale
}

// BatchSourceHash is the SourceHash the batch candidate fetch writes for a
// row it searched by (query, author hint): the batch shape of
// hashSearchInputs, which the identity checks above compare against. It lets
// a caller outside this package build a row the checks judge exactly as a
// real one.
func BatchSourceHash(bookID, query, authorHint string) string {
	return hashSearchInputs(bookID, query, authorHint, "", "")
}

// CurrentAuthorForms lists every string a cache writer could have recorded as
// book's author, first the Book.Author snapshot, then the live primary author
// and all live authors joined with " & " (the API's author_name), without
// repeats. It is never empty.
//
// The empty form ("no author") is listed only when the book has no author at
// all -- no snapshot and no live author. GetBookByID does not fill
// Book.Author, so an empty snapshot is the normal case, and listing "" for a
// book with a live author let a row hashed with no author prove the author
// half of the identity for ANY author: a row fetched before the author
// changed still matched. (A placeholder live author still matches a
// no-author row through cachedQueryMatches' placeholder leg: the fetch sends
// and hashes no hint for it.)
//
// It exists for the identity checks only: they compare a recorded input with
// the book NOW, and the recorded input came from whichever form its writer
// read. Judging the book by its author (the certainty gate) uses the live
// authors alone.
func CurrentAuthorForms(book *database.Book, liveAuthors []string) []string {
	snapshot := ""
	if book != nil && book.Author != nil {
		snapshot = book.Author.Name
	}
	var forms []string
	if strings.TrimSpace(snapshot) != "" || len(liveAuthors) == 0 {
		forms = append(forms, snapshot)
	}
	add := func(a string) {
		for _, f := range forms {
			if f == a {
				return
			}
		}
		forms = append(forms, a)
	}
	if len(liveAuthors) > 0 {
		add(liveAuthors[0])
		add(strings.Join(liveAuthors, " & "))
	}
	return forms
}

// ErrNoSourceAnswered is returned by FetchAndCache and FetchAndCacheLimited
// when a search found nothing AND no source's query ladder completed: every
// provider errored, was throttled, or was cancelled. The search core returns a
// nil error in that case, and recording it as an empty answer stamped
// LastEmptyFetchAt = now, so one provider outage or quota exhaustion during a
// forced stale refetch marked the whole backlog as freshly checked for 30 days
// (MetadataCacheTTL) without anyone having been asked anything. Nothing is
// written; the entry stays exactly as it was, stale if it was stale.
var ErrNoSourceAnswered = errors.New("metadata search: no source answered (every provider errored, was throttled or was cancelled)")

// noSourceAnswered reports the ErrNoSourceAnswered case for resp, naming the
// failures so the caller's result row says what happened.
//
// A response WITH results is never this case: the candidates came from a
// source that returned them, so dating them now is true even if that source's
// later ladder steps failed.
//
// When the response names the sources it asked (SourcesAsked), only an
// answered source that was ASKED counts: a source with nothing to be asked
// (Audnexus, asked only by ASIN, for a book with none) is "answered" so the
// batch does not re-ask it forever, but it said nothing about the book, and a
// search whose every asked source errored must still refuse to cache
// "nothing found". A search that asked nobody (a partial re-ask of only
// Audnexus) counts its answered sources as before.
func noSourceAnswered(resp *SearchMetadataResponse) error {
	if resp == nil || len(resp.Results) > 0 {
		return nil
	}
	asked := make(map[string]bool, len(resp.SourcesAsked))
	for _, n := range resp.SourcesAsked {
		asked[n] = true
	}
	for _, n := range resp.SourcesAnswered {
		if len(asked) == 0 || asked[n] {
			return nil
		}
	}
	if len(resp.SourcesFailed) == 0 {
		return ErrNoSourceAnswered
	}
	names := make([]string, 0, len(resp.SourcesFailed))
	for name := range resp.SourcesFailed {
		names = append(names, name)
	}
	slices.Sort(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+": "+resp.SourcesFailed[name])
	}
	return fmt.Errorf("%w: %s", ErrNoSourceAnswered, strings.Join(parts, "; "))
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
	if err := noSourceAnswered(resp); err != nil {
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
	if err := noSourceAnswered(resp); err != nil {
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
// it is not this function's job: the store drops the row when a write changes
// the book's title or author (database candidateSearchIdentityChanged), and
// InvalidateCachedCandidates does it for the undo and revert paths.
//
// SourceHash is the discriminator, and this is its first non-diagnostic use.
// Same inputs + zero results means the providers had nothing to say this time
// and the stored candidates are still the best answer anyone has, so they stay
// and only LastEmptyFetchAt moves. Different inputs means the title/author/
// series the candidates answer to no longer exists, so replacing them is right.
//
// Callers must rule out noSourceAnswered first: an empty response nobody
// answered is not a look, and recording it would date the row as checked.
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
		// The ASIN these candidates were fetched for (ASINReplaced reads it).
		FetchedForASIN: resp.BookASIN,
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
					carried := prev.Candidates
					// Candidates an earlier search version cached answered ITS
					// questions, unfiltered by this version's position rules: a
					// sibling it pooled is dropped here (strongCriteria.
					// filterCarried), with the same criteria this search used.
					// Any row not stamped by this version is filtered, not only
					// one whose legacy fingerprint matches (filterLegacyCandidates
					// says why).
					legacy := resp.LegacyFingerprint != "" && prev.SearchFingerprint == resp.LegacyFingerprint
					if !isCurrentFingerprint(prev.SearchFingerprint) && resp.carryFilter != nil {
						carried = resp.carryFilter(carried)
					}
					if len(carried) > 0 {
						entry.Candidates = carried
						// The carried candidates were fetched for the
						// earlier row's ASIN, not this search's. A row with
						// none recorded (written before 2026-10-05) takes
						// this search's: any replacement since was stamped
						// on the row by the store, so an unstamped row was
						// last vouched for the ASIN the book holds now.
						if prev.FetchedForASIN != "" {
							entry.FetchedForASIN = prev.FetchedForASIN
						}
						// NOT bumped: FetchedAt dates the CANDIDATES, and these are
						// the ones the previous search returned. Moving it would
						// relabel month-old candidates as freshly fetched.
						entry.FetchedAt = prev.FetchedAt
						// Nor re-stamped: stamping the current fingerprint would
						// pass legacy candidates off as this version's answers.
						if legacy {
							entry.SearchFingerprint = prev.SearchFingerprint
						}
					}
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
// rule, checked with the book's live authors through the same functions the
// apply planner uses (ValidateCachedIdentityForBook, and
// CachedQueryMatchesIdentity for a stand-in query; a missing hash is never
// trusted), and the search fingerprint, which binds it to the questions
// actually asked (resolved author included). Sharing the check is the point:
// a row served here as fresh is never refetched, so if the apply gate read it
// as identity_stale the book would be stuck until the row expired. An entry with no fingerprint predates
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
	// The batch fetch hashes the live primary author (fetchCandidateForBook's
	// author hint), so the identity is checked against the live authors, the
	// same input the apply planner passes. A read failure answers None: the
	// book is re-asked rather than served on an identity nobody checked.
	//
	// A row hashed with the query itself is accepted too
	// (CachedQueryMatchesIdentity). For a book with a real title the query IS
	// the title, so this changes nothing; for a book whose title is blank or
	// a placeholder the batch fetch searches a stand-in (its transcribed
	// title) and hashes the row with that, which the book-title check can
	// never match -- every run would re-ask every provider for it. The apply
	// planner accepts that row the same way, and bulk-applies its candidate
	// only on the separate transcribed-title evidence (owner decision
	// 2026-09-28: applygate.EvaluateTranscribed).
	live, lerr := database.LiveBookAuthorNames(mfs.db, book)
	if lerr != nil {
		return entry, BatchVerdictNone, nil
	}
	if mfs.ValidateCachedIdentityForBook(entry, book, live) != nil &&
		!mfs.CachedQueryMatchesIdentity(entry, book, live, query) {
		return entry, BatchVerdictNone, nil
	}
	// A row from the version "1" ladder for the same inputs keeps its fresh
	// CANDIDATES; a "nothing found" under it answered the old questions, not
	// the fan-out's, so it is re-asked (searchInputVersion).
	fp := mfs.matchSearchFingerprint(entry.SearchFingerprint, book, query, author, "")
	if fp == fingerprintStale || ((fp == fingerprintLegacy || fp == fingerprintPrior) && len(entry.Candidates) == 0) {
		return entry, BatchVerdictNone, nil
	}
	// The book's ASIN was replaced or cleared after these candidates were
	// fetched: the apply gate refuses each of them that does not carry the
	// new ASIN (CandidateASINStale), so the row is re-asked rather than
	// served as fresh -- unless the current questions were already asked and
	// came back empty (the row then carries the old candidates under the
	// current fingerprint with a recent LastEmptyFetchAt), which would only
	// repeat that empty search on every run.
	if _, replaced := entry.ASINReplaced(book.ASIN); replaced {
		askedNow := fp == fingerprintCurrent && entry.LastEmptyFetchAt != nil &&
			nowUTC().Sub(*entry.LastEmptyFetchAt) < database.MetadataCacheTTL
		if !askedNow {
			return entry, BatchVerdictNone, nil
		}
	}
	if fp == fingerprintLegacy {
		// Its candidates, filtered by this version's position rules: a row
		// whose every legacy candidate was a sibling has nothing to apply and
		// is re-asked -- only that book, never a mass refetch.
		entry = mfs.filterLegacyCandidates(entry, book, query, author)
		if len(entry.Candidates) == 0 {
			return entry, BatchVerdictNone, nil
		}
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

// InvalidateCachedCandidates removes the cached candidates for bookID. Its
// callers are the undo and revert handlers, which restore an earlier state of
// the book. An apply no longer calls it: deleting after every apply destroyed
// the candidate just applied. The store itself drops the row when a write
// changes the book's title or author (database candidateSearchIdentityChanged),
// and keeps it for an ASIN/ISBN fill, which never makes a candidate wrong.
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
	return mfs.fetchCacheIdentityForTitle(book, book.Title)
}

// fetchCacheIdentityForTitle is fetchCacheIdentity with the title replaced.
// searchMetadataForBook uses it for a book whose own title is a placeholder
// and is searched by a stand-in (its transcribed title): the cache row must
// name the question actually asked, not the placeholder.
func (mfs *Service) fetchCacheIdentityForTitle(book *database.Book, title string) string {
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
	return FetchCacheIdentity(title, author, book.ASIN, book.ISBN13, book.ISBN10)
}

// hashSearchInputs builds a short stable digest of the search inputs
// so v2 can compare against the inputs the cached entry came from.
func hashSearchInputs(bookID, query, author, narrator, series string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s", bookID, query, author, narrator, series)
	return hex.EncodeToString(h.Sum(nil))[:16]
}
