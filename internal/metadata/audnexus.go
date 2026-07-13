// file: internal/metadata/audnexus.go
// version: 2.5.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-a3b4c5d6e7f8
// last-edited: 2026-07-13

package metadata

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// AudnexusClient fetches audiobook metadata from the Audnexus community API,
// which provides Audible-sourced data including narrator information.
// The API requires an ASIN for book lookups — there is no title search endpoint.
// For title-based search, we search authors by name and then look up their books.
type AudnexusClient struct {
	httpClient *http.Client
	baseURL    string
}

// NewAudnexusClient creates a new Audnexus API client.
func NewAudnexusClient() *AudnexusClient {
	baseURL := os.Getenv("AUDNEXUS_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.audnex.us"
	}
	return &AudnexusClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

// NewAudnexusClientWithBaseURL creates a client with a custom base URL (for testing).
func NewAudnexusClientWithBaseURL(baseURL string) *AudnexusClient {
	return &AudnexusClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

// Name returns the display name for this metadata source.
func (c *AudnexusClient) Name() string {
	return "Audnexus (Audible)"
}

// Audnexus API response types matching the OpenAPI spec
type audnexusPerson struct {
	ASIN string `json:"asin"`
	Name string `json:"name"`
}

type audnexusSeries struct {
	ASIN     string `json:"asin"`
	Name     string `json:"name"`
	Position string `json:"position"`
}

type audnexusBook struct {
	ASIN            string           `json:"asin"`
	Title           string           `json:"title"`
	Subtitle        string           `json:"subtitle"`
	Authors         []audnexusPerson `json:"authors"`
	Narrators       []audnexusPerson `json:"narrators"`
	PublisherName   string           `json:"publisherName"`
	ReleaseDate     string           `json:"releaseDate"`
	Language        string           `json:"language"`
	Image           string           `json:"image"`
	Description     string           `json:"description"`
	Summary         string           `json:"summary"`
	ISBN            string           `json:"isbn"`
	Copyright       int              `json:"copyright"`
	SeriesPrimary   *audnexusSeries  `json:"seriesPrimary"`
	SeriesSecondary *audnexusSeries  `json:"seriesSecondary"`
}

type audnexusAuthor struct {
	ASIN        string           `json:"asin"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Image       string           `json:"image"`
	Similar     []audnexusPerson `json:"similar"`
}

// SearchByTitle cannot search Audnexus by title alone (no such endpoint exists).
// Returns empty results so the chain moves to the next source.
// Silent — the chain logs at a higher level which sources it queried.
func (c *AudnexusClient) SearchByTitle(ctx context.Context, title string) ([]BookMetadata, error) {
	return nil, nil
}

// SearchByTitleAndAuthor searches for an author on Audnexus, then looks up
// each author's books to find a title match.
func (c *AudnexusClient) SearchByTitleAndAuthor(ctx context.Context, title, author string) ([]BookMetadata, error) {
	// Step 1: Search authors by name → GET /authors?name={name}
	authorsURL := fmt.Sprintf("%s/authors?name=%s", c.baseURL, url.QueryEscape(author))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authorsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to search Audnexus authors: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("audnexus author search returned status %d", resp.StatusCode)
	}

	var authors []audnexusAuthor
	if err := json.UnmarshalRead(resp.Body, &authors); err != nil {
		return nil, fmt.Errorf("failed to decode Audnexus author response: %w", err)
	}

	if len(authors) == 0 {
		return nil, nil
	}

	// Step 2: For the first matching author, try to look up the book by
	// checking known ASINs. Since Audnexus doesn't list an author's books,
	// we can't enumerate them. Return the author info as partial metadata.
	// In the future, this could be enhanced with an ASIN lookup if we have one.
	// (Silenced — the chain's higher-level "not found" summary is enough.)
	_ = authors
	return nil, nil
}

// SearchByContext implements ContextualSearch interface.
// Audnexus prefers ASIN-based lookups. If an ASIN is available, we look it up.
// Otherwise, return nil to let the chain fall through to the next source.
func (c *AudnexusClient) SearchByContext(ctx *SearchContext) ([]BookMetadata, error) {
	if ctx == nil || ctx.ASIN == "" {
		return nil, nil
	}

	// Look up the book by ASIN. This ContextualSearch entry point carries no
	// Go context.Context (its caller, FetchMetadataForBook, has none to thread),
	// so we use context.Background(); LookupByASIN still bounds each region
	// request with a per-request timeout so a missing/unreachable ASIN can't burn
	// the full 9×30s. Callers that DO hold a cancellable ctx (the candidate op's
	// direct-ASIN path) call LookupByASIN directly with it.
	metadata, err := c.LookupByASIN(context.Background(), ctx.ASIN)
	if err != nil {
		return nil, err
	}
	if metadata == nil {
		return nil, nil
	}
	return []BookMetadata{*metadata}, nil
}

// audnexusRegions are the regions to try when looking up a book by ASIN.
// Some books are only available in certain regional Audible stores.
var audnexusRegions = []string{"", "us", "uk", "au", "ca", "in", "de", "fr", "jp"}

// audnexusPerRegionTimeout bounds a single region request so one slow/unreachable
// regional Audible store can't consume the whole lookup budget. Nine regions ×
// this cap is the worst case for a missing ASIN (was 9×30s = 270s uninterruptible
// before contexts were threaded).
const audnexusPerRegionTimeout = 10 * time.Second

// LookupByASIN fetches a book directly by its Audible ASIN.
// Tries multiple regions since books may only be indexed in certain Audible stores.
//
// The region loop honors ctx: it bails immediately when ctx is cancelled/expired
// (so a batch cancel aborts promptly instead of grinding through all 9 regions),
// and each region request is additionally bounded by audnexusPerRegionTimeout via
// http.NewRequestWithContext so a single hung region can't stall the lookup.
func (c *AudnexusClient) LookupByASIN(ctx context.Context, asin string) (*BookMetadata, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var lastErr error
	for _, region := range audnexusRegions {
		// Bail promptly on cancellation rather than starting another region.
		if err := ctx.Err(); err != nil {
			if lastErr == nil {
				lastErr = err
			}
			return nil, lastErr
		}
		meta, done, err := c.lookupRegion(ctx, asin, region)
		if done {
			return meta, nil
		}
		if err != nil {
			// Propagate a context error immediately — no other region will
			// fare better once the caller has cancelled/timed out.
			if ctx.Err() != nil {
				return nil, err
			}
			lastErr = err
		}
	}
	return nil, lastErr
}

// lookupRegion issues a single-region ASIN lookup bounded by
// audnexusPerRegionTimeout. Returns done=true with the metadata on a hit;
// done=false with an error to try the next region. Kept as its own function so
// the per-request context cancel is deferred cleanly (vet-safe) rather than
// juggling cancel() across the loop's multiple continue paths.
func (c *AudnexusClient) lookupRegion(ctx context.Context, asin, region string) (*BookMetadata, bool, error) {
	reqCtx, cancel := context.WithTimeout(ctx, audnexusPerRegionTimeout)
	defer cancel()

	bookURL := fmt.Sprintf("%s/books/%s", c.baseURL, url.PathEscape(asin))
	if region != "" {
		bookURL += "?region=" + region
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, bookURL, nil)
	if err != nil {
		return nil, false, fmt.Errorf("failed to create Audnexus request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("failed to lookup Audnexus book: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var book audnexusBook
		if err := json.UnmarshalRead(resp.Body, &book); err != nil {
			return nil, false, fmt.Errorf("failed to decode Audnexus book: %w", err)
		}
		slog.Debug("Audnexus found ASIN in region", "asin", asin, "region", region)
		return c.bookToMetadata(&book), true, nil
	}
	return nil, false, fmt.Errorf("audnexus book lookup returned status %d (region=%s)", resp.StatusCode, region)
}

func (c *AudnexusClient) bookToMetadata(book *audnexusBook) *BookMetadata {
	meta := &BookMetadata{
		Title:     book.Title,
		Publisher: book.PublisherName,
		Language:  book.Language,
		CoverURL:  book.Image,
		ISBN:      book.ISBN,
		ASIN:      book.ASIN,
	}

	// Use summary or description
	if book.Summary != "" {
		meta.Description = book.Summary
	} else if book.Description != "" {
		meta.Description = book.Description
	}

	// Authors
	authorNames := make([]string, 0, len(book.Authors))
	for _, a := range book.Authors {
		authorNames = append(authorNames, a.Name)
	}
	if len(authorNames) > 0 {
		meta.Author = strings.Join(authorNames, ", ")
	}

	// Narrators
	narratorNames := make([]string, 0, len(book.Narrators))
	for _, n := range book.Narrators {
		narratorNames = append(narratorNames, n.Name)
	}
	if len(narratorNames) > 0 {
		meta.Narrator = strings.Join(narratorNames, ", ")
	}

	// Year from releaseDate or copyright — this is the AUDIOBOOK release year,
	// not the work's original print year. Flag it so apply routes it to
	// Book.AudiobookReleaseYear (never PrintYear).
	if len(book.ReleaseDate) >= 4 {
		fmt.Sscanf(book.ReleaseDate[:4], "%d", &meta.PublishYear)
	} else if book.Copyright > 0 {
		meta.PublishYear = book.Copyright
	}
	meta.PublishYearIsAudiobookRelease = true

	// Series
	if book.SeriesPrimary != nil {
		meta.Series = book.SeriesPrimary.Name
		meta.SeriesPosition = book.SeriesPrimary.Position
	}

	return meta
}
