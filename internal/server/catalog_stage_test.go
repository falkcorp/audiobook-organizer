// file: internal/server/catalog_stage_test.go
// version: 1.3.0
// guid: e9c34a70-5b18-4d2f-86a1-3d0f7b92c584
// last-edited: 2026-10-13

package server

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/time/rate"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// silentAudible is an Audible that answers nothing, counting the calls.
type silentAudible struct{ calls atomic.Int64 }

func (s *silentAudible) Name() string       { return "Audible" }
func (s *silentAudible) ProviderID() string { return metadata.SourceIDAudible }
func (s *silentAudible) SearchByTitle(context.Context, string) ([]metadata.BookMetadata, error) {
	s.calls.Add(1)
	return nil, nil
}
func (s *silentAudible) SearchByTitleAndAuthor(context.Context, string, string) ([]metadata.BookMetadata, error) {
	s.calls.Add(1)
	return nil, nil
}

// The whole path on a real store: a harvest fills the catalog (and its
// alias rows, then the end-of-run backfill), the matcher is installed the way
// NewServer installs it, and a candidate-fetch search of two books -- one
// credited as the catalog spells the author, one credited by a variant
// spelling -- returns review-only catalog candidates while the providers are
// asked exactly as before. The counters the op prints are read off the tally.
func TestCatalogStage_EndToEnd_CountersAndSummaryLine(t *testing.T) {
	withCatalogEnabled(t, true)
	ps := seedCatalogLibrary(t)
	cat := database.NewCatalogStoreFromStore(ps)
	rep := &catalogTestReporter{}
	if err := runCatalogHarvest(context.Background(), rep, ps, cat, &catalogFakeLister{}, catalogHarvestParams{}, false, config.AppConfig.Catalog); err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(rep.logs, "\n"); !strings.Contains(joined, "author aliases: 2 entries read") {
		t.Errorf("the harvest op did not report the alias backfill:\n%s", joined)
	}

	mkBook := func(title, credit string) string {
		t.Helper()
		b, err := ps.CreateBook(&database.Book{Title: title, FilePath: "/lib/" + title})
		if err != nil {
			t.Fatal(err)
		}
		a, err := ps.CreateAuthor(credit)
		if err != nil {
			t.Fatal(err)
		}
		if err := ps.SetBookAuthors(b.ID, []database.BookAuthor{{BookID: b.ID, AuthorID: a.ID, Role: "author"}}); err != nil {
			t.Fatal(err)
		}
		return b.ID
	}
	exact := mkBook("Second", "Ann Author")
	variant := mkBook("First", "A. Author")

	prev := config.AppConfig.MetadataFetch
	t.Cleanup(func() { config.AppConfig.MetadataFetch = prev })
	config.AppConfig.MetadataFetch = config.DefaultMetadataFetchConfig()
	config.AppConfig.MetadataFetch.SharedCache = false

	audibleCalls := func(stageOn bool) int64 {
		config.AppConfig.MetadataFetch.CatalogStage = stageOn
		aud := &silentAudible{}
		svc := metafetch.NewService(ps)
		svc.SetOverrideSources([]metadata.MetadataSource{aud})
		// The production wiring: NewServer passes the raw observed Pebble store
		// (07-S3 removed the indexed decorator).
		wireCatalogStage(svc, ps)
		stats := &metafetch.CatalogStageStats{}
		ctx := metafetch.WithCatalogStageStats(context.Background(), stats)
		lim := rate.NewLimiter(rate.Inf, 1)
		var got []string
		for _, id := range []string{exact, variant} {
			_, resp, err := svc.FetchAndCacheWithResponse(ctx, lim, id, "", "", "", "", metafetch.SearchOptions{BypassFetchCache: true})
			if err != nil {
				t.Fatalf("search %s: %v", id, err)
			}
			for _, c := range resp.Results {
				got = append(got, c.Source+"/"+c.Title+"/"+c.Stage)
			}
		}
		tally := &candidateFetchTally{stage: stats}
		t.Logf("stage=%v summary counts: %s", stageOn, tally.counts())
		if stageOn {
			want := "Audible Catalog (local)/Second/B2,Audible Catalog (local)/First/B2"
			if strings.Join(got, ",") != want {
				t.Errorf("results = %v; want the two catalog candidates %s", got, want)
			}
			wantLine := "catalog_matched 2, catalog_candidates 2, provider_candidates 0, oversize_block 0, homonym_skipped 0"
			if !strings.Contains(tally.counts(), wantLine) {
				t.Errorf("summary counts %q lack %q", tally.counts(), wantLine)
			}
			if !metafetch.IsReviewOnlyCandidateSource(metadata.CatalogSourceName) {
				t.Error("the catalog source is not review-only")
			}
		} else if len(got) != 0 || stats.CatalogCandidates.Load() != 0 {
			t.Errorf("stage off: results %v, catalog candidates %d; want none", got, stats.CatalogCandidates.Load())
		}
		return aud.calls.Load()
	}
	off, on := audibleCalls(false), audibleCalls(true)
	if off == 0 || off != on {
		t.Errorf("provider calls: stage off %d, stage on %d; the stage must not change them", off, on)
	}
	t.Logf("provider calls for the two books: stage off %d, stage on %d", off, on)
}

func TestCandidateFetchTally_CountsLineCarriesTheStageCounters(t *testing.T) {
	stats := &metafetch.CatalogStageStats{}
	stats.CatalogMatched.Store(14)
	stats.CatalogCandidates.Store(12)
	stats.ProviderCandidates.Store(2400)
	stats.OversizeBlocks.Store(3)
	stats.HomonymSkipped.Store(1)
	tally := &candidateFetchTally{stage: stats}
	want := "matched 0, no match 0, skipped 0, deferred 0, errors 0, from cache 0, known empty 0, " +
		"catalog_matched 14, catalog_candidates 12, provider_candidates 2400, oversize_block 3, homonym_skipped 1"
	if got := tally.counts(); got != want {
		t.Errorf("counts = %q; want %q", got, want)
	}
	if got := (&candidateFetchTally{}).counts(); !strings.HasSuffix(got, "catalog_matched 0, catalog_candidates 0, provider_candidates 0, oversize_block 0, homonym_skipped 0") {
		t.Errorf("a tally with no stage counters reads zeros: %q", got)
	}
}
