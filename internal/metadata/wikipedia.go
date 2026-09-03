// file: internal/metadata/wikipedia.go
// version: 1.3.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f

package metadata

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/metadata/providerhttp"
)

// WikipediaClient fetches metadata from the MediaWiki API and Wikidata.
// It serves as a last-resort metadata source for obscure titles.
type WikipediaClient struct {
	httpClient  *http.Client
	baseURL     string
	wikidataURL string
}

// NewWikipediaClient creates a new Wikipedia/Wikidata metadata client.
func NewWikipediaClient() *WikipediaClient {
	return &WikipediaClient{
		httpClient:  providerhttp.Client(SourceIDWikipedia),
		baseURL:     "https://en.wikipedia.org/w/api.php",
		wikidataURL: "https://www.wikidata.org/w/api.php",
	}
}

// NewWikipediaClientWithBaseURL creates a client with custom URLs (for testing).
func NewWikipediaClientWithBaseURL(baseURL, wikidataURL string) *WikipediaClient {
	return &WikipediaClient{
		httpClient:  providerhttp.Client(SourceIDWikipedia),
		baseURL:     strings.TrimRight(baseURL, "/"),
		wikidataURL: strings.TrimRight(wikidataURL, "/"),
	}
}

// Name returns the display name for this metadata source.
// ProviderID is the canonical id shared by config, the request-budget
// table and this client. Name() is a display string and must not be used
// as a key.
func (c *WikipediaClient) ProviderID() string { return SourceIDWikipedia }

func (c *WikipediaClient) Name() string {
	return "Wikipedia"
}

// mediawikiSearchResponse represents the MediaWiki API search response.
type mediawikiSearchResponse struct {
	Query struct {
		Search []mediawikiSearchResult `json:"search"`
	} `json:"query"`
}

type mediawikiSearchResult struct {
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
	PageID  int    `json:"pageid"`
}

// wikidataSearchResponse represents the Wikidata entity search response.
type wikidataSearchResponse struct {
	Search []wikidataSearchResult `json:"search"`
}

type wikidataSearchResult struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// wikidataEntityResponse represents a Wikidata entity response.
type wikidataEntityResponse struct {
	Entities map[string]wikidataEntity `json:"entities"`
}

type wikidataEntity struct {
	Claims map[string][]wikidataClaim `json:"claims"`
}

type wikidataClaim struct {
	MainSnak wikidataSnak `json:"mainsnak"`
}

type wikidataSnak struct {
	DataValue *wikidataDataValue `json:"datavalue"`
}

type wikidataDataValue struct {
	Type  string         `json:"type"`
	Value jsontext.Value `json:"value"`
}

type wikidataTimeValue struct {
	Time string `json:"time"`
}

// SearchByTitle searches Wikipedia by title, appending "audiobook OR novel" to improve results.
func (c *WikipediaClient) SearchByTitle(ctx context.Context, title string) ([]BookMetadata, error) {
	query := title + " audiobook OR novel"
	return c.search(ctx, query)
}

// SearchByTitleAndAuthor searches Wikipedia by title and author.
func (c *WikipediaClient) SearchByTitleAndAuthor(ctx context.Context, title, author string) ([]BookMetadata, error) {
	query := title + " " + author
	return c.search(ctx, query)
}

func (c *WikipediaClient) search(ctx context.Context, query string) ([]BookMetadata, error) {
	searchURL := fmt.Sprintf("%s?action=query&list=search&srsearch=%s&format=json&srlimit=5",
		c.baseURL, url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to search Wikipedia: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, StatusError(SourceIDWikipedia, resp)
	}

	var mwResp mediawikiSearchResponse
	if err := json.UnmarshalRead(resp.Body, &mwResp); err != nil {
		return nil, fmt.Errorf("failed to decode Wikipedia response: %w", err)
	}

	results := make([]BookMetadata, 0, len(mwResp.Query.Search))
	for _, item := range mwResp.Query.Search {
		meta := BookMetadata{
			Title: item.Title,
		}

		// Best-effort Wikidata enrichment
		c.enrichFromWikidata(ctx, &meta, item.Title)

		results = append(results, meta)
	}
	return results, nil
}

