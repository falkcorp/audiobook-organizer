// file: internal/metadata/audible.go
// version: 1.6.0
// guid: a9b8c7d6-e5f4-3a2b-1c0d-9e8f7a6b5c4d
// last-edited: 2026-07-13

package metadata

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// AudibleClient fetches audiobook metadata from Audible's undocumented catalog API.
// No authentication required. Rate limit: ~1 req/sec recommended.
type AudibleClient struct {
	httpClient *http.Client
	baseURL    string
}

// NewAudibleClient creates a new Audible API client.
func NewAudibleClient() *AudibleClient {
	baseURL := os.Getenv("AUDIBLE_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.audible.com/1.0"
	}
	return &AudibleClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

// NewAudibleClientWithBaseURL creates a client with a custom base URL (for testing).
func NewAudibleClientWithBaseURL(baseURL string) *AudibleClient {
	return &AudibleClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

// Name returns the display name for this metadata source.
func (c *AudibleClient) Name() string {
	return "Audible"
}

// Audible API response types

type audibleCatalogResponse struct {
	Products    []audibleProduct `json:"products"`
	TotalResult int              `json:"total_results"`
}

type audibleProductResponse struct {
	Product audibleProduct `json:"product"`
}

type audibleProduct struct {
	ASIN                 string                  `json:"asin"`
	Title                string                  `json:"title"`
	Subtitle             string                  `json:"subtitle"`
	Authors              []audiblePerson         `json:"authors"`
	Narrators            []audiblePerson         `json:"narrators"`
	PublisherName        string                  `json:"publisher_name"`
	Language             string                  `json:"language"`
	IssueDate            string                  `json:"issue_date"`
	ReleaseDate          string                  `json:"release_date"`
	FormatType           string                  `json:"format_type"`
	MerchandisingSummary string                  `json:"merchandising_summary"`
	ProductImages        map[string]string       `json:"product_images"`
	Series               []audibleSeries         `json:"series"`
	ContentDeliveryType  string                  `json:"content_delivery_type"`
	RuntimeLengthMin     *int                    `json:"runtime_length_min"` // nullable in API
	Rating               *audibleRating          `json:"rating"`
	CategoryLadders      []audibleCategoryLadder `json:"category_ladders"`
}

type audibleRating struct {
	NumReviews              int                       `json:"num_reviews"`
	OverallDistribution     audibleRatingDistribution `json:"overall_distribution"`
	PerformanceDistribution audibleRatingDistribution `json:"performance_distribution"`
	StoryDistribution       audibleRatingDistribution `json:"story_distribution"`
}

// flexFloat64 handles Audible API responses where numeric fields arrive as
// either a JSON number (4.5) or a quoted string ("4.5"). encoding/json/v2
// is strict about type mismatches so we implement the v1-compatible interface
// (UnmarshalJSON([]byte)) which v2 also recognizes.
type flexFloat64 float64

func (f *flexFloat64) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '"' {
		// Quoted string — strip quotes and parse the number inside
		s := string(data[1 : len(data)-1])
		if s == "" {
			return nil
		}
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("flexFloat64: cannot parse %q: %w", s, err)
		}
		*f = flexFloat64(n)
		return nil
	}
	n, err := strconv.ParseFloat(string(data), 64)
	if err != nil {
		return fmt.Errorf("flexFloat64: cannot parse number %q: %w", data, err)
	}
	*f = flexFloat64(n)
	return nil
}

type audibleRatingDistribution struct {
	DisplayAverageRating flexFloat64 `json:"display_average_rating"`
	NumRatings           int         `json:"num_ratings"`
}

type audiblePerson struct {
	ASIN string `json:"asin"`
	Name string `json:"name"`
}

type audibleSeries struct {
	ASIN     string `json:"asin"`
	Title    string `json:"title"`
	Sequence string `json:"sequence"`
}

type audibleCategoryNode struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type audibleCategoryLadder struct {
	Ladder []audibleCategoryNode `json:"ladder"`
	Root   string                `json:"root"`
}

const audibleResponseGroups = "product_desc,contributors,media,product_attrs,series,rating,category_ladders"

// SearchByTitle searches Audible's catalog by title.
func (c *AudibleClient) SearchByTitle(ctx context.Context, title string) ([]BookMetadata, error) {
	searchURL := fmt.Sprintf("%s/catalog/products?title=%s&num_results=10&products_sort_by=Relevance&response_groups=%s",
		c.baseURL, url.QueryEscape(title), audibleResponseGroups)
	return c.searchCatalog(ctx, searchURL)
}

// SearchByTitleAndAuthor searches Audible's catalog by title and author.
func (c *AudibleClient) SearchByTitleAndAuthor(ctx context.Context, title, author string) ([]BookMetadata, error) {
	searchURL := fmt.Sprintf("%s/catalog/products?title=%s&author=%s&num_results=10&products_sort_by=Relevance&response_groups=%s",
		c.baseURL, url.QueryEscape(title), url.QueryEscape(author), audibleResponseGroups)
	return c.searchCatalog(ctx, searchURL)
}

