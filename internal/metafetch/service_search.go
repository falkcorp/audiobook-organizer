// file: internal/metafetch/service_search.go
// version: 1.3.0
// guid: bcba782a-8ed4-4285-be91-2af3eddc90e3
// last-edited: 2026-07-01

package metafetch

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"log/slog"
	"sort"
	"strings"
	"time"
)

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
func (mfs *Service) BuildSourceChain() []metadata.MetadataSource {
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
		var rawSource metadata.MetadataSource
		switch src.ID {
		case "openlibrary":
			client := metadata.NewOpenLibraryClient()
			if mfs.olStore != nil {
				client.SetOLStore(mfs.olStore)
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
			chain = append(chain, metadata.NewProtectedSource(rawSource, 5, 30*time.Second))
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
func (mfs *Service) SearchMetadataForBookWithOptions(
	id, query, author, narrator, series string,
	opts SearchOptions,
) (*SearchMetadataResponse, error) {
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

	for _, src := range sources {
		var allResults []metadata.BookMetadata
		var lastErr error
		sourcesTried = append(sourcesTried, src.Name())
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
				allResults = cachedResults
				cacheHit = true
				slog.Debug("metadata-search cache HIT for ( ) — results, age", "id", id, "name", src.Name(), "count", len(cachedResults), "value", time.Since(cached.CachedAt).Round(time.Second))
			}
		}

		if !cacheHit {
			// If author hint provided, use title+author search for better results
			if searchAuthor != "" {
				if results, serr := src.SearchByTitleAndAuthor(context.Background(), searchTitle, searchAuthor); serr == nil {
					allResults = append(allResults, results...)
				} else {
					lastErr = serr
					slog.Debug("metadata-search SearchByTitleAndAuthor( ) error", "name", src.Name(), "value", searchTitle, "value", searchAuthor, "error", serr)
				}
			}

			// Narrator-as-author fallback: author/narrator fields are frequently
			// swapped in audiobook metadata. Try searching with the narrator as
			// author to catch these cases.
			if bookNarrator != "" && bookNarrator != searchAuthor {
				if results, serr := src.SearchByTitleAndAuthor(context.Background(), searchTitle, bookNarrator); serr == nil {
					allResults = append(allResults, results...)
				} else {
					slog.Debug("metadata-search narrator-as-author fallback( ) error", "name", src.Name(), "value", searchTitle, "value", bookNarrator, "error", serr)
				}
			}

			// Always also search by title only to get broader results
			if results, serr := src.SearchByTitle(context.Background(), searchTitle); serr == nil {
				allResults = append(allResults, results...)
			} else {
				lastErr = serr
				slog.Debug("metadata-search SearchByTitle() error", "name", src.Name(), "value", searchTitle, "error", serr)
			}
			// SearchByTitle with original title if different
			if searchTitle != book.Title {
				if results, serr := src.SearchByTitle(context.Background(), book.Title); serr == nil {
					allResults = append(allResults, results...)
				} else {
					lastErr = serr
				}
			}

			// If all calls failed (no results and there was an error), record it
			if len(allResults) == 0 && lastErr != nil {
				sourcesFailed[src.Name()] = lastErr.Error()
			}

			slog.Debug("metadata-search returned raw results for", "name", src.Name(), "count", len(allResults), "value", searchTitle)

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

		baseScores, baseTier := mfs.ScoreBaseCandidates(context.Background(), book, allResults, searchWords)
		slog.Debug("metadata-search scored results from with tier", "count", len(allResults), "name", src.Name(), "value", baseTier)

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
			score := ApplyNonBaseAdjustments(baseScore, r, baseWordCount)

			// Tier-specific minimum on the adjusted score. F1 path filters at <= 0
			// (preserves original behavior); embedding path uses the configured
			// MetadataEmbeddingMinScore threshold.
			minScore := 0.0
			if baseTier == "embedding" {
				minScore = config.AppConfig.MetadataScoring.EmbeddingMinScore
			}
			if score <= minScore {
				slog.Debug("metadata-search adjusted score%.3f (tier) below threshold for by from", "value", score, "value", baseTier, "value", r.Title, "value", r.Author, "name", src.Name())
				continue
			}

			// Author-based scoring: boost matches, penalize mismatches or missing
			if bookAuthor != "" {
				if r.Author != "" {
					rAuthorLower := strings.ToLower(r.Author)
					bAuthorLower := strings.ToLower(bookAuthor)
					if strings.Contains(rAuthorLower, bAuthorLower) || strings.Contains(bAuthorLower, rAuthorLower) {
						score *= 1.5 // Strong boost for author match
					} else {
						score *= 0.7 // Penalize non-matching authors
					}
				} else {
					score *= 0.75 // Penalize results missing author when we know the book's author
				}
			}

			// Narrator-based scoring: boost matches as secondary tiebreaker
			if bookNarrator != "" && r.Narrator != "" {
				rNarrLower := strings.ToLower(r.Narrator)
				bNarrLower := strings.ToLower(bookNarrator)
				if strings.Contains(rNarrLower, bNarrLower) || strings.Contains(bNarrLower, rNarrLower) {
					score *= 1.3 // Boost for narrator match
				}
			}

			// Series-based scoring: boost results in the matching series
			if searchSeries != "" && r.Series != "" {
				rSeriesLower := strings.ToLower(r.Series)
				sSeriesLower := strings.ToLower(searchSeries)
				if strings.Contains(rSeriesLower, sSeriesLower) || strings.Contains(sSeriesLower, rSeriesLower) {
					score *= 1.4 // Boost for series match
				}
			}

			// Audiobook-specific scoring: boost results with narrator info,
			// penalize sparse results from non-audiobook sources
			if r.Narrator != "" {
				score *= 1.15 // Results with narrator are more likely correct audiobook matches
			} else {
				score *= 0.85 // Penalize results without narrator info (likely non-audiobook sources)
			}

			var transcriptionBoosted bool
			if !th.empty() {
				score, transcriptionBoosted = transcriptionBoost(score, r, th)
			}

			// Duration-based scoring: compare candidate runtime vs. local file duration.
			score *= durationScoreMultiplier(bookDurationSec, r.DurationSec)

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
		// Try Audible API first (more complete), fall back to Audnexus
		audibleClient := metadata.NewAudibleClient()
		result, err := audibleClient.LookupByASIN(asinToLookup)
		if err != nil || result == nil {
			slog.Debug("metadata-search Audible API lookup for failed, trying Audnexus", "value", asinToLookup, "error", err)
			audnexus := metadata.NewAudnexusClient()
			result, err = audnexus.LookupByASIN(asinToLookup)
		}
		if err == nil && result != nil {
			key := strings.ToLower(result.Title + "|" + result.Author)
			if !seen[key] {
				score := ScoreOneResult(*result, searchWords)
				if score <= 0 {
					score = 1.0 // Direct ASIN match always scores high
				}
				asinDurationDelta := 0
				if bookDurationSec > 0 && result.DurationSec > 0 {
					asinDurationDelta = bookDurationSec - result.DurationSec
					if asinDurationDelta < 0 {
						asinDurationDelta = -asinDurationDelta
					}
				}
				score *= durationScoreMultiplier(bookDurationSec, result.DurationSec)
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
	// entries that clutter the results. Keep them only if ALL results lack covers.
	var withCover []MetadataCandidate
	for _, c := range candidates {
		if c.CoverURL != "" {
			withCover = append(withCover, c)
		}
	}
	if len(withCover) > 0 {
		candidates = withCover
	}

	// Series-number tiebreaker: if the original title contains a number that
	// was stripped for search (e.g. "We Hunt Monsters 8" → "We Hunt Monsters"),
	// boost candidates whose SeriesPosition or title number matches.
	originalTitle := query
	if originalTitle == "" {
		originalTitle = book.Title
	}
	if expectedNum := extractTrailingNumber(originalTitle); expectedNum != "" {
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
				c.Score *= 2.0 // Strong boost for exact number match
			} else if candidateNum != "" && candidateNum != expectedNum {
				c.Score *= 0.5 // Penalize wrong number in same series
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
		candidates = mfs.RerankTopK(context.Background(), book, candidates)
	}

	slog.Debug("metadata-search returning candidates for (search words )", "id", len(candidates), "value", searchTitle, "value", searchWords)

	return &SearchMetadataResponse{
		Results:       candidates,
		Query:         searchTitle,
		SourcesTried:  sourcesTried,
		SourcesFailed: sourcesFailed,
	}, nil
}