// enrichFromWikidata attempts to find a Wikidata entity for the given title
// and extract author (P50), publication date (P577), and ISBN-13 (P212).
func (c *WikipediaClient) enrichFromWikidata(ctx context.Context, meta *BookMetadata, title string) {
	// Search for entity
	searchURL := fmt.Sprintf("%s?action=wbsearchentities&search=%s&language=en&format=json&limit=1",
		c.wikidataURL, url.QueryEscape(title))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return
	}

	var wdSearch wikidataSearchResponse
	if err := json.UnmarshalRead(resp.Body, &wdSearch); err != nil || len(wdSearch.Search) == 0 {
		return
	}

	entityID := wdSearch.Search[0].ID

	// Fetch entity claims
	entityURL := fmt.Sprintf("%s?action=wbgetentities&ids=%s&props=claims&format=json",
		c.wikidataURL, entityID)

	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, entityURL, nil)
	if err != nil {
		return
	}

	resp2, err := c.httpClient.Do(req2)
	if err != nil {
		return
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		return
	}

	var entityResp wikidataEntityResponse
	if err := json.UnmarshalRead(resp2.Body, &entityResp); err != nil {
		return
	}

	entity, ok := entityResp.Entities[entityID]
	if !ok {
		return
	}

	// P50 = author
	if claims, ok := entity.Claims["P50"]; ok && len(claims) > 0 {
		if claims[0].MainSnak.DataValue != nil && claims[0].MainSnak.DataValue.Type == "wikibase-entityid" {
			var val struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(claims[0].MainSnak.DataValue.Value, &val) == nil && val.ID != "" {
				// Resolve author entity label
				if label := c.resolveEntityLabel(ctx, val.ID); label != "" {
					meta.Author = label
				}
			}
		}
	}

	// P577 = publication date
	if claims, ok := entity.Claims["P577"]; ok && len(claims) > 0 {
		if claims[0].MainSnak.DataValue != nil && claims[0].MainSnak.DataValue.Type == "time" {
			var tv wikidataTimeValue
			if json.Unmarshal(claims[0].MainSnak.DataValue.Value, &tv) == nil && len(tv.Time) >= 5 {
				// Time format: "+YYYY-MM-DDT00:00:00Z"
				fmt.Sscanf(tv.Time[1:5], "%d", &meta.PublishYear)
			}
		}
	}

	// P212 = ISBN-13
	if claims, ok := entity.Claims["P212"]; ok && len(claims) > 0 {
		if claims[0].MainSnak.DataValue != nil && claims[0].MainSnak.DataValue.Type == "string" {
			var isbn string
			if json.Unmarshal(claims[0].MainSnak.DataValue.Value, &isbn) == nil {
				meta.ISBN = isbn
			}
		}
	}
}

// resolveEntityLabel fetches the English label for a Wikidata entity ID.
func (c *WikipediaClient) resolveEntityLabel(ctx context.Context, entityID string) string {
	labelURL := fmt.Sprintf("%s?action=wbgetentities&ids=%s&props=labels&languages=en&format=json",
		c.wikidataURL, entityID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, labelURL, nil)
	if err != nil {
		return ""
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var result struct {
		Entities map[string]struct {
			Labels map[string]struct {
				Value string `json:"value"`
			} `json:"labels"`
		} `json:"entities"`
	}
	if err := json.UnmarshalRead(resp.Body, &result); err != nil {
		return ""
	}

	if entity, ok := result.Entities[entityID]; ok {
		if label, ok := entity.Labels["en"]; ok {
			return label.Value
		}
	}
	return ""
}
