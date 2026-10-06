// file: internal/metafetch/service_search.go
// version: 1.36.0
// guid: bcba782a-8ed4-4285-be91-2af3eddc90e3
// last-edited: 2026-10-06

package metafetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/authority"
	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/foldernames"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metadata/providerhttp"
	"github.com/falkcorp/audiobook-organizer/internal/openlibrary"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

// defaultSourceFanout is the fallback for how many metadata sources are queried
// concurrently for ONE book when config.MetadataScoring.SourceFanoutWorkers is
// unset. Kept small on purpose: this multiplies with the per-book pool
// (BulkFetchWorkers), so 4 books × 4 sources is already 16 provider requests in
// flight. Each provider enforces its own token bucket in
// internal/metadata/providerhttp, so raising this past the source count buys
// nothing — it only makes requests queue behind a limiter instead of a channel.
const defaultSourceFanout = 4

// sourceFanoutLimit resolves the per-book source fan-out width. The `> 0` guard
// is load-bearing: a config that never set the key unmarshals to 0, and
// errgroup.SetLimit(0) blocks forever on the first Go call — a search that
// simply never returns rather than one that runs slowly.
func sourceFanoutLimit() int {
	if w := config.AppConfig.MetadataScoring.SourceFanoutWorkers; w > 0 {
		return w
	}
	return defaultSourceFanout
}

// BuildSourceChain returns metadata sources ordered by config priority.
// Each source is wrapped with a circuit breaker that opens after 5 consecutive
// failures and retries after 30 seconds.
// buildSearchContext gathers the richer context fields from a Book
// that metadata.ContextualSearch implementations can use to do better
// than plain title+author lookups. Empty fields are left empty so
// sources see "" instead of a garbage placeholder.
//
// Method on *Service so the series lookup uses mfs.db rather than the
// package global (SERVER-GLOBAL-STORE-AUDIT phase 4).
func (mfs *Service) buildSearchContext(book *database.Book, searchTitle, author, narrator string) *metadata.SearchContext {
	ctx := &metadata.SearchContext{
		Title:    searchTitle,
		Author:   author,
		Narrator: narrator,
	}
	if book != nil {
		if book.ISBN10 != nil {
			ctx.ISBN10 = *book.ISBN10
		}
		if book.ISBN13 != nil {
			ctx.ISBN13 = *book.ISBN13
		}
		if book.ASIN != nil {
			ctx.ASIN = *book.ASIN
		}
		if book.SeriesID != nil && mfs != nil && mfs.db != nil {
			if series, err := mfs.db.GetSeriesByID(*book.SeriesID); err == nil && series != nil {
				ctx.Series = series.Name
			}
		}
	}
	return ctx
}

// BuildSourceChain returns the metadata-source chain, MEMOIZED on the Service so
// the same *ProtectedSource (and the underlying source clients) are reused across
// every per-book fetch in a batch. This is what makes the Hardcover 60-rpm limiter
// and the per-source circuit breakers accumulate across a batch instead of being
// recreated fresh on each book (which let a thundering herd through and prevented
// the breaker from ever tripping for a down source).
//
// The memo is keyed on a fingerprint of the metadata-source config, so a runtime
// settings change (enabling/disabling a source, editing a token/priority) rebuilds
// the chain; an unchanged config returns the identical chain instances. The
// returned slice must be treated as read-only (callers append to NEW slices, never
// mutate in place) — the underlying clients are shared and concurrency-safe.
func (mfs *Service) BuildSourceChain() []metadata.MetadataSource {
	fp := sourceChainFingerprint()
	mfs.chainMu.Lock()
	defer mfs.chainMu.Unlock()
	if mfs.cachedChain != nil && mfs.cachedChainFP == fp {
		return mfs.cachedChain
	}
	chain := buildSourceChainFromConfig(mfs.olStore)
	mfs.cachedChain = chain
	mfs.cachedChainFP = fp
	return chain
}

// waitForLimiter acquires one token from limiter, blocking until a token is
// available or ctx is cancelled. A nil limiter is a no-op (returns nil), so the
// non-batch search paths (interactive dialog, bulk fetch) are unthrottled exactly
// as before. Returns ctx's error if the wait is cancelled.
func waitForLimiter(ctx context.Context, limiter *rate.Limiter) error {
	if limiter == nil {
		return nil
	}
	return limiter.Wait(ctx)
}

// sourceChainFingerprint is a deterministic digest of the config that
// BuildSourceChain reads, so the memoized chain is rebuilt exactly when that
// config changes. encoding/json sorts map keys (the per-source Credentials map),
// so the marshaled form is stable for equal configs.
func sourceChainFingerprint() string {
	payload := struct {
		Sources   []config.MetadataSource
		Hardcover string
		Google    string
	}{
		Sources:   config.AppConfig.MetadataSources,
		Hardcover: config.AppConfig.HardcoverAPIToken,
		Google:    config.AppConfig.GoogleBooksAPIKey,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		// Marshal of plain config structs shouldn't fail; on the off chance it
		// does, return a unique-ish value so we rebuild rather than serve stale.
		return fmt.Sprintf("fp-error-%d", time.Now().UnixNano())
	}
	return string(b)
}

// applyProviderLimits resolves one provider's configured request budget and
// installs it, so the client built moments later picks it up.
//
// Resolution order, most specific first: an explicit advanced value for a
// field, else the tier-scaled built-in for that field, else the built-in. The
// tier is a MULTIPLIER over the provider's own built-in figure rather than an
// absolute rate, because the built-ins differ for real reasons -- Hardcover
// documents 60 requests/minute, Audible is an unofficial surface -- and one
// absolute number would be reckless for one provider and needlessly slow for
// another.
func applyProviderLimits(src config.MetadataSource) {
	// The config id IS the budget key -- providerhttp stores budgets under the
	// same ids config uses, so nothing is translated here. An unrecognised but
	// non-empty id still gets a budget (providerhttp falls back to a
	// deliberately conservative default), which is the safe direction for a
	// source we do not ship.
	key := strings.TrimSpace(src.ID)
	if key == "" {
		slog.Warn("metadata source has no id; cannot apply a rate-limit budget")
		return
	}
	eff := effectiveProviderLimits(src)
	providerhttp.SetLimits(key, eff)
	// Drop the cached client/limiter so the next Client() rebuilds on the new
	// budget. Without this the setting is stored and never takes effect.
	providerhttp.ResetProvider(key)
	slog.Debug("applied provider rate limit", "provider", key, "tier", src.RateLimit.Tier,
		"rps", eff.RPS, "burst", eff.Burst, "timeout", eff.Timeout)
}

// SourcesBudget is the effective request budget (effectiveProviderLimits) of
// the enabled metadata sources: the summed RPS and burst the per-provider
// token buckets in providerhttp allow together, and the source whose queue
// drains slowest (the lowest RPS x Timeout), which bounds how many requests
// may wait on one bucket before the last of them times out.
type SourcesBudget struct {
	RPS   float64
	Burst int
	// SlowestID, SlowestRPS and SlowestTimeout describe the enabled source
	// with the lowest RPS x Timeout; SlowestID is "" when none is enabled.
	// The ASIN-only source (Audnexus) counts too: a book can send it an ASIN
	// lookup (up to maxAudnexusRequests requests), so workers queue on it.
	SlowestID      string
	SlowestRPS     float64
	SlowestTimeout time.Duration
	// CallsPerBook is the most requests one book sends any one enabled
	// source in the worst case (MaxSearchCallsPerBook): Audible's variant
	// cap plus its ASIN lookup when Audible is enabled.
	CallsPerBook int
	// BindingID and BooksPerSec name the enabled source that bounds the
	// batch's throughput in the worst case (every variant asked, no early
	// stop): the lowest RPS / MaxSearchCallsPerBook. Every book waits on
	// every source, so this is the op's books/s ceiling.
	BindingID   string
	BooksPerSec float64
}