// LookupByASIN fetches a product directly by ASIN.
func (c *AudibleClient) LookupByASIN(asin string) (*BookMetadata, error) {
	productURL := fmt.Sprintf("%s/catalog/products/%s?response_groups=%s",
		c.baseURL, url.PathEscape(asin), audibleResponseGroups)

	req, err := http.NewRequest("GET", productURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create Audible request: %w", err)
	}
	req.Header.Set("User-Agent", "Audible/3.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to query Audible API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("audible API returned status %d for ASIN %s", resp.StatusCode, asin)
	}

	var result audibleProductResponse
	if err := json.UnmarshalRead(resp.Body, &result, json.DiscardUnknownMembers(true)); err != nil {
		return nil, fmt.Errorf("failed to decode Audible response: %w", err)
	}

	if result.Product.Title == "" {
		return nil, fmt.Errorf("audible returned empty product for ASIN %s", asin)
	}

	meta := c.productToMetadata(&result.Product)
	return &meta, nil
}

func (c *AudibleClient) searchCatalog(ctx context.Context, searchURL string) ([]BookMetadata, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create Audible request: %w", err)
	}
	req.Header.Set("User-Agent", "Audible/3.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to query Audible API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("audible API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read Audible response: %w", err)
	}
	var catalog audibleCatalogResponse
	if err := json.Unmarshal(body, &catalog, json.DiscardUnknownMembers(true)); err != nil {
		return nil, fmt.Errorf("failed to decode Audible response: %w", err)
	}

	// Demote the per-query result-count line. With background batchers
	// running it floods logs and the count alone is meaningless without
	// the query string the caller used. Callers that care about the
	// count log it themselves with the query context.
	if len(catalog.Products) == 0 {
		slog.Debug("audible searchCatalog returned 0 products for", "searchURL", searchURL)
	} else {
		slog.Debug("audible searchCatalog returned products for", "count", len(catalog.Products), "searchURL", searchURL)
	}

	results := make([]BookMetadata, 0, len(catalog.Products))
	for _, p := range catalog.Products {
		results = append(results, c.productToMetadata(&p))
	}
	return results, nil
}

func (c *AudibleClient) productToMetadata(p *audibleProduct) BookMetadata {
	meta := BookMetadata{
		Title:     p.Title,
		Publisher: p.PublisherName,
		Language:  p.Language,
		ASIN:      p.ASIN,
	}

	// Strip HTML from merchandising summary
	if p.MerchandisingSummary != "" {
		desc := p.MerchandisingSummary
		// Simple HTML tag stripping
		for strings.Contains(desc, "<") {
			start := strings.Index(desc, "<")
			end := strings.Index(desc, ">")
			if end > start {
				desc = desc[:start] + desc[end+1:]
			} else {
				break
			}
		}
		meta.Description = strings.TrimSpace(desc)
	}

	// Authors
	authorNames := make([]string, 0, len(p.Authors))
	for _, a := range p.Authors {
		authorNames = append(authorNames, a.Name)
	}
	if len(authorNames) > 0 {
		meta.Author = strings.Join(authorNames, ", ")
	}

	// Narrators
	narratorNames := make([]string, 0, len(p.Narrators))
	for _, n := range p.Narrators {
		narratorNames = append(narratorNames, n.Name)
	}
	if len(narratorNames) > 0 {
		meta.Narrator = strings.Join(narratorNames, ", ")
	}

	// Year from issue_date or release_date — this is the AUDIOBOOK release year,
	// not the work's original print year. Flag it so apply routes it to
	// Book.AudiobookReleaseYear (never PrintYear).
	dateStr := p.IssueDate
	if dateStr == "" {
		dateStr = p.ReleaseDate
	}
	if len(dateStr) >= 4 {
		fmt.Sscanf(dateStr[:4], "%d", &meta.PublishYear)
	}
	meta.PublishYearIsAudiobookRelease = true

	// Cover image — prefer largest available
	for _, size := range []string{"500", "252", "128"} {
		if imgURL, ok := p.ProductImages[size]; ok && imgURL != "" {
			meta.CoverURL = imgURL
			break
		}
	}

	// Series
	if len(p.Series) > 0 {
		meta.Series = p.Series[0].Title
		meta.SeriesPosition = p.Series[0].Sequence
	}

	// Runtime: Audible returns minutes; BookMetadata stores seconds.
	if p.RuntimeLengthMin != nil && *p.RuntimeLengthMin > 0 {
		meta.DurationSec = *p.RuntimeLengthMin * 60
	}

	// Ratings: overall, narrator performance, story quality.
	if p.Rating != nil {
		meta.AudibleRatingOverall = float64(p.Rating.OverallDistribution.DisplayAverageRating)
		meta.AudibleRatingPerformance = float64(p.Rating.PerformanceDistribution.DisplayAverageRating)
		meta.AudibleRatingStory = float64(p.Rating.StoryDistribution.DisplayAverageRating)
		meta.AudibleRatingCount = p.Rating.OverallDistribution.NumRatings
		meta.AudibleNumReviews = p.Rating.NumReviews
	}

	// Category ladders: collect all node names from all ladders, deduplicate.
	// Each ladder is a path from broad to specific (e.g. "Science Fiction" →
	// "Space Opera"). The Root field is a navigation bucket, not a genre — skip it.
	if len(p.CategoryLadders) > 0 {
		seen := map[string]struct{}{}
		tags := make([]string, 0)
		for _, ladder := range p.CategoryLadders {
			for _, node := range ladder.Ladder {
				name := strings.TrimSpace(node.Name)
				if name == "" {
					continue
				}
				if _, ok := seen[name]; !ok {
					seen[name] = struct{}{}
					tags = append(tags, name)
				}
			}
		}
		if len(tags) > 0 {
			meta.CategoryTags = tags
		}
	}

	return meta
}
