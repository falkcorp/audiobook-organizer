// file: internal/maintenance/jobs/bulk_fetch_metadata.go
// version: 1.2.0
// guid: b3c9d7e8-0f1a-2b3c-4d5e-6f7a8b9c0d1e
// last-edited: 2026-07-07

package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

func init() { maintenance.Register(&bulkFetchMetadataJob{}) }

type bulkFetchMetadataJob struct{}

type bmf_params struct {
	PreferAudible bool `json:"prefer_audible"`
	SkipCached    bool `json:"skip_cached"`
}

func (j *bulkFetchMetadataJob) ID() string       { return "bulk-fetch-metadata" }
func (j *bulkFetchMetadataJob) Name() string     { return "Bulk Fetch Metadata" }
func (j *bulkFetchMetadataJob) Category() string { return "Metadata" }
func (j *bulkFetchMetadataJob) Description() string {
	return "Fetches and caches metadata from all configured sources for every book in the library"
}
func (j *bulkFetchMetadataJob) DefaultParams() any { return &bmf_params{} }
func (j *bulkFetchMetadataJob) CanResume() bool    { return true }
func (j *bulkFetchMetadataJob) Permission() string { return string(auth.PermLibraryEditMetadata) }

func (j *bulkFetchMetadataJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
	opID := maintenance.OperationIDFromCtx(ctx)

	preferAudible := false
	skipCached := false
	if opID != "" {
		if raw, err := store.GetOperationParams(opID); err == nil && len(raw) > 0 {
			var p bmf_params
			if jerr := json.Unmarshal(raw, &p); jerr == nil {
				preferAudible = p.PreferAudible
				skipCached = p.SkipCached
			}
		}
	}

	allBooks, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return fmt.Errorf("GetAllBooksCore: %w", err)
	}

	ttlDays := config.AppConfig.MetadataFetchCacheTTLDays

	var existingResults []database.OperationResult
	if opID != "" {
		existingResults, _ = store.GetOperationResults(opID)
	}
	done := make(map[string]bool, len(existingResults))
	for _, r := range existingResults {
		done[r.BookID] = true
	}

	allAuthors, err := store.GetAllAuthors()
	if err != nil {
		return fmt.Errorf("GetAllAuthors: %w", err)
	}
	authorByID := make(map[int]string, len(allAuthors))
	for _, a := range allAuthors {
		authorByID[a.ID] = a.Name
	}

	sourceChain := bmf_buildSourceChain()
	if len(sourceChain) == 0 {
		sourceChain = []metadata.MetadataSource{metadata.NewAudibleClient()}
	}
	if preferAudible {
		audible := metadata.NewAudibleClient()
		var rest []metadata.MetadataSource
		for _, src := range sourceChain {
			if src.Name() != audible.Name() {
				rest = append(rest, src)
			}
		}
		sourceChain = append([]metadata.MetadataSource{audible}, rest...)
	}

	type bookWork struct {
		book       database.BookCore
		authorName string
	}
	var work []bookWork
	for i := range allBooks {
		b := &allBooks[i]
		if done[b.ID] || strings.TrimSpace(b.Title) == "" {
			continue
		}
		if skipCached {
			maxAge := time.Duration(ttlDays) * 24 * time.Hour
			hasFreshCache := false
			for _, src := range sourceChain {
				if cached, _, cerr := database.GetCachedMetadataFetchWithMaxAge(store, b.ID, src.Name(), maxAge); cerr == nil && cached != nil {
					hasFreshCache = true
					break
				}
			}
			if hasFreshCache {
				continue
			}
		}
		author := ""
		if b.AuthorID != nil {
			author = authorByID[*b.AuthorID]
		}
		work = append(work, bookWork{book: *b, authorName: author})
	}

	totalBooks := len(existingResults) + len(work)
	alreadyDone := len(existingResults)
	slog.Info("bulk-fetch-metadata books total, already cached, to fetch", "opID", opID, "totalBooks", totalBooks, "alreadyDone", alreadyDone, "work_count", len(work))

	reporter.SetTotal(totalBooks)
	for i := 0; i < alreadyDone; i++ {
		reporter.Increment()
	}

	if len(work) == 0 {
		slog.Info("all books already cached")
		return nil
	}

	completed := int64(alreadyDone)
	found := 0
	notFound := 0

	for i, w := range work {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		bookID := w.book.ID
		currentAuthor := w.authorName
		searchTitle := bmf_stripChapterFromTitle(w.book.Title)

		var metaResults []metadata.BookMetadata
		var sourceName string
		cacheHit := false

		maxAge := time.Duration(ttlDays) * 24 * time.Hour
		for _, src := range sourceChain {
			if cached, _, cerr := database.GetCachedMetadataFetchWithMaxAge(store, bookID, src.Name(), maxAge); cerr == nil && cached != nil {
				var cachedResults []metadata.BookMetadata
				if jerr := json.Unmarshal(cached.Results, &cachedResults); jerr == nil && len(cachedResults) > 0 {
					metaResults = cachedResults
					sourceName = src.Name()
					cacheHit = true
					break
				}
			}
			var fetchErr error
			if currentAuthor != "" {
				metaResults, fetchErr = src.SearchByTitleAndAuthor(ctx, searchTitle, currentAuthor)
				if fetchErr == nil && len(metaResults) > 0 {
					sourceName = src.Name()
					break
				}
			}
			metaResults, fetchErr = src.SearchByTitle(ctx, searchTitle)
			if fetchErr == nil && len(metaResults) > 0 {
				sourceName = src.Name()
				break
			}
			if searchTitle != w.book.Title {
				if currentAuthor != "" {
					metaResults, fetchErr = src.SearchByTitleAndAuthor(ctx, w.book.Title, currentAuthor)
					if fetchErr == nil && len(metaResults) > 0 {
						sourceName = src.Name()
						break
					}
				}
				metaResults, fetchErr = src.SearchByTitle(ctx, w.book.Title)
				if fetchErr == nil && len(metaResults) > 0 {
					sourceName = src.Name()
					break
				}
			}
		}

		resultStatus := "not_found"
		if len(metaResults) > 0 && sourceName != "" {
			if !cacheHit {
				if blob, merr := json.Marshal(metaResults); merr == nil {
					_ = database.PutCachedMetadataFetch(store, bookID, sourceName, blob, 0)
				}
			}
			found++
			resultStatus = "cached"
		} else {
			notFound++
		}

		if opID != "" {
			_ = store.CreateOperationResult(&database.OperationResult{
				OperationID: opID,
				BookID:      bookID,
				ResultJSON:  fmt.Sprintf(`{"status":%q,"source":%q}`, resultStatus, sourceName),
				Status:      resultStatus,
			})
		}

		atomic.AddInt64(&completed, 1)
		reporter.Increment()

		// Rate-limit live API calls.
		if !cacheHit && sourceName != "" && i < len(work)-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
	}

	finalCount := atomic.LoadInt64(&completed)
	slog.Info("bulk-fetch-metadata done books — cached not_found", "opID", opID, "finalCount", finalCount, "found", found, "notFound", notFound)
	slog.Info("complete — cached not_found", "found", found, "notFound", notFound)
	return nil
}

