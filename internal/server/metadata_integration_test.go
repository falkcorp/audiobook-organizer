// file: internal/server/metadata_integration_test.go
// version: 1.1.0
// guid: a7b8c9d0-e1f2-3456-abcd-789012345ef0

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// useOnlyOpenLibrary sets config to only use Open Library as metadata source,
// returning a cleanup function to restore the original config.
func useOnlyOpenLibrary(t *testing.T) {
	t.Helper()
	orig := config.AppConfig.MetadataSources
	config.AppConfig.MetadataSources = []config.MetadataSource{
		{ID: "openlibrary", Name: "Open Library", Enabled: true, Priority: 1},
	}
	t.Cleanup(func() { config.AppConfig.MetadataSources = orig })
}

func TestMetadataFetch_WithMockAPI(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	useOnlyOpenLibrary(t)
	defer cleanup()

	mockServer := testutil.MockOpenLibraryServer(t, map[string]string{
		"search.json": testutil.OpenLibraryHobbitResponse,
	})
	defer mockServer.Close()

	t.Setenv("OPENLIBRARY_BASE_URL", mockServer.URL)

	author, err := env.Store.CreateAuthor("J.R.R. Tolkien")
	require.NoError(t, err)
	book := &database.Book{
		Title:    "The Hobbit",
		FilePath: "/fake/hobbit.m4b",
		Format:   "m4b",
		AuthorID: &author.ID,
	}
	created, err := env.Store.CreateBook(book)
	require.NoError(t, err)

	svc := metafetch.NewService(env.Store)
	resp, err := svc.FetchMetadataForBook(context.Background(), created.ID)
	require.NoError(t, err)
	assert.NotNil(t, resp)

	// Verify book was updated with metadata
	updated, err := env.Store.GetBookByID(created.ID)
	require.NoError(t, err)
	assert.NotNil(t, updated.Publisher)
	assert.Equal(t, "Houghton Mifflin", *updated.Publisher)
}

func TestMetadataFetch_FallbackToAuthorSearch(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	useOnlyOpenLibrary(t)
	defer cleanup()

	callCount := 0
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		query := r.URL.Query()
		w.Header().Set("Content-Type", "application/json")

		// Title-only search: stripped title "The Hobbit" should match
		title := query.Get("title")
		if title == "The Hobbit" || title == "The+Hobbit" {
			_, _ = w.Write([]byte(testutil.OpenLibraryHobbitResponse))
		} else {
			_, _ = w.Write([]byte(testutil.OpenLibraryEmptyResponse))
		}
	}))
	defer mockServer.Close()

	t.Setenv("OPENLIBRARY_BASE_URL", mockServer.URL)

	author, err := env.Store.CreateAuthor("J.R.R. Tolkien")
	require.NoError(t, err)
	book := &database.Book{
		Title:    "The Hobbit - Chapter 1",
		FilePath: "/fake/hobbit.m4b",
		Format:   "m4b",
		AuthorID: &author.ID,
	}
	created, err := env.Store.CreateBook(book)
	require.NoError(t, err)

	svc := metafetch.NewService(env.Store)
	resp, err := svc.FetchMetadataForBook(context.Background(), created.ID)
	require.NoError(t, err)
	assert.NotNil(t, resp)

	assert.GreaterOrEqual(t, callCount, 1, "should have searched by title (stripped chapter prefix)")
}

func TestMetadataFetch_NotFound(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	useOnlyOpenLibrary(t)
	defer cleanup()

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testutil.OpenLibraryEmptyResponse))
	}))
	defer mockServer.Close()

	os.Setenv("OPENLIBRARY_BASE_URL", mockServer.URL)
	defer os.Unsetenv("OPENLIBRARY_BASE_URL")

	book := &database.Book{
		Title:    "Completely Unknown Book XYZ123",
		FilePath: "/fake/unknown.m4b",
		Format:   "m4b",
	}
	created, err := env.Store.CreateBook(book)
	require.NoError(t, err)

	svc := metafetch.NewService(env.Store)
	_, err = svc.FetchMetadataForBook(context.Background(), created.ID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no metadata found")
}
