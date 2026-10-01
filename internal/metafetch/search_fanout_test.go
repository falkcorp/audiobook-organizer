// file: internal/metafetch/search_fanout_test.go
// version: 1.0.0
// guid: de3f610d-605b-4338-96c9-3316a2dcd8d4
// last-edited: 2026-10-01

package metafetch

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// fakeProvider is a MetadataSource that declares a real provider id, so the
// fan-out applies that provider's policy, and records every question.
type fakeProvider struct {
	id, name string
	answer   func(title, author string) []metadata.BookMetadata

	mu    sync.Mutex
	calls [][2]string
}

func (f *fakeProvider) Name() string       { return f.name }
func (f *fakeProvider) ProviderID() string { return f.id }
func (f *fakeProvider) ask(title, author string) []metadata.BookMetadata {
	f.mu.Lock()
	f.calls = append(f.calls, [2]string{title, author})
	f.mu.Unlock()
	if f.answer == nil {
		return nil
	}
	return f.answer(title, author)
}
func (f *fakeProvider) SearchByTitle(_ context.Context, title string) ([]metadata.BookMetadata, error) {
	return f.ask(title, ""), nil
}
func (f *fakeProvider) SearchByTitleAndAuthor(_ context.Context, title, author string) ([]metadata.BookMetadata, error) {
	return f.ask(title, author), nil
}
func (f *fakeProvider) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

const reacherTitle = "Jack Reacher 17: A Wanted Man (Jeff Harding)"

// fanoutHarness serves one book (runtime 10h when durationSec > 0) from a
// mock store with an in-memory fetch cache, searched through providers.
func fanoutHarness(t *testing.T, book *database.Book, providers ...metadata.MetadataSource) *Service {
	t.Helper()
	raw := map[string][]byte{}
	var mu sync.Mutex
	mock := &database.MockStore{
		GetBookByIDFunc:  func(string) (*database.Book, error) { return book, nil },
		GetBookFilesFunc: func(string) ([]database.BookFile, error) { return nil, nil },
		GetRawFunc: func(k string) ([]byte, error) {
			mu.Lock()
			defer mu.Unlock()
			return raw[k], nil
		},
		SetRawFunc: func(k string, v []byte) error {
			mu.Lock()
			defer mu.Unlock()
			raw[k] = v
			return nil
		},
		DeleteRawFunc: func(k string) error {
			mu.Lock()
			defer mu.Unlock()
			delete(raw, k)
			return nil
		},
	}
	svc := NewService(mock)
	svc.SetOverrideSources(providers)
	svc.asinLookupOverride = func(context.Context, string, string) (*metadata.BookMetadata, error) {
		return nil, errors.New("no live ASIN lookups in tests")
	}
	return svc
}

func reacherBook(durationSec int) *database.Book {
	b := &database.Book{ID: "b1", Title: reacherTitle}
	if durationSec > 0 {
		b.Duration = &durationSec
	}
	return b
}

// Nothing answers, so every round runs: Audible and Open Library are asked
// every variant up to the cap, Google Books exactly once (its 1,000/day key
// quota), and Audnexus -- which has no title search -- never, while still
// counting as answered so the batch does not re-ask it forever.
func TestSearchFanout_GoogleOnceCapHoldsAudnexusNeverTitleSearched(t *testing.T) {
	audible := &fakeProvider{id: metadata.SourceIDAudible, name: "Audible"}
	ol := &fakeProvider{id: metadata.SourceIDOpenLibrary, name: "Open Library"}
	google := &fakeProvider{id: metadata.SourceIDGoogleBooks, name: "Google Books"}
	audnexus := &fakeProvider{id: metadata.SourceIDAudnexus, name: "Audnexus (Audible)"}
	svc := fanoutHarness(t, reacherBook(0), audible, ol, google, audnexus)

	resp, err := svc.SearchMetadataForBookWithOptions("b1", reacherTitle, "Lee Child", "", "", SearchOptions{})
	require.NoError(t, err)

	assert.Equal(t, maxQueryVariants, audible.callCount(), "Audible: every variant up to the cap")
	assert.Equal(t, maxQueryVariants, ol.callCount(), "Open Library: every variant up to the cap")
	assert.Equal(t, 1, google.callCount(), "Google Books: exactly one query per book")
	assert.Equal(t, [][2]string{{"A Wanted Man", "Lee Child"}}, google.calls, "Google gets the best variant")
	assert.Zero(t, audnexus.callCount(), "Audnexus is never title-searched")
	assert.ElementsMatch(t, []string{"Audible", "Open Library", "Google Books", "Audnexus (Audible)"}, resp.SourcesAnswered)

	// A second search replays every variant from the per-variant cache rows
	// only for questions that had an answer; empty answers are never cached,
	// so they are asked again.
	_, err = svc.SearchMetadataForBookWithOptions("b1", reacherTitle, "Lee Child", "", "", SearchOptions{})
	require.NoError(t, err)
	assert.Equal(t, 2*maxQueryVariants, audible.callCount())
}

