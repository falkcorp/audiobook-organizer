// file: internal/server/pipeline_integration_test.go
// version: 1.1.2
// guid: b1c2d3e4-f5a6-7890-abcd-ef1234567890
// last-edited: 2026-10-06

package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockHTTPServer creates a test server that matches URL patterns to responses.
// statusOverride lets you force a specific HTTP status code for all responses.
func mockHTTPServer(t *testing.T, responses map[string]string, statusOverride int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if statusOverride != 0 {
			w.WriteHeader(statusOverride)
			return
		}
		for pattern, body := range responses {
			if strings.Contains(r.URL.String(), pattern) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
				return
			}
		}
		http.NotFound(w, r)
	}))
}

func TestPipeline_ImportThenFetchMetadata(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	// 1. Create an author
	author, err := env.Store.CreateAuthor("J.R.R. Tolkien")
	require.NoError(t, err)
	require.NotNil(t, author)

	// 2. Create a book record
	book, err := env.Store.CreateBook(&database.Book{
		Title:    "The Hobbit",
		AuthorID: &author.ID,
		FilePath: "/fake/hobbit.m4b",
		Format:   "m4b",
	})
	require.NoError(t, err)
	require.NotEmpty(t, book.ID)

	// 3. Start a mock Audible (Open Library and Google Books are
	// review-only and never applied by the auto-fetch, 2026-10-06)
	audServer := testutil.MockAudibleServer(t, audibleAnswersTitle(testutil.AudibleHobbitProduct, "The Hobbit"))

	// 4. Configure metadata sources — only Audible, pointed at the mock
	config.AppConfig.MetadataSources = []config.MetadataSource{
		{ID: "audible", Name: "Audible", Enabled: true, Priority: 1, BaseURL: audServer.URL},
	}
	config.AppConfig.WriteBackMetadata = false

	// 6. Call FetchMetadataForBook
	svc := metafetch.NewService(env.Store)
	resp, err := svc.FetchMetadataForBook(context.Background(), book.ID)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// 7. Assert source
	assert.Equal(t, "Audible", resp.Source)

	// 8. Re-read book from DB
	updated, err := env.Store.GetBookByID(book.ID)
	require.NoError(t, err)

	// 9. Assert metadata applied
	assert.Equal(t, book.ID, updated.ID)
	require.NotNil(t, updated.Publisher)
	assert.Equal(t, "Houghton Mifflin", *updated.Publisher)
	assert.Equal(t, "The Hobbit", updated.Title)
}

func TestPipeline_FetchMetadata_MultiSourceFallback(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	// 1. Create a book
	book, err := env.Store.CreateBook(&database.Book{
		Title:    "Dune",
		FilePath: "/fake/dune.m4b",
		Format:   "m4b",
	})
	require.NoError(t, err)

	// 2. Mock Open Library returns 500
	olServer := mockHTTPServer(t, nil, http.StatusInternalServerError)
	defer olServer.Close()

	// 3. Mock Google Books returns a valid Dune response whose publisher,
	// description and ISBN differ from Audible's, so a Google match that was
	// applied would show in the book row. Counted, so the test proves Google
	// was asked (fetched) and not merely never reached.
	duneGoogleResponse := `{
		"totalItems": 1,
		"items": [{
			"volumeInfo": {
				"title": "Dune",
				"authors": ["Frank Herbert"],
				"publisher": "Synthetic Review Press",
				"publishedDate": "1965",
				"description": "Synthetic review-only description",
				"language": "en",
				"industryIdentifiers": [
					{"type": "ISBN_13", "identifier": "9780441172719"}
				]
			}
		}]
	}`
	gbInner := mockHTTPServer(t, map[string]string{
		"volumes": duneGoogleResponse,
	}, 0)
	defer gbInner.Close()
	var gbHits atomic.Int64
	gbServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gbHits.Add(1)
		r2, _ := http.NewRequestWithContext(r.Context(), r.Method, gbInner.URL+r.URL.RequestURI(), nil)
		res, err := gbInner.Client().Do(r2)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer res.Body.Close()
		w.Header().Set("Content-Type", res.Header.Get("Content-Type"))
		w.WriteHeader(res.StatusCode)
		_, _ = io.Copy(w, res.Body)
	}))
	defer gbServer.Close()

	// 3b. Mock Audible returns a Dune record too
	audServer := testutil.MockAudibleServer(t, audibleAnswersTitle(testutil.AudibleTestProduct{
		ASIN: "B0TESTDUNE", Title: "Dune", Authors: []string{"Frank Herbert"},
		Publisher: "Chilton Books", Language: "en", ReleaseDate: "2007-01-01",
	}, "Dune"))

	// 4. Configure all three sources, pointed at their mocks
	config.AppConfig.MetadataSources = []config.MetadataSource{
		{ID: "openlibrary", Name: "Open Library", Enabled: true, Priority: 1, BaseURL: olServer.URL},
		{ID: "google-books", Name: "Google Books", Enabled: true, Priority: 2, BaseURL: gbServer.URL},
		{ID: "audible", Name: "Audible", Enabled: true, Priority: 3, BaseURL: audServer.URL},
	}
	config.AppConfig.WriteBackMetadata = false

	// 5. Call FetchMetadataForBook
	svc := metafetch.NewService(env.Store)
	resp, err := svc.FetchMetadataForBook(context.Background(), book.ID)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// 6. Assert: source is Audible -- Open Library failed, and Google
	// Books' match is review-only (fetched, never applied by the
	// auto-fetch, owner decision 2026-10-06), so the chain moved on.
	assert.Equal(t, "Audible", resp.Source)
	assert.Positive(t, gbHits.Load(), "Google Books was never asked: the test would pass without the review-only skip")

	// 7. Assert: book metadata updated in DB
	updated, err := env.Store.GetBookByID(book.ID)
	require.NoError(t, err)
	assert.Equal(t, "Dune", updated.Title)
	require.NotNil(t, updated.Publisher)
	assert.Equal(t, "Chilton Books", *updated.Publisher)
	require.NotNil(t, updated.Language)
	assert.Equal(t, "en", *updated.Language)
	// Nothing of Google's matched record was written.
	if updated.Description != nil {
		assert.NotEqual(t, "Synthetic review-only description", *updated.Description)
	}
	if updated.ISBN13 != nil {
		assert.NotEqual(t, "9780441172719", *updated.ISBN13)
	}
}

