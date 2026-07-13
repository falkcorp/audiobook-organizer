// file: internal/metadata/audible_test.go
// version: 1.3.0
// guid: b8c7d6e5-f4a3-2b1c-0d9e-8f7a6b5c4d3e
// last-edited: 2026-07-13

package metadata

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAudibleClient_Name(t *testing.T) {
	c := NewAudibleClient()
	if c.Name() != "Audible" {
		t.Errorf("expected 'Audible', got %q", c.Name())
	}
}

func TestAudibleClient_SearchByTitle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/catalog/products" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("title") == "" {
			t.Error("expected title param")
		}
		resp := audibleCatalogResponse{
			Products: []audibleProduct{
				{
					ASIN:  "B0CPKNRSL5",
					Title: "Rogue Ascension",
					Authors: []audiblePerson{
						{ASIN: "B0BHR8YPLS", Name: "Hunter Mythos"},
					},
					Narrators: []audiblePerson{
						{Name: "André Santana"},
					},
					IssueDate:     "2024-02-13",
					Language:      "english",
					PublisherName: "Podium Audio",
					ProductImages: map[string]string{"500": "https://example.com/cover.jpg"},
					Series: []audibleSeries{
						{ASIN: "B0CPLN9WDR", Title: "Rogue Ascension", Sequence: "1"},
					},
				},
			},
			TotalResult: 1,
		}
		json.MarshalWrite(w, resp)
	}))
	defer server.Close()

	client := NewAudibleClientWithBaseURL(server.URL)
	results, err := client.SearchByTitle(context.Background(), "Rogue Ascension")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	r := results[0]
	if r.Title != "Rogue Ascension" {
		t.Errorf("title: got %q", r.Title)
	}
	if r.Author != "Hunter Mythos" {
		t.Errorf("author: got %q", r.Author)
	}
	if r.Narrator != "André Santana" {
		t.Errorf("narrator: got %q", r.Narrator)
	}
	if r.ASIN != "B0CPKNRSL5" {
		t.Errorf("ASIN: got %q", r.ASIN)
	}
	if r.Series != "Rogue Ascension" {
		t.Errorf("series: got %q", r.Series)
	}
	if r.SeriesPosition != "1" {
		t.Errorf("series position: got %q", r.SeriesPosition)
	}
	if r.PublishYear != 2024 {
		t.Errorf("year: got %d", r.PublishYear)
	}
	if !r.PublishYearIsAudiobookRelease {
		t.Error("Audible PublishYear must be flagged as an audiobook release year")
	}
	if r.CoverURL != "https://example.com/cover.jpg" {
		t.Errorf("cover: got %q", r.CoverURL)
	}
}

func TestAudibleClient_LookupByASIN(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/catalog/products/B0CPKNRSL5" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		resp := audibleProductResponse{
			Product: audibleProduct{
				ASIN:  "B0CPKNRSL5",
				Title: "Rogue Ascension",
				Authors: []audiblePerson{
					{Name: "Hunter Mythos"},
				},
			},
		}
		json.MarshalWrite(w, resp)
	}))
	defer server.Close()

	client := NewAudibleClientWithBaseURL(server.URL)
	result, err := client.LookupByASIN("B0CPKNRSL5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Title != "Rogue Ascension" {
		t.Errorf("title: got %q", result.Title)
	}
}

func TestAudibleClient_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewAudibleClientWithBaseURL(server.URL)
	_, err := client.SearchByTitle(context.Background(), "test")
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

func TestAudibleClient_HTMLStripping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := audibleCatalogResponse{
			Products: []audibleProduct{
				{
					ASIN:                 "B123",
					Title:                "Test",
					MerchandisingSummary: "<p>Hello <b>world</b></p>",
				},
			},
		}
		json.MarshalWrite(w, resp)
	}))
	defer server.Close()

	client := NewAudibleClientWithBaseURL(server.URL)
	results, err := client.SearchByTitle(context.Background(), "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results[0].Description != "Hello world" {
		t.Errorf("description: got %q, want 'Hello world'", results[0].Description)
	}
}

// TestAudibleClient_StringRating covers the Audible API quirk where
// display_average_rating is returned as a JSON string ("4.5") rather than a
// JSON number. encoding/json/v2 is strict about types, so flexFloat64 is needed.
func TestAudibleClient_StringRating(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate real Audible response with string-encoded rating
		fmt.Fprint(w, `{
			"products": [{
				"asin": "B001",
				"title": "Test Book",
				"rating": {
					"num_reviews": 500,
					"overall_distribution": {
						"display_average_rating": "4.5",
						"num_ratings": 1200
					},
					"performance_distribution": {
						"display_average_rating": "4.7",
						"num_ratings": 1100
					},
					"story_distribution": {
						"display_average_rating": "4.3",
						"num_ratings": 1050
					}
				}
			}],
			"total_results": 1
		}`)
	}))
	defer server.Close()

	client := NewAudibleClientWithBaseURL(server.URL)
	results, err := client.SearchByTitle(context.Background(), "test")
	if err != nil {
		t.Fatalf("unexpected error with string-encoded rating: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.AudibleRatingOverall != 4.5 {
		t.Errorf("overall rating: got %v, want 4.5", r.AudibleRatingOverall)
	}
	if r.AudibleRatingPerformance != 4.7 {
		t.Errorf("performance rating: got %v, want 4.7", r.AudibleRatingPerformance)
	}
	if r.AudibleRatingStory != 4.3 {
		t.Errorf("story rating: got %v, want 4.3", r.AudibleRatingStory)
	}
	if r.AudibleRatingCount != 1200 {
		t.Errorf("rating count: got %d, want 1200", r.AudibleRatingCount)
	}
}

var _ MetadataSource = (*AudibleClient)(nil)

func TestProductToMetadata_CategoryLadders(t *testing.T) {
	client := NewAudibleClientWithBaseURL("http://unused")

	p := &audibleProduct{
		ASIN:  "B08G9PRS1K",
		Title: "Test Book",
		CategoryLadders: []audibleCategoryLadder{
			{
				Root: "Audible Books & Originals",
				Ladder: []audibleCategoryNode{
					{ID: "18685580011", Name: "Science Fiction & Fantasy"},
					{ID: "18685589011", Name: "Science Fiction"},
					{ID: "18685594011", Name: "Space Opera"},
				},
			},
			{
				Root: "Audible Books & Originals",
				Ladder: []audibleCategoryNode{
					{ID: "18685580011", Name: "Science Fiction & Fantasy"},
					{ID: "18685589011", Name: "Science Fiction"},
				},
			},
		},
	}

	meta := client.productToMetadata(p)

	// Expect 3 unique tags (deduplication across overlapping ladders)
	want := []string{"Science Fiction & Fantasy", "Science Fiction", "Space Opera"}
	if len(meta.CategoryTags) != len(want) {
		t.Fatalf("CategoryTags: got %d tags, want %d: %v", len(meta.CategoryTags), len(want), meta.CategoryTags)
	}
	for i, w := range want {
		if meta.CategoryTags[i] != w {
			t.Errorf("CategoryTags[%d]: got %q, want %q", i, meta.CategoryTags[i], w)
		}
	}
}

func TestProductToMetadata_NoCategoryLadders(t *testing.T) {
	client := NewAudibleClientWithBaseURL("http://unused")

	p := &audibleProduct{
		ASIN:  "B000001",
		Title: "No Genres",
	}

	meta := client.productToMetadata(p)

	if meta.CategoryTags != nil {
		t.Errorf("expected nil CategoryTags when no ladders, got: %v", meta.CategoryTags)
	}
}
