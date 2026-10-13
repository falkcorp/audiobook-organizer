// file: internal/catalog/scenario_test.go
// version: 1.0.0
// guid: 4d8b0e26-9c71-4f35-a2b8-6e1f3a05d947
// last-edited: 2026-10-12

package catalog

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// The 02-PR7b measurement: 5,000 unidentified books against a catalog of 800
// author blocks of about 100 titles each (plus three franchise blocks over the
// cap and five homonym authors), searched through the real metadata search
// with a counting fake Audible, with the catalog stage off, off with no
// matcher installed, and on. Synthetic throughout.
//
// Shape of the library (by book index mod 25):
//
//	0..15  plain title of a harvested author's book; the fake Audible answers
//	       it, so the catalog's candidate duplicates the provider's
//	16..19 series-decorated title ("Series 05, Book 3 - Title 012034") of a
//	       harvested author's book, which the fake Audible cannot answer
//	20..21 a book of an author the catalog never harvested
//	22..24 a book of a franchise author whose block is over the cap
const (
	scenarioBooks      = 5000
	scenarioAuthors    = 800
	scenarioPerAuthor  = 100
	scenarioHomonymAut = 5 // authors 0..4 carry two provider author ids
	scenarioFranchise  = 3
)

type scenarioAnswerer struct {
	calls atomic.Int64
	// byTitle: exact query title -> the provider's answer (only even-numbered
	// titles are on "Audible").
	byTitle map[string]metadata.BookMetadata
}

func (s *scenarioAnswerer) Name() string       { return "Audible" }
func (s *scenarioAnswerer) ProviderID() string { return metadata.SourceIDAudible }
func (s *scenarioAnswerer) ask(title string) []metadata.BookMetadata {
	s.calls.Add(1)
	if a, ok := s.byTitle[title]; ok {
		return []metadata.BookMetadata{a}
	}
	return nil
}
func (s *scenarioAnswerer) SearchByTitle(_ context.Context, title string) ([]metadata.BookMetadata, error) {
	return s.ask(title), nil
}
func (s *scenarioAnswerer) SearchByTitleAndAuthor(_ context.Context, title, _ string) ([]metadata.BookMetadata, error) {
	return s.ask(title), nil
}

func scenarioTitle(author, n int) string { return fmt.Sprintf("Title %03d%03d", author, n) }
func scenarioAuthorName(i int) string    { return fmt.Sprintf("Author %03d", i) }

type scenarioBook struct {
	id, title string
	authorIdx int
	kind      string // plain | decorated | unharvested | franchise
}

