// file: internal/metafetch/review_only_autofetch_test.go
// version: 1.0.0
// guid: b881bf7b-f075-494b-b50e-cdc671118002
// last-edited: 2026-10-06

package metafetch

import (
	"context"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/stretchr/testify/require"
)

// Owner decision 2026-10-06, "fetch but don't apply": the auto-fetch
// (FetchMetadataForBook, FetchMetadataForBookByTitle) applies with nobody
// picking the candidate, so an Open Library or Google Books match is
// searched and cached for review but never applied. All fixtures are
// synthetic.

const autoFetchTitle = "Quiet Harbor Lights"

func autoFetchFixture(t *testing.T, sources ...metadata.MetadataSource) (*verdictFixture, string) {
	t.Helper()
	f := newVerdictFixture(t)
	b, err := f.store.CreateBook(&database.Book{Title: autoFetchTitle, FilePath: "/lib/qhl/book.m4b"})
	require.NoError(t, err)
	f.mfs.SetOverrideSources(sources)
	return f, b.ID
}

func autoFetchRecord(desc string) []metadata.BookMetadata {
	return []metadata.BookMetadata{{Title: autoFetchTitle, Author: "Synthetic Penwright", Description: desc}}
}

// A review-only source ranked first in the chain is skipped; the Audible
// match after it is applied.
func TestAutoFetch_ReviewOnlyFirstThenAudibleAppliesAudible(t *testing.T) {
	for _, name := range []string{"Open Library", "Google Books"} {
		t.Run(name, func(t *testing.T) {
			ro := &verdictSource{name: name, results: autoFetchRecord("review-only description")}
			aud := &verdictSource{name: "Audible", results: autoFetchRecord("audible description")}
			for _, fetch := range []struct {
				label string
				run   func(*Service, string) (*FetchMetadataResponse, error)
			}{
				{"FetchMetadataForBook", func(s *Service, id string) (*FetchMetadataResponse, error) {
					return s.FetchMetadataForBook(context.Background(), id)
				}},
				{"FetchMetadataForBookByTitle", func(s *Service, id string) (*FetchMetadataResponse, error) {
					return s.FetchMetadataForBookByTitle(id)
				}},
			} {
				f, id := autoFetchFixture(t, ro, aud)
				resp, err := fetch.run(f.mfs, id)
				require.NoError(t, err, fetch.label)
				require.Equal(t, "Audible", resp.Source, fetch.label)
				book := f.book(id)
				require.NotNil(t, book.Description, fetch.label)
				require.Equal(t, "audible description", *book.Description, fetch.label)
				require.Positive(t, ro.calls.Load(), "%s: the review-only source must still be searched", fetch.label)
			}
		})
	}
}

// Only review-only sources match: nothing is applied, the error says why,
// and FetchMetadataForBook's answer is cached for review.
func TestAutoFetch_OnlyReviewOnlyMatchesAppliesNothing(t *testing.T) {
	for _, name := range []string{"Open Library", "Google Books"} {
		t.Run(name, func(t *testing.T) {
			ro := &verdictSource{name: name, results: autoFetchRecord("review-only description")}
			f, id := autoFetchFixture(t, &verdictSource{name: "Audible"}, ro)
			before := f.book(id)

			_, err := f.mfs.FetchMetadataForBook(context.Background(), id)
			require.ErrorIs(t, err, ErrReviewOnlyCandidatesNotApplied)
			after := f.book(id)
			require.Nil(t, after.Description, "nothing may be written")
			require.Equal(t, before.Title, after.Title)
			require.Nil(t, after.MetadataReviewStatus)

			cached, _, cerr := database.CachedMetadataForProvider(f.store, id, metadata.ProviderIDOf(ro), ro.Name(),
				f.mfs.fetchCacheIdentity(after), 24*time.Hour)
			require.NoError(t, cerr)
			require.NotNil(t, cached, "the review-only answer must be cached for review")

			_, err = f.mfs.FetchMetadataForBookByTitle(id)
			require.ErrorIs(t, err, ErrReviewOnlyCandidatesNotApplied)
			require.Nil(t, f.book(id).Description)
		})
	}
}
