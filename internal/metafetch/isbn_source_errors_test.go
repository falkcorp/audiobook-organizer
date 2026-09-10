// file: internal/metafetch/isbn_source_errors_test.go
// version: 1.0.0
// guid: 6f6c2d71-a97b-4a17-ac0a-23524632ec1a
// last-edited: 2026-09-10

package metafetch

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// erroringSource always fails both search calls, simulating a
// throttled/circuit-open provider -- ProtectedSource.SearchByTitle and
// SearchByTitleAndAuthor return a real error in that case (see
// internal/metadata/circuitbreaker.go's allowThrottle/AllowRequest gates).
type erroringSource struct {
	name string
	err  error
}

func (s erroringSource) Name() string {
	if s.name == "" {
		return "Audible"
	}
	return s.name
}
func (s erroringSource) SearchByTitle(context.Context, string) ([]metadata.BookMetadata, error) {
	return nil, s.err
}
func (s erroringSource) SearchByTitleAndAuthor(context.Context, string, string) ([]metadata.BookMetadata, error) {
	return nil, s.err
}

// TestEnrichBookISBN_AllSourcesErrored_DistinguishesFromZeroResult is the
// regression guard for SF-03 (isbn.go discarded every provider search error
// at `results, _ = src.Search...`, so a throttled/circuit-open source
// rendered identically to a genuine "no such book" search -- isbn/asin=="",
// no error, either way). It fails against the pre-fix code because
// EnrichBookISBN's returned error there is always nil for a search failure,
// so the first t.Run's `err == nil` check fails.
func TestEnrichBookISBN_AllSourcesErrored_DistinguishesFromZeroResult(t *testing.T) {
	breakerErr := errors.New("circuit breaker open: external metadata source is unavailable")

	t.Run("every_source_errors_is_reported_as_an_error_naming_the_source", func(t *testing.T) {
		mock := &database.MockStore{
			GetBookByIDFunc: func(id string) (*database.Book, error) {
				return &database.Book{ID: id, Title: "Mistborn"}, nil
			},
			GetMetadataFieldStatesFunc: func(string) ([]database.MetadataFieldState, error) { return nil, nil },
		}
		svc := NewISBNService(mock, []metadata.MetadataSource{erroringSource{err: breakerErr}})

		found, err := svc.EnrichBookISBN(context.Background(), "b1")
		if found {
			t.Fatalf("expected found=false when every source errored, got true")
		}
		if err == nil {
			t.Fatalf("expected a non-nil error distinguishing a provider outage from a genuine zero-result search, got nil")
		}
		var se *sourceSearchError
		if !errors.As(err, &se) {
			t.Fatalf("expected a *sourceSearchError, got %T: %v", err, err)
		}
		if !se.allErrored {
			t.Fatalf("expected allErrored=true when every source call errored, got false (bySource=%v)", se.bySource)
		}
		if se.bySource["Audible"] == 0 {
			t.Fatalf("expected the error to be recorded per source, got bySource=%v", se.bySource)
		}
	})

	t.Run("genuine_zero_result_is_not_reported_as_an_error", func(t *testing.T) {
		mock := &database.MockStore{
			GetBookByIDFunc: func(id string) (*database.Book, error) {
				return &database.Book{ID: id, Title: "Mistborn"}, nil
			},
			GetMetadataFieldStatesFunc: func(string) ([]database.MetadataFieldState, error) { return nil, nil },
		}
		svc := NewISBNService(mock, []metadata.MetadataSource{noHitSource{}})

		found, err := svc.EnrichBookISBN(context.Background(), "b1")
		if found {
			t.Fatalf("expected found=false for a genuine zero-result search, got true")
		}
		if err != nil {
			t.Fatalf("expected a genuine zero-result search to return a nil error (distinct from an all-sources-errored search), got %v", err)
		}
	})
}

// TestEnrichMissingISBNs_AllSourcesErrored_ReportsFailedRun is the
// batch-level counterpart of the regression above: a run where every
// attempted book has every source error must come back as a failed op via
// the returned error (both existing callers, internal/scheduler/extra_ops.go
// and internal/server/metadata_ops.go, already do `if err != nil { return
// err }`, propagating it to their reporter), and must not count those books
// as "checked" in the returned/logged summary -- the same distinction
// TestEnrichBookISBN_AllSourcesErrored_DistinguishesFromZeroResult proves at
// the single-book level.
func TestEnrichMissingISBNs_AllSourcesErrored_ReportsFailedRun(t *testing.T) {
	breakerErr := errors.New("circuit breaker open: external metadata source is unavailable")
	books := []database.Book{candidateBook("b1", "A"), candidateBook("b2", "B")}

	t.Run("all_errored_reports_failed_run_and_zero_checked", func(t *testing.T) {
		store, _, _ := cursorFixture(t, books)
		svc := NewISBNService(store, []metadata.MetadataSource{erroringSource{err: breakerErr}})

		checked, updated, err := svc.EnrichMissingISBNs(context.Background(), 100, nil, "op-errored")
		if err == nil {
			t.Fatalf("expected a run-level error when every attempted book had every source error, got nil")
		}
		if !errors.Is(err, ErrAllSourcesErrored) {
			t.Fatalf("expected error to wrap ErrAllSourcesErrored, got %v", err)
		}
		if checked != 0 {
			t.Fatalf("expected checked=0 (every book errored, none genuinely checked), got %d", checked)
		}
		if updated != 0 {
			t.Fatalf("expected updated=0, got %d", updated)
		}
	})

	t.Run("genuine_zero_result_run_reports_success_with_a_real_checked_count", func(t *testing.T) {
		store, _, _ := cursorFixture(t, books)
		svc := NewISBNService(store, []metadata.MetadataSource{noHitSource{}})

		checked, updated, err := svc.EnrichMissingISBNs(context.Background(), 100, nil, "op-clean")
		if err != nil {
			t.Fatalf("expected a genuine zero-result run to succeed, got error: %v", err)
		}
		if checked != len(books) {
			t.Fatalf("expected checked=%d for a genuine zero-result run, got %d", len(books), checked)
		}
		if updated != 0 {
			t.Fatalf("expected updated=0, got %d", updated)
		}
	})
}