func TestScenario_LocalCatalogStage_5000Books_800Blocks(t *testing.T) {
	if testing.Short() {
		t.Skip("5,000-book scenario")
	}
	db, err := pebble.Open("scenario.pebble", &pebble.Options{FS: vfs.NewMem()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	st := database.NewCatalogStore(database.NewGatedDB(db))

	// The catalog: 800 blocks of 100, five of them homonym blocks, and three
	// franchise blocks over MaxBlockEntries.
	provider := &scenarioAnswerer{byTitle: map[string]metadata.BookMetadata{}}
	var batch []metadata.CatalogProduct
	flush := func(key string) {
		if len(batch) == 0 {
			return
		}
		seedProducts(t, st, key, batch...)
		batch = batch[:0]
	}
	for a := range scenarioAuthors {
		name := scenarioAuthorName(a)
		for n := range scenarioPerAuthor {
			asin := fmt.Sprintf("B0S%03d%03d", a, n)
			authorASIN := fmt.Sprintf("AUTHS%03d", a)
			if a < scenarioHomonymAut && n%2 == 1 {
				authorASIN += "X" // a second person of the same name
			}
			batch = append(batch, synthProduct(asin, scenarioTitle(a, n), name, authorASIN))
			if n%2 == 0 { // on "Audible": exactly the catalog's product
				provider.byTitle[scenarioTitle(a, n)] = metadata.BookMetadata{
					Title: scenarioTitle(a, n), Author: name, ASIN: asin, CoverURL: "c", Narrator: "Narrator 03", DurationSec: 36000}
			}
		}
		flush(HarvestKey(name))
	}
	for f := range scenarioFranchise {
		name := fmt.Sprintf("Franchise %03d", f)
		for n := 0; n < MaxBlockEntries+1; n++ {
			batch = append(batch, synthProduct(fmt.Sprintf("B0F%03d%05d", f, n), fmt.Sprintf("Franchise Title %05d", n), name, fmt.Sprintf("AUTHF%03d", f)))
			if len(batch) == 1000 {
				flush(HarvestKey(name))
			}
		}
		flush(HarvestKey(name))
	}

	// The library.
	books := make([]scenarioBook, scenarioBooks)
	for i := range books {
		b := scenarioBook{id: fmt.Sprintf("book-%05d", i)}
		a := (i / 25 * 7) % scenarioAuthors
		switch k := i % 25; {
		case k <= 15:
			b.kind, b.authorIdx = "plain", a
			b.title = scenarioTitle(a, (i%50)*2) // even: on Audible
		case k <= 19:
			b.kind, b.authorIdx = "decorated", a
			b.title = fmt.Sprintf("Series 05, Book 3 - %s", scenarioTitle(a, (i%50)*2+1)) // odd: not on Audible
		case k <= 21:
			b.kind, b.authorIdx = "unharvested", scenarioAuthors+i%40
			b.title = scenarioTitle(b.authorIdx, i%90)
		default:
			b.kind, b.authorIdx = "franchise", scenarioAuthors+100+i%scenarioFranchise
			b.title = fmt.Sprintf("Franchise Title %05d", i%1000)
		}
		books[i] = b
	}
	nameOf := func(idx int) string {
		if idx >= scenarioAuthors+100 {
			return fmt.Sprintf("Franchise %03d", idx-scenarioAuthors-100)
		}
		return scenarioAuthorName(idx)
	}

	// The search service over a mock store holding the books.
	var rawMu sync.Mutex
	raw := map[string][]byte{}
	byID := map[string]*database.Book{}
	for i := range books {
		aid := books[i].authorIdx + 1
		byID[books[i].id] = &database.Book{ID: books[i].id, Title: books[i].title, AuthorID: &aid}
	}
	mock := &database.MockStore{
		GetBookByIDFunc:  func(id string) (*database.Book, error) { return byID[id], nil },
		GetBookFilesFunc: func(string) ([]database.BookFile, error) { return nil, nil },
		GetAuthorByIDFunc: func(id int) (*database.Author, error) {
			return &database.Author{ID: id, Name: nameOf(id - 1)}, nil
		},
		GetRawFunc: func(k string) ([]byte, error) { rawMu.Lock(); defer rawMu.Unlock(); return raw[k], nil },
		SetRawFunc: func(k string, v []byte) error { rawMu.Lock(); defer rawMu.Unlock(); raw[k] = v; return nil },
		DeleteRawFunc: func(k string) error {
			rawMu.Lock()
			defer rawMu.Unlock()
			delete(raw, k)
			return nil
		},
	}
	prev := config.AppConfig.MetadataFetch
	t.Cleanup(func() { config.AppConfig.MetadataFetch = prev })
	config.AppConfig.MetadataFetch = config.DefaultMetadataFetchConfig()
	config.AppConfig.MetadataFetch.SharedCache = false

	type outcome struct {
		providerCalls int64
		results       []string // per book: source/asin/stage of every candidate, in order
		stats         *metafetch.CatalogStageStats
	}
	run := func(matcher metafetch.CatalogMatcher, stageOn bool) outcome {
		aud := &scenarioAnswerer{byTitle: provider.byTitle}
		svc := metafetch.NewService(mock)
		svc.SetOverrideSources([]metadata.MetadataSource{aud})
		if matcher != nil {
			svc.SetCatalogMatcher(matcher)
		}
		config.AppConfig.MetadataFetch.CatalogStage = stageOn
		stats := &metafetch.CatalogStageStats{}
		ctx := metafetch.WithCatalogStageStats(context.Background(), stats)
		out := outcome{results: make([]string, len(books)), stats: stats}
		var g errgroup.Group
		g.SetLimit(runtime.NumCPU())
		for i := range books {
			g.Go(func() error {
				// The same entry the candidate fetch uses (a context, no limiter).
				_, resp, err := svc.FetchAndCacheWithResponse(ctx, nil, books[i].id, "", "", "", "", metafetch.SearchOptions{BypassFetchCache: true})
				if err != nil {
					return fmt.Errorf("%s: %w", books[i].id, err)
				}
				var parts []string
				for _, c := range resp.Results {
					parts = append(parts, c.Source+"/"+c.ASIN+"/"+c.Stage)
				}
				out.results[i] = strings.Join(parts, ";")
				return nil
			})
		}
		require.NoError(t, g.Wait())
		out.providerCalls = aud.calls.Load()
		return out
	}

	matcher := NewMatcher(st, func() string { return "us" }, nil)
	none := run(nil, false)
	off := run(matcher, false)
	on := run(matcher, true)

	// Flag off == today: the same provider calls and the same candidates as a
	// service with no matcher installed (the diff is empty).
	assert.Equal(t, none.providerCalls, off.providerCalls)
	diff := 0
	for i := range none.results {
		if none.results[i] != off.results[i] {
			diff++
		}
	}
	assert.Zero(t, diff, "stage off: books whose candidate list differs from the no-matcher run")
	assert.Zero(t, off.stats.CatalogCandidates.Load())

	// Flag on: the fan-out is untouched.
	assert.Equal(t, off.providerCalls, on.providerCalls, "the stage changes no provider call")

	var wantCatalogBooks, wantOversizeBooks, wantHomonymBooks int
	for _, b := range books {
		switch {
		case b.kind == "decorated" && b.authorIdx < scenarioHomonymAut:
			wantHomonymBooks++
		case b.kind == "decorated":
			wantCatalogBooks++
		case b.kind == "plain" && b.authorIdx < scenarioHomonymAut:
			wantHomonymBooks++
		case b.kind == "franchise":
			wantOversizeBooks++
		}
	}
	n, k, h := on.stats.BooksWithCatalog.Load(), on.stats.OversizeBlocks.Load(), on.stats.HomonymSkipped.Load()
	m := on.stats.ProviderCandidates.Load()
	t.Logf("MEASURED (5,000 books, %d author blocks of %d + %d franchise blocks of %d, %d homonym authors):", scenarioAuthors, scenarioPerAuthor, scenarioFranchise, MaxBlockEntries+1, scenarioHomonymAut)
	t.Logf("  provider calls: no matcher %d, stage off %d, stage on %d (unchanged: the fan-out still runs)", none.providerCalls, off.providerCalls, on.providerCalls)
	t.Logf("  N books with at least one catalog candidate: %d of %d", n, scenarioBooks)
	t.Logf("  catalog candidates: %d; M provider candidates: %d", on.stats.CatalogCandidates.Load(), m)
	t.Logf("  K oversize blocks skipped (occurrences over the library): %d; homonym blocks skipped: %d", k, h)
	t.Logf("  stage off vs no matcher: %d books with a different candidate list", diff)
	assert.EqualValues(t, wantCatalogBooks, n, "every decorated book of a non-homonym harvested author, and no other, gets a catalog candidate")
	assert.EqualValues(t, wantOversizeBooks, k)
	assert.EqualValues(t, wantHomonymBooks, h)
	assert.EqualValues(t, wantCatalogBooks, on.stats.CatalogCandidates.Load(), "one catalog candidate per such book")
	assert.Equal(t, off.stats.ProviderCandidates.Load(), m, "the provider candidates are the same with the stage on")
}
