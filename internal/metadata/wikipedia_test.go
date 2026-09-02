// file: internal/metadata/wikipedia_test.go
// version: 1.0.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a

package metadata

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Verify interface compliance
var _ MetadataSource = (*WikipediaClient)(nil)

func TestWikipediaClient_Name(t *testing.T) {
	client := NewWikipediaClient()
	if client.Name() != "Wikipedia" {
		t.Errorf("expected Name() = 'Wikipedia', got %q", client.Name())
	}
}

func TestWikipediaClient_SearchByTitle(t *testing.T) {
	mw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("srsearch")
		if query == "" {
			t.Error("expected srsearch parameter")
		}
		resp := mediawikiSearchResponse{}
		resp.Query.Search = []mediawikiSearchResult{
			{Title: "The Great Gatsby", PageID: 1234, Snippet: "A novel by F. Scott Fitzgerald"},
			{Title: "Gatsby (audiobook)", PageID: 5678, Snippet: "Audiobook version"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, resp)
	}))
	defer mw.Close()

	// Wikidata server that returns no results (skip enrichment)
	wd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, wikidataSearchResponse{})
	}))
	defer wd.Close()

	client := NewWikipediaClientWithBaseURL(mw.URL, wd.URL)
	results, err := client.SearchByTitle(context.Background(), "The Great Gatsby")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Title != "The Great Gatsby" {
		t.Errorf("expected title 'The Great Gatsby', got %q", results[0].Title)
	}
}

func TestWikipediaClient_SearchByTitleAndAuthor(t *testing.T) {
	mw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := mediawikiSearchResponse{}
		resp.Query.Search = []mediawikiSearchResult{
			{Title: "1984 (novel)", PageID: 42},
		}
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, resp)
	}))
	defer mw.Close()

	wd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, wikidataSearchResponse{})
	}))
	defer wd.Close()

	client := NewWikipediaClientWithBaseURL(mw.URL, wd.URL)
	results, err := client.SearchByTitleAndAuthor(context.Background(), "1984", "George Orwell")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Title != "1984 (novel)" {
		t.Errorf("expected title '1984 (novel)', got %q", results[0].Title)
	}
}

func TestWikipediaClient_APIError(t *testing.T) {
	mw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer mw.Close()

	wd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.MarshalWrite(w, wikidataSearchResponse{})
	}))
	defer wd.Close()

	client := NewWikipediaClientWithBaseURL(mw.URL, wd.URL)
	_, err := client.SearchByTitle(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestWikipediaClient_WikidataEnrichment(t *testing.T) {
	mw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := mediawikiSearchResponse{}
		resp.Query.Search = []mediawikiSearchResult{
			{Title: "Dune (novel)", PageID: 100},
		}
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, resp)
	}))
	defer mw.Close()

	callCount := 0
	wd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		action := r.URL.Query().Get("action")

		switch action {
		case "wbsearchentities":
			callCount++
			json.MarshalWrite(w, wikidataSearchResponse{
				Search: []wikidataSearchResult{
					{ID: "Q190228", Label: "Dune", Description: "novel by Frank Herbert"},
				},
			})
		case "wbgetentities":
			ids := r.URL.Query().Get("ids")
			props := r.URL.Query().Get("props")

			if props == "labels" {
				// Author label resolution
				json.MarshalWrite(w, map[string]any{
					"entities": map[string]any{
						ids: map[string]any{
							"labels": map[string]any{
								"en": map[string]string{"value": "Frank Herbert"},
							},
						},
					},
				})
				return
			}

			// Entity claims
			json.MarshalWrite(w, wikidataEntityResponse{
				Entities: map[string]wikidataEntity{
					"Q190228": {
						Claims: map[string][]wikidataClaim{
							"P50": {{MainSnak: wikidataSnak{DataValue: &wikidataDataValue{
								Type:  "wikibase-entityid",
								Value: jsontext.Value(`{"id":"Q44804"}`),
							}}}},
							"P577": {{MainSnak: wikidataSnak{DataValue: &wikidataDataValue{
								Type:  "time",
								Value: jsontext.Value(`{"time":"+1965-08-01T00:00:00Z"}`),
							}}}},
							"P212": {{MainSnak: wikidataSnak{DataValue: &wikidataDataValue{
								Type:  "string",
								Value: jsontext.Value(`"978-0441172719"`),
							}}}},
						},
					},
				},
			})
		}
	}))
	defer wd.Close()

	client := NewWikipediaClientWithBaseURL(mw.URL, wd.URL)
	results, err := client.SearchByTitle(context.Background(), "Dune")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	r := results[0]
	if r.Title != "Dune (novel)" {
		t.Errorf("expected title 'Dune (novel)', got %q", r.Title)
	}
	if r.Author != "Frank Herbert" {
		t.Errorf("expected author 'Frank Herbert', got %q", r.Author)
	}
	if r.PublishYear != 1965 {
		t.Errorf("expected publish year 1965, got %d", r.PublishYear)
	}
	if r.ISBN != "978-0441172719" {
		t.Errorf("expected ISBN '978-0441172719', got %q", r.ISBN)
	}
}
