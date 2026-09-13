// file: internal/metadata/googlebooks.go
// version: 1.8.0
// guid: b2c3d4e5-f6a7-8b9c-0d1e-f2a3b4c5d6e7
// last-edited: 2026-09-13

package metadata

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/metadata/providerhttp"
)

// GoogleBooksClient fetches metadata from the Google Books Volume API.
// An API key raises the quota from ~100 req/day to 1000 req/day.
type GoogleBooksClient struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
}

// NewGoogleBooksClient creates a new Google Books API client.
func NewGoogleBooksClient(apiKey string) *GoogleBooksClient {
	baseURL := resolveBaseURL("google-books", "https://www.googleapis.com/books/v1")
	return &GoogleBooksClient{
		httpClient: providerhttp.Client(SourceIDGoogleBooks),
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
	}
}

// NewGoogleBooksClientWithBaseURL creates a client with a custom base URL (for testing).
func NewGoogleBooksClientWithBaseURL(baseURL string) *GoogleBooksClient {
	return &GoogleBooksClient{
		httpClient: providerhttp.Client(SourceIDGoogleBooks),
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

// Name returns the display name for this metadata source.
// ProviderID is the canonical id shared by config, the request-budget
// table and this client. Name() is a display string and must not be used
// as a key.
func (c *GoogleBooksClient) ProviderID() string { return SourceIDGoogleBooks }

func (c *GoogleBooksClient) Name() string {
	return "Google Books"
}

type googleBooksResponse struct {
	TotalItems int              `json:"totalItems"`
	Items      []googleBooksVol `json:"items"`
}

type googleBooksVol struct {
	VolumeInfo googleBooksVolumeInfo `json:"volumeInfo"`
}

type googleBooksVolumeInfo struct {
	Title               string                  `json:"title"`
	Subtitle            string                  `json:"subtitle"`
	Authors             []string                `json:"authors"`
	Publisher           string                  `json:"publisher"`
	PublishedDate       string                  `json:"publishedDate"`
	Description         string                  `json:"description"`
	IndustryIdentifiers []googleBooksIndustryID `json:"industryIdentifiers"`
	ImageLinks          *googleBooksImageLinks  `json:"imageLinks"`
	Language            string                  `json:"language"`
	AverageRating       float64                 `json:"averageRating"`
	RatingsCount        int                     `json:"ratingsCount"`
}

type googleBooksIndustryID struct {
	Type       string `json:"type"`
	Identifier string `json:"identifier"`
}

type googleBooksImageLinks struct {
	Thumbnail      string `json:"thumbnail"`
	SmallThumbnail string `json:"smallThumbnail"`
}

// SearchByTitle searches Google Books by title.
func (c *GoogleBooksClient) SearchByTitle(ctx context.Context, title string) ([]BookMetadata, error) {
	q := url.QueryEscape(fmt.Sprintf("intitle:%s", title))
	return c.search(ctx, q)
}

// SearchByTitleAndAuthor searches Google Books by title and author.
func (c *GoogleBooksClient) SearchByTitleAndAuthor(ctx context.Context, title, author string) ([]BookMetadata, error) {
	q := url.QueryEscape(fmt.Sprintf("intitle:%s+inauthor:%s", title, author))
	return c.search(ctx, q)
}

func (c *GoogleBooksClient) search(ctx context.Context, escapedQuery string) ([]BookMetadata, error) {
	searchURL := fmt.Sprintf("%s/volumes?q=%s&maxResults=5", c.baseURL, escapedQuery)
	if c.apiKey != "" {
		searchURL += "&key=" + url.QueryEscape(c.apiKey)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to search Google Books: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, StatusError(SourceIDGoogleBooks, resp)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read Google Books response: %w", err)
	}
	var gbResp googleBooksResponse
	if err := json.Unmarshal(body, &gbResp); err != nil {
		return nil, fmt.Errorf("failed to decode Google Books response: %w", err)
	}

	results := make([]BookMetadata, 0, len(gbResp.Items))
	for _, item := range gbResp.Items {
		vi := item.VolumeInfo
		meta := BookMetadata{
			Title:       googleBooksFullTitle(vi.Title, vi.Subtitle),
			Subtitle:    strings.TrimSpace(vi.Subtitle),
			Publisher:   vi.Publisher,
			Description: vi.Description,
			Language:    vi.Language,
		}
		if len(vi.Authors) > 0 {
			meta.Author = strings.Join(vi.Authors, ", ")
		}
		if len(vi.PublishedDate) >= 4 {
			fmt.Sscanf(vi.PublishedDate, "%d", &meta.PublishYear)
		}
		for _, id := range vi.IndustryIdentifiers {
			switch id.Type {
			case "ISBN_13":
				if meta.ISBN13 == "" {
					meta.ISBN13 = id.Identifier
				}
			case "ISBN_10":
				if meta.ISBN10 == "" {
					meta.ISBN10 = id.Identifier
				}
			}
		}
		// Single ISBN kept for back-compat: prefer 13, then 10.
		switch {
		case meta.ISBN13 != "":
			meta.ISBN = meta.ISBN13
		case meta.ISBN10 != "":
			meta.ISBN = meta.ISBN10
		}
		if vi.ImageLinks != nil && vi.ImageLinks.Thumbnail != "" {
			meta.CoverURL = strings.Replace(vi.ImageLinks.Thumbnail, "http://", "https://", 1)
		}
		if vi.AverageRating > 0 {
			meta.GoogleRatingAverage = vi.AverageRating
			meta.GoogleRatingCount = vi.RatingsCount
		}
		results = append(results, meta)
	}
	return results, nil
}

// googleBooksFullTitle joins Google's title and subtitle into the single
// "Title: Subtitle" form the rest of the pipeline treats as canonical
// (metafetch's stripSubtitle and titleSegment both split on ": ").
//
// Google Books often files a franchise or series name as the title and the
// book's real title as the subtitle: "A New Dawn" comes back as title
// "Star Wars", subtitle "A New Dawn". Using the bare title made the candidate
// indistinguishable from every other book in the franchise, and scoring
// compared "Star Wars" against the book's real title. Audible and Audnexus
// keep Title as-is because their title field IS the book title; Google's is
// not reliably, so the full form is the only safe title to score and display.
// The raw subtitle is still carried separately in BookMetadata.Subtitle.
func googleBooksFullTitle(title, subtitle string) string {
	title = strings.TrimSpace(title)
	subtitle = strings.TrimSpace(subtitle)
	switch {
	case subtitle == "":
		return title
	case title == "":
		return subtitle
	case strings.Contains(strings.ToLower(title), strings.ToLower(subtitle)):
		// Already baked in (or identical): avoid "X: Y: Y".
		return title
	}
	return title + ": " + subtitle
}
