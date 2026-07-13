// file: internal/metadata/openlibrary_test.go
// version: 1.2.0
// guid: 2b3c4d5e-6f7a-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-07-13

package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewOpenLibraryClient(t *testing.T) {
	t.Setenv("OPENLIBRARY_BASE_URL", "")
	client := NewOpenLibraryClient()
	if client == nil {
		t.Fatal("Expected non-nil client")
	}
	if client.baseURL != "https://openlibrary.org" {
		t.Errorf("Expected baseURL to be https://openlibrary.org, got %s", client.baseURL)
	}
	if client.httpClient == nil {
		t.Fatal("Expected non-nil HTTP client")
	}
}

func TestNewOpenLibraryClientUsesEnvBaseURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("OPENLIBRARY_BASE_URL", server.URL)

	client := NewOpenLibraryClient()
	if client.baseURL != server.URL {
		t.Errorf("Expected baseURL to use env %s, got %s", server.URL, client.baseURL)
	}
}

func TestSearchByTitle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search.json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"numFound":1,"start":0,"docs":[{"title":"The Hobbit","author_name":["J.R.R. Tolkien"],"first_publish_year":1937}]}`))
	}))
	defer server.Close()

	client := NewOpenLibraryClientWithBaseURL(server.URL)

	results, err := client.SearchByTitle(context.Background(), "The Hobbit")
	if err != nil {
		t.Fatalf("SearchByTitle failed: %v", err)
	}

	if len(results) == 0 {
		t.Error("Expected at least one result for 'The Hobbit'")
	}

	if len(results) > 0 {
		firstResult := results[0]
		if firstResult.Title == "" {
			t.Error("Expected non-empty title in result")
		}
	}
}

func TestSearchByTitleAndAuthor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search.json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"numFound":1,"start":0,"docs":[{"title":"The Hobbit","author_name":["J.R.R. Tolkien"],"first_publish_year":1937,"publisher":["Allen & Unwin"]}]}`))
	}))
	defer server.Close()

	client := NewOpenLibraryClientWithBaseURL(server.URL)

	results, err := client.SearchByTitleAndAuthor(context.Background(), "The Hobbit", "Tolkien")
	if err != nil {
		t.Fatalf("SearchByTitleAndAuthor failed: %v", err)
	}

	if len(results) == 0 {
		t.Error("Expected at least one result for 'The Hobbit' by Tolkien")
	}

	if len(results) > 0 {
		found := false
		for _, result := range results {
			if result.Author != "" {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected at least one result with author information")
		}
	}
}

func TestSearchByTitleNoResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search.json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"numFound":0,"start":0,"docs":[]}`))
	}))
	defer server.Close()

	client := NewOpenLibraryClientWithBaseURL(server.URL)

	results, err := client.SearchByTitle(context.Background(), "xyzabc123456789nonexistent")
	if err != nil {
		t.Fatalf("SearchByTitle failed: %v", err)
	}

	if len(results) != 0 {
		t.Errorf("Expected no results, got %d", len(results))
	}
}

func TestGetBookByISBN(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/isbn/9780547928227.json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"title":"The Hobbit","publish_date":"1937","publishers":["Allen & Unwin"],"covers":[123]}`))
	}))
	defer server.Close()

	client := NewOpenLibraryClientWithBaseURL(server.URL)

	result, err := client.GetBookByISBN(context.Background(), "9780547928227")
	if err != nil {
		t.Fatalf("GetBookByISBN failed: %v", err)
	}

	if result == nil {
		t.Fatal("Expected non-nil result for valid ISBN")
	}

	if result.ISBN != "9780547928227" {
		t.Errorf("Expected ISBN 9780547928227, got %s", result.ISBN)
	}
	if result.Title != "The Hobbit" {
		t.Errorf("Expected title The Hobbit, got %s", result.Title)
	}
	if result.Publisher != "Allen & Unwin" {
		t.Errorf("Expected publisher Allen & Unwin, got %s", result.Publisher)
	}
	if result.PublishYear != 1937 {
		t.Errorf("Expected publish year 1937, got %d", result.PublishYear)
	}
	if result.CoverURL == "" {
		t.Error("Expected cover URL to be set")
	}
}

