// file: internal/metadata/googlebooks_test.go
// version: 1.0.1
// guid: d4e5f6a7-b8c9-0d1e-2f3a-b4c5d6e7f8a9

package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGoogleBooksClient_Name(t *testing.T) {
	c := NewGoogleBooksClient("")
	if c.Name() != "Google Books" {
		t.Errorf("expected 'Google Books', got %q", c.Name())
	}
}

func TestGoogleBooksClient_SearchByTitle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/volumes" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{
			"totalItems": 1,
			"items": [{
				"volumeInfo": {
					"title": "The Hobbit",
					"authors": ["J.R.R. Tolkien"],
					"publisher": "HarperCollins",
					"publishedDate": "1937-09-21",
					"language": "en",
					"industryIdentifiers": [
						{"type": "ISBN_13", "identifier": "9780261103344"},
						{"type": "ISBN_10", "identifier": "0261103342"}
					],
					"imageLinks": {"thumbnail": "http://example.com/cover.jpg"}
				}
			}]
		}`))
	}))
	defer server.Close()

	client := NewGoogleBooksClientWithBaseURL(server.URL)
	results, err := client.SearchByTitle(context.Background(), "The Hobbit")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Title != "The Hobbit" {
		t.Errorf("expected title 'The Hobbit', got %q", r.Title)
	}
	if r.Author != "J.R.R. Tolkien" {
		t.Errorf("expected author 'J.R.R. Tolkien', got %q", r.Author)
	}
	if r.ISBN != "9780261103344" {
		t.Errorf("expected ISBN13, got %q", r.ISBN)
	}
	if r.Publisher != "HarperCollins" {
		t.Errorf("expected publisher 'HarperCollins', got %q", r.Publisher)
	}
	if r.PublishYear != 1937 {
		t.Errorf("expected year 1937, got %d", r.PublishYear)
	}
	if r.CoverURL != "https://example.com/cover.jpg" {
		t.Errorf("expected cover URL, got %q", r.CoverURL)
	}
}

func TestGoogleBooksClient_SearchByTitleAndAuthor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"totalItems": 0, "items": []}`))
	}))
	defer server.Close()

	client := NewGoogleBooksClientWithBaseURL(server.URL)
	results, err := client.SearchByTitleAndAuthor(context.Background(), "Unknown", "Nobody")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestGoogleBooksClient_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewGoogleBooksClientWithBaseURL(server.URL)
	_, err := client.SearchByTitle(context.Background(), "test")
	if err == nil {
		t.Error("expected error on 500 response")
	}
}

func TestGoogleBooksClient_APIKeySentInHeader(t *testing.T) {
	var receivedURL, receivedHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedURL = r.URL.String()
		receivedHeader = r.Header.Get("X-Goog-Api-Key")
		_, _ = w.Write([]byte(`{"totalItems": 0, "items": []}`))
	}))
	defer server.Close()

	client := &GoogleBooksClient{
		httpClient: server.Client(),
		baseURL:    server.URL,
		apiKey:     "test-key-123",
	}
	_, err := client.SearchByTitle(context.Background(), "Dune")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedHeader != "test-key-123" {
		t.Errorf("expected API key in X-Goog-Api-Key, got %q", receivedHeader)
	}
	if strings.Contains(receivedURL, "key") {
		t.Errorf("API key must not appear in the URL, got %q", receivedURL)
	}
}

func TestGoogleBooksClient_NoAPIKey(t *testing.T) {
	var receivedURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedURL = r.URL.String()
		_, _ = w.Write([]byte(`{"totalItems": 0, "items": []}`))
	}))
	defer server.Close()

	client := NewGoogleBooksClientWithBaseURL(server.URL)
	_, err := client.SearchByTitle(context.Background(), "Dune")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(receivedURL, "key=") {
		t.Errorf("expected no API key in URL, got %q", receivedURL)
	}
}

// Verify interface compliance
var _ MetadataSource = (*GoogleBooksClient)(nil)