// EnabledSourcesBudget returns the SourcesBudget of every enabled metadata
// source. A batch op sizes its own gate and worker pool from it, so the op
// never throttles below what the providers themselves permit and never
// queues more callers on one provider than its timeout lets drain. It has no
// side effects.
func EnabledSourcesBudget() SourcesBudget {
	var b SourcesBudget
	for _, src := range config.AppConfig.MetadataSources {
		if !src.Enabled || strings.TrimSpace(src.ID) == "" {
			continue
		}
		eff := effectiveProviderLimits(src)
		b.RPS += eff.RPS
		b.Burst += eff.Burst
		calls := MaxSearchCallsPerBook(strings.TrimSpace(src.ID))
		b.CallsPerBook = max(b.CallsPerBook, calls)
		if bps := eff.RPS / float64(calls); b.BindingID == "" || bps < b.BooksPerSec {
			b.BindingID, b.BooksPerSec = strings.TrimSpace(src.ID), bps
		}
		timeout := eff.Timeout
		if timeout <= 0 {
			timeout = providerhttp.BuiltinLimitsFor("").Timeout
		}
		if b.SlowestID == "" || eff.RPS*timeout.Seconds() < b.SlowestRPS*b.SlowestTimeout.Seconds() {
			b.SlowestID, b.SlowestRPS, b.SlowestTimeout = strings.TrimSpace(src.ID), eff.RPS, timeout
		}
	}
	return b
}

// effectiveProviderLimits is applyProviderLimits' resolution without
// installing anything.
func effectiveProviderLimits(src config.MetadataSource) providerhttp.Limits {
	key := strings.TrimSpace(src.ID)
	base := providerhttp.BuiltinLimitsFor(key)
	rl := src.RateLimit
	mult := rl.Tier.Multiplier()

	eff := providerhttp.Limits{
		RPS:        base.RPS * mult,
		Burst:      base.Burst,
		MaxRetries: base.MaxRetries,
		Timeout:    base.Timeout,
	}
	// Scale burst with the tier too. Raising RPS while leaving Burst at 1 is
	// close to a no-op for the bursty, one-request-per-book traffic a bulk
	// fetch generates.
	if mult != 1.0 {
		if scaled := int(math.Round(float64(base.Burst) * mult)); scaled >= 1 {
			eff.Burst = scaled
		}
	}

	// Advanced overrides win per field, so entering only an RPS keeps the
	// tier-derived burst rather than silently resetting it.
	if rl.RPS > 0 {
		eff.RPS = rl.RPS
	}
	if rl.Burst > 0 {
		eff.Burst = rl.Burst
	}
	if rl.MaxRetries > 0 {
		eff.MaxRetries = rl.MaxRetries
	}
	if rl.TimeoutSeconds > 0 {
		eff.Timeout = time.Duration(rl.TimeoutSeconds) * time.Second
	}
	return eff
}

// buildSourceChainFromConfig constructs a fresh source chain from the current
// config. Extracted from BuildSourceChain so the memoization wrapper stays small;
// olStore is passed explicitly (rather than reading mfs) to keep it a pure builder.
func buildSourceChainFromConfig(olStore *openlibrary.OLStore) []metadata.MetadataSource {
	// Copy and sort by priority
	sources := make([]config.MetadataSource, len(config.AppConfig.MetadataSources))
	copy(sources, config.AppConfig.MetadataSources)
	sort.Slice(sources, func(i, j int) bool {
		return sources[i].Priority < sources[j].Priority
	})

	var chain []metadata.MetadataSource
	for _, src := range sources {
		if !src.Enabled {
			continue
		}
		// Apply this provider's configured request budget BEFORE its client is
		// constructed. providerhttp.Client caches per provider and a client
		// keeps the limiter it was built with, so a budget applied afterwards
		// would never reach the client actually issuing requests.
		applyProviderLimits(src)
		var rawSource metadata.MetadataSource
		switch src.ID {
		case "openlibrary":
			client := metadata.NewOpenLibraryClient()
			if olStore != nil {
				client.SetOLStore(olStore)
			}
			rawSource = client
		case "google-books":
			apiKey := config.AppConfig.GoogleBooksAPIKey
			if apiKey == "" {
				if k, ok := src.Credentials["apiKey"]; ok && k != "" {
					apiKey = k
				}
			}
			rawSource = metadata.NewGoogleBooksClient(apiKey)
		case "audible":
			rawSource = metadata.NewAudibleClient()
		case "audnexus":
			rawSource = metadata.NewAudnexusClient()
		case "hardcover":
			token := config.AppConfig.HardcoverAPIToken
			if token == "" {
				// Also check credentials map from metadata source config
				if apiToken, ok := src.Credentials["api_token"]; ok && apiToken != "" {
					token = apiToken
				} else if apiKey, ok := src.Credentials["apiKey"]; ok && apiKey != "" {
					token = apiKey
				}
			}
			if token != "" {
				rawSource = metadata.NewHardcoverClient(token)
			} else {
				slog.Warn("Hardcover source enabled but no API token configured")
			}
		case "wikipedia":
			rawSource = metadata.NewWikipediaClient()
		default:
			slog.Warn("Unknown metadata source", "id", src.ID)
		}
		if rawSource != nil {
			chain = append(chain, metadata.NewChainSource(rawSource))
		}
	}
	return chain
}

// SearchMetadataForBook searches all configured metadata sources and returns
// scored candidates for manual matching.
// SearchMetadataForBook is the backward-compatible variadic entry point.
// New callers should prefer SearchMetadataForBookWithOptions — the variadic
// author/narrator/series positioning is historical and easy to get wrong.
func (mfs *Service) SearchMetadataForBook(id string, query string, authorHint ...string) (*SearchMetadataResponse, error) {
	var author, narrator, series string
	if len(authorHint) > 0 {
		author = authorHint[0]
	}
	if len(authorHint) > 1 {
		narrator = authorHint[1]
	}
	if len(authorHint) > 2 {
		series = authorHint[2]
	}
	return mfs.SearchMetadataForBookWithOptions(id, query, author, narrator, series, SearchOptions{})
}

// SearchMetadataForBookWithOptions is the canonical search entry point. The
// old variadic signature wraps this and passes default options. All new call
// sites should use this method directly so they can pass SearchOptions fields
// (UseRerank etc.) explicitly.
//
// This is a thin wrapper over searchMetadataForBook with no rate limiter
// (nil) and a background context — behavior is identical to the historical
// method, so the interactive search-dialog path and the bulk-fetch path are
// unchanged. Batch callers that need per-request rate limiting or cancellation
// (the candidate-fetch op) go through FetchAndCacheLimited instead.
func (mfs *Service) SearchMetadataForBookWithOptions(
	id, query, author, narrator, series string,
	opts SearchOptions,
) (*SearchMetadataResponse, error) {
	return mfs.searchMetadataForBook(context.Background(), nil, id, query, author, narrator, series, opts)
}

// searchMetadataForBook is the core search pipeline. When limiter != nil, every
// LIVE outbound source call (each SearchByTitle/SearchByTitleAndAuthor attempt and
// each direct ASIN lookup) first acquires one limiter token, so the configured
// rate governs ACTUAL requests rather than books. Cache hits skip the source calls
// entirely and therefore consume no tokens. ctx is threaded to every source call
// (and to the Audnexus ASIN lookup) so a batch cancel aborts in-flight requests.
// searchInputVersion versions the provider query ladder in
// searchMetadataForBook. It is part of every SearchFingerprint, so BUMP IT
// whenever the ladder changes what it asks (new rung, new title variant, a
// different author/narrator resolution): a cached "every provider has nothing"
// verdict is only valid for the exact questions that produced it.
//
// "2" (2026-10-01): the stop-at-first-hit ladder became the multi-combination
// fan-out (buildQueryVariants) over cleaned titles (parseSearchTitle), and the
// book's own ASIN is looked up whenever it has one. The bump does NOT re-ask
// every cached row: a row stamped with the version "1" fingerprint for the
// same inputs (legacyFingerprint) keeps its candidates valid -- for the batch
// verdict and for the apply gate's fingerprint leg -- and only its "nothing
// found" verdicts are re-asked. Re-asking every row would be ~100k books
// against Google Books' 1,000/day quota.
const searchInputVersion = "2"

