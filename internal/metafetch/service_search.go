// file: internal/metafetch/service_search.go
// version: 1.13.0
// guid: bcba782a-8ed4-4285-be91-2af3eddc90e3
// last-edited: 2026-09-05

package metafetch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
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

	providerhttp.SetLimits(key, eff)
	// Drop the cached client/limiter so the next Client() rebuilds on the new
	// budget. Without this the setting is stored and never takes effect.
	providerhttp.ResetProvider(key)
	slog.Debug("applied provider rate limit", "provider", key, "tier", rl.Tier,
		"rps", eff.RPS, "burst", eff.Burst, "timeout", eff.Timeout)
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

	searchTitle := query
	if searchTitle == "" {
		searchTitle = book.Title
	}
	searchTitle = stripChapterFromTitle(searchTitle)

	// If title is effectively empty but we have author/narrator hints,
	// use the author name as search query to get results
	if strings.TrimSpace(searchTitle) == "" || searchTitle == "-" {
		if author != "" {
			searchTitle = author
		} else if book.AuthorID != nil {
			if a, aerr := mfs.db.GetAuthorByID(*book.AuthorID); aerr == nil && a != nil {
				searchTitle = a.Name
			}
		}
	}

	var sources []metadata.MetadataSource
	if len(mfs.overrideSources) > 0 {
		sources = mfs.overrideSources
	} else {
		sources = mfs.BuildSourceChain()
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("no metadata sources enabled")
	}

	// Normalize explicit author/narrator/series hints for downstream scoring.
	searchAuthor := strings.TrimSpace(author)
	searchNarrator := strings.TrimSpace(narrator)
	searchSeries := strings.TrimSpace(series)

	// Always resolve the book's own author and narrator for scoring tiebreaks,
	// even when no explicit hints were provided in the search request
	bookAuthor := searchAuthor
	if bookAuthor == "" && book.AuthorID != nil {
		if author, aerr := mfs.db.GetAuthorByID(*book.AuthorID); aerr == nil && author != nil {
			bookAuthor = author.Name
		}
	}
	if IsGarbageValue(bookAuthor) {
		bookAuthor = ""
	}
	bookNarrator := searchNarrator
	if bookNarrator == "" && book.Narrator != nil && *book.Narrator != "" {
		bookNarrator = *book.Narrator
	}
	if IsGarbageValue(bookNarrator) {
		bookNarrator = ""
	}

	searchWords := SignificantWords(searchTitle)
	if book.Title != searchTitle {
		for w := range SignificantWords(book.Title) {
			searchWords[w] = true
		}
	}

	// Duration of the local audiobook files (seconds). Used to score candidates
	// by how closely their Audible runtime matches our files. Zero = unknown.
	bookDurationSec := 0
	if book.Duration != nil {
		bookDurationSec = *book.Duration
	}
	th := hintsFromBook(book)

	// Dedupe by lowercase title+author
	seen := map[string]bool{}
	var candidates []MetadataCandidate
	var sourcesTried []string
	sourcesFailed := map[string]string{}

	// gatedSearch runs one LIVE source call after acquiring a limiter token
	// (when a limiter is set) and threads ctx through. Cache hits never call this,
	// so they consume no tokens — the limiter therefore governs actual outbound
	// requests, not books. A cancelled/expired ctx short-circuits without a call.
	gatedSearch := func(fn func(context.Context) ([]metadata.BookMetadata, error)) ([]metadata.BookMetadata, error) {
		if err := waitForLimiter(ctx, limiter); err != nil {
			return nil, err
		}
		return fn(ctx)
	}

	// Fan out across sources. Each provider now has its OWN rate-limit token
	// bucket in internal/metadata/providerhttp, so running sources concurrently
	// does not make them contend for tokens with each other -- that is precisely
	// what made this unsafe before and safe now. Previously every source was
	// queried in series, and with up to four search attempts per source plus a
	// scoring pass, one book's search took a measured 13s on production.
	//
	// Only the I/O half is parallel. The dedupe/scoring merge below stays
	// sequential and in source order, because `sources` is PRIORITY-ORDERED and
	// the dedupe is first-wins: parallelizing the merge would make which source
	// wins a duplicate title+author nondeterministic between runs.
	type sourceFetch struct {
		name       string
		results    []metadata.BookMetadata
		baseScores []float64
		baseTier   string
		failedErr  string
	}
	fetched := make([]sourceFetch, len(sources))

	var fg errgroup.Group
	fg.SetLimit(sourceFanoutLimit())
	for srcIdx, source := range sources {
		srcIdx, src := srcIdx, source
		fg.Go(func() error {
			// Stop promptly if the caller cancelled — don't start another source.
			if err := ctx.Err(); err != nil {
				return nil
			}
			var allResults []metadata.BookMetadata
			var lastErr error
			var failedErr string
			cacheHit := false

			// Check the metadata fetch cache before hitting the
			// external API. Cache key is (bookID, source name) —
			// on hit, we use the cached results as-is and skip the
			// Search* calls entirely. On miss we fall through to
			// the API path and write the result back at the end
			// of the per-source block.
			//
			// Added 2026-04-11 after the OpenAI quota incident
			// where re-fetching 8000 books hit every external API
			// 8000 times even for books we'd already matched with
			// high confidence.
			maxAge := time.Duration(config.AppConfig.MetadataFetchCacheTTLDays) * 24 * time.Hour
			if cached, _, cerr := database.GetCachedMetadataFetchWithMaxAge(mfs.db, id, src.Name(), maxAge); cerr == nil && cached != nil {
				var cachedResults []metadata.BookMetadata
				if jerr := json.Unmarshal(cached.Results, &cachedResults); jerr == nil {
					// Keep the in-memory []BookMetadata internally consistent with the
					// year-kind flag (#1940): entries cached before it shipped
					// deserialize with PublishYearIsAudiobookRelease=false, so re-derive
					// it from the source (cache key includes src.Name()). NOTE: on the
					// search path this is defensive-only — candidates carry Source and
					// re-derive the flag at apply time (service_apply.go) — but it keeps
					// the two cache-replay sites symmetric.
					isRelease := metadata.SourceProducesAudiobookReleaseYear(src.Name())
					for i := range cachedResults {
						cachedResults[i].PublishYearIsAudiobookRelease = isRelease
					}
					allResults = cachedResults
					cacheHit = true
					slog.Debug("metadata-search cache HIT for ( ) — results, age", "id", id, "name", src.Name(), "count", len(cachedResults), "value", time.Since(cached.CachedAt).Round(time.Second))
				}
			}

			if !cacheHit {
				// One ladder per source. A throttle hold or an open breaker
				// answers every later rung the same way, and a cancelled
				// context would fail each rung fast while counting against the
				// shared breaker — both close the ladder. A sentinel never
				// displaces a real diagnosis already held (keepDiagnosis).
				ladderOpen := true
				note := func(serr error) {
					lastErr = keepDiagnosis(lastErr, serr)
					if providerSentinel(serr) {
						ladderOpen = false
					}
				}
				open := func() bool { return ladderOpen && ctx.Err() == nil }

				// If author hint provided, use title+author search for better results
				if open() && searchAuthor != "" {
					if results, serr := gatedSearch(func(c context.Context) ([]metadata.BookMetadata, error) {
						return src.SearchByTitleAndAuthor(c, searchTitle, searchAuthor)
					}); serr == nil {
						allResults = append(allResults, results...)
					} else {
						note(serr)
						slog.Debug("metadata-search SearchByTitleAndAuthor( ) error", "name", src.Name(), "searchTitle", searchTitle, "searchAuthor", searchAuthor, "error", serr)
					}
				}

				// Narrator-as-author fallback: author/narrator fields are frequently
				// swapped in audiobook metadata. Try searching with the narrator as
				// author to catch these cases.
				if open() && bookNarrator != "" && bookNarrator != searchAuthor {
					if results, serr := gatedSearch(func(c context.Context) ([]metadata.BookMetadata, error) {
						return src.SearchByTitleAndAuthor(c, searchTitle, bookNarrator)
					}); serr == nil {
						allResults = append(allResults, results...)
					} else {
						if providerSentinel(serr) {
							ladderOpen = false
						}
						slog.Debug("metadata-search narrator-as-author fallback( ) error", "name", src.Name(), "searchTitle", searchTitle, "narrator", bookNarrator, "error", serr)
					}
				}

				// Always also search by title only to get broader results
				if open() {
					if results, serr := gatedSearch(func(c context.Context) ([]metadata.BookMetadata, error) {
						return src.SearchByTitle(c, searchTitle)
					}); serr == nil {
						allResults = append(allResults, results...)
					} else {
						note(serr)
						slog.Debug("metadata-search SearchByTitle() error", "name", src.Name(), "value", searchTitle, "error", serr)
					}
				}
				// SearchByTitle with original title if different
				if open() && searchTitle != book.Title {
					if results, serr := gatedSearch(func(c context.Context) ([]metadata.BookMetadata, error) {
						return src.SearchByTitle(c, book.Title)
					}); serr == nil {
						allResults = append(allResults, results...)
					} else {
						note(serr)
					}
				}

				// Nothing under the literal titles: retry with the book's own
				// name, the series decoration stripped, and each side of the
				// subtitle separator (see extraTitleVariants), stopping at the
				// first variant that answers. These results are scored and
				// shown to a person like any other, so they are not anchored.
				// A book the literal queries found pays nothing here.
				if len(allResults) == 0 {
					for _, v := range extraTitleVariants(book.Title, searchTitle, false) {
						if !open() {
							break
						}
						if searchAuthor != "" {
							if results, serr := gatedSearch(func(c context.Context) ([]metadata.BookMetadata, error) {
								return src.SearchByTitleAndAuthor(c, v.Query, searchAuthor)
							}); serr == nil {
								allResults = append(allResults, results...)
							} else {
								note(serr)
							}
						}
						if len(allResults) == 0 && open() {
							if results, serr := gatedSearch(func(c context.Context) ([]metadata.BookMetadata, error) {
								return src.SearchByTitle(c, v.Query)
							}); serr == nil {
								allResults = append(allResults, results...)
							} else {
								note(serr)
							}
						}
						if len(allResults) > 0 {
							slog.Debug("metadata-search hit on title variant", "name", src.Name(), "variant", v.Query, "count", len(allResults))
							break
						}
					}
				}
				// If all calls failed (no results and there was an error), record it
				if len(allResults) == 0 && lastErr != nil {
					failedErr = lastErr.Error()
				}

				slog.Debug("metadata-search returned raw results for", "name", src.Name(), "count", len(allResults), "searchTitle", searchTitle)

				// Write to cache on a successful non-empty fetch.
				// Empty and error cases are not cached so they can
				// be retried. Cache is best-effort — a Put failure
				// is logged but doesn't fail the outer search.
				if len(allResults) > 0 {
					if blob, merr := json.Marshal(allResults); merr == nil {
						if perr := database.PutCachedMetadataFetch(mfs.db, id, src.Name(), blob, 0); perr != nil {
							slog.Warn("metadata-search cache put failed for ( )", "id", id, "name", src.Name(), "error", perr)
						}
					}
				}
			}

			baseScores, baseTier := mfs.ScoreBaseCandidates(ctx, book, allResults, searchWords)
			fetched[srcIdx] = sourceFetch{
				name:       src.Name(),
				results:    allResults,
				baseScores: baseScores,
				baseTier:   baseTier,
				failedErr:  failedErr,
			}
			return nil
		})
	}
	// Errors are never returned above (a failing source is recorded per-source and
	// must not abort the others), so Wait's error is structurally always nil.
	_ = fg.Wait()

	// Merge in SOURCE ORDER — see the note above about first-wins dedupe.
	for srcIdx := range sources {
		sf := fetched[srcIdx]
		if sf.name == "" {
			continue // cancelled before this source ran
		}
		src := sources[srcIdx]
		sourcesTried = append(sourcesTried, sf.name)
		if sf.failedErr != "" {
			sourcesFailed[sf.name] = sf.failedErr
		}
		allResults := sf.results
		baseScores, baseTier := sf.baseScores, sf.baseTier
		slog.Debug("metadata-search scored results from with tier", "count", len(allResults), "name", src.Name(), "baseTier", baseTier)

		for i, r := range allResults {
			key := strings.ToLower(r.Title + "|" + r.Author)
			if seen[key] {
				continue
			}
			seen[key] = true

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
				slog.Debug("metadata-search adjusted score (tier) below threshold for by from", "score", score, "baseTier", baseTier, "title", r.Title, "author", r.Author, "name", src.Name())
				continue
			}

			// Author-based scoring: boost matches, penalize mismatches or missing
			if bookAuthor != "" {
				if r.Author != "" {
					rAuthorLower := strings.ToLower(r.Author)
					bAuthorLower := strings.ToLower(bookAuthor)
					if strings.Contains(rAuthorLower, bAuthorLower) || strings.Contains(bAuthorLower, rAuthorLower) {
						rec.mul("author", "Author match", 1.5,
							"The result's author matches the book's known author.")
					} else {
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

			durationDelta := 0
			if bookDurationSec > 0 && r.DurationSec > 0 {
				durationDelta = bookDurationSec - r.DurationSec
				if durationDelta < 0 {
					durationDelta = -durationDelta
				}
			}

			candidates = append(candidates, MetadataCandidate{
				Title:                r.Title,
				Author:               r.Author,
				Narrator:             r.Narrator,
				Series:               r.Series,
				SeriesPosition:       r.SeriesPosition,
				Year:                 r.PublishYear,
				Publisher:            r.Publisher,
				ISBN:                 r.ISBN,
				ASIN:                 r.ASIN,
				CoverURL:             r.CoverURL,
				Description:          r.Description,
				Language:             r.Language,
				Source:               src.Name(),
				Score:                score,
				ScoreBreakdown:       rec.breakdown(),
				DurationSec:          r.DurationSec,
				DurationDeltaSec:     durationDelta,
				DurationScore:        computeDurationScore(bookDurationSec, r.DurationSec),
				CategoryTags:         r.CategoryTags,
				DurationMismatch:     durationDelta > 600,
				TranscriptionBoosted: transcriptionBoosted,
				AudibleRatingOverall: r.AudibleRatingOverall,
				AudibleRatingCount:   r.AudibleRatingCount,
				GoogleRatingAverage:  r.GoogleRatingAverage,
				GoogleRatingCount:    r.GoogleRatingCount,
			})
		}
	}

	// Try ASIN lookup: either the whole query is an ASIN, or extract one from the query
	asinToLookup := ""
	if looksLikeASIN(searchTitle) {
		asinToLookup = searchTitle
	} else {
		asinToLookup = extractASIN(searchTitle)
	}
	if asinToLookup != "" {
		// Try Audible API first (more complete), fall back to Audnexus. Each is a
		// live request, so acquire a limiter token before it (when limiter != nil)
		// to keep the per-request rate ceiling honest. The Audnexus lookup is now
		// ctx-aware — a batch cancel aborts its 9-region loop promptly instead of
		// burning up to 9×30s.
		// LookupByASIN is not on the MetadataSource interface, so these calls
		// cannot go through ProtectedSource and are invisible to both the
		// circuit breaker and the throttle. Gate and record them by hand.
		//
		// Without this, one function held two live paths to the same provider
		// where one honoured a global hold and the other did not — and a 429
		// here, now well-shaped as a ProviderStatusError, reached no classifier
		// at all. Bypassed contexts skip the gate here exactly as they do in
		// ProtectedSource.
		bypass := metadata.ThrottleBypassed(ctx)
		reg := metadata.DefaultThrottleRegistry()

		var result *metadata.BookMetadata
		var err error
		if !bypass && reg.Throttled(metadata.SourceIDAudible) {
			err = metadata.ErrProviderThrottled
		} else if err = waitForLimiter(ctx, limiter); err == nil {
			startedAt := time.Now()
			result, err = metadata.NewAudibleClient().LookupByASIN(asinToLookup)
			if err != nil {
				reg.RecordFailure(metadata.SourceIDAudible, err)
			} else {
				reg.RecordSuccess(metadata.SourceIDAudible, startedAt)
			}
		}
		if err != nil || result == nil {
			slog.Debug("metadata-search Audible API lookup for failed, trying Audnexus", "value", asinToLookup, "error", err)
			if !bypass && reg.Throttled(metadata.SourceIDAudnexus) {
				err = metadata.ErrProviderThrottled
			} else if werr := waitForLimiter(ctx, limiter); werr != nil {
				err = werr
			} else {
				startedAt := time.Now()
				result, err = metadata.NewAudnexusClient().LookupByASIN(ctx, asinToLookup)
				if err != nil {
					reg.RecordFailure(metadata.SourceIDAudnexus, err)
				} else {
					reg.RecordSuccess(metadata.SourceIDAudnexus, startedAt)
				}
			}
		}
		if err == nil && result != nil {
			key := strings.ToLower(result.Title + "|" + result.Author)
			if !seen[key] {
				score, asinBd := ScoreOneResultWithBreakdown(*result, searchWords)
				asinRec := &scoreRecorder{score: score, steps: asinBd.Steps}
				if score <= 0 {
					// Direct ASIN match always scores high. This OVERWRITES the
					// pipeline result rather than adjusting it, so it is recorded
					// as a replace -- a reviewer seeing 1.0 needs to know the
					// title/author evidence was bypassed, not that it was strong.
					asinRec.replace("asin_match", "Direct ASIN match", 1.0,
						"This result was matched by ASIN, which is authoritative, so the "+
							"title/author score was overridden.")
				}
				asinDurationDelta := 0
				if bookDurationSec > 0 && result.DurationSec > 0 {
					asinDurationDelta = bookDurationSec - result.DurationSec
					if asinDurationDelta < 0 {
						asinDurationDelta = -asinDurationDelta
					}
				}
				asinRec.mul("duration", "Runtime comparison",
					durationScoreMultiplier(bookDurationSec, result.DurationSec),
					durationStepDetail(bookDurationSec, result.DurationSec))
				score = asinRec.score
				candidates = append(candidates, MetadataCandidate{
					Title:                result.Title,
					Author:               result.Author,
					Narrator:             result.Narrator,
					Series:               result.Series,
					SeriesPosition:       result.SeriesPosition,
					Year:                 result.PublishYear,
					Publisher:            result.Publisher,
					ISBN:                 result.ISBN,
					ASIN:                 result.ASIN,
					CoverURL:             result.CoverURL,
					Description:          result.Description,
					Language:             result.Language,
					Source:               "Audnexus (Audible)",
					Score:                score,
					ScoreBreakdown:       asinRec.breakdown(),
					DurationSec:          result.DurationSec,
					DurationDeltaSec:     asinDurationDelta,
					DurationScore:        computeDurationScore(bookDurationSec, result.DurationSec),
					CategoryTags:         result.CategoryTags,
					DurationMismatch:     asinDurationDelta > 600,
					AudibleRatingOverall: result.AudibleRatingOverall,
					AudibleRatingCount:   result.AudibleRatingCount,
					GoogleRatingAverage:  result.GoogleRatingAverage,
					GoogleRatingCount:    result.GoogleRatingCount,
				})
			}
		} else {
			slog.Debug("metadata-search ASIN lookup for failed", "value", asinToLookup, "error", err)
		}
	}

	// Filter out results without cover images — they're typically low-quality
	// entries that clutter the results. Exempt strong-evidence candidates:
	// direct ASIN-lookup, transcription-boosted, and the top-scored candidate.
	candidates = filterCoverlessCandidates(candidates)

	// Series-number tiebreaker: if the original title contains a number that
	// was stripped for search (e.g. "We Hunt Monsters 8" → "We Hunt Monsters"),
	// boost candidates whose SeriesPosition or title number matches.
	originalTitle := query
	if originalTitle == "" {
		originalTitle = book.Title
	}
	if expectedNum := extractTrailingNumber(originalTitle); expectedNum != "" {
		k := scoringKnobs()
		for i := range candidates {
			c := &candidates[i]
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

	// Sort by score descending
	sort.Slice(candidates, func(i, j int) bool {
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

	slog.Debug("metadata-search returning candidates for (search words )", "candidateCount", len(candidates), "searchTitle", searchTitle, "searchWords", searchWords)

	return &SearchMetadataResponse{
		Results:       candidates,
		Query:         searchTitle,
		SourcesTried:  sourcesTried,
		SourcesFailed: sourcesFailed,
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
