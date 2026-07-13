// file: internal/metadata/hardcover.go
// version: 1.3.0
// guid: e7e02554-8931-49ba-9528-d3d51279da1d
// last-edited: 2026-07-13

package metadata

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// HardcoverClient fetches metadata from the Hardcover.app GraphQL API.
// Requires a Bearer token for authentication.
type HardcoverClient struct {
	httpClient *http.Client
	baseURL    string
	apiToken   string

	// Simple rate limiter: 60 requests per minute
	mu         sync.Mutex
	requestLog []time.Time
	rateLimit  int
	ratePeriod time.Duration
}

// NewHardcoverClient creates a new Hardcover API client with the given token.
func NewHardcoverClient(apiToken string) *HardcoverClient {
	return &HardcoverClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    "https://api.hardcover.app/v1/graphql",
		apiToken:   apiToken,
		rateLimit:  60,
		ratePeriod: time.Minute,
	}
}

// NewHardcoverClientWithBaseURL creates a client with a custom base URL (for testing).
func NewHardcoverClientWithBaseURL(baseURL, apiToken string) *HardcoverClient {
	return &HardcoverClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiToken:   apiToken,
		rateLimit:  60,
		ratePeriod: time.Minute,
	}
}

// Name returns the display name for this metadata source.
func (c *HardcoverClient) Name() string {
	return "Hardcover"
}

// waitForRateLimit blocks until a request can be made within the rate limit.
func (c *HardcoverClient) waitForRateLimit() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-c.ratePeriod)

	// Remove expired entries
	valid := c.requestLog[:0]
	for _, t := range c.requestLog {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	c.requestLog = valid

	if len(c.requestLog) >= c.rateLimit {
		// Wait until the oldest request expires
		waitUntil := c.requestLog[0].Add(c.ratePeriod)
		sleepDuration := time.Until(waitUntil)
		if sleepDuration > 0 {
			c.mu.Unlock()
			time.Sleep(sleepDuration)
			c.mu.Lock()
			// Re-clean after sleep
			now = time.Now()
			cutoff = now.Add(-c.ratePeriod)
			valid = c.requestLog[:0]
			for _, t := range c.requestLog {
				if t.After(cutoff) {
					valid = append(valid, t)
				}
			}
			c.requestLog = valid
		}
	}

	c.requestLog = append(c.requestLog, time.Now())
}

// GraphQL request/response types

type hardcoverGraphQLRequest struct {
	Query string `json:"query"`
}

type hardcoverGraphQLResponse struct {
	Data   *hardcoverData   `json:"data"`
	Errors []hardcoverError `json:"errors"`
}

type hardcoverError struct {
	Message string `json:"message"`
}

type hardcoverData struct {
	SearchBooks *hardcoverSearchBooks `json:"search_books"`
}

type hardcoverSearchBooks struct {
	Results *hardcoverResults `json:"results"`
}

type hardcoverResults struct {
	Hits []hardcoverHit `json:"hits"`
}

type hardcoverHit struct {
	Document hardcoverDocument `json:"document"`
}

type hardcoverDocument struct {
	Title            string              `json:"title"`
	AuthorNames      []string            `json:"author_names"`
	ContributorNames []string            `json:"contributor_names"`
	Image            *hardcoverImage     `json:"image"`
	Description      string              `json:"description"`
	ReleaseYear      int                 `json:"release_year"`
	Slug             string              `json:"slug"`
	Publisher        string              `json:"publisher"`
	ISBNs            []string            `json:"isbns"`
	Pages            int                 `json:"pages"`
	AudioSeconds     int                 `json:"audio_seconds"`
	SeriesNames      []string            `json:"series_names"`
	FeaturedSeries   *hardcoverFeaturedS `json:"featured_series"`
	Genres           []string            `json:"genres"`
	Language         string              `json:"language"`
}

// hardcoverFeaturedS is the subset of Hardcover's featured_series
// field we need — the series name and the book's position within it.
// Field names match Hardcover's GraphQL schema as of early 2026;
// query is defensive so missing/renamed fields produce zero-valued
// defaults rather than parse errors.
type hardcoverFeaturedS struct {
	SeriesName string  `json:"series_name"`
	Position   float64 `json:"position"`
}

type hardcoverImage struct {
	URL string `json:"url"`
}

// SearchByTitle searches Hardcover by title.
func (c *HardcoverClient) SearchByTitle(ctx context.Context, title string) ([]BookMetadata, error) {
	if c.apiToken == "" {
		// Silent — no token is a config state, not an event worth logging
		// on every per-book fetch.
		return nil, nil
	}
	return c.search(ctx, title)
}

// SearchByTitleAndAuthor searches Hardcover by title (author is used for ranking but
// the GraphQL search_books endpoint only accepts a single query string).
func (c *HardcoverClient) SearchByTitleAndAuthor(ctx context.Context, title, author string) ([]BookMetadata, error) {
	if c.apiToken == "" {
		// Silent — no token is a config state, not an event worth logging
		// on every per-book fetch.
		return nil, nil
	}
	query := title + " " + author
	return c.search(ctx, query)
}

// hardcoverDocumentFields is the full set of fields we request from
// Hardcover's GraphQL schema on every search. Kept as a single const
// so the query is easy to tune and every call site fetches the same
// shape. Fields the API doesn't know about produce GraphQL errors,
// which we catch and surface — if Hardcover renames something, we
// find out immediately instead of silently dropping data.
const hardcoverDocumentFields = `title
author_names
contributor_names
image { url }
description
release_year
slug
publisher
isbns
pages
audio_seconds
series_names
featured_series { series_name position }
genres
language`

