// file: internal/metabatch/upgrade_rank_test.go
// version: 1.2.0
// guid: 5e0b7c3a-91d4-4f6e-8a2b-c7d1e9f40a63
// last-edited: 2026-09-28
//
// Owner decision 2026-09-27: the nightly metadata upgrade REPLACES a book's
// metadata when it came from a lower-ranked source and a strictly
// higher-ranked source's candidate passes the bulk-apply gate. These tests
// drive tryUpgradeBook / RunUpgrade with a stubbed search and the REAL apply
// (metafetch.Service over a MockStore), so what they assert is what the
// book row ends up holding.

package metabatch

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/stretchr/testify/require"
)

// rankFetcher stubs the search and records every apply before handing it to
// the real metafetch apply.
type rankFetcher struct {
	svc      *metafetch.Service
	results  []metafetch.MetadataCandidate
	mu       sync.Mutex
	searches int
	ids      []string
	applied  []metafetch.ApplyOptions
	sources  []string
}

func (f *rankFetcher) SearchMetadataForBook(id string, _ string, _ ...string) (*metafetch.SearchMetadataResponse, error) {
	f.mu.Lock()
	f.searches++
	f.ids = append(f.ids, id)
	f.mu.Unlock()
	return &metafetch.SearchMetadataResponse{Results: f.results}, nil
}

func (f *rankFetcher) ApplyMetadataCandidateWithOptions(id string, c metafetch.MetadataCandidate, fields []string, opts metafetch.ApplyOptions) (*metafetch.FetchMetadataResponse, error) {
	f.mu.Lock()
	f.applied = append(f.applied, opts)
	f.sources = append(f.sources, c.Source)
	f.mu.Unlock()
	return f.svc.ApplyMetadataCandidateWithOptions(id, c, fields, opts)
}

// rankFixture is a book whose metadata came from Open Library: a filled
// description and publisher, a path naming the author (one gate agreement)
// and a title the candidates match (the second).
type rankFixture struct {
	store   *database.MockStore
	book    *database.Book
	mu      sync.Mutex
	updated *database.Book
	locks   []database.MetadataFieldState
	// sourceTag is the book's metadata:source:* system tag, as the apply's
	// EnsureSingletonBookTag leaves it.
	sourceTag string
	// seriesName and files feed the owner-manual-only check.
	seriesName string
	files      []string
}

func (f *rankFixture) tag() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sourceTag
}

func newRankFixture() *rankFixture {
	f := &rankFixture{book: &database.Book{
		ID:          "rank-1",
		Title:       "Project Hail Mary",
		FilePath:    "/library/Andy Weir/Project Hail Mary/Project Hail Mary.m4b",
		Description: new("Open Library's description"),
		Publisher:   new("Open Library Publisher"),
	}, sourceTag: "metadata:source:open_library"}
	f.store = &database.MockStore{
		GetBookByIDFunc: func(string) (*database.Book, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			src := f.book
			if f.updated != nil {
				src = f.updated
			}
			clone := *src
			return &clone, nil
		},
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			cp := *b
			f.updated = &cp
			return &cp, nil
		},
		GetMetadataFieldStatesFunc: func(string) ([]database.MetadataFieldState, error) {
			return f.locks, nil
		},
		GetBookTagsDetailedFunc: func(string) ([]database.BookTag, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return []database.BookTag{{Tag: f.sourceTag, Source: "system"}}, nil
		},
		RemoveBookTagsByPrefixFunc: func(_, prefix, _ string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if strings.HasPrefix(f.sourceTag, prefix) {
				f.sourceTag = ""
			}
			return nil
		},
		AddBookTagWithSourceFunc: func(_, tag, _ string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if strings.HasPrefix(tag, "metadata:source:") {
				f.sourceTag = tag
			}
			return nil
		},
		GetSeriesByIDFunc: func(id int) (*database.Series, error) {
			return &database.Series{ID: id, Name: f.seriesName}, nil
		},
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			var out []database.BookFile
			for _, p := range f.files {
				out = append(out, database.BookFile{BookID: bookID, FilePath: p})
			}
			return out, nil
		},
		GetBooksByTagFunc: func(tag string) ([]string, error) {
			if tag == "metadata:source:open_library" {
				return []string{"rank-1"}, nil
			}
			return nil, nil
		},
	}
	return f
}

func (f *rankFixture) written() *database.Book {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.updated
}

func (f *rankFixture) service(results ...metafetch.MetadataCandidate) (*MetadataUpgradeService, *rankFetcher) {
	fetcher := &rankFetcher{svc: metafetch.NewService(f.store), results: results}
	return &MetadataUpgradeService{DB: f.store, Fetcher: fetcher}, fetcher
}

