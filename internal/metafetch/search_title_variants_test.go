// file: internal/metafetch/search_title_variants_test.go
// version: 2.0.0
// guid: 5b1c7d0e-3a4f-4e8b-9c2d-7f6a1e0b9d31
// last-edited: 2026-09-05

package metafetch

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// The titles below are the first books prod's 2026-09-05 bulk fetch judged
// not_found with every provider live; each is a real book that Audible returns
// for the decoration-free query.
func TestStripSeriesDecoration(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Eternal Dominion, Book 04 - Assertions", "Eternal Dominion - Assertions"},
		{"Path Of The Voidwalker - BK07", "Path Of The Voidwalker"},
		{"Pip & Flinx - Book 1 For Love of Mother Not", "Pip & Flinx - For Love of Mother Not"},
		{"Champion of Deania: A Cultivating Gamelit Harem Adventure (Spellheart Book 6)", "Champion of Deania: A Cultivating Gamelit Harem Adventure (Spellheart)"},
		{"Dune", "Dune"},
		{"Book 4", "Book 4"}, // nothing but a decoration: unchanged, never ""
		{"1984", "1984"},     // a bare number is not a series slot
		{"The Book Thief", "The Book Thief"},
		{"Part of Your World", "Part of Your World"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := stripSeriesDecoration(tt.in); got != tt.want {
				t.Errorf("stripSeriesDecoration(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSplitSeriesDecoration(t *testing.T) {
	tests := []struct {
		in, series, book string
		found            bool
	}{
		{"Eternal Dominion, Book 04 - Assertions", "Eternal Dominion", "Assertions", true},
		{"Pip & Flinx - Book 1 For Love of Mother Not", "Pip & Flinx", "For Love of Mother Not", true},
		{"Champion of Deania: A Cultivating Gamelit Harem Adventure (Spellheart Book 6)", "Spellheart", "Champion of Deania: A Cultivating Gamelit Harem Adventure", true},
		// Series and number only: the book has no name of its own.
		{"Path Of The Voidwalker - BK07", "Path Of The Voidwalker", "", true},
		{"The Way of Kings, Book 1", "The Way of Kings", "", true},
		{"Dune", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			_, series, book, found := splitSeriesDecoration(tt.in)
			if series != tt.series || book != tt.book || found != tt.found {
				t.Errorf("splitSeriesDecoration(%q) = (series %q, book %q, found %v), want (%q, %q, %v)",
					tt.in, series, book, found, tt.series, tt.book, tt.found)
			}
		})
	}
}

func queries(vs []titleVariant) []string {
	var out []string
	for _, v := range vs {
		out = append(out, v.Query)
	}
	return out
}

func TestExtraTitleVariants_BulkIsAnchoredOnTheBookName(t *testing.T) {
	tests := []struct {
		raw  string
		want []string
	}{
		{"Eternal Dominion, Book 04 - Assertions", []string{"Assertions", "Eternal Dominion - Assertions"}},
		{"Pip & Flinx - Book 1 For Love of Mother Not", []string{"For Love of Mother Not", "Pip & Flinx - For Love of Mother Not"}},
		{"Champion of Deania: A Cultivating Gamelit Harem Adventure (Spellheart Book 6)", []string{"Champion of Deania: A Cultivating Gamelit Harem Adventure", "Champion of Deania: A Cultivating Gamelit Harem Adventure (Spellheart)"}},
		// No book name → nothing on the bulk path: a series-name query would
		// return the series' OTHER books, and the walk caches unseen.
		{"Path Of The Voidwalker - BK07", nil},
		{"The Way of Kings, Book 1", nil},
		// No decoration → nothing: the literal queries already covered it.
		{"A Plain Title", nil},
		{"Dune - Frank Herbert", nil},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got := extraTitleVariants(tt.raw, stripChapterFromTitle(tt.raw), true)
			if !reflect.DeepEqual(queries(got), tt.want) {
				t.Errorf("bulk variants(%q) = %v, want %v", tt.raw, queries(got), tt.want)
			}
			for _, v := range got {
				if len(v.Anchor) == 0 {
					t.Errorf("bulk variant %q has no anchor", v.Query)
				}
			}
		})
	}
}