// searchInputs is what the provider ladder actually queries with, after hint
// defaulting, chapter stripping and author/narrator resolution.
type searchInputs struct {
	title      string // searchTitle
	author     string // searchAuthor: the hint, else the book's resolved author
	bookAuthor string // the book's own author for scoring ("" if garbage)
	narrator   string // bookNarrator
	// asin is the book's own ASIN; the search looks it up directly.
	asin string
	// rawQuery is the query (else the book's title) before any cleaning;
	// literal is it chapter-stripped -- what the old ladder searched by.
	rawQuery string
	literal  string
	// parsed is what parseSearchTitle read out of rawQuery.
	parsed parsedTitle
	// legacy is what the searchInputVersion "1" ladder asked with
	// (legacyFingerprint).
	legacy legacyInputs
}

// legacyInputs are the questions the stop-at-first-hit ladder
// (searchInputVersion "1") asked for a book: its chapter-stripped title, the
// resolved author and narrator, and the ASIN only when there was no author.
type legacyInputs struct {
	title, author, narrator, asin string
}

// resolveSearchInputs derives the ladder's query inputs from the book row and
// the caller's hints. searchMetadataForBook and the cache's fingerprint checks
// (matchSearchFingerprint, CachedBatchVerdict) both use it, so a fingerprint
// computed before a search names the same questions the search asks.
func (mfs *Service) resolveSearchInputs(book *database.Book, query, author, narrator string) searchInputs {
	rawQuery := query
	if rawQuery == "" {
		rawQuery = book.Title
	}
	searchTitle := stripChapterFromTitle(rawQuery)
	literal := searchTitle

	// A placeholder author ("Unknown Author", "read by narrator") is never a
	// search input: not as the author hint, not as the book's own author the
	// search falls back to, and not as a stand-in title below. It is dropped
	// here, in the one resolver every search and fingerprint goes through,
	// because the hint is refilled from AuthorID further down: stripping it
	// only at a caller would put it straight back. See SearchAuthorHint.
	searchAuthor := SearchAuthorHint(author)
	searchNarrator := strings.TrimSpace(narrator)

	// Always resolve the book's own author and narrator for scoring tiebreaks,
	// even when no explicit hints were provided in the search request
	bookAuthor := searchAuthor
	// authorDropped: the book carried an author credit that was junk ("[XYZ]",
	// a credit restating the title). Only then may an author folder stand in
	// for it (knownAuthorFolder): a book with no credit at all keeps the
	// questions it was always asked.
	authorDropped := strings.TrimSpace(author) != "" && searchAuthor == ""
	if bookAuthor == "" && book.AuthorID != nil {
		if a, aerr := mfs.db.GetAuthorByID(*book.AuthorID); aerr == nil && a != nil {
			bookAuthor = SearchAuthorHint(a.Name)
			if bookAuthor == "" && !authorname.IsPlaceholderAuthor(a.Name) {
				authorDropped = true
			}
		}
	}
	// The search asks by the book's own author when no hint was passed.
	// GetBookByID leaves book.Author unhydrated, so the batch candidate fetch
	// always passed an empty hint and every provider was asked by title
	// alone: Audible, which answers only exact titles, missed books it finds
	// with the author ("Blood of Elves" + Sapkowski).
	if searchAuthor == "" {
		searchAuthor = bookAuthor
	}
	bookNarrator := searchNarrator
	if bookNarrator == "" && book.Narrator != nil && *book.Narrator != "" {
		bookNarrator = *book.Narrator
	}
	// The narrator is sent as an author too (the swapped variant), so it gets
	// the author's placeholder rule: "read by narrator" is never a hint.
	bookNarrator = SearchAuthorHint(bookNarrator)

	legacy := legacyInputs{title: searchTitle, author: searchAuthor, narrator: bookNarrator}
	if strings.TrimSpace(legacy.title) == "" || legacy.title == "-" {
		legacy.title = bookAuthor
	}
	if searchAuthor == "" && book.ASIN != nil && looksLikeASIN(*book.ASIN) {
		legacy.asin = strings.TrimSpace(*book.ASIN)
	}

	// Read the book's own name, series slot and any packed-in people out of
	// the title ("Magma Heart - Unknown Author", "read by Cathfach (Erryn's
	// World)", "Jack Reacher 17: A Wanted Man (Jeff Harding)").
	ev := mfs.nameEvidence(book)
	// rawAuthor is the credit as stored, before SearchAuthorHint cleaned it:
	// the batch fetch passes the cleaned hint, so the stored name is read
	// from AuthorID. A cleaned-away suffix ("Some Book_10-02") is evidence
	// that the credit was a file name, not a person.
	rawAuthor := strings.TrimSpace(author)
	if rawAuthor == "" && book.AuthorID != nil {
		if a, aerr := mfs.db.GetAuthorByID(*book.AuthorID); aerr == nil && a != nil {
			rawAuthor = strings.TrimSpace(a.Name)
		}
	}
	// Title and author swapped (a person's name as the title, a book name
	// as the author): the title is a person the authority lists know as an
	// author, the author is not, AND the author side carries positive junk
	// evidence -- a file-name suffix the hint cleaned away, or a shape no
	// person has. Without that evidence a person-titled book by another
	// person is a biography ("Steve Jobs" by Walter Isaacson) and is asked
	// as stored. Shape alone never swaps.
	if title := strings.TrimSpace(metadata.ParseBookName(rawQuery, metadata.NameEvidence{}).Title); searchAuthor != "" &&
		ev.IsKnownAuthor != nil && personShaped(title) && ev.IsKnownAuthor(title) && !mfs.knownAuthorOrFault(searchAuthor) &&
		!sameNormalizedText(title, searchAuthor) && (SearchAuthorHint(rawAuthor) != rawAuthor || !personShaped(searchAuthor)) {
		rawQuery, searchAuthor = searchAuthor, title
		searchTitle, literal = stripChapterFromTitle(rawQuery), stripChapterFromTitle(rawQuery)
		if bookAuthor == "" || sameNormalizedText(bookAuthor, rawQuery) {
			bookAuthor = title
		}
	}
	parsed := parseSearchTitleWith(rawQuery, searchAuthor, bookNarrator, ev)
	// An author that is the title itself ("Hammer Fall Rising" by "Hammer Fall
	// Rising"; the organizer filed an untagged book under its own title), or
	// that restates the title's words in another order (an organizer folder
	// name split as a composite credit, the title's series, its genre
	// tagline), narrows by nothing real. authorVsTitle decides on evidence:
	// dropped only when the credit is proven junk, kept with an extra
	// title-only question when it is merely suspect, kept as is otherwise.
	if searchAuthor != "" {
		exact := sameNormalizedText(searchAuthor, parsed.Title)
		switch mfs.authorVsTitle(book.ID, searchAuthor, parsed, exact, rawQuery, literal, parsed.Title) {
		case authorJunk:
			if sameNormalizedText(bookAuthor, searchAuthor) || sameNormalizedText(bookAuthor, parsed.Title) {
				bookAuthor = ""
			}
			searchAuthor = ""
			parsed = parseSearchTitleWith(rawQuery, "", bookNarrator, ev)
			// The exact-title filter holds a question asked with no person
			// to only answers titled exactly the book. Set for an exact
			// author==title only: a restated credit leaves the series in
			// the query, and the filter would refuse the catalog's bare name.
			parsed.AuthorIsTitle = exact
			authorDropped = true
		case authorSuspect:
			parsed.SuspectAuthor = true
		}
	}
	// A title that names no position takes the book's stored series sequence
	// for the strong gates only ("Overlord", sequence 8, is not strong on
	// "Overlord" at series_position 1). See parsedTitle.StoredPosition.
	if parsed.Position == "" && book.SeriesSequence != nil && *book.SeriesSequence > 0 {
		parsed.StoredPosition = strconv.Itoa(*book.SeriesSequence)
	}
	if t := strings.TrimSpace(parsed.Title); t != "" && !metadata.IsUnsearchableTitle(t) {
		searchTitle = t
	}
	if searchAuthor == "" {
		searchAuthor = SearchAuthorHint(parsed.Author)
		if bookAuthor == "" {
			bookAuthor = searchAuthor
		}
	}
	// A junk credit dropped above is replaced only by evidence: an ancestor
	// folder the authority lists know as an author ("John Sample/Example
	// Worlds/..."). Otherwise the title is asked alone; an author is never
	// guessed ("Pat Reader/Some Summoner/..." is a narrator's folder).
	if searchAuthor == "" && authorDropped {
		if a := knownAuthorFolder(book.FilePath, ev, mfs.isKnownNarrator); a != "" {
			searchAuthor = a
			if bookAuthor == "" {
				bookAuthor = a
			}
		}
	}
	if bookNarrator == "" {
		bookNarrator = SearchAuthorHint(parsed.Narrator)
	}

	// If title is effectively empty but we have an author, use the author
	// name as the search query to get results.
	if strings.TrimSpace(searchTitle) == "" || searchTitle == "-" {
		searchTitle = bookAuthor
	}
	// The book's own ASIN, when it has one, is looked up directly: it is the
	// one question whose answer is the book by definition.
	asin := ""
	if book.ASIN != nil && looksLikeASIN(*book.ASIN) {
		asin = strings.TrimSpace(*book.ASIN)
	}
	return searchInputs{title: searchTitle, author: searchAuthor, bookAuthor: bookAuthor, narrator: bookNarrator, asin: asin,
		rawQuery: rawQuery, literal: literal, parsed: parsed, legacy: legacy}
}