// A strong match in the first round (title words + the author + a runtime
// within tolerance) stops the fan-out for every source.
func TestSearchFanout_EarlyStopOnStrongMatch(t *testing.T) {
	audible := &fakeProvider{id: metadata.SourceIDAudible, name: "Audible", answer: func(title, author string) []metadata.BookMetadata {
		if title == "A Wanted Man" && author == "Lee Child" {
			return []metadata.BookMetadata{{Title: "A Wanted Man", Author: "Lee Child", Narrator: "Jeff Harding", DurationSec: 36600, CoverURL: "c"}}
		}
		return nil
	}}
	ol := &fakeProvider{id: metadata.SourceIDOpenLibrary, name: "Open Library"}
	google := &fakeProvider{id: metadata.SourceIDGoogleBooks, name: "Google Books"}
	svc := fanoutHarness(t, reacherBook(36000), audible, ol, google)

	resp, err := svc.SearchMetadataForBookWithOptions("b1", reacherTitle, "Lee Child", "", "", SearchOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, audible.callCount())
	assert.Equal(t, 1, ol.callCount(), "Open Library stops on Audible's strong match")
	assert.Equal(t, 1, google.callCount())
	require.NotEmpty(t, resp.Results)
	assert.Equal(t, "A Wanted Man", resp.Results[0].Title)
	assert.Equal(t, 600, resp.Results[0].DurationDeltaSec, "candidates still carry duration_delta_sec")

	// A runtime far off is not strong: the fan-out goes on.
	audible2 := &fakeProvider{id: metadata.SourceIDAudible, name: "Audible", answer: func(title, author string) []metadata.BookMetadata {
		return []metadata.BookMetadata{{Title: "A Wanted Man", Author: "Lee Child", DurationSec: 9000, CoverURL: "c"}}
	}}
	svc2 := fanoutHarness(t, reacherBook(36000), audible2)
	_, err = svc2.SearchMetadataForBookWithOptions("b1", reacherTitle, "Lee Child", "", "", SearchOptions{})
	require.NoError(t, err)
	assert.Equal(t, maxQueryVariants, audible2.callCount())
}

// Ranking: of two editions with the same title and author (different
// ASINs, so both survive the dedupe), the one agreeing on runtime and
// narrator outranks the one that only matches the title and author, even
// when the provider lists it second.
func TestSearchFanout_RuntimeAndNarratorBeatTitleOnly(t *testing.T) {
	audible := &fakeProvider{id: metadata.SourceIDAudible, name: "Audible", answer: func(title, author string) []metadata.BookMetadata {
		return []metadata.BookMetadata{
			{Title: "A Wanted Man", Author: "Lee Child", Narrator: "Dick Hill", DurationSec: 15000, CoverURL: "c", ASIN: "B000000001"},
			{Title: "A Wanted Man", Author: "Lee Child", Narrator: "Jeff Harding", DurationSec: 36200, CoverURL: "c", ASIN: "B000000002"},
		}
	}}
	svc := fanoutHarness(t, reacherBook(36000), audible)

	resp, err := svc.SearchMetadataForBookWithOptions("b1", reacherTitle, "Lee Child", "Jeff Harding", "", SearchOptions{})
	require.NoError(t, err)
	require.Len(t, resp.Results, 2)
	assert.Equal(t, "B000000002", resp.Results[0].ASIN, "runtime + narrator agreement wins")
}

// Ranking: a result carrying the book's own ASIN wins over every other
// signal, even against a result agreeing on title, author, narrator, series
// and runtime.
func TestSearchFanout_ASINMatchWins(t *testing.T) {
	audible := &fakeProvider{id: metadata.SourceIDAudible, name: "Audible", answer: func(title, author string) []metadata.BookMetadata {
		return []metadata.BookMetadata{
			{Title: "A Wanted Man", Author: "Lee Child", Narrator: "Jeff Harding", Series: "Jack Reacher", SeriesPosition: "17",
				DurationSec: 36000, CoverURL: "c", ASIN: "B000OTHER1"},
			{Title: "A Wanted Man (Dramatized)", Author: "Someone Else", DurationSec: 9000, CoverURL: "c", ASIN: "B000OWNED1"},
		}
	}}
	book := reacherBook(36000)
	own := "B000OWNED1"
	book.ASIN = &own
	svc := fanoutHarness(t, book, audible)

	resp, err := svc.SearchMetadataForBookWithOptions("b1", reacherTitle, "Lee Child", "Jeff Harding", "Jack Reacher", SearchOptions{})
	require.NoError(t, err)
	require.Len(t, resp.Results, 2)
	assert.Equal(t, own, resp.Results[0].ASIN, "the book's own ASIN wins")
	assert.Equal(t, 1, audible.callCount(), "the book's own ASIN in the pool is a strong match: no further variant")
}

