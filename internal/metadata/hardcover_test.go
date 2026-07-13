// file: internal/metadata/hardcover_test.go
// version: 1.0.0
// guid: c8d9e0f1-a2b3-4567-890a-bcdef1234567

package metadata

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHardcoverClient_SearchByTitle_ParsesRichFields verifies the
// expanded GraphQL query pulls narrator (via contributor_names),
// ISBN, series with position, and genres out of the Hardcover
// response and folds them into BookMetadata.
func TestHardcoverClient_SearchByTitle_ParsesRichFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// Sanity: the query must request every field we care about
		// so a Hardcover schema rename surfaces as a test failure.
		body, _ := io.ReadAll(r.Body)
		bodyStr := string(body)
		wantFields := []string{
			"contributor_names",
			"isbns",
			"featured_series",
			"series_names",
			"genres",
		}
		for _, f := range wantFields {
			if !strings.Contains(bodyStr, f) {
				t.Errorf("expanded query missing field %q in %s", f, bodyStr)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": {
				"search_books": {
					"results": {
						"hits": [
							{
								"document": {
									"title": "Foundation and Empire",
									"author_names": ["Isaac Asimov"],
									"contributor_names": ["Isaac Asimov", "Scott Brick"],
									"image": {"url": "http://example.com/foundation.jpg"},
									"description": "The Mule rises.",
									"release_year": 1952,
									"slug": "foundation-and-empire",
									"publisher": "Gnome Press",
									"isbns": ["9780553293371", "0553293370"],
									"pages": 256,
									"audio_seconds": 34800,
									"series_names": ["Foundation"],
									"featured_series": {"series_name": "Foundation", "position": 4},
									"genres": ["Science Fiction", "Classics"],
									"language": "en"
								}
							}
						]
					}
				}
			}
		}`))
	}))
	defer server.Close()

	client := NewHardcoverClientWithBaseURL(server.URL, "test-token")
	results, err := client.SearchByTitle(context.Background(), "Foundation and Empire")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	r := results[0]
	if r.Title != "Foundation and Empire" {
		t.Errorf("title = %q", r.Title)
	}
	if r.Author != "Isaac Asimov" {
		t.Errorf("author = %q", r.Author)
	}
	// Narrator should be derived from contributor_names minus author_names.
	if r.Narrator != "Scott Brick" {
		t.Errorf("narrator = %q, want Scott Brick", r.Narrator)
	}
	if r.Publisher != "Gnome Press" {
		t.Errorf("publisher = %q", r.Publisher)
	}
	if r.PublishYear != 1952 {
		t.Errorf("publish year = %d", r.PublishYear)
	}
	// ISBN-13 prefers over ISBN-10.
	if r.ISBN != "9780553293371" {
		t.Errorf("isbn = %q, want 13-digit 9780553293371", r.ISBN)
	}
	if r.Series != "Foundation" {
		t.Errorf("series = %q", r.Series)
	}
	if r.SeriesPosition != "4" {
		t.Errorf("series position = %q, want 4", r.SeriesPosition)
	}
	if r.Genre != "Science Fiction, Classics" {
		t.Errorf("genre = %q", r.Genre)
	}
	if r.Language != "en" {
		t.Errorf("language = %q", r.Language)
	}
	if r.CoverURL != "http://example.com/foundation.jpg" {
		t.Errorf("cover = %q", r.CoverURL)
	}
}

// TestHardcoverClient_SearchByContext_PrefersISBN verifies that
// when the context has an ISBN, the search query uses it directly
// instead of title+author. ISBN is a more precise match signal.
func TestHardcoverClient_SearchByContext_PrefersISBN(t *testing.T) {
	var lastQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		lastQuery = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"search_books":{"results":{"hits":[]}}}}`))
	}))
	defer server.Close()

	client := NewHardcoverClientWithBaseURL(server.URL, "test-token")

	// ISBN-13 path: query text must contain the ISBN, not the title.
	_, _ = client.SearchByContext(context.Background(), &SearchContext{
		Title:  "Foundation and Empire",
		Author: "Isaac Asimov",
		ISBN13: "9780553293371",
	})
	if !strings.Contains(lastQuery, "9780553293371") {
		t.Errorf("expected ISBN-13 in query, got: %s", lastQuery)
	}

	// Fall-through: no ISBN → use title+author.
	_, _ = client.SearchByContext(context.Background(), &SearchContext{
		Title:  "Foundation and Empire",
		Author: "Isaac Asimov",
	})
	if !strings.Contains(lastQuery, "Foundation and Empire") {
		t.Errorf("expected title in query, got: %s", lastQuery)
	}
	if !strings.Contains(lastQuery, "Isaac Asimov") {
		t.Errorf("expected author in query, got: %s", lastQuery)
	}
}

// TestHardcoverClient_NoToken_NoOps verifies the client gracefully
// returns empty results without making any HTTP request when no
// API token is configured.
func TestHardcoverClient_NoToken_NoOps(t *testing.T) {
	client := NewHardcoverClient("")
	results, err := client.SearchByTitle(context.Background(), "anything")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results with no token, got %d", len(results))
	}

	results, err = client.SearchByContext(context.Background(), &SearchContext{
		Title:  "anything",
		ISBN13: "9780553293371",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results with no token on SearchByContext, got %d", len(results))
	}
}

// Verify interface compliance
var _ MetadataSource = (*HardcoverClient)(nil)
var _ ContextualSearch = (*HardcoverClient)(nil)