// nameEvidence is what metadata.ParseBookName may check a title's segments
// against for book: its path (a segment repeating an ancestor folder), the
// authority lists' known people (internal/authority), the library's author
// rows and its authorless series rows (author junk filtered). A lookup fault is no
// evidence, so a segment is kept in the title: a search never loses words to
// a read error.
func (mfs *Service) nameEvidence(book *database.Book) metadata.NameEvidence {
	ev := metadata.NameEvidence{Path: book.FilePath}
	if mfs != nil && mfs.db != nil {
		idx := authority.NewIndex(mfs.db)
		ev.IsKnownAuthor = func(name string) bool {
			ok, err := idx.IsKnownPerson(name, authority.RoleAuthor)
			return err == nil && ok
		}
		db := mfs.db
		ev.IsAuthorRow = func(name string) bool {
			a, err := db.GetAuthorByName(name)
			return err == nil && a != nil
		}
		// An authorless series row only: the store indexes series by author,
		// and a search has no series list to hand (the scanner reads one,
		// scanner.FolderNameEvidence). The row is judged the way the scanner
		// judges it (foldernames.IsRealSeries): a series named after an
		// author row and holding only that author's books is author junk,
		// not series evidence. A curated franchise is a series anyway.
		ev.IsKnownSeries = func(name string) bool { return foldernames.IsRealSeries(db, name) }
	}
	return ev
}

// titleTokenRe finds the words restatesTitleWords compares.
var titleTokenRe = regexp.MustCompile(`[\pL\pN]+`)

// restatesTitleWords reports whether every word of author is already in one
// of titles, in any order ("and" aside), and author has a letter.
func restatesTitleWords(author string, titles ...string) bool {
	have := map[string]bool{}
	for _, t := range titles {
		for _, w := range titleTokenRe.FindAllString(strings.ToLower(metadata.NormalizeNameText(t)), -1) {
			have[w] = true
		}
	}
	letters := false
	for _, w := range titleTokenRe.FindAllString(strings.ToLower(author), -1) {
		if w == "and" {
			continue
		}
		if !have[w] {
			return false
		}
		if hasLetter.MatchString(w) {
			letters = true
		}
	}
	return letters
}

// authorTitleVerdict is authorVsTitle's answer.
type authorTitleVerdict int

const (
	// authorKept: nothing says the credit is not the book's author.
	authorKept authorTitleVerdict = iota
	// authorSuspect: the credit restates the title but nothing proves it
	// junk; it is kept, and the title is also asked alone
	// (parsedTitle.SuspectAuthor).
	authorSuspect
	// authorJunk: proven junk; the title is asked without it.
	authorJunk
)

// authorVsTitle judges an author credit whose words may all be the title's.
// exact: it equals the parsed title. It is JUNK only on positive evidence:
// an author row whose every other book's title restates the credit (a series
// or a title filed as an author), or a shape no person has (digits, a genre
// tagline) -- one word is no evidence ("Moby", "Homer"). It is KEPT when the title names it as its owner ("Tom Clancy's
// ...") or the parse read it as a credit segment ("Brandon Sanderson -
// Mistborn"). An author the authority lists know (a lookup fault counts as
// known: a read error never drops an author), or a person-shaped credit with
// books of its own ("Stephen King": "The Stephen King Collection") or none
// to judge, is SUSPECT: kept, with a title-only question added. An exact
// author==title with no evidence either way keeps the rule it always had:
// junk.
func (mfs *Service) authorVsTitle(bookID, author string, parsed parsedTitle, exact bool, titles ...string) authorTitleVerdict {
	if !exact && !restatesTitleWords(author, titles...) {
		return authorKept
	}
	a := strings.ToLower(strings.TrimSpace(author))
	for _, t := range titles {
		lt := strings.ToLower(t)
		if strings.Contains(lt, a+"'s") || strings.Contains(lt, a+"’s") {
			return authorKept
		}
	}
	known := mfs.knownAuthorOrFault(author)
	row := mfs.authorRowOnlyRestates(author, bookID)
	if row == rowOnlyRestates && !known {
		return authorJunk
	}
	// A credit segment ("Brandon Sanderson - Mistborn") names the author --
	// unless, read with no author evidence, that segment is the series of a
	// slot ("Some Series - 4 - Some Book").
	if samePerson(parsed.Author, author) && len(titles) > 0 &&
		!samePerson(metadata.ParseBookName(titles[0], metadata.NameEvidence{}).Series, author) {
		return authorKept
	}
	if known {
		return authorSuspect
	}
	// A shape no person's name has: a book or volume number, a genre
	// tagline. (A one-word credit is a person often enough: "Moby",
	// "Homer".)
	if strings.ContainsAny(author, "0123456789") || authorjunk.IsGenreTagline(author) {
		return authorJunk
	}
	if row == rowHasOtherBooks || row == rowUnknown || !exact {
		return authorSuspect
	}
	// An exact author==title with no other evidence keeps the rule it
	// always had.
	return authorJunk
}

// knownAuthorOrFault reports whether the authority lists know name as an
// author; a lookup fault answers true, so a read error never drops a credit.
func (mfs *Service) knownAuthorOrFault(name string) bool {
	if mfs == nil || mfs.db == nil {
		return false
	}
	ok, err := authority.NewIndex(mfs.db).IsKnownPerson(name, authority.RoleAuthor)
	return err != nil || ok
}

// authorRowEvidence is what an author row says about a credit.
type authorRowEvidence int

const (
	rowNone authorRowEvidence = iota // no row of that name, or no other book on it
	rowOnlyRestates
	rowHasOtherBooks
	rowUnknown // a read fault: no evidence
)

// maxAuthorRowBooks bounds how many of an author row's books are read.
// A row with more books than this is a real author's.
const maxAuthorRowBooks = 200

// authorRowOnlyRestates reads the library's author row named name, apart
// from the book being searched (selfID) and books whose stored title is no
// title (metadata.IsUnsearchableTitle: "read by narrator", "copy2"): a row
// whose every other book's title carries all of the credit's words is a
// series or a title filed as an author ("Some Series" credited on "Some
// Series 4" and "Some Series 5"); a row with any other book is a person's;
// a row with no other book says nothing (rowNone).
func (mfs *Service) authorRowOnlyRestates(name, selfID string) authorRowEvidence {
	if mfs == nil || mfs.db == nil {
		return rowUnknown
	}
	a, err := mfs.db.GetAuthorByName(name)
	if err != nil {
		return rowUnknown
	}
	if a == nil {
		return rowNone
	}
	books, err := mfs.db.GetBooksByAuthorIDCore(a.ID)
	if err != nil {
		return rowUnknown
	}
	if len(books) > maxAuthorRowBooks {
		return rowHasOtherBooks
	}
	others := 0
	for _, b := range books {
		if b.ID == selfID || metadata.IsUnsearchableTitle(b.Title) {
			continue
		}
		others++
		if !restatesTitleWords(name, b.Title) {
			return rowHasOtherBooks
		}
	}
	if others == 0 {
		return rowNone
	}
	return rowOnlyRestates
}