func TestExtraTitleVariants_InteractiveIsBroaderAndUnanchored(t *testing.T) {
	tests := []struct {
		raw  string
		want []string
	}{
		{"Eternal Dominion, Book 04 - Assertions", []string{"Assertions", "Eternal Dominion - Assertions", "Eternal Dominion"}},
		{"Path Of The Voidwalker - BK07", []string{"Path Of The Voidwalker"}},
		{"Champion of Deania: A Cultivating Gamelit Harem Adventure (Spellheart Book 6)", []string{"Champion of Deania: A Cultivating Gamelit Harem Adventure", "Champion of Deania: A Cultivating Gamelit Harem Adventure (Spellheart)", "Champion of Deania", "A Cultivating Gamelit Harem Adventure (Spellheart)"}},
		{"A Plain Title", nil},
		{"Dune", nil},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got := extraTitleVariants(tt.raw, stripChapterFromTitle(tt.raw), false)
			if !reflect.DeepEqual(queries(got), tt.want) {
				t.Errorf("interactive variants(%q) = %v, want %v", tt.raw, queries(got), tt.want)
			}
			for _, v := range got {
				if v.Anchor != nil {
					t.Errorf("interactive variant %q must not be anchored", v.Query)
				}
			}
		})
	}
}

// answeringSource answers one exact query with the given results and every
// other query with nothing, recording every query it sees.
type answeringSource struct {
	name    string
	answers map[string][]metadata.BookMetadata
	queries []string
}

func (a *answeringSource) Name() string { return a.name }
func (a *answeringSource) SearchByTitle(_ context.Context, title string) ([]metadata.BookMetadata, error) {
	a.queries = append(a.queries, title)
	return a.answers[title], nil
}
func (a *answeringSource) SearchByTitleAndAuthor(ctx context.Context, title, _ string) ([]metadata.BookMetadata, error) {
	return a.SearchByTitle(ctx, title)
}

// The bulk walk: a provider that only answers the book's own name is a hit,
// tried after both literal titles, and the walk stops there.
func TestWalkSourceChain_SeriesDecoratedTitleFallsBackToBookName(t *testing.T) {
	src := &answeringSource{name: "audible", answers: map[string][]metadata.BookMetadata{
		"Assertions": {{Title: "Assertions", Author: "Bern Dean"}},
	}}
	chain := []metadata.MetadataSource{src}
	sem := NewProviderSemaphore(chain, 2)

	out, err := WalkSourceChain(context.Background(), emptyKV{}, chain, sem,
		"01BOOK", "Eternal Dominion, Book 04 - Assertions", "Bern Dean", time.Hour)
	if err != nil {
		t.Fatalf("WalkSourceChain: %v", err)
	}
	if out.Status() != FetchStatusCached {
		t.Fatalf("status = %q, want %q (queries: %v)", out.Status(), FetchStatusCached, src.queries)
	}
	if out.Variant != "Assertions" {
		t.Fatalf("Variant = %q, want the query that hit; queries: %v", out.Variant, src.queries)
	}
	if src.queries[0] != "Eternal Dominion, Book 04 - Assertions" {
		t.Fatalf("literal title must be tried first; queries: %v", src.queries)
	}
	if last := src.queries[len(src.queries)-1]; last != "Assertions" {
		t.Fatalf("walk must stop at the variant that hit, last query %q; queries: %v", last, src.queries)
	}
}

// A variant answer that does not name the book is NOT a hit: the series'
// other books must never be cached and ledgered as this one.
func TestWalkSourceChain_VariantAnswerMustNameTheBook(t *testing.T) {
	src := &answeringSource{name: "audible", answers: map[string][]metadata.BookMetadata{
		"Eternal Dominion - Assertions": {{Title: "Eternal Dominion: Ascension"}, {Title: "Foundations (Eternal Dominion 1)"}},
	}}
	chain := []metadata.MetadataSource{src}
	sem := NewProviderSemaphore(chain, 2)

	out, err := WalkSourceChain(context.Background(), emptyKV{}, chain, sem,
		"01BOOK", "Eternal Dominion, Book 04 - Assertions", "", time.Hour)
	if err != nil {
		t.Fatalf("WalkSourceChain: %v", err)
	}
	if out.Status() != FetchStatusNotFound || len(out.Results) != 0 {
		t.Fatalf("a series-name answer was accepted: status %q, results %v", out.Status(), out.Results)
	}
	if out.Variant != "" {
		t.Fatalf("Variant = %q on a miss", out.Variant)
	}
}

