// file: internal/metadata/audnexus.go
// version: 2.13.1
// guid: c3d4e5f6-a7b8-9c0d-1e2f-a3b4c5d6e7f8
// last-edited: 2026-11-16

package metadata

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/metadata/providerhttp"
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
	baseURL := resolveBaseURL("audnexus", "https://api.audnex.us")
	return &AudnexusClient{
		httpClient: providerhttp.Client(SourceIDAudnexus),
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

// Name returns the display name for this metadata source.
// ProviderID is the canonical id shared by config, the request-budget
// table and this client. Name() is a display string and must not be used
// as a key.
func (c *AudnexusClient) ProviderID() string { return SourceIDAudnexus }

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
	ASIN             string           `json:"asin"`
	Title            string           `json:"title"`
	Subtitle         string           `json:"subtitle"`
	Authors          []audnexusPerson `json:"authors"`
	Narrators        []audnexusPerson `json:"narrators"`
	PublisherName    string           `json:"publisherName"`
	ReleaseDate      string           `json:"releaseDate"`
	Language         string           `json:"language"`
	Image            string           `json:"image"`
	Description      string           `json:"description"`
	Summary          string           `json:"summary"`
	ISBN             string           `json:"isbn"`
	Copyright        int              `json:"copyright"`
	RuntimeLengthMin *int             `json:"runtimeLengthMin"` // nullable; audio runtime in minutes
	SeriesPrimary    *audnexusSeries  `json:"seriesPrimary"`
	SeriesSecondary  *audnexusSeries  `json:"seriesSecondary"`
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
		return nil, StatusError(SourceIDAudnexus, resp)
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
func (c *AudnexusClient) SearchByContext(ctx context.Context, sc *SearchContext) ([]BookMetadata, error) {
	if sc == nil || sc.ASIN == "" {
		return nil, nil
	}

	// Look up the book by ASIN, threading the caller's real context so a
	// batch/import cancel aborts the 9-region loop promptly (LookupByASIN also
	// bounds each region request with a per-request timeout). This entry point
	// used to get context.Background() because FetchMetadataForBook had no ctx to
	// thread — that ctx is now plumbed end-to-end.
	//
	// A value that is not an ASIN is nothing to ask, not a failure: answering
	// it as an error made the source chain record Audnexus as failing (and its
	// breaker count it) for a book whose "ASIN" was a word.
	metadata, err := c.LookupByASIN(ctx, sc.ASIN)
	if err != nil {
		if IsBadASIN(err) {
			return nil, nil
		}
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

// The per-request time bound of a region lookup is NOT set here. It used to be a
// 10 s context deadline created before the request was handed to the transport,
// so it ran while the request waited its turn on the shared token bucket
// (providerhttp, 2 rps): 20 workers queued on that bucket wait 20/2 = 10 s, the
// whole budget. That is the INFERRED cause of the 482 of 814 lookups in the
// 2026-11-16 candidate-fetch run that died with a bare "context deadline
// exceeded" (the arithmetic fits and nothing in the logs contradicts it; the
// per-lookup wait was not measured), on requests Audnexus answers in 0.3 s when
// asked on its own.
// providerhttp.Limits.AttemptTimeout bounds the exchange itself and starts only
// once the request holds its token, so a long queue no longer fails a lookup.

// LookupByASIN fetches a book directly by its Audible ASIN.
// Tries multiple regions since books may only be indexed in certain Audible stores.
//
// The region loop honors ctx: it bails immediately when ctx is cancelled/expired
// (so a batch cancel aborts promptly instead of grinding through all 9 regions),
// and each region request is bounded by the transport's attempt timeout so a
// single hung region can't stall the lookup.
//
// A value outside the ASIN grammar is refused with ErrBadASIN before any request
// is sent (asin.go): Audnexus would answer every region 400.
func (c *AudnexusClient) LookupByASIN(ctx context.Context, asin string) (*BookMetadata, error) {
	return c.LookupByASINInRegions(ctx, asin, audnexusRegions)
}

// LookupByASINInRegions is LookupByASIN over only the given regions ("" is
// the default store), at most one request each. The metadata search uses it
// to bound its one Audnexus lookup per book to a known request count.
func (c *AudnexusClient) LookupByASINInRegions(ctx context.Context, asin string, regions []string) (*BookMetadata, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	canonical, ok := CanonicalASIN(asin)
	if !ok {
		return nil, fmt.Errorf("audnexus lookup not sent: %w", ErrBadASIN)
	}
	asin = canonical
	// A region that failed (5xx, 429, transport) outranks one that answered
	// "not here" (IsNotFound): the ASIN may live in the failed region, so the
	// lookup as a whole failed. Only when every region said "not here" is the
	// result a not-found error.
	var failed, notFound error
	for _, region := range regions {
		// Bail promptly on cancellation rather than starting another region.
		if err := ctx.Err(); err != nil {
			if failed == nil {
				failed = err
			}
			return nil, failed
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
			// A refusal of the ASIN itself is the same in every region: stop.
			// Trying the rest sent 3 requests per bad ASIN (9 on the
			// full list) for an answer that was final after the first.
			if IsBadASIN(err) {
				return nil, err
			}
			if IsNotFound(err) {
				notFound = err
			} else {
				failed = err
			}
		}
	}
	if failed != nil {
		return nil, failed
	}
	return nil, notFound
}

// lookupRegion issues a single-region ASIN lookup. Returns done=true with the
// metadata on a hit; done=false with an error to try the next region. The
// request's time bound is the transport's attempt timeout (see above), not a
// deadline set here.
func (c *AudnexusClient) lookupRegion(ctx context.Context, asin, region string) (*BookMetadata, bool, error) {
	bookURL := fmt.Sprintf("%s/books/%s", c.baseURL, url.PathEscape(asin))
	if region != "" {
		bookURL += "?region=" + region
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, bookURL, nil)
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
	return nil, false, fmt.Errorf("region=%s: %w", region, StatusError(SourceIDAudnexus, resp))
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
	if book.SeriesSecondary != nil {
		meta.SeriesSecondary = book.SeriesSecondary.Name
		meta.SeriesSecondaryPosition = book.SeriesSecondary.Position
	}

	// Runtime: Audnexus returns minutes; BookMetadata stores seconds. Audnexus
	// previously contributed no duration even though the DurationSec sink exists.
	if book.RuntimeLengthMin != nil && *book.RuntimeLengthMin > 0 {
		meta.DurationSec = *book.RuntimeLengthMin * 60
	}

	if book.Subtitle != "" {
		meta.Subtitle = book.Subtitle
	}

	return meta
}