// The prod failure "Magma Heart - Unknown Author": the organizer's
// placeholder suffix is stripped, so Audible is asked "Magma Heart" by the
// book's author and the hit surfaces.
func TestSearchFanout_PlaceholderSuffixStripped(t *testing.T) {
	audible := &fakeProvider{id: metadata.SourceIDAudible, name: "Audible", answer: func(title, author string) []metadata.BookMetadata {
		if title == "Magma Heart" {
			return []metadata.BookMetadata{{Title: "Magma Heart", Author: "Plum Parrot", CoverURL: "c"}}
		}
		return nil
	}}
	book := &database.Book{ID: "b1", Title: "Magma Heart - Unknown Author"}
	svc := fanoutHarness(t, book, audible)

	resp, err := svc.SearchMetadataForBookWithOptions("b1", "", "Plum Parrot", "", "", SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Results)
	assert.Equal(t, "Magma Heart", resp.Results[0].Title)
	assert.Equal(t, [2]string{"Magma Heart", "Plum Parrot"}, audible.calls[0])
}

// failingProvider errors on every question.
type failingProvider struct{ fakeProvider }

func (f *failingProvider) SearchByTitle(_ context.Context, title string) ([]metadata.BookMetadata, error) {
	f.ask(title, "")
	return nil, errors.New("provider down")
}
func (f *failingProvider) SearchByTitleAndAuthor(_ context.Context, title, author string) ([]metadata.BookMetadata, error) {
	f.ask(title, author)
	return nil, errors.New("provider down")
}

// Every title source failing must still be ErrNoSourceAnswered, so nothing
// is cached as "nothing found": Audnexus, asked nothing, is answered for the
// per-provider bookkeeping but cannot vouch for the book.
func TestSearchFanout_AllAskedSourcesFailIsNoSourceAnswered(t *testing.T) {
	audible := &failingProvider{fakeProvider{id: metadata.SourceIDAudible, name: "Audible"}}
	ol := &failingProvider{fakeProvider{id: metadata.SourceIDOpenLibrary, name: "Open Library"}}
	google := &failingProvider{fakeProvider{id: metadata.SourceIDGoogleBooks, name: "Google Books"}}
	audnexus := &fakeProvider{id: metadata.SourceIDAudnexus, name: "Audnexus (Audible)"}
	svc := fanoutHarness(t, reacherBook(0), audible, ol, google, audnexus)

	resp, err := svc.SearchMetadataForBookWithOptions("b1", reacherTitle, "Lee Child", "", "", SearchOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"Audnexus (Audible)"}, resp.SourcesAnswered)
	assert.ErrorIs(t, noSourceAnswered(resp), ErrNoSourceAnswered)

	// A partial re-ask of only Audnexus asks nobody: its answer stands.
	resp, err = svc.SearchMetadataForBookWithOptions("b1", reacherTitle, "Lee Child", "", "",
		SearchOptions{OnlySources: []string{"Audnexus (Audible)"}})
	require.NoError(t, err)
	assert.NoError(t, noSourceAnswered(resp))
}

func TestNoSourceAnswered_AskedSourcesDecide(t *testing.T) {
	assert.ErrorIs(t, noSourceAnswered(&SearchMetadataResponse{
		SourcesAnswered: []string{"Audnexus (Audible)"}, SourcesAsked: []string{"Audible"},
		SourcesFailed: map[string]string{"Audible": "down"},
	}), ErrNoSourceAnswered, "an answered source that was not asked does not count")
	assert.NoError(t, noSourceAnswered(&SearchMetadataResponse{
		SourcesAnswered: []string{"Audible", "Audnexus (Audible)"}, SourcesAsked: []string{"Audible"},
	}), "an asked source answered")
	assert.NoError(t, noSourceAnswered(&SearchMetadataResponse{SourcesAnswered: []string{"Audnexus (Audible)"}}),
		"nobody asked: answered sources count as before")
	assert.ErrorIs(t, noSourceAnswered(&SearchMetadataResponse{}), ErrNoSourceAnswered)
}