// bmf_buildSourceChain reads config.AppConfig.MetadataSources and returns
// ordered, circuit-breaker-wrapped metadata sources.
func bmf_buildSourceChain() []metadata.MetadataSource {
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
			rawSource = metadata.NewOpenLibraryClient()
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
			slog.Warn("Unknown metadata source", "src", src.ID)
		}
		if rawSource != nil {
			chain = append(chain, metadata.NewProtectedSource(rawSource, 5, 30*time.Second))
		}
	}
	return chain
}

// bmf_stripChapterFromTitle removes common track/chapter prefixes and suffixes from a title.
func bmf_stripChapterFromTitle(title string) string {
	cleaned := title
	trackNumPrefix := regexp.MustCompile(`^\d{1,3}\s*[-–.]\s*`)
	cleaned = trackNumPrefix.ReplaceAllString(cleaned, "")
	bareNumPrefix := regexp.MustCompile(`^\d{1,3}\s+`)
	if stripped := strings.TrimSpace(bareNumPrefix.ReplaceAllString(cleaned, "")); stripped != "" {
		cleaned = stripped
	}
	trackWordPrefix := regexp.MustCompile(`(?i)^[Tt]rack\s*\d+\s*[-–.]\s*`)
	cleaned = trackWordPrefix.ReplaceAllString(cleaned, "")
	discWordPrefix := regexp.MustCompile(`(?i)^[Dd]is[ck]\s*\d+\s*[-–.]\s*`)
	cleaned = discWordPrefix.ReplaceAllString(cleaned, "")
	bracketPrefix := regexp.MustCompile(`^\[.*?\]\s*[-–]?\s*`)
	cleaned = bracketPrefix.ReplaceAllString(cleaned, "")
	bracketSuffix := regexp.MustCompile(`\s*\[.*?\]\s*$`)
	cleaned = bracketSuffix.ReplaceAllString(cleaned, "")
	if strings.TrimSpace(cleaned) == "" {
		return title
	}
	return strings.TrimSpace(cleaned)
}
