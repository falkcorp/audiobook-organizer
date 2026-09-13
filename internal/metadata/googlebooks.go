// file: internal/metadata/googlebooks.go
// version: 1.9.0
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
	PageCount           int                     `json:"pageCount"`
	PrintedPageCount    int                     `json:"printedPageCount"`
	Categories          []string                `json:"categories"`
	MainCategory        string                  `json:"mainCategory"`
}

type googleBooksIndustryID struct {
	Type       string `json:"type"`
	Identifier string `json:"identifier"`
}

// googleBooksImageLinks is volumeInfo.imageLinks. Search results usually carry
// only the two thumbnails; the larger sizes appear when Google has them.
type googleBooksImageLinks struct {
	ExtraLarge     string `json:"extraLarge"`
	Large          string `json:"large"`
	Medium         string `json:"medium"`
	Small          string `json:"small"`
	Thumbnail      string `json:"thumbnail"`
	SmallThumbnail string `json:"smallThumbnail"`
}

// largest returns the largest image Google offered, upgraded to https, or "".
func (l *googleBooksImageLinks) largest() string {
	if l == nil {
		return ""
	}
	for _, u := range []string{l.ExtraLarge, l.Large, l.Medium, l.Small, l.Thumbnail, l.SmallThumbnail} {
		if u = strings.TrimSpace(u); u != "" {
			return strings.Replace(u, "http://", "https://", 1)
		}
	}
	return ""
}

// googleBooksCategories returns the categories de-duplicated in order, with
// mainCategory first when present. Google's categories are BISAC-style
// strings ("Fiction / Science Fiction / Space Opera", or just "Fiction").
func googleBooksCategories(main string, cats []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range append([]string{main}, cats...) {
		c = strings.TrimSpace(c)
		if c == "" || seen[strings.ToLower(c)] {
			continue
		}
		seen[strings.ToLower(c)] = true
		out = append(out, c)
	}
	return out
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
		// Google's authors list is plain strings, but a credit can carry its role
		// in the text ("Jane Doe (Editor)"). Only authors go in the author
		// string; a narrator goes to Narrator; other roles are dropped.
		authors, narrators := PartitionCredits(vi.Authors)
		if len(authors) > 0 {
			meta.Author = strings.Join(authors, ", ")
		}
		if len(narrators) > 0 {
			meta.Narrator = strings.Join(narrators, ", ")
		}
		// printedPageCount is the physical book; pageCount can be the scanned
		// ebook's page count. Prefer the print figure.
		meta.PageCount = vi.PrintedPageCount
		if meta.PageCount <= 0 {
			meta.PageCount = vi.PageCount
		}
		if cats := googleBooksCategories(vi.MainCategory, vi.Categories); len(cats) > 0 {
			meta.Genre = cats[0]
			meta.CategoryTags = cats
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
		meta.CoverURL = vi.ImageLinks.largest()
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