// maxAuthorFolderHops bounds how far up from the book knownAuthorFolder
// looks: "<author>/<series>/<book>/<file>" is three folders up.
const maxAuthorFolderHops = 3

// knownAuthorFolder returns the nearest ancestor folder of path, within
// maxAuthorFolderHops, that is person-shaped and that the authority lists
// know as an author, or "". A folder they also know as a narrator is no
// evidence: a narrator with a few author credits qualifies as an author too,
// and the "[XYZ]" rows sit under narrators' folders ("Pat Reader/Some
// Summoner/..."). That book is asked by title alone.
func knownAuthorFolder(path string, ev metadata.NameEvidence, isNarrator func(string) bool) string {
	if strings.TrimSpace(path) == "" || ev.IsKnownAuthor == nil {
		return ""
	}
	dir := filepath.Dir(filepath.ToSlash(path))
	for range maxAuthorFolderHops {
		name := strings.TrimSpace(filepath.Base(dir))
		if name == "" || name == "/" || name == "." {
			return ""
		}
		if personShaped(name) && ev.IsKnownAuthor(name) {
			if isNarrator != nil && isNarrator(name) {
				return ""
			}
			return name
		}
		dir = filepath.Dir(dir)
	}
	return ""
}

// isKnownNarrator reports whether the authority lists know name as a
// narrator. A lookup fault answers true: the folder is then no evidence.
func (mfs *Service) isKnownNarrator(name string) bool {
	if mfs == nil || mfs.db == nil {
		return false
	}
	ok, err := authority.NewIndex(mfs.db).IsKnownPerson(name, authority.RoleNarrator)
	return err != nil || ok
}

// sameNormalizedText reports whether a and b are the same text ignoring case,
// punctuation and spacing ("Hammer Fall Rising" / "hammer fall rising").
func sameNormalizedText(a, b string) bool {
	na, nb := authority.Fold(a), authority.Fold(b)
	return na != "" && na == nb
}

// FingerprintPrefix starts every fingerprint this searchInputVersion writes.
// Version "1" fingerprints are bare hex and older rows have none, so a row an
// earlier ladder wrote is told apart by its stamp alone (isCurrentFingerprint)
// -- no book read, no input resolution. That is what lets GetCachedCandidates
// filter every such row, including one a user-typed query wrote, whose legacy
// fingerprint the book's own title can never reproduce.
const FingerprintPrefix = "v" + searchInputVersion + ":"

// isCurrentFingerprint reports whether fp was written by this
// searchInputVersion (FingerprintPrefix).
func isCurrentFingerprint(fp string) bool {
	return strings.HasPrefix(fp, FingerprintPrefix)
}