// SearchByContext prefers ISBN-based lookup when available (more
// precise than the fuzzy search_books endpoint), falling back to a
// title+author query. ASIN isn't something Hardcover indexes directly
// so we ignore that field.
func (c *HardcoverClient) SearchByContext(ctx context.Context, sc *SearchContext) ([]BookMetadata, error) {
	if c.apiToken == "" {
		return nil, nil
	}
	if sc == nil {
		return nil, nil
	}
	// Prefer ISBN-13 → ISBN-10 → title+author. ctx is the caller's real context
	// so a batch/import cancel aborts the GraphQL request promptly.
	switch {
	case sc.ISBN13 != "":
		return c.search(ctx, sc.ISBN13)
	case sc.ISBN10 != "":
		return c.search(ctx, sc.ISBN10)
	case sc.Title != "" && sc.Author != "":
		return c.search(ctx, sc.Title+" "+sc.Author)
	case sc.Title != "":
		return c.search(ctx, sc.Title)
	}
	return nil, nil
}

func (c *HardcoverClient) search(ctx context.Context, query string) ([]BookMetadata, error) {
	c.waitForRateLimit()

	// Escape the query for embedding in the GraphQL string
	escapedQuery := strings.ReplaceAll(query, `\`, `\\`)
	escapedQuery = strings.ReplaceAll(escapedQuery, `"`, `\"`)

	graphqlQuery := fmt.Sprintf(`query { search_books(query: "%s", limit: 5) { results { hits { document { %s } } } } }`, escapedQuery, hardcoverDocumentFields)

	reqBody := hardcoverGraphQLRequest{Query: graphqlQuery}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal Hardcover request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create Hardcover request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to query Hardcover: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hardcover API returned status %d", resp.StatusCode)
	}

	var gqlResp hardcoverGraphQLResponse
	if err := json.UnmarshalRead(resp.Body, &gqlResp); err != nil {
		return nil, fmt.Errorf("failed to decode Hardcover response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		// GraphQL field errors are fatal to the query — log every one
		// so a schema change surfaces clearly instead of producing
		// silently-empty results.
		for _, e := range gqlResp.Errors {
			slog.Warn("Hardcover GraphQL error", "e", e.Message)
		}
		return nil, fmt.Errorf("hardcover GraphQL error: %s", gqlResp.Errors[0].Message)
	}

	if gqlResp.Data == nil || gqlResp.Data.SearchBooks == nil || gqlResp.Data.SearchBooks.Results == nil {
		return nil, nil
	}

	hits := gqlResp.Data.SearchBooks.Results.Hits
	results := make([]BookMetadata, 0, len(hits))
	for _, hit := range hits {
		results = append(results, hardcoverDocumentToMetadata(&hit.Document))
	}
	return results, nil
}

// hardcoverDocumentToMetadata folds a Hardcover GraphQL document into
// our internal BookMetadata shape. Extracted so every code path uses
// the same field-mapping rules and alt-result resurfaces share one
// implementation.
func hardcoverDocumentToMetadata(doc *hardcoverDocument) BookMetadata {
	meta := BookMetadata{
		Title:       doc.Title,
		Description: doc.Description,
		Publisher:   doc.Publisher,
		PublishYear: doc.ReleaseYear,
		Language:    doc.Language,
	}
	if len(doc.AuthorNames) > 0 {
		meta.Author = strings.Join(doc.AuthorNames, ", ")
	}
	// Narrator: Hardcover exposes it via contributor_names. That's
	// the list of every credited contributor (co-authors, narrators,
	// translators, illustrators) without role labels in the search
	// index, so we take the first name that isn't already in
	// AuthorNames as the probable narrator. Imperfect but better than
	// dropping it entirely.
	if len(doc.ContributorNames) > 0 {
		authorSet := make(map[string]struct{}, len(doc.AuthorNames))
		for _, a := range doc.AuthorNames {
			authorSet[strings.ToLower(strings.TrimSpace(a))] = struct{}{}
		}
		var candidates []string
		for _, name := range doc.ContributorNames {
			key := strings.ToLower(strings.TrimSpace(name))
			if key == "" {
				continue
			}
			if _, dup := authorSet[key]; dup {
				continue
			}
			candidates = append(candidates, name)
		}
		if len(candidates) > 0 {
			meta.Narrator = strings.Join(candidates, ", ")
		}
	}
	if doc.Image != nil && doc.Image.URL != "" {
		meta.CoverURL = doc.Image.URL
	}
	// ISBN: prefer 13 over 10 when both exist. We record the first
	// one we see since the order within hardcover's isbns array is
	// not guaranteed.
	for _, isbn := range doc.ISBNs {
		if len(isbn) == 13 {
			meta.ISBN = isbn
			break
		}
	}
	if meta.ISBN == "" && len(doc.ISBNs) > 0 {
		meta.ISBN = doc.ISBNs[0]
	}
	// Series: prefer featured_series (name + position) over
	// series_names (just names, no position).
	if doc.FeaturedSeries != nil && doc.FeaturedSeries.SeriesName != "" {
		meta.Series = doc.FeaturedSeries.SeriesName
		if doc.FeaturedSeries.Position > 0 {
			if doc.FeaturedSeries.Position == float64(int(doc.FeaturedSeries.Position)) {
				meta.SeriesPosition = fmt.Sprintf("%d", int(doc.FeaturedSeries.Position))
			} else {
				meta.SeriesPosition = fmt.Sprintf("%.2f", doc.FeaturedSeries.Position)
			}
		}
	} else if len(doc.SeriesNames) > 0 {
		meta.Series = doc.SeriesNames[0]
	}
	// Genre: Hardcover returns a list; join for storage.
	if len(doc.Genres) > 0 {
		meta.Genre = strings.Join(doc.Genres, ", ")
	}
	return meta
}