// candidate builds a gate-passing candidate from the named provider (its real
// display name, as metadata.MetadataSource.Name() returns it).
func candidate(source string, score float64) metafetch.MetadataCandidate {
	return metafetch.MetadataCandidate{
		Title:       "Project Hail Mary",
		Author:      "Andy Weir",
		Description: source + "'s description",
		Publisher:   source + " Publisher",
		Score:       score,
		Source:      source,
	}
}

// The owner's case: an Open Library book, an Audible candidate that passes
// the gate. The filled description and publisher are REPLACED, and the apply
// is the automatic replace path (FillOnly false, UnseenCandidate, labeled).
func TestTryUpgradeBook_OpenLibraryToAudibleReplacesFilledFields(t *testing.T) {
	f := newRankFixture()
	svc, fetcher := f.service(candidate("Audible", 0.95))

	outcome, err := svc.tryUpgradeBook(context.Background(), "rank-1", "open_library")
	upgraded := outcome.Upgraded
	require.NoError(t, err)
	require.True(t, upgraded, "an Audible candidate that passes the gate upgrades an Open Library book")

	got := f.written()
	require.NotNil(t, got, "the apply committed a row")
	require.NotNil(t, got.Description)
	require.Equal(t, "Audible's description", *got.Description, "replace mode overwrites a filled description")
	require.NotNil(t, got.Publisher)
	require.Equal(t, "Audible Publisher", *got.Publisher)

	require.Len(t, fetcher.applied, 1)
	opts := fetcher.applied[0]
	require.False(t, opts.FillOnly, "a rank upgrade replaces, it does not only fill")
	require.True(t, opts.UnseenCandidate, "nobody picked the candidate: the automatic guards stay on")
	require.False(t, opts.OwnerReplace, "no person pressed anything; history must not say one did")
	require.Equal(t, "rank upgrade: open_library -> audible", opts.RankUpgrade)

	// The source tag moves to the candidate's, so the next run does not visit
	// (and overwrite) this book again.
	require.Equal(t, "metadata:source:audible", f.tag())
}

// A locked TITLE means the book keeps its own title, so the automatic apply
// does not record the match (UnseenCandidate). The source tag must still
// move, or the book would be re-searched and re-overwritten every night.
func TestTryUpgradeBook_LockedTitleStillRetagsSource(t *testing.T) {
	f := newRankFixture()
	locked := `"My Curated Title"`
	f.locks = []database.MetadataFieldState{{Field: database.FieldKeyTitle, OverrideValue: &locked}}
	c := candidate("Audible", 0.95)
	c.Title = "Project Hail Mary: A Novel"
	svc, _ := f.service(c)

	outcome, err := svc.tryUpgradeBook(context.Background(), "rank-1", "open_library")
	upgraded := outcome.Upgraded
	require.NoError(t, err)
	require.True(t, upgraded)
	require.Equal(t, "Project Hail Mary", f.written().Title, "the locked title is kept")
	require.Equal(t, "metadata:source:audible", f.tag())
}

// The same book through RunUpgrade: the open_library tag list is visited
// (open_library is now a low-quality source) and the book is counted upgraded.
func TestRunUpgrade_VisitsOpenLibraryBooks(t *testing.T) {
	f := newRankFixture()
	svc, _ := f.service(candidate("Audnexus (Audible)", 0.95))

	res, err := svc.RunUpgrade(context.Background(), 200, nil)
	require.NoError(t, err)
	require.Equal(t, [2]int{1, 1}, [2]int{res.Checked, res.Upgraded})
	require.Equal(t, "Audnexus (Audible)'s description", *f.written().Description)
}

// The worker pool: many books in one run, every one counted exactly once.
// Run under -race this exercises the shared result counters.
func TestRunUpgrade_PoolCountsEveryBook(t *testing.T) {
	f := newRankFixture()
	// Distinct IDs so the de-duplication keeps them all; the store answers
	// every ID with the same fixture book.
	ids := make([]string, 30)
	for i := range ids {
		ids[i] = fmt.Sprintf("rank-%d", i)
	}
	f.store.GetBooksByTagFunc = func(tag string) ([]string, error) {
		if tag == "metadata:source:open_library" {
			return append(ids, ids[0]), nil // one duplicate, dropped
		}
		return nil, nil
	}
	svc, fetcher := f.service(candidate("Audible", 0.95))
	res, err := svc.RunUpgrade(context.Background(), 200, nil)
	require.NoError(t, err)
	require.Equal(t, [3]int{30, 30, 0}, [3]int{res.Checked, res.Upgraded, res.Errors})
	require.Len(t, fetcher.applied, 30)
}

