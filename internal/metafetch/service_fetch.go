// file: internal/metafetch/service_fetch.go
// version: 1.4.1
// guid: b24c7a25-2efa-4b85-adb0-2d591218eff2
// last-edited: 2026-07-03

package metafetch

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"log/slog"
	"path/filepath"
	"strings"
	"time"
)

// queueISBNEnrichment starts a background goroutine to enrich ISBN/ASIN for a book
// if the book is missing those identifiers.
func (mfs *Service) queueISBNEnrichment(id string, book *database.Book) {
	if mfs.isbnEnrichment == nil {
		return
	}
	needsISBN := (book.ISBN10 == nil || *book.ISBN10 == "") && (book.ISBN13 == nil || *book.ISBN13 == "")
	needsASIN := book.ASIN == nil || *book.ASIN == ""
	if !needsISBN && !needsASIN {
		return
	}
	go func(bid string) {
		found, err := mfs.isbnEnrichment.EnrichBookISBN(context.Background(), bid)
		if err != nil {
						slog.Warn("ISBN enrichment failed for", "id", bid, "error", err)
		} else if found {
						slog.Info("ISBN enrichment succeeded for", "id", bid)
		}
	}(id)
}

// FetchMetadataForBook fetches and applies metadata for a single audiobook,
// trying each configured source in priority order until one succeeds.
func (mfs *Service) FetchMetadataForBook(id string) (*FetchMetadataResponse, error) {
	book, err := mfs.db.GetBookByID(id)
	if err != nil || book == nil {
		return nil, fmt.Errorf("audiobook not found")
	}

	if book.MetadataReviewStatus != nil && *book.MetadataReviewStatus == "no_match" {
		return nil, fmt.Errorf("book %q is marked as no-match; use search-metadata to re-evaluate", book.Title)
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

	searchTitle := stripChapterFromTitle(book.Title)

	// Resolve current author and narrator for search refinement and scoring
	currentAuthor := ""
	if book.Author != nil {
		currentAuthor = book.Author.Name
	} else if book.AuthorID != nil {
		if author, aErr := mfs.db.GetAuthorByID(*book.AuthorID); aErr == nil && author != nil {
			currentAuthor = author.Name
		}
	}
	if IsGarbageValue(currentAuthor) {
		currentAuthor = ""
	}
	currentNarrator := ""
	if book.Narrator != nil && *book.Narrator != "" && !IsGarbageValue(*book.Narrator) {
		currentNarrator = *book.Narrator
	}

	// Transcription fallback: when the curated title/author/narrator is missing
	// or garbage, fall back to what Whisper heard in the book's own intro
	// ("TITLE by AUTHOR. Read by NARRATOR."). These are stored separately so a
	// noisy transcript never overwrites curated metadata, but for discovery they
	// are the best available query terms for an otherwise-unsearchable book.
	th := hintsFromBook(book)
	if IsGarbageValue(searchTitle) || strings.TrimSpace(searchTitle) == "" {
		if th.title != "" {
			searchTitle = th.title
		}
	}
	if currentAuthor == "" && th.author != "" {
		currentAuthor = th.author
	}
	if currentNarrator == "" && th.narrator != "" {
		currentNarrator = th.narrator
	}

	var lastErr error
	for _, src := range sources {
		var results []metadata.BookMetadata
		var searchErr error

		// Check the persistent fetch cache before hitting the external API.
		// The cache is shared with the search-dialog path — a bulk library
		// fetch or a prior search dialog populates it, so a subsequent single-
		// book fetch can return immediately without another network round-trip.
		maxAge := time.Duration(config.AppConfig.MetadataFetchCacheTTLDays) * 24 * time.Hour
		if cached, _, cerr := database.GetCachedMetadataFetchWithMaxAge(mfs.db, id, src.Name(), maxAge); cerr == nil && cached != nil {
			var cachedResults []metadata.BookMetadata
			if jerr := json.Unmarshal(cached.Results, &cachedResults); jerr == nil && len(cachedResults) > 0 {
				results = cachedResults
								slog.Debug("metadata-fetch cache HIT for ( ) — results, age", "id", id, "name", src.Name(), "count", len(cachedResults), "value", time.Since(cached.CachedAt).Round(time.Second))
			}
		}

		if len(results) == 0 {
			// Try the ContextualSearch path first if the source implements
			// it. This hands richer context (ASIN, ISBN, narrator) to
			// sources that can use it — Audnexus uses the ASIN for a direct
			// lookup that works when title search can't, Hardcover uses
			// the ISBN for a more precise match than the fuzzy GraphQL
			// search. Sources that don't implement the interface just
			// fall through to the title/author path below.
			if ctxSearch, ok := src.(metadata.ContextualSearch); ok {
				ctx := mfs.buildSearchContext(book, searchTitle, currentAuthor, currentNarrator)
				results, searchErr = ctxSearch.SearchByContext(ctx)
				if searchErr != nil {
										slog.Warn("context search failed for", "name", src.Name(), "value", book.Title, "error", searchErr)
					// Context search failure is non-fatal — fall through
					// to the regular title/author path in case that works.
				}
			}

			// Try title+author search first for better match quality
			if len(results) == 0 && currentAuthor != "" {
				results, searchErr = src.SearchByTitleAndAuthor(context.Background(), searchTitle, currentAuthor)
				if searchErr != nil {
										slog.Warn("title+author search failed for by", "name", src.Name(), "searchTitle", searchTitle, "currentAuthor", currentAuthor, "error", searchErr)
				}
			}

			// Fall back to title-only search
			if len(results) == 0 {
				results, searchErr = src.SearchByTitle(context.Background(), searchTitle)
				if searchErr != nil {
										slog.Warn("failed for", "name", src.Name(), "value", searchTitle, "error", searchErr)
					lastErr = searchErr
				}
			}

			// Try original title if cleaned title returned nothing
			if len(results) == 0 && searchTitle != book.Title {
				results, searchErr = src.SearchByTitle(context.Background(), book.Title)
				if searchErr != nil {
					lastErr = searchErr
					continue
				}
			}

			// Try with subtitle stripped (e.g. "Title: Subtitle" → "Title")
			if len(results) == 0 {
				strippedTitle := stripSubtitle(searchTitle)
				if strippedTitle != searchTitle && strippedTitle != book.Title {
					results, searchErr = src.SearchByTitle(context.Background(), strippedTitle)
					if searchErr != nil {
						lastErr = searchErr
						continue
					}
				}
			}

			// Last resort: the audio-derived (transcribed) title, when it differs
			// from everything tried above. For books with bad/empty curated titles
			// the intro narration ("TITLE by AUTHOR") is often the only term that
			// finds the right edition. Pair it with the transcribed author when we
			// have one for a tighter match.
			if len(results) == 0 && th.title != "" && th.title != searchTitle && th.title != book.Title {
				if th.author != "" {
					results, searchErr = src.SearchByTitleAndAuthor(context.Background(), th.title, th.author)
				}
				if len(results) == 0 {
					results, searchErr = src.SearchByTitle(context.Background(), th.title)
				}
				if searchErr != nil {
					lastErr = searchErr
				}
			}

			// Write non-empty results to cache so future fetch/search calls
			// for this book+source can skip the external API entirely.
			if len(results) > 0 {
				if blob, merr := json.Marshal(results); merr == nil {
					if perr := database.PutCachedMetadataFetch(mfs.db, id, src.Name(), blob, 0); perr != nil {
												slog.Warn("metadata-fetch cache put failed for ( )", "id", id, "name", src.Name(), "error", perr)
					}
				}
			}
		}

		if len(results) == 0 {
						slog.Debug("returned 0 results for", "name", src.Name(), "value", searchTitle)
		}
		if len(results) > 0 {
			// Score all results and pick the best; reject if below quality threshold.
			// Pass the transcribed title too (when present) so its significant
			// words contribute to F1 base scoring alongside the curated title.
			scoreTitles := []string{searchTitle, book.Title}
			if th.title != "" {
				scoreTitles = append(scoreTitles, th.title)
			}
			scored := mfs.bestTitleMatchForBook(book, results, currentAuthor, currentNarrator, scoreTitles...)
			if len(scored) == 0 {
								slog.Debug("all results rejected by quality scorer for", "name", src.Name(), "count", len(results), "value", searchTitle)
				continue // try next source
			}
			// Apply series position filter if the book's position is already known.
			if book.SeriesSequence != nil {
				scored = ApplySeriesPositionFilter(scored, *book.SeriesSequence)
				if len(scored) == 0 {
										slog.Debug("best result rejected by series position filter for", "name", src.Name(), "value", searchTitle)
					continue
				}
			}
			meta := scored[0]
			NormalizeMetaSeries(&meta)

			// When the book has an audio-derived (transcribed) title, require the
			// chosen candidate's title to agree before auto-applying. Otherwise a
			// high-scoring same-author / wrong-title candidate would overwrite good
			// data ("matches the author but not the actual book"). Defer to the next
			// source (and ultimately manual review) rather than apply a wrong book.
			if th.title != "" && !transcribedTitleAgrees(meta.Title, th.title) {
				slog.Debug("auto-fetch: candidate title disagrees with transcribed title; skipping auto-apply",
					"name", src.Name(), "candidate_title", meta.Title, "transcribed_title", th.title)
				continue
			}

			// Safety: never apply empty/untitled metadata
			if meta.Title == "" || strings.ToLower(meta.Title) == "untitled" {
				meta.Title = book.Title // keep original
			}

			// Record history before applying changes
			mfs.RecordChangeHistory(book, meta, src.Name())

			// Apply metadata with downgrade protection
			mfs.ApplyMetadataToBook(book, meta)

			updatedBook, updateErr := mfs.db.UpdateBook(id, book)
			if updateErr != nil {
				return nil, fmt.Errorf("failed to update book: %w", updateErr)
			}

			mfs.persistFetchedMetadata(id, meta)

			// Download cover art locally if we got a cover URL
			if meta.CoverURL != "" && config.AppConfig.RootDir != "" {
				coverPath, coverErr := metadata.DownloadCoverArt(meta.CoverURL, config.AppConfig.RootDir, id)
				if coverErr != nil {
										slog.Warn("cover art download failed for", "id", id, "error", coverErr)
				} else {
										slog.Info("cover art saved to", "path", coverPath)
					// Update book's cover_url to the local path for serving
					localCoverURL := "/api/v1/covers/local/" + filepath.Base(coverPath)
					if updatedBook != nil {
						updatedBook.CoverURL = &localCoverURL
						// Write the full book back — UpdateBook does full column
						// replacement, so passing only CoverURL would wipe everything.
						mfs.db.UpdateBook(id, updatedBook)
					}
					// Embed cover art into all audio files for this book
					if updatedBook != nil {
						mfs.embedCoverInBookFiles(updatedBook, coverPath)
					}
				}
			}

			// Write metadata back to audio file(s) if enabled
			if config.AppConfig.WriteBackMetadata {
				mfs.writeBackMetadata(updatedBook, meta)
			}

			// Queue background ISBN/ASIN enrichment if identifiers are missing
			if updatedBook != nil {
				mfs.queueISBNEnrichment(id, updatedBook)
			}

			return &FetchMetadataResponse{
				Message: "metadata fetched and applied",
				Book:    updatedBook,
				Source:  src.Name(),
			}, nil
		}
	}

	if lastErr != nil {
		return nil, fmt.Errorf("no metadata found from any source (last error: %v)", lastErr)
	}
	return nil, fmt.Errorf("no metadata found for '%s' from any source", book.Title)
}

// FetchMetadataForBookByTitle searches metadata sources using only the book's title,
// suppressing the author name. This is useful when the current author is a production
// company and we want to discover the real author from external sources.
func (mfs *Service) FetchMetadataForBookByTitle(id string) (*FetchMetadataResponse, error) {
	book, err := mfs.db.GetBookByID(id)
	if err != nil || book == nil {
		return nil, fmt.Errorf("audiobook not found")
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

	searchTitle := stripChapterFromTitle(book.Title)

	// Resolve narrator for scoring (author intentionally suppressed in this path)
	titleOnlyNarrator := ""
	if book.Narrator != nil && *book.Narrator != "" && !IsGarbageValue(*book.Narrator) {
		titleOnlyNarrator = *book.Narrator
	}

	var lastErr error
	for _, src := range sources {
		results, searchErr := src.SearchByTitle(context.Background(), searchTitle)
		if searchErr != nil {
			lastErr = searchErr
			continue
		}
		if len(results) == 0 && searchTitle != book.Title {
			results, searchErr = src.SearchByTitle(context.Background(), book.Title)
			if searchErr != nil {
				lastErr = searchErr
				continue
			}
		}
		if len(results) == 0 {
			strippedTitle := stripSubtitle(searchTitle)
			if strippedTitle != searchTitle {
				results, searchErr = src.SearchByTitle(context.Background(), strippedTitle)
				if searchErr != nil {
					lastErr = searchErr
					continue
				}
			}
		}
		if len(results) == 0 {
			continue
		}

		scored := mfs.bestTitleMatchForBook(book, results, "", titleOnlyNarrator, searchTitle, book.Title)
		if len(scored) == 0 {
			continue
		}
		meta := scored[0]
		NormalizeMetaSeries(&meta)

		mfs.RecordChangeHistory(book, meta, src.Name())
		mfs.ApplyMetadataToBook(book, meta)

		updatedBook, updateErr := mfs.db.UpdateBook(id, book)
		if updateErr != nil {
			return nil, fmt.Errorf("failed to update book: %w", updateErr)
		}

		mfs.persistFetchedMetadata(id, meta)

		// Mirror of ApplyMetadataCandidate: tag the book with the
		// source and language so downstream filters (review dialog,
		// upgrade jobs) have provenance to key on.
		mfs.ApplyMetadataSystemTags(id, src.Name(), meta.Language)

		return &FetchMetadataResponse{
			Message: "metadata fetched by title only",
			Book:    updatedBook,
			Source:  src.Name(),
		}, nil
	}

	if lastErr != nil {
		return nil, fmt.Errorf("no metadata found from any source (last error: %v)", lastErr)
	}
	return nil, fmt.Errorf("no metadata found for '%s' from any source (title-only search)", book.Title)
}