func TestGetBookByISBNInvalid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewOpenLibraryClientWithBaseURL(server.URL)

	_, err := client.GetBookByISBN(context.Background(), "0000000000")
	if err == nil {
		t.Error("Expected error for invalid ISBN")
	}
}

func TestSearchByTitle_NetworkError(t *testing.T) {
	// Use invalid URL to trigger network error
	client := NewOpenLibraryClientWithBaseURL("http://invalid.localhost:99999")

	_, err := client.SearchByTitle(context.Background(), "Test")
	if err == nil {
		t.Error("Expected network error")
	}
}

func TestSearchByTitle_NonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewOpenLibraryClientWithBaseURL(server.URL)

	_, err := client.SearchByTitle(context.Background(), "Test")
	if err == nil {
		t.Error("Expected error for non-OK status")
	}
}

func TestSearchByTitle_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("invalid json"))
	}))
	defer server.Close()

	client := NewOpenLibraryClientWithBaseURL(server.URL)

	_, err := client.SearchByTitle(context.Background(), "Test")
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

func TestSearchByTitle_CompleteMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"numFound":1,
			"start":0,
			"docs":[{
				"title":"Complete Book",
				"author_name":["Author Name"],
				"first_publish_year":2020,
				"publisher":["Publisher Name"],
				"isbn":["1234567890"],
				"language":["eng"],
				"cover_i":12345
			}]
		}`))
	}))
	defer server.Close()

	client := NewOpenLibraryClientWithBaseURL(server.URL)

	results, err := client.SearchByTitle(context.Background(), "Complete Book")
	if err != nil {
		t.Fatalf("SearchByTitle failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	result := results[0]
	if result.Title != "Complete Book" {
		t.Errorf("Expected title 'Complete Book', got %q", result.Title)
	}
	if result.Author != "Author Name" {
		t.Errorf("Expected author 'Author Name', got %q", result.Author)
	}
	if result.PublishYear != 2020 {
		t.Errorf("Expected year 2020, got %d", result.PublishYear)
	}
	if result.Publisher != "Publisher Name" {
		t.Errorf("Expected publisher 'Publisher Name', got %q", result.Publisher)
	}
	if result.ISBN != "1234567890" {
		t.Errorf("Expected ISBN '1234567890', got %q", result.ISBN)
	}
	if result.Language != "eng" {
		t.Errorf("Expected language 'eng', got %q", result.Language)
	}
	if result.CoverURL == "" {
		t.Error("Expected cover URL to be set")
	}
}

func TestSearchByTitleAndAuthor_NetworkError(t *testing.T) {
	client := NewOpenLibraryClientWithBaseURL("http://invalid.localhost:99999")

	_, err := client.SearchByTitleAndAuthor(context.Background(), "Test", "Author")
	if err == nil {
		t.Error("Expected network error")
	}
}

func TestSearchByTitleAndAuthor_NonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	client := NewOpenLibraryClientWithBaseURL(server.URL)

	_, err := client.SearchByTitleAndAuthor(context.Background(), "Test", "Author")
	if err == nil {
		t.Error("Expected error for non-OK status")
	}
}

func TestSearchByTitleAndAuthor_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{invalid json"))
	}))
	defer server.Close()

	client := NewOpenLibraryClientWithBaseURL(server.URL)

	_, err := client.SearchByTitleAndAuthor(context.Background(), "Test", "Author")
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

func TestSearchByTitleAndAuthor_MultipleAuthors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"numFound":1,
			"start":0,
			"docs":[{
				"title":"Multi-Author Book",
				"author_name":["First Author", "Second Author"],
				"first_publish_year":2021
			}]
		}`))
	}))
	defer server.Close()

	client := NewOpenLibraryClientWithBaseURL(server.URL)

	results, err := client.SearchByTitleAndAuthor(context.Background(), "Multi-Author Book", "First Author")
	if err != nil {
		t.Fatalf("SearchByTitleAndAuthor failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	// Should join multiple authors
	if results[0].Author != "First Author, Second Author" {
		t.Errorf("Expected joined authors, got %q", results[0].Author)
	}
}

func TestGetBookByISBN_NetworkError(t *testing.T) {
	client := NewOpenLibraryClientWithBaseURL("http://invalid.localhost:99999")

	_, err := client.GetBookByISBN(context.Background(), "1234567890")
	if err == nil {
		t.Error("Expected network error")
	}
}

func TestGetBookByISBN_NonOKNonNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewOpenLibraryClientWithBaseURL(server.URL)

	_, err := client.GetBookByISBN(context.Background(), "1234567890")
	if err == nil {
		t.Error("Expected error for internal server error")
	}
}