// An Audible book is never "upgraded" to a lower-ranked source, however well
// the candidate scores, and Audnexus (the same rank) does not replace it
// either. Driven through tryUpgradeBook with the current source "audible",
// because RunUpgrade never visits Audible books at all.
func TestTryUpgradeBook_NeverDowngradesOrSideGrades(t *testing.T) {
	for _, current := range []string{"audible", "audnexus"} {
		t.Run(current, func(t *testing.T) {
			f := newRankFixture()
			svc, fetcher := f.service(
				candidate("Open Library", 0.99),
				candidate("Google Books", 0.99),
				candidate("Hardcover", 0.99),
				candidate("Audible", 0.99),
				candidate("Audnexus (Audible)", 0.99),
			)
			outcome, err := svc.tryUpgradeBook(context.Background(), "rank-1", current)
			upgraded := outcome.Upgraded
			require.NoError(t, err)
			require.False(t, upgraded)
			require.Empty(t, fetcher.applied, "no candidate outranks %s", current)
			require.Nil(t, f.written(), "nothing was written")
		})
	}
	for _, slug := range LowQualitySources {
		require.NotContains(t, []string{"audible", "audnexus", "hardcover"}, slug)
	}
}

// Among candidates that pass the gate, the highest RANK wins, not the best
// score: an Open Library book lands on Audible, not on a better-scoring
// Hardcover candidate (hardcover books are never revisited).
func TestTryUpgradeBook_HighestRankBeatsBetterScore(t *testing.T) {
	f := newRankFixture()
	svc, fetcher := f.service(candidate("Hardcover", 0.99), candidate("Audible", 0.92))
	outcome, err := svc.tryUpgradeBook(context.Background(), "rank-1", "open_library")
	upgraded := outcome.Upgraded
	require.NoError(t, err)
	require.True(t, upgraded)
	require.Equal(t, []string{"Audible"}, fetcher.sources)
}

// A field the user locked is kept on a replace, while the unlocked field in
// the same apply is replaced.
func TestTryUpgradeBook_ReplaceKeepsUserLockedFields(t *testing.T) {
	f := newRankFixture()
	locked := `"Open Library's description"`
	f.locks = []database.MetadataFieldState{{Field: database.FieldKeyDescription, OverrideValue: &locked}}
	svc, _ := f.service(candidate("Audible", 0.95))

	outcome, err := svc.tryUpgradeBook(context.Background(), "rank-1", "open_library")
	upgraded := outcome.Upgraded
	require.NoError(t, err)
	require.True(t, upgraded)
	got := f.written()
	require.NotNil(t, got)
	require.Equal(t, "Open Library's description", *got.Description, "the locked description is kept")
	require.Equal(t, "Audible Publisher", *got.Publisher, "the unlocked publisher in the same apply is replaced")
}

// A candidate the gate refuses (score under the 0.90 floor) applies nothing,
// even from the highest-ranked source. The passing case above is the
// contrast: the same fixture with a passing score writes.
func TestTryUpgradeBook_GateRefusalAppliesNothing(t *testing.T) {
	f := newRankFixture()
	svc, fetcher := f.service(candidate("Audible", 0.80))
	outcome, err := svc.tryUpgradeBook(context.Background(), "rank-1", "open_library")
	upgraded := outcome.Upgraded
	require.NoError(t, err)
	require.False(t, upgraded)
	require.Empty(t, fetcher.applied)
	require.Nil(t, f.written())
}

// A book marked "no match" by the owner is skipped before the search.
func TestTryUpgradeBook_SkipsNoMatchBook(t *testing.T) {
	f := newRankFixture()
	f.book.MetadataReviewStatus = new("no_match")
	svc, fetcher := f.service(candidate("Audible", 0.95))
	outcome, err := svc.tryUpgradeBook(context.Background(), "rank-1", "open_library")
	upgraded := outcome.Upgraded
	require.NoError(t, err)
	require.False(t, upgraded)
	require.Zero(t, fetcher.searches)
	require.Nil(t, f.written())
}

// An unranked current source is never replaced; the search is not even run.
func TestTryUpgradeBook_UnrankedCurrentSourceIsNotReplaced(t *testing.T) {
	f := newRankFixture()
	svc, fetcher := f.service(candidate("Audible", 0.99))
	outcome, err := svc.tryUpgradeBook(context.Background(), "rank-1", "some_new_provider")
	upgraded := outcome.Upgraded
	require.NoError(t, err)
	require.False(t, upgraded)
	require.Zero(t, fetcher.searches)
}

// LowQualitySources is derived from the rank table: open_library is in it,
// hardcover is not, and the owner's case is visited first.
func TestLowQualitySources_DerivedFromRankTable(t *testing.T) {
	want := []string{"open_library", "google_books", "wikipedia"}
	if !reflect.DeepEqual(LowQualitySources, want) {
		t.Fatalf("LowQualitySources = %v, want %v", LowQualitySources, want)
	}
	for _, slug := range LowQualitySources {
		if strings.Contains(slug, " ") {
			t.Errorf("%q is not a tag slug", slug)
		}
	}
}