// A series-plus-number title has no variants on the bulk path.
func TestWalkSourceChain_SeriesOnlyTitleGetsNoVariants(t *testing.T) {
	src := &recordingSource{name: "audible"}
	chain := []metadata.MetadataSource{src}
	sem := NewProviderSemaphore(chain, 2)

	if _, err := WalkSourceChain(context.Background(), emptyKV{}, chain, sem,
		"01BOOK", "Path Of The Voidwalker - BK07", "", time.Hour); err != nil {
		t.Fatalf("WalkSourceChain: %v", err)
	}
	for _, q := range src.queries {
		if q != "Path Of The Voidwalker - BK07" {
			t.Fatalf("unexpected variant query %q; queries: %v", q, src.queries)
		}
	}
}

// A hit on the literal title never reaches the variants: the extra calls are
// paid only by books the literal queries missed.
func TestWalkSourceChain_LiteralHitSkipsVariants(t *testing.T) {
	src := &recordingSource{name: "audible", hitOn: "Eternal Dominion, Book 04 - Assertions"}
	chain := []metadata.MetadataSource{src}
	sem := NewProviderSemaphore(chain, 2)

	out, err := WalkSourceChain(context.Background(), emptyKV{}, chain, sem,
		"01BOOK", "Eternal Dominion, Book 04 - Assertions", "", time.Hour)
	if err != nil {
		t.Fatalf("WalkSourceChain: %v", err)
	}
	if out.Status() != FetchStatusCached || out.Variant != "" {
		t.Fatalf("status = %q variant = %q, want a literal hit", out.Status(), out.Variant)
	}
	if len(src.queries) != 1 {
		t.Fatalf("expected exactly the literal query, got %v", src.queries)
	}
}

// scriptedSource answers each call with the next scripted step.
type scriptedSource struct {
	name  string
	steps []func() ([]metadata.BookMetadata, error)
	calls int
	ctx   context.Context
}

func (s *scriptedSource) Name() string { return s.name }
func (s *scriptedSource) SearchByTitle(ctx context.Context, _ string) ([]metadata.BookMetadata, error) {
	s.ctx = ctx
	i := s.calls
	s.calls++
	if i < len(s.steps) {
		return s.steps[i]()
	}
	return nil, nil
}
func (s *scriptedSource) SearchByTitleAndAuthor(ctx context.Context, title, _ string) ([]metadata.BookMetadata, error) {
	return s.SearchByTitle(ctx, title)
}

// A sentinel (open breaker, throttle hold) closes the ladder for that source
// and never displaces the real diagnosis an earlier rung produced.
func TestWalkSourceChain_SentinelClosesTheLadderAndKeepsTheDiagnosis(t *testing.T) {
	real := errors.New("google Books API returned status 400: bad query")
	src := &scriptedSource{name: "google", steps: []func() ([]metadata.BookMetadata, error){
		func() ([]metadata.BookMetadata, error) { return nil, real },
		func() ([]metadata.BookMetadata, error) { return nil, metadata.ErrCircuitOpen },
		func() ([]metadata.BookMetadata, error) {
			t.Fatal("ladder continued past an open breaker")
			return nil, nil
		},
	}}
	chain := []metadata.MetadataSource{src}
	sem := NewProviderSemaphore(chain, 2)

	out, err := WalkSourceChain(context.Background(), emptyKV{}, chain, sem,
		"01BOOK", "Eternal Dominion, Book 04 - Assertions", "Bern Dean", time.Hour)
	if err != nil {
		t.Fatalf("WalkSourceChain: %v", err)
	}
	if src.calls != 2 {
		t.Fatalf("calls = %d, want 2 (literal, then the sentinel that closes the ladder)", src.calls)
	}
	if !errors.Is(out.Err, real) {
		t.Fatalf("Err = %v, want the provider's own diagnosis, not the sentinel", out.Err)
	}
	if out.Status() != FetchStatusFetchError {
		t.Fatalf("status = %q, want %q", out.Status(), FetchStatusFetchError)
	}
}

// A cancelled walk stops calling: each further call would fail fast and
// count against the shared breaker.
func TestWalkSourceChain_CancelStopsTheLadder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	src := &scriptedSource{name: "audible", steps: []func() ([]metadata.BookMetadata, error){
		func() ([]metadata.BookMetadata, error) { cancel(); return nil, nil },
		func() ([]metadata.BookMetadata, error) { t.Fatal("ladder continued after cancel"); return nil, nil },
	}}
	chain := []metadata.MetadataSource{src}
	sem := NewProviderSemaphore(chain, 2)

	_, err := WalkSourceChain(ctx, emptyKV{}, chain, sem,
		"01BOOK", "Eternal Dominion, Book 04 - Assertions", "Bern Dean", time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if src.calls != 1 {
		t.Fatalf("calls = %d, want 1", src.calls)
	}
}