func TestGetBookByISBN_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{not valid json}"))
	}))
	defer server.Close()

	client := NewOpenLibraryClientWithBaseURL(server.URL)

	_, err := client.GetBookByISBN(context.Background(), "1234567890")
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

func TestGetBookByISBN_EmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	client := NewOpenLibraryClientWithBaseURL(server.URL)

	result, err := client.GetBookByISBN(context.Background(), "1234567890")
	if err != nil {
		t.Fatalf("GetBookByISBN failed: %v", err)
	}

	// Should handle empty fields gracefully
	if result.ISBN != "1234567890" {
		t.Errorf("Expected ISBN to be set, got %q", result.ISBN)
	}
	if result.Title != "" {
		t.Errorf("Expected empty title, got %q", result.Title)
	}
}

func TestGetBookByISBN_ShortPublishDate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"title":"Test","publish_date":"20"}`))
	}))
	defer server.Close()

	client := NewOpenLibraryClientWithBaseURL(server.URL)

	result, err := client.GetBookByISBN(context.Background(), "1234567890")
	if err != nil {
		t.Fatalf("GetBookByISBN failed: %v", err)
	}

	// Should handle short date strings
	if result.PublishYear != 0 {
		t.Errorf("Expected year 0 for short date, got %d", result.PublishYear)
	}
}

func TestNewOpenLibraryClientWithBaseURL_TrailingSlash(t *testing.T) {
	client := NewOpenLibraryClientWithBaseURL("https://example.com/")

	if client.baseURL != "https://example.com" {
		t.Errorf("Expected trailing slash to be trimmed, got %q", client.baseURL)
	}
}

// TestUnambiguousLanguage covers the helper directly.
func TestUnambiguousLanguage(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"empty", nil, ""},
		{"single", []string{"eng"}, "eng"},
		{"duplicate_same", []string{"eng", "eng"}, "eng"},
		{"case_insensitive_same", []string{"eng", "ENG"}, "eng"},
		{"ambiguous_differ", []string{"spa", "eng"}, ""},
		{"blank_then_single", []string{"", "spa"}, "spa"},
	}
	for _, c := range cases {
		if got := unambiguousLanguage(c.in); got != c.want {
			t.Errorf("%s: unambiguousLanguage(%v) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestSearchByTitle_AmbiguousLanguageSkipped proves an Open Library search doc
// whose editions list differing languages does NOT persist a guessed language,
// while a single-language doc still does. Guessing Language[0] on an unordered
// array mislabels a book whose first-listed edition is a translation, because
// the value becomes a metadata:language:<code> system tag driving the review
// language filter.
func TestSearchByTitle_AmbiguousLanguageSkipped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"numFound":2,
			"start":0,
			"docs":[
				{"title":"Translated First","language":["spa","eng"]},
				{"title":"English Only","language":["eng"]}
			]
		}`))
	}))
	defer server.Close()

	client := NewOpenLibraryClientWithBaseURL(server.URL)
	results, err := client.SearchByTitle(context.Background(), "anything")
	if err != nil {
		t.Fatalf("SearchByTitle failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Language != "" {
		t.Errorf("ambiguous language should be skipped, got %q", results[0].Language)
	}
	if results[1].Language != "eng" {
		t.Errorf("single-language doc should set language 'eng', got %q", results[1].Language)
	}
}