func TestPipeline_ChapterTitle_StillFindsBook(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	// 1. Create author and book with chapter in title
	author, err := env.Store.CreateAuthor("J.R.R. Tolkien")
	require.NoError(t, err)

	book, err := env.Store.CreateBook(&database.Book{
		Title:    "The Hobbit - Chapter 3",
		AuthorID: &author.ID,
		FilePath: "/fake/hobbit-ch3.m4b",
		Format:   "m4b",
	})
	require.NoError(t, err)

	// 2. Mock server: title-only search with stripped chapter prefix should match.
	// The service strips " - Chapter 3" via stripChapterFromTitle, then
	// searches by title "The Hobbit" (no author in query).
	callCount := 0
	answer := audibleAnswersTitle(testutil.AudibleHobbitProduct, "The Hobbit")
	audServer := testutil.MockAudibleServer(t, func(title string) []testutil.AudibleTestProduct {
		callCount++
		return answer(title) // stripped title "The Hobbit" matches; others are empty
	})

	// 3. Configure Audible, pointed at the mock
	config.AppConfig.MetadataSources = []config.MetadataSource{
		{ID: "audible", Name: "Audible", Enabled: true, Priority: 1, BaseURL: audServer.URL},
	}
	config.AppConfig.WriteBackMetadata = false

	// 4. Call FetchMetadataForBook
	svc := metafetch.NewService(env.Store)
	resp, err := svc.FetchMetadataForBook(context.Background(), book.ID)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// 5. Assert: metadata found via title-only search with stripped chapter prefix
	assert.Equal(t, "Audible", resp.Source)

	// 6. Assert: book title in DB is now clean (from metadata response)
	updated, err := env.Store.GetBookByID(book.ID)
	require.NoError(t, err)
	assert.Equal(t, "The Hobbit", updated.Title)
	require.NotNil(t, updated.Publisher)
	assert.Equal(t, "Houghton Mifflin", *updated.Publisher)
}

func TestPipeline_FetchMetadata_NoResults_AllSources(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	// 1. Create book with nonsense title, no author
	book, err := env.Store.CreateBook(&database.Book{
		Title:    "asdflkj32523 Unknown",
		FilePath: "/fake/unknown.m4b",
		Format:   "m4b",
	})
	require.NoError(t, err)

	// 2. All mock sources return empty
	olServer := testutil.MockOpenLibraryServer(t, map[string]string{
		"search.json": testutil.OpenLibraryEmptyResponse,
	})
	defer olServer.Close()

	gbEmptyResponse := `{"totalItems": 0, "items": []}`
	gbServer := mockHTTPServer(t, map[string]string{
		"volumes": gbEmptyResponse,
	}, 0)
	defer gbServer.Close()

	// 3. Configure sources, pointed at their mocks
	config.AppConfig.MetadataSources = []config.MetadataSource{
		{ID: "openlibrary", Name: "Open Library", Enabled: true, Priority: 1, BaseURL: olServer.URL},
		{ID: "google-books", Name: "Google Books", Enabled: true, Priority: 2, BaseURL: gbServer.URL},
	}
	config.AppConfig.WriteBackMetadata = false

	// 4. Call FetchMetadataForBook
	svc := metafetch.NewService(env.Store)
	resp, err := svc.FetchMetadataForBook(context.Background(), book.ID)

	// 5. Assert: error contains "no metadata found"
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no metadata found")
	assert.Nil(t, resp)

	// 6. Assert: book in DB is unchanged
	unchanged, err := env.Store.GetBookByID(book.ID)
	require.NoError(t, err)
	assert.Equal(t, "asdflkj32523 Unknown", unchanged.Title)
	assert.Nil(t, unchanged.Publisher)
}