// fingerprint hashes the questions the ladder asks for these inputs: the
// ladder version, the book's own title (the ladder also queries it and its
// variants) and the resolved title, author and narrator. Unlike the cache's
// SourceHash, which hashes the caller's HINTS, this sees an author resolved
// from AuthorID, so renaming the author changes it. It carries
// FingerprintPrefix.
func (in searchInputs) fingerprint(bookTitle string) string {
	h := sha256.New()
	parts := []string{searchInputVersion, bookTitle, in.title, in.author, in.narrator}
	// Appended only when set, so a book whose ladder asks no ASIN question
	// keeps the fingerprint it had before the ASIN rung existed.
	if in.asin != "" {
		parts = append(parts, "asin:"+in.asin)
	}
	// Appended only when set, like the ASIN: a suspect author adds a
	// title-only question (parsedTitle.SuspectAuthor), so a "nothing found"
	// asked without it is re-asked.
	if in.parsed.SuspectAuthor {
		parts = append(parts, "suspect-author")
	}
	for _, part := range parts {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return FingerprintPrefix + hex.EncodeToString(h.Sum(nil))
}

// legacyFingerprint is the fingerprint the searchInputVersion "1" ladder
// recorded for these inputs, byte for byte. A cached row carrying it asked
// the old questions: its CANDIDATES are still the book's (the identity checks
// and freshness rules are unchanged), but a "nothing found" under it is not
// an answer to the new questions. See matchSearchFingerprint.
func (in searchInputs) legacyFingerprint(bookTitle string) string {
	h := sha256.New()
	parts := []string{"1", bookTitle, in.legacy.title, in.legacy.author, in.legacy.narrator}
	if in.legacy.asin != "" {
		parts = append(parts, "asin:"+in.legacy.asin)
	}
	for _, part := range parts {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// fingerprintMatch says how a stored fingerprint relates to the questions a
// search for book would ask now.
type fingerprintMatch int

const (
	fingerprintStale fingerprintMatch = iota
	// fingerprintLegacy: the row was fetched by the version "1" ladder for
	// the same book inputs. Valid for candidates, not for empty verdicts.
	fingerprintLegacy
	fingerprintCurrent
)

func (mfs *Service) matchSearchFingerprint(stored string, book *database.Book, query, author, narrator string) fingerprintMatch {
	if mfs == nil || mfs.db == nil || book == nil || stored == "" {
		return fingerprintStale
	}
	in := mfs.resolveSearchInputs(book, query, author, narrator)
	switch stored {
	case in.fingerprint(book.Title):
		return fingerprintCurrent
	case in.legacyFingerprint(book.Title):
		return fingerprintLegacy
	}
	return fingerprintStale
}

// SearchFingerprintCurrent reports whether stored, a cache row's
// SearchFingerprint, names exactly the questions a search for book with this
// query and author hint would ask now. A row written before the book's title,
// author or the query parser changed what is asked is not current, and
// neither is a version "1" (legacy) row: its "nothing found" answered other
// questions. The scheduled candidate fetch re-asks a book whose empty row is
// not current (server.unfetchedCandidateBookIDs).
func (mfs *Service) SearchFingerprintCurrent(stored string, book *database.Book, query, author string) bool {
	return mfs.matchSearchFingerprint(stored, book, query, author, "") == fingerprintCurrent
}

// SearchAuthorFor returns the author a search for book with this author hint
// actually narrows providers by: the hint, else the book's own author, with a
// placeholder dropped to "" (SearchAuthorHint). The batch fetch records it so
// its op log names the author that was really asked, not the book's.
func (mfs *Service) SearchAuthorFor(book *database.Book, query, author string) string {
	if mfs == nil || mfs.db == nil || book == nil {
		return SearchAuthorHint(author)
	}
	return mfs.resolveSearchInputs(book, query, author, "").author
}

// SearchQuestion returns the title and author a search for book with this
// query and author hint actually asks providers: resolveSearchInputs, the one
// resolver every search and fingerprint goes through. An author of "" means
// the book is searched by title alone.
func (mfs *Service) SearchQuestion(book *database.Book, query, author string) (title, queryAuthor string) {
	if mfs == nil || mfs.db == nil || book == nil {
		return strings.TrimSpace(query), SearchAuthorHint(author)
	}
	in := mfs.resolveSearchInputs(book, query, author, "")
	return in.title, in.author
}

// SearchAsksTitleAlone reports whether that search also asks its title with
// no author: the author resolved to "", or it is a suspect credit
// (parsedTitle.SuspectAuthor) kept beside a title-only question.
func (mfs *Service) SearchAsksTitleAlone(book *database.Book, query, author string) bool {
	if mfs == nil || mfs.db == nil || book == nil {
		return SearchAuthorHint(author) == ""
	}
	in := mfs.resolveSearchInputs(book, query, author, "")
	return in.author == "" || in.parsed.SuspectAuthor
}

func (mfs *Service) searchMetadataForBook(
	ctx context.Context,
	limiter *rate.Limiter,
	id, query, author, narrator, series string,
	opts SearchOptions,
) (*SearchMetadataResponse, error) {
	// A user-initiated single-book lookup is exempt from the global provider
	// throttle. Applied here, in the one core the entry points all funnel
	// through, so no caller can honour the flag on one path and drop it on
	// another.
	if opts.BypassProviderThrottle {
		ctx = metadata.WithThrottleBypass(ctx)
	}

	book, err := mfs.db.GetBookByID(id)
	if err != nil || book == nil {
		return nil, fmt.Errorf("audiobook not found")
	}

	// The fetch-cache stamp comes from the book ROW, not from this search's
	// query or hints, so a row this path writes stays readable by the fetch
	// and bulk paths and vice versa (A3#14).
	searchIdentity := mfs.fetchCacheIdentity(book)

	in := mfs.resolveSearchInputs(book, query, author, narrator)
	searchTitle, searchAuthor, bookAuthor, bookNarrator := in.title, in.author, in.bookAuthor, in.narrator

	// A book whose own title is not worth searching ("", "Unknown Title",
	// "Chapter 3", "06 Chapter 6": metadata.IsUnsearchableTitle, the one
	// predicate the batch and bulk fetches use too) is searched by a stand-in
	// query (its transcribed title or folder name,
	// metabatch.ResolveCandidateSearchQuery). The raw title must then
	// play NO part in the search: it is not sent to a provider as a second
	// query (a "" search answers with whatever the catalog ranks first -- two
	// books titled "" "matched" Audible's "Bad in Bed" that way), it adds no
	// words to the scorer, and it seeds no title variants. The per-provider
	// fetch cache is keyed on the stand-in too: keyed on the row's "" title it
	// would replay the junk an earlier "" search cached for this book.
	rawTitle := book.Title
	if metadata.IsUnsearchableTitle(rawTitle) && !metadata.IsUnsearchableTitle(searchTitle) {
		rawTitle = searchTitle
		searchIdentity = mfs.fetchCacheIdentityForTitle(book, searchTitle)
	}
	searchSeries := strings.TrimSpace(series)

	var sources []metadata.MetadataSource
	if len(mfs.overrideSources) > 0 {
		sources = mfs.overrideSources
	} else {
		sources = mfs.BuildSourceChain()
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("no metadata sources enabled")
	}
	// A partial re-ask (the batch fetch asking only the providers without a
	// valid answer): restrict to the named sources.
	if len(opts.OnlySources) > 0 {
		want := make(map[string]bool, len(opts.OnlySources))
		for _, n := range opts.OnlySources {
			want[n] = true
		}
		kept := make([]metadata.MetadataSource, 0, len(opts.OnlySources))
		for _, src := range sources {
			if want[src.Name()] {
				kept = append(kept, src)
			}
		}
		if len(kept) == 0 {
			return nil, fmt.Errorf("none of the requested metadata sources %v is enabled", opts.OnlySources)
		}
		sources = kept
	}

	if searchSeries == "" {
		searchSeries = in.parsed.Series
	}

	// A genre tagline subtitle ("Catch 22: A Novel") adds no words to the
	// scorer: "novel" would rank "Closing Time: A Novel" over "Catch-22".
	searchWords := SignificantWords(dropGenreTagline(searchTitle))
	if rawTitle != searchTitle {
		// The book's own title, cleaned the same way, so a placeholder
		// suffix or a narrator credit adds no words to the scorer.
		for w := range SignificantWords(dropGenreTagline(parseSearchTitle(rawTitle, searchAuthor, bookNarrator).Title)) {
			searchWords[w] = true
		}
	}

	// Runtime of the local audiobook files (seconds). Used to score candidates
	// by how closely their Audible runtime matches our files. Zero = unknown,
	// which includes a partial runtime (see bookRuntimeSec).
	bookDurationSec := mfs.bookRuntimeSec(book)
	th := hintsFromBook(book)

	// The ASIN to look up directly: the whole query is one, or the query
	// carries one, else the book's own (resolveSearchInputs).
	asinToLookup := ""
	if looksLikeASIN(searchTitle) {
		asinToLookup = searchTitle
	} else {
		asinToLookup = extractASIN(searchTitle)
	}
	if asinToLookup == "" {
		asinToLookup = in.asin
	}

	// MULTI-COMBINATION FAN-OUT. Every search -- interactive and batch --
	// asks the quota-free title sources (Audible, Open Library) up to
	// maxQueryVariants title/author combinations, Google Books and the other
	// metered sources only the best one, and Audnexus (no title search) only
	// by ASIN. The answers are pooled, deduplicated by ASIN/ISBN or by
	// normalized title+author, and ranked below. It is not a fallback: the
	// old ladder stopped at the first rung that answered, so a book whose
	// literal title missed was never asked the cleaned one. See
	// search_variants.go for the variants and the expected calls per book.
	variants := buildQueryVariants(in.parsed, in.literal, in.rawQuery, searchAuthor, bookNarrator)
	// "; "-separated, so sharesPerson can tell the names apart.
	people := strings.Trim(searchAuthor+"; "+bookNarrator, "; ")
	strong := newStrongCriteria(in.parsed, searchTitle, in.literal, asinToLookup, searchAuthor, bookDurationSec)
	states := mfs.runSearchFanout(fanoutParams{
		ctx: ctx, limiter: limiter, bookID: id, identity: searchIdentity, opts: opts, people: people, strong: strong,
	}, sources, variants)
	// One Audnexus lookup per book: the own-ASIN fallback below, when it may
	// run, is reserved first; otherwise one runtime fill.
	needOwnASIN := asinToLookup != "" && !poolHasASINWithRuntime(states, asinToLookup)
	if !needOwnASIN {
		mfs.enrichRuntimeByASIN(ctx, limiter, states, bookDurationSec)
	}

	type sourceFetch struct {
		name       string
		results    []metadata.BookMetadata
		baseScores []float64
		baseTier   string
		failedErr  string
		answered   bool
	}
	fetched := make([]sourceFetch, len(states))
	var sg errgroup.Group
	sg.SetLimit(sourceFanoutLimit())
	for i, st := range states {
		failedErr := ""
		lastErr := st.lastErr
		// A fan-out cut short by cancellation still names its cause.
		if lastErr == nil && ctx.Err() != nil && st.asked > 0 {
			lastErr = ctx.Err()
		}
		if len(st.results) == 0 && lastErr != nil {
			failedErr = lastErr.Error()
		}
		// The pooled per-provider row the single-book fetch and the bulk
		// fetch replay (CachedMetadataForProvider): written when this search
		// asked the provider live and it answered, never empty. The search
		// itself reads only its per-variant rows.
		if st.live > 0 && len(st.results) > 0 {
			if blob, merr := json.Marshal(st.results); merr == nil {
				if perr := database.PutCachedMetadataFetch(mfs.db, id, st.name, searchIdentity, blob, 0); perr != nil {
					searchFanoutLog.Warn("search pooled cache put failed: book=%s source=%s err=%v",
						logger.SanitizeLogValue(id), st.name, perr)
				}
			}
		}
		fetched[i] = sourceFetch{name: st.name, results: st.results, failedErr: failedErr, answered: st.answered(ctx)}
		sg.Go(func() error {
			fetched[i].baseScores, fetched[i].baseTier = mfs.ScoreBaseCandidates(ctx, book, fetched[i].results, searchWords)
			return nil
		})
	}
	// Scoring never returns an error (the callbacks return nil); one here is
	// a broken invariant, logged rather than dropped.
	if err := sg.Wait(); err != nil {
		searchFanoutLog.Error("search scoring: unexpected error (book %s): %v", logger.SanitizeLogValue(id), err)
	}

	seen := candidateSeen{}
	var candidates []MetadataCandidate
	var sourcesTried, sourcesAnswered, sourcesAsked []string
	sourcesFailed := map[string]string{}

	// Merge in SOURCE ORDER: `sources` is priority-ordered and the dedupe is
	// first-wins, so a parallel merge would make the winner of a duplicate
	// nondeterministic between runs.
	for srcIdx := range sources {
		sf := fetched[srcIdx]
		src := sources[srcIdx]
		sourcesTried = append(sourcesTried, sf.name)
		if sf.failedErr != "" {
			sourcesFailed[sf.name] = sf.failedErr
		}
		if sf.answered {
			sourcesAnswered = append(sourcesAnswered, sf.name)
		}
		if states[srcIdx].asked > 0 {
			sourcesAsked = append(sourcesAsked, sf.name)
		}
		allResults := sf.results
		baseScores, baseTier := sf.baseScores, sf.baseTier

		for i, r := range allResults {
			if !seen.add(r) {
				continue
			}

			baseScore := baseScores[i]

			// Apply non-base adjustments (compilation, length, rich metadata). For
			// non-F1 tiers, pass baseWordCount=0 so the length penalty is suppressed —
			// it's a token-overlap-specific signal that doesn't translate to semantic
			// embedding scores.
			baseWordCount := 0
			if baseTier == "f1" {
				baseWordCount = len(searchWords)
			}
			adjusted, adjSteps := ApplyNonBaseAdjustmentsWithBreakdown(baseScore, r, baseWordCount)
			rec := newScoreRecorder(baseScore, baseTierLabel(baseTier), baseTierDetail(baseTier))
			rec.adopt(adjSteps, adjusted)
			score := rec.score

			// Tier-specific minimum on the adjusted score. F1 path filters at <= 0
			// (preserves original behavior); embedding path uses the configured
			// MetadataEmbeddingMinScore threshold.
			minScore := 0.0
			if baseTier == "embedding" {
				minScore = config.AppConfig.MetadataScoring.EmbeddingMinScore
			}
			if score <= minScore {
				searchFanoutLog.Debug("search candidate below threshold: score=%.3f tier=%s title=%s source=%s",
					score, baseTier, logger.SanitizeLogValue(r.Title), src.Name())
				continue
			}

			// Author-based scoring: boost matches, penalize mismatches or missing.
			// Taggers swap author and narrator, so a result whose author is the
			// book's narrator is credited as a swap rather than penalised.
			if bookAuthor != "" {
				if r.Author != "" {
					rAuthorLower := strings.ToLower(r.Author)
					bAuthorLower := strings.ToLower(bookAuthor)
					nLower := strings.ToLower(bookNarrator)
					switch {
					case strings.Contains(rAuthorLower, bAuthorLower) || strings.Contains(bAuthorLower, rAuthorLower):
						rec.mul("author", "Author match", 1.5,
							"The result's author matches the book's known author.")
					case nLower != "" && (strings.Contains(rAuthorLower, nLower) || strings.Contains(nLower, rAuthorLower)):
						rec.mul("author", "Author/narrator swapped", 1.4,
							"The result's author is the book's narrator: the book's tags have the two swapped.")
					default:
						rec.mul("author", "Author mismatch", 0.7,
							"The result names a different author than the book.")
					}
				} else {
					rec.mul("author", "Author missing", 0.75,
						"The book's author is known but the result does not name one.")
				}
			}

			// Narrator-based scoring: boost matches as secondary tiebreaker
			if bookNarrator != "" && r.Narrator != "" {
				rNarrLower := strings.ToLower(r.Narrator)
				bNarrLower := strings.ToLower(bookNarrator)
				if strings.Contains(rNarrLower, bNarrLower) || strings.Contains(bNarrLower, rNarrLower) {
					rec.mul("narrator_match", "Narrator match", 1.3,
						"The result's narrator matches the book's known narrator.")
				}
			}

			// Series-based scoring: boost results in the matching series
			if searchSeries != "" && r.Series != "" {
				rSeriesLower := strings.ToLower(r.Series)
				sSeriesLower := strings.ToLower(searchSeries)
				if strings.Contains(rSeriesLower, sSeriesLower) || strings.Contains(sSeriesLower, rSeriesLower) {
					rec.mul("series", "Series match", scoringKnobs().SeriesNameMatchBoost,
						"The result belongs to the same series as the search.")
				}
			}

			// Audiobook-specific scoring: boost results with narrator info,
			// penalize sparse results from non-audiobook sources
			if r.Narrator != "" {
				rec.mul("narrator_present", "Has narrator", 1.15,
					"The result names a narrator, so it is more likely an audiobook edition.")
			} else {
				rec.mul("narrator_present", "No narrator", 0.85,
					"The result names no narrator, typical of a print or ebook record.")
			}

			// The ASIN multiplier goes only to an answer that agrees with the
			// book (ownASINAgrees). A stored ASIN is sometimes a sibling's,
			// and a sibling that names no number ("The Final" for "The Final
			// Four") is one the apply gate cannot refute: doubling it put it
			// above the right answer, and bulk apply takes Candidates[0].
			if asinToLookup != "" && strings.EqualFold(strings.TrimSpace(r.ASIN), asinToLookup) {
				if strong.ownASINAgrees(r) {
					rec.mul("asin_match", "ASIN match", 2.0,
						"The result carries the book's own ASIN, names the book's position and no other, and agrees with the book on its title or on a runtime within 2%; it is ranked above every result that does not.")
				} else {
					rec.mul("asin_match", "ASIN match (no boost)", 1.0,
						"The result carries the book's own ASIN but names another series position, names none of the book's position or numbers, or agrees with the book on neither its title nor a runtime within 2%, so the ASIN earns it no boost: the stored ASIN may be a sibling's.")
				}
			}

			var transcriptionBoosted bool
			if !th.empty() {
				var boosted float64
				boosted, transcriptionBoosted = transcriptionBoost(rec.score, r, th)
				rec.mulResult("transcription", "Transcription match", boosted,
					"The result agrees with the title/author/narrator heard in the book's own audio intro.")
			}

			// Duration-based scoring: compare candidate runtime vs. local file duration.
			rec.mul("duration", "Runtime comparison",
				durationScoreMultiplier(bookDurationSec, r.DurationSec),
				durationStepDetail(bookDurationSec, r.DurationSec))
			score = rec.score

			candidates = append(candidates, newSearchCandidate(r, src.Name(), score, rec.breakdown(), bookDurationSec, transcriptionBoosted))
		}
	}

	// The direct ASIN lookup: Audible first (more complete), Audnexus as the
	// fallback. Skipped when the pool already holds that ASIN with a runtime:
	// the lookup could not change the ranking, and the book would wait on it.
	// A lookup that ERRORS (not "no such ASIN") marks its provider failed and
	// unanswered, so a search whose identity question failed is never
	// recorded as that provider's fresh "nothing" (noSourceAnswered,
	// cacheSearchResponse).
	lookupFailed := map[string]string{}
	providerName := func(id, fallback string) string {
		for _, st := range states {
			if metadata.ProviderIDOf(st.src) == id {
				return st.name
			}
		}
		return fallback
	}
	if needOwnASIN {
		result, err := mfs.lookupASIN(ctx, limiter, metadata.SourceIDAudible, asinToLookup)
		if err != nil {
			lookupFailed[providerName(metadata.SourceIDAudible, "Audible")] = err.Error()
		}
		if err != nil || result == nil {
			searchFanoutLog.Debug("search ASIN lookup on Audible failed, trying Audnexus: asin=%s err=%v",
				logger.SanitizeLogValue(asinToLookup), err)
			result, err = mfs.lookupASIN(ctx, limiter, metadata.SourceIDAudnexus, asinToLookup)
			if err != nil {
				lookupFailed[providerName(metadata.SourceIDAudnexus, "Audnexus (Audible)")] = err.Error()
			} else {
				delete(lookupFailed, providerName(metadata.SourceIDAudible, "Audible"))
			}
		}
		if err == nil && result != nil && strong.positionConflicts(*result) {
			// The stored ASIN is a sibling's ("Rogue Ascension 7" stored on
			// book 8 by an earlier bad match): the store answered for another
			// book, which the position rules drop exactly as they drop it from
			// the pool. Not a failure -- the provider answered.
			searchFanoutLog.Debug("search ASIN lookup returned another position, dropped: asin=%s title=%s position=%s",
				logger.SanitizeLogValue(asinToLookup), logger.SanitizeLogValue(result.Title), logger.SanitizeLogValue(result.SeriesPosition))
			result = nil
		}
		if err == nil && result != nil {
			if seen.add(*result) {
				score, asinBd := ScoreOneResultWithBreakdown(*result, searchWords)
				asinRec := &scoreRecorder{score: score, steps: asinBd.Steps}
				if score <= 0 && !strong.siblingEvidence(*result) {
					// A direct ASIN match always scores high: a score <= 0 means
					// the title gave no search words, so the stored ASIN is the
					// only evidence there is. The floor is withheld only on
					// positive evidence of a sibling (siblingEvidence: another
					// position, or a number the book's title lacks) -- never on a
					// mere lack of agreement, or such a book loses its only
					// candidate (score 0 fails the apply gate). This OVERWRITES the
					// pipeline result rather than adjusting it, so it is recorded
					// as a replace -- a reviewer seeing 1.0 needs to know the
					// title/author evidence was bypassed, not that it was strong.
					asinRec.replace("asin_match", "Direct ASIN match", 1.0,
						"This result was matched by ASIN, which is authoritative, so the "+
							"title/author score was overridden.")
				}
				asinRec.mul("duration", "Runtime comparison",
					durationScoreMultiplier(bookDurationSec, result.DurationSec),
					durationStepDetail(bookDurationSec, result.DurationSec))
				candidates = append(candidates, newSearchCandidate(*result, "Audnexus (Audible)", asinRec.score, asinRec.breakdown(), bookDurationSec, false))
			}
		} else {
			searchFanoutLog.Debug("search ASIN lookup failed: asin=%s err=%v", logger.SanitizeLogValue(asinToLookup), err)
		}
	}
	if len(lookupFailed) > 0 {
		kept := sourcesAnswered[:0:0]
		for _, n := range sourcesAnswered {
			if _, failed := lookupFailed[n]; !failed {
				kept = append(kept, n)
			}
		}
		sourcesAnswered = kept
		for n, e := range lookupFailed {
			if _, had := sourcesFailed[n]; !had {
				sourcesFailed[n] = "ASIN lookup: " + e
			}
			if !slices.Contains(sourcesAsked, n) {
				sourcesAsked = append(sourcesAsked, n)
			}
		}
	}

	// Filter out results without cover images — they're typically low-quality
	// entries that clutter the results. Exempt strong-evidence candidates:
	// direct ASIN-lookup, transcription-boosted, and the top-scored candidate.
	candidates = filterCoverlessCandidates(candidates)

	// Series-number tiebreaker: if the original title contains a number that
	// was stripped for search (e.g. "We Hunt Monsters 8" → "We Hunt Monsters"),
	// boost candidates whose SeriesPosition or title number matches.
	// The title's own trailing number, read from the title less what
	// metadata.ParseBookName removed: the " - 01" of "Discworld 24 - The Fifth
	// Elephant - 01" is a track, and expecting 1 boosted book 1 of the series
	// over book 24 and penalised the book itself.
	originalTitle := in.parsed.Cleaned
	if strings.TrimSpace(originalTitle) == "" {
		originalTitle = in.rawQuery
	}
	expectedNum := extractTrailingNumber(originalTitle)
	if expectedNum == "" && in.parsed.Position != "" {
		// The position parseSearchTitle read out of the title ("Jack Reacher
		// 17: A Wanted Man", "The Witcher - 4 - ...").
		expectedNum = normalizeSeriesNumber(in.parsed.Position)
	}
	if expectedNum != "" {
		k := scoringKnobs()
		for i := range candidates {
			c := &candidates[i]
			// A range or list ("8-10") is an omnibus: neither this book's
			// number nor another's (normPosition), so no boost and no penalty.
			if len(titleNumberRe.FindAllString(c.SeriesPosition, 2)) > 1 {
				continue
			}
			candidateNum := ""
			// Check SeriesPosition first (most reliable)
			if c.SeriesPosition != "" {
				candidateNum = normalizeSeriesNumber(c.SeriesPosition)
			}
			// Fall back to trailing number in candidate title
			if candidateNum == "" {
				candidateNum = extractTrailingNumber(c.Title)
			}
			if candidateNum == expectedNum {
				c.Score *= k.SeriesNumberExactBoost // Strong boost for exact number match
			} else if candidateNum != "" && candidateNum != expectedNum {
				c.Score *= k.SeriesNumberWrongPenalty // Penalize wrong number in same series
			}
		}
	}

	// Sort by score descending, with every candidate that carries the ASIN
	// looked up (the book's own, or the query's) AND agrees with the book
	// (ownASINAgrees: the book's position and no other, and its title or a
	// runtime within 2%) ranked first. A stored ASIN is sometimes a
	// sibling's, so one that disagrees gets no ASIN multiplier and no tier. The candidate's series fields
	// go in too: the position check needs its explicit series_position.
	asinFirst := func(c MetadataCandidate) bool {
		return strong.ownASINAgrees(metadata.BookMetadata{Title: c.Title, Author: c.Author, Narrator: c.Narrator,
			Series: c.Series, SeriesPosition: c.SeriesPosition, ASIN: c.ASIN, DurationSec: c.DurationSec})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if ai, aj := asinFirst(candidates[i]), asinFirst(candidates[j]); ai != aj {
			return ai
		}
		return candidates[i].Score > candidates[j].Score
	})

	// Cap at 50 to support large series
	if len(candidates) > 50 {
		candidates = candidates[:50]
	}

	// Optional LLM rerank pass on the top ambiguous candidates.
	if opts.UseRerank && mfs.llmScorer != nil && config.AppConfig.MetadataScoring.LLMEnabled {
		candidates = mfs.RerankTopK(ctx, book, candidates)
	}

	searchFanoutLog.Debug("search returning %d candidates for %s (%d variants)", len(candidates),
		logger.SanitizeLogValue(searchTitle), len(variants))

	return &SearchMetadataResponse{
		Results:           candidates,
		Query:             searchTitle,
		SourcesTried:      sourcesTried,
		SourcesFailed:     sourcesFailed,
		SourcesAnswered:   sourcesAnswered,
		SourcesAsked:      sourcesAsked,
		InputFingerprint:  in.fingerprint(book.Title),
		LegacyFingerprint: in.legacyFingerprint(book.Title),
		BookASIN:          trimmedASIN(book),
		carryFilter:       strong.filterCarried,
	}, nil
}

// filterCoverlessCandidates drops candidates with no CoverURL, except:
//   - the direct ASIN-lookup candidate (Source == "Audnexus (Audible)"),
//   - any TranscriptionBoosted candidate,
//   - the single highest-scored candidate in the input slice.
//
// If every candidate lacks a cover, the input is returned unchanged.
func filterCoverlessCandidates(candidates []MetadataCandidate) []MetadataCandidate {
	if len(candidates) == 0 {
		return candidates
	}

	// Check if any candidate has a cover. If none do, return unchanged.
	hasCover := false
	for _, c := range candidates {
		if c.CoverURL != "" {
			hasCover = true
			break
		}
	}
	if !hasCover {
		return candidates
	}

	// Find the highest-scored candidate
	bestIdx := 0
	for i := range candidates {
		if candidates[i].Score > candidates[bestIdx].Score {
			bestIdx = i
		}
	}

	// Apply exemption logic
	var withCover []MetadataCandidate
	for i, c := range candidates {
		switch {
		case c.CoverURL != "":
			withCover = append(withCover, c)
		case c.Source == "Audnexus (Audible)":
			withCover = append(withCover, c)
		case c.TranscriptionBoosted:
			withCover = append(withCover, c)
		case i == bestIdx:
			withCover = append(withCover, c)
		}
	}

	if len(withCover) > 0 {
		return withCover
	}
	return candidates
}

// trimmedASIN is book's ASIN without surrounding space, "" when it has none.
func trimmedASIN(book *database.Book) string {
	if book == nil || book.ASIN == nil {
		return ""
	}
	return strings.TrimSpace(*book.ASIN)
}
