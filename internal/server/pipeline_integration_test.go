// file: internal/server/pipeline_integration_test.go
// version: 1.0.0
// guid: b1c2d3e4-f5a6-7890-abcd-ef1234567890

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

	// 3. Configure metadata sources — only Open Library
	config.AppConfig.MetadataSources = []config.MetadataSource{
		{ID: "openlibrary", Name: "Open Library", Enabled: true, Priority: 1},
	}
	config.AppConfig.WriteBackMetadata = false

	// 4. Start mock Open Library server
	olServer := testutil.MockOpenLibraryServer(t, map[string]string{
		"search.json": testutil.OpenLibraryHobbitResponse,
	})
	defer olServer.Close()

	// 5. Set env var so the client uses our mock
	t.Setenv("OPENLIBRARY_BASE_URL", olServer.URL)

	// 6. Call FetchMetadataForBook
	svc := metafetch.NewService(env.Store)
	resp, err := svc.FetchMetadataForBook(context.Background(), book.ID)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// 7. Assert source
	assert.Equal(t, "Open Library", resp.Source)

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

	// 2. Configure two sources
	config.AppConfig.MetadataSources = []config.MetadataSource{
		{ID: "openlibrary", Name: "Open Library", Enabled: true, Priority: 1},
		{ID: "google-books", Name: "Google Books", Enabled: true, Priority: 2},
	}
	config.AppConfig.WriteBackMetadata = false

	// 3. Mock Open Library returns 500
	olServer := mockHTTPServer(t, nil, http.StatusInternalServerError)
	defer olServer.Close()
	t.Setenv("OPENLIBRARY_BASE_URL", olServer.URL)

	// 4. Mock Google Books returns valid Dune response
	duneGoogleResponse := `{
		"totalItems": 1,
		"items": [{
			"volumeInfo": {
				"title": "Dune",
				"authors": ["Frank Herbert"],
				"publisher": "Chilton Books",
				"publishedDate": "1965",
				"description": "A science fiction masterpiece",
				"language": "en",
				"industryIdentifiers": [
					{"type": "ISBN_13", "identifier": "9780441172719"}
				]
			}
		}]
	}`
	gbServer := mockHTTPServer(t, map[string]string{
		"volumes": duneGoogleResponse,
	}, 0)
	defer gbServer.Close()
	t.Setenv("GOOGLE_BOOKS_BASE_URL", gbServer.URL)

	// 5. Call FetchMetadataForBook
	svc := metafetch.NewService(env.Store)
	resp, err := svc.FetchMetadataForBook(context.Background(), book.ID)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// 6. Assert: source is Google Books (OL failed)
	assert.Equal(t, "Google Books", resp.Source)

	// 7. Assert: book metadata updated in DB
	updated, err := env.Store.GetBookByID(book.ID)
	require.NoError(t, err)
	assert.Equal(t, "Dune", updated.Title)
	require.NotNil(t, updated.Publisher)
	assert.Equal(t, "Chilton Books", *updated.Publisher)
	require.NotNil(t, updated.Language)
	assert.Equal(t, "en", *updated.Language)
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

	// 2. Configure Open Library
	config.AppConfig.MetadataSources = []config.MetadataSource{
		{ID: "openlibrary", Name: "Open Library", Enabled: true, Priority: 1},
	}
	config.AppConfig.WriteBackMetadata = false

	// 3. Mock server: title-only search with stripped chapter prefix should match.
	// The service strips " - Chapter 3" via stripChapterFromTitle, then
	// searches by title "The Hobbit" (no author in query).
	callCount := 0
	olServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")

		// Stripped title "The Hobbit" should match
		query := r.URL.Query()
		title := query.Get("title")
		if title == "The Hobbit" || title == "The+Hobbit" {
			_, _ = w.Write([]byte(testutil.OpenLibraryHobbitResponse))
			return
		}

		// Other searches: return empty
		_, _ = w.Write([]byte(testutil.OpenLibraryEmptyResponse))
	}))
	defer olServer.Close()
	t.Setenv("OPENLIBRARY_BASE_URL", olServer.URL)

	// 4. Call FetchMetadataForBook
	svc := metafetch.NewService(env.Store)
	resp, err := svc.FetchMetadataForBook(context.Background(), book.ID)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// 5. Assert: metadata found via title-only search with stripped chapter prefix
	assert.Equal(t, "Open Library", resp.Source)

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

	// 2. Configure sources
	config.AppConfig.MetadataSources = []config.MetadataSource{
		{ID: "openlibrary", Name: "Open Library", Enabled: true, Priority: 1},
		{ID: "google-books", Name: "Google Books", Enabled: true, Priority: 2},
	}
	config.AppConfig.WriteBackMetadata = false

	// 3. All mock sources return empty
	olServer := testutil.MockOpenLibraryServer(t, map[string]string{
		"search.json": testutil.OpenLibraryEmptyResponse,
	})
	defer olServer.Close()
	t.Setenv("OPENLIBRARY_BASE_URL", olServer.URL)

	gbEmptyResponse := `{"totalItems": 0, "items": []}`
	gbServer := mockHTTPServer(t, map[string]string{
		"volumes": gbEmptyResponse,
	}, 0)
	defer gbServer.Close()
	t.Setenv("GOOGLE_BOOKS_BASE_URL", gbServer.URL)

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
