// file: internal/metadata/googlebooks.go
// version: 1.4.0
// guid: b2c3d4e5-f6a7-8b9c-0d1e-f2a3b4c5d6e7

package metadata

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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
	baseURL := os.Getenv("GOOGLE_BOOKS_BASE_URL")
	if baseURL == "" {
		baseURL = "https://www.googleapis.com/books/v1"
	}
	return &GoogleBooksClient{
		httpClient: providerhttp.Client("googlebooks"),
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
	}
}

// NewGoogleBooksClientWithBaseURL creates a client with a custom base URL (for testing).
func NewGoogleBooksClientWithBaseURL(baseURL string) *GoogleBooksClient {
	return &GoogleBooksClient{
		httpClient: providerhttp.Client("googlebooks"),
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

// Name returns the display name for this metadata source.
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
		return nil, fmt.Errorf("google Books API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read Google Books response: %w", err)
	}
	var gbResp googleBooksResponse
	if err := json.Unmarshal(body, &gbResp, json.DiscardUnknownMembers(true)); err != nil {
		return nil, fmt.Errorf("failed to decode Google Books response: %w", err)
	}

	results := make([]BookMetadata, 0, len(gbResp.Items))
	for _, item := range gbResp.Items {
		vi := item.VolumeInfo
		meta := BookMetadata{
			Title:       vi.Title,
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
			if id.Type == "ISBN_13" {
				meta.ISBN = id.Identifier
			} else if id.Type == "ISBN_10" && meta.ISBN == "" {
				meta.ISBN = id.Identifier
			}
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
