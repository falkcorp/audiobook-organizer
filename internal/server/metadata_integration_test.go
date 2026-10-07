// file: internal/server/metadata_integration_test.go
// version: 1.2.1
// guid: a7b8c9d0-e1f2-3456-abcd-789012345ef0
// last-edited: 2026-10-06

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// useOnlyOpenLibrary sets config to only use Open Library as metadata source,
// pointed at baseURL, and returns a cleanup function to restore the original
// config. baseURL is set directly on the MetadataSource rather than via
// t.Setenv + config.InitConfig(): InitConfig would rebuild MetadataSources
// from viper defaults and discard this single-source override.
func useOnlyOpenLibrary(t *testing.T, baseURL string) {
	t.Helper()
	orig := config.AppConfig.MetadataSources
	config.AppConfig.MetadataSources = []config.MetadataSource{
		{ID: "openlibrary", Name: "Open Library", Enabled: true, Priority: 1, BaseURL: baseURL},
	}
	t.Cleanup(func() { config.AppConfig.MetadataSources = orig })
}

// useOnlyAudible sets config to only use a mock Audible as metadata source.
// Tests whose auto-fetch must APPLY a match use it: Open Library and Google
// Books are review-only (owner decision 2026-10-06) and are never applied by
// the auto-fetch.
func useOnlyAudible(t *testing.T, baseURL string) {
	t.Helper()
	orig := config.AppConfig.MetadataSources
	config.AppConfig.MetadataSources = []config.MetadataSource{
		{ID: "audible", Name: "Audible", Enabled: true, Priority: 1, BaseURL: baseURL},
	}
	t.Cleanup(func() { config.AppConfig.MetadataSources = orig })
}

// audibleAnswersTitle is a MockAudibleServer answer: p for any of titles.
func audibleAnswersTitle(p testutil.AudibleTestProduct, titles ...string) func(string) []testutil.AudibleTestProduct {
	return func(title string) []testutil.AudibleTestProduct {
		for _, want := range titles {
			if title == want {
				return []testutil.AudibleTestProduct{p}
			}
		}
		return nil
	}
}

// An Open Library match is fetched but never applied by the auto-fetch
// (owner decision 2026-10-06): the book is left untouched for review.
func TestMetadataFetch_OpenLibraryMatchIsNotApplied(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	mockServer := testutil.MockOpenLibraryServer(t, map[string]string{
		"search.json": testutil.OpenLibraryHobbitResponse,
	})
	defer mockServer.Close()
	useOnlyOpenLibrary(t, mockServer.URL)

	created, err := env.Store.CreateBook(&database.Book{Title: "The Hobbit", FilePath: "/fake/hobbit.m4b", Format: "m4b"})
	require.NoError(t, err)

	_, err = metafetch.NewService(env.Store).FetchMetadataForBook(context.Background(), created.ID)
	require.ErrorIs(t, err, metafetch.ErrReviewOnlyCandidatesNotApplied)
	updated, err := env.Store.GetBookByID(created.ID)
	require.NoError(t, err)
	assert.Nil(t, updated.Publisher, "an Open Library match must not be applied")
}

func TestMetadataFetch_WithMockAPI(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	mockServer := testutil.MockAudibleServer(t, audibleAnswersTitle(testutil.AudibleHobbitProduct, "The Hobbit"))
	useOnlyAudible(t, mockServer.URL)

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
	defer cleanup()

	callCount := 0
	// Title-only search: stripped title "The Hobbit" should match.
	answer := audibleAnswersTitle(testutil.AudibleHobbitProduct, "The Hobbit")
	mockServer := testutil.MockAudibleServer(t, func(title string) []testutil.AudibleTestProduct {
		callCount++
		return answer(title)
	})
	useOnlyAudible(t, mockServer.URL)

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
	defer cleanup()

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testutil.OpenLibraryEmptyResponse))
	}))
	defer mockServer.Close()

	useOnlyOpenLibrary(t, mockServer.URL)

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