func newVariantService(t *testing.T, title string, src metadata.MetadataSource) *Service {
	t.Helper()
	book := &database.Book{ID: "b1", Title: title}
	mock := &database.MockStore{
		GetBookByIDFunc: func(string) (*database.Book, error) { return book, nil },
	}
	svc := NewService(mock)
	svc.SetOverrideSources([]metadata.MetadataSource{src})
	return svc
}

// The interactive search (the review dialog's lookup) takes the same fallback;
// with an author every variant costs a SearchByTitleAndAuthor call, so a search
// that did not stop at the hit would be seen querying the next variant.
func TestSearchMetadataForBook_SeriesDecoratedTitleFallsBackToVariants(t *testing.T) {
	src := &recordingSource{name: "audible", hitOn: "Assertions"}
	svc := newVariantService(t, "Eternal Dominion, Book 04 - Assertions", src)

	resp, err := svc.searchMetadataForBook(context.Background(), nil, "b1", "", "Bern Dean", "", "", SearchOptions{})
	if err != nil {
		t.Fatalf("searchMetadataForBook: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatalf("expected the variant hit to surface; queries: %v", src.queries)
	}
	if last := src.queries[len(src.queries)-1]; last != "Assertions" {
		t.Fatalf("search must stop at the variant that hit, last query %q; queries: %v", last, src.queries)
	}
}

// A literal-title hit on the interactive path never reaches the variants.
func TestSearchMetadataForBook_LiteralHitSkipsVariants(t *testing.T) {
	src := &recordingSource{name: "audible", hitOn: "Eternal Dominion, Book 04 - Assertions"}
	svc := newVariantService(t, "Eternal Dominion, Book 04 - Assertions", src)

	if _, err := svc.searchMetadataForBook(context.Background(), nil, "b1", "", "Bern Dean", "", "", SearchOptions{}); err != nil {
		t.Fatalf("searchMetadataForBook: %v", err)
	}
	for _, q := range src.queries {
		if q != "Eternal Dominion, Book 04 - Assertions" {
			t.Fatalf("variant %q was queried after a literal hit; queries: %v", q, src.queries)
		}
	}
}

// And a plain title makes no extra call on the interactive path either.
func TestSearchMetadataForBook_PlainTitleMakesNoVariantCalls(t *testing.T) {
	src := &recordingSource{name: "audible"} // never hits
	svc := newVariantService(t, "A Plain Title", src)

	if _, err := svc.searchMetadataForBook(context.Background(), nil, "b1", "", "", "", "", SearchOptions{}); err != nil {
		t.Fatalf("searchMetadataForBook: %v", err)
	}
	for _, q := range src.queries {
		if q != "A Plain Title" {
			t.Fatalf("unexpected variant query %q in %v", q, src.queries)
		}
	}
}

// On the interactive path too, a sentinel closes the ladder and the failure
// shown to the user is the provider's own words, not the sentinel.
func TestSearchMetadataForBook_SentinelClosesTheLadderAndKeepsTheDiagnosis(t *testing.T) {
	real := errors.New("google Books API returned status 429: Quota exceeded for 'Queries per day'")
	src := &scriptedSource{name: "google", steps: []func() ([]metadata.BookMetadata, error){
		func() ([]metadata.BookMetadata, error) { return nil, real },
		func() ([]metadata.BookMetadata, error) { return nil, metadata.ErrProviderThrottled },
		func() ([]metadata.BookMetadata, error) {
			t.Fatal("ladder continued past a throttle hold")
			return nil, nil
		},
	}}
	svc := newVariantService(t, "Eternal Dominion, Book 04 - Assertions", src)

	resp, err := svc.searchMetadataForBook(context.Background(), nil, "b1", "", "Bern Dean", "", "", SearchOptions{})
	if err != nil {
		t.Fatalf("searchMetadataForBook: %v", err)
	}
	if src.calls != 2 {
		t.Fatalf("calls = %d, want 2", src.calls)
	}
	got := resp.SourcesFailed["google"]
	if !strings.Contains(got, "Queries per day") {
		t.Fatalf("sources_failed[google] = %q, want the quota message the provider sent", got)
	}
}
