// file: internal/server/metadata_candidate_refresh_test.go
// version: 1.3.0
// guid: 0d7c3e59-a418-4b62-93f7-5e2b8c1d6a04
// last-edited: 2026-11-17

package server

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// None of this touches the network: the Audnexus lookup is the metafetch
// service's asinLookupOverride and every provider is a canned fake.

const refreshASIN = "B0GOTHAM99" // fake

// newRefreshFixture is the fallback fixture with an ASIN-only Audnexus added to
// the chain, whose lookups go to lookup.
func newRefreshFixture(t *testing.T, lookup func(provider string) error) (*fallbackFixture, *callCounter) {
	t.Helper()
	return newRefreshFixtureRecord(t, func(provider string) (*metadata.BookMetadata, error) {
		return nil, lookup(provider)
	})
}

// newRefreshFixtureRecord is newRefreshFixture whose lookups can also answer
// with a record (the provider's product for the ASIN), so a source that failed
// can later recover. The throttle registry is process-wide: a failed lookup
// records a failure there and can put Audnexus on hold, so every fixture starts
// and ends with it cleared.
func newRefreshFixtureRecord(t *testing.T, lookup func(provider string) (*metadata.BookMetadata, error)) (*fallbackFixture, *callCounter) {
	t.Helper()
	metadata.ResetThrottlesForTesting()
	t.Cleanup(metadata.ResetThrottlesForTesting)
	f := newFallbackFixture(t, 800)
	audnexus := &idSource{id: metadata.SourceIDAudnexus, name: "Audnexus (Audible)", log: f.log}
	f.mfs.SetOverrideSources([]metadata.MetadataSource{f.audible, audnexus, f.openlib, f.google})
	cc := &callCounter{}
	f.mfs.SetASINLookupOverride(func(_ context.Context, provider, _ string) (*metadata.BookMetadata, error) {
		cc.add(provider)
		return lookup(provider)
	})
	return f, cc
}

type callCounter struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *callCounter) add(p string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == nil {
		c.n = map[string]int{}
	}
	c.n[p]++
}

func (c *callCounter) get(p string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[p]
}

func withASIN(asin string) func(*database.Book) {
	return func(b *database.Book) { b.ASIN = &asin }
}

// Audnexus 400 "Bad ASIN" is its answer, not its failure: the book is neither
// an error nor degraded, which is what the 814 books of the 2026-11-16 run
// were.
func TestCandidateRefresh_BadASINIsNotAFailure(t *testing.T) {
	f, cc := newRefreshFixture(t, func(provider string) error {
		if provider == metadata.SourceIDAudnexus {
			return &metadata.ProviderStatusError{Provider: provider, Status: 400,
				Body: `{"error":{"code":"BAD_REQUEST","message":"Bad ASIN"}}`}
		}
		return nil
	})
	b := f.book(t, "Gotham Year One", withASIN(refreshASIN))

	r := f.run(t, b.ID)[b.ID]
	if r.Status == "error" {
		t.Fatalf("status = error (%s); a refused ASIN must not fail the book", r.Error)
	}
	if len(r.FailedSources) != 0 || r.ErrorCause != "" {
		t.Fatalf("degraded = %v cause %q, want none", r.FailedSources, r.ErrorCause)
	}
	if cc.get(metadata.SourceIDAudnexus) == 0 {
		t.Fatal("the fixture never reached the Audnexus lookup, so this tests nothing")
	}
}

// A real Audnexus failure with the other sources answering: the book is
// DEGRADED -- the result says which source and why -- and the fallback still
// runs.
func TestCandidateRefresh_AudnexusFailureDegradesInsteadOfFailing(t *testing.T) {
	f, _ := newRefreshFixture(t, func(provider string) error {
		if provider == metadata.SourceIDAudnexus {
			return &metadata.ProviderStatusError{Provider: provider, Status: 502, Body: "bad gateway"}
		}
		return nil
	})
	f.google.answer = answersTitle("Gotham Year One", "Bruce Wayne")
	b := f.book(t, "Gotham Year One", withASIN(refreshASIN))

	r := f.run(t, b.ID)[b.ID]
	if r.Status == "error" {
		t.Fatalf("status = error (%s); the title sources answered", r.Error)
	}
	if len(r.FailedSources) != 1 || r.FailedSources[0] != "Audnexus (Audible)" {
		t.Fatalf("FailedSources = %v, want [Audnexus (Audible)]", r.FailedSources)
	}
	if r.ErrorCause != metadata.CauseUpstream {
		t.Fatalf("ErrorCause = %q, want %q", r.ErrorCause, metadata.CauseUpstream)
	}
	if f.openlib.calls.Load() == 0 {
		t.Fatal("the fallback providers were not asked")
	}
}

// The second run asks only the source that still owes an answer. When THAT is
// the only thing that fails, the fallback provider that still owes the book
// (Google Books, deferred in the first run) is asked anyway instead of waiting
// behind Audnexus; the result is not an error.
func TestCandidateRefresh_FailedASINOnlySourceDoesNotBlockTheFallback(t *testing.T) {
	f, _ := newRefreshFixture(t, func(provider string) error {
		if provider == metadata.SourceIDAudnexus {
			return &metadata.ProviderStatusError{Provider: provider, Status: 503, Body: "unavailable"}
		}
		return nil
	})
	// Google defers (a passing 503), so it stays owed after the first run.
	f.google.err = &metadata.ProviderStatusError{Provider: metadata.SourceIDGoogleBooks, Status: 503, Body: "backend error"}
	b := f.book(t, "Gotham Year One", withASIN(refreshASIN))

	first := f.run(t, b.ID)[b.ID]
	if first.Status != candidateStatusDeferred {
		t.Fatalf("first run status = %q (%s), want deferred", first.Status, first.Error)
	}
	googleBefore := f.google.calls.Load()

	second := f.run(t, b.ID)[b.ID]
	if second.Status == "error" {
		t.Fatalf("second run status = error (%s); a failed ASIN-only source must not block the fallback", second.Error)
	}
	if f.google.calls.Load() <= googleBefore {
		t.Fatalf("Google Books calls %d -> %d: the owed fallback was not asked", googleBefore, f.google.calls.Load())
	}
	// The first run's 503 put Audnexus on hold, so the second run did not ask.
	if len(second.FailedSources) != 1 || second.FailedSources[0] != "Audnexus (Audible)" || second.ErrorCause != metadata.CauseHeld {
		t.Fatalf("second run FailedSources = %v cause %q, want [Audnexus (Audible)] provider_held", second.FailedSources, second.ErrorCause)
	}
}

// A title source that fails still blocks the fallback and still fails the
// book: its question went unanswered. The cause rides on the row.
func TestCandidateRefresh_TitleSourceFailureStaysAnError(t *testing.T) {
	f, _ := newRefreshFixture(t, func(string) error { return nil })
	f.audible.err = &metadata.ProviderStatusError{Provider: metadata.SourceIDAudible, Status: 500, Body: "boom"}
	b := f.book(t, "Gotham Year One")

	r := f.run(t, b.ID)[b.ID]
	if r.Status != "error" {
		t.Fatalf("status = %q, want error", r.Status)
	}
	if r.ErrorCause != metadata.CauseUpstream {
		t.Fatalf("ErrorCause = %q, want upstream_5xx", r.ErrorCause)
	}
	if f.openlib.calls.Load() != 0 {
		t.Fatal("the fallback ran behind a failed title source")
	}
}

func TestCandidateFetchTally_RefreshSummary(t *testing.T) {
	var tally candidateFetchTally
	rows := []CandidateResult{
		{Status: "matched"},
		{Status: "matched"},
		{Status: "no_match"},
		{Status: "matched", FailedSources: []string{"Audnexus (Audible)"}, ErrorCause: metadata.CauseTimeout},
		{Status: candidateStatusDeferred, FailedSources: []string{"Audnexus (Audible)"}, ErrorCause: metadata.CauseTimeout},
		{Status: "no_match", FailedSources: []string{"Audnexus (Audible)"}, ErrorCause: metadata.CauseUpstream},
		{Status: "error", ErrorCause: metadata.CauseRateLimited},
		{Status: "error", ErrorCause: metadata.CauseRateLimited},
		{Status: "error"},
		{Status: "matched", Cached: candidateCachedCandidates},
		{Status: "skipped"},
	}
	for _, r := range rows {
		tally.record(r)
	}
	tally.recordError("result_not_recorded")

	want := "refresh summary: refreshed 3, degraded 2 (timeout 1, upstream_5xx 1), " +
		"failed 4 (other 1, rate_limited 2, result_not_recorded 1), deferred 1, not asked 2, gave up on a source 0"
	if got := tally.refreshSummary(); got != want {
		t.Fatalf("refreshSummary =\n %q\nwant\n %q", got, want)
	}
	// The running line is unchanged: it carries no refresh words.
	if strings.Contains(tally.counts(), "degraded") && !strings.Contains(tally.counts(), "deferred") {
		t.Fatalf("counts() changed shape: %s", tally.counts())
	}
}

func TestCandidateFetchTally_RefreshSummaryEmpty(t *testing.T) {
	var tally candidateFetchTally
	if got, want := tally.refreshSummary(), "refresh summary: refreshed 0, degraded 0, failed 0, deferred 0, not asked 0, gave up on a source 0"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A deferred book is not "refreshed": its fallback lookup was put off, so it
// has its own bucket and the buckets partition the run.
func TestCandidateFetchTally_DeferredIsNotRefreshed(t *testing.T) {
	var tally candidateFetchTally
	tally.record(CandidateResult{Status: candidateStatusDeferred})
	tally.record(CandidateResult{Status: candidateStatusDeferred, Fallback: []metabatch.FallbackStep{
		{Provider: "google-books", Outcome: metabatch.FallbackDeferred}}})
	want := "refresh summary: refreshed 0, degraded 0, failed 0, deferred 2, not asked 0, gave up on a source 0"
	if got := tally.refreshSummary(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// "Not asked" means NO source was asked. A row served from the cache that
// still ran a fallback provider live was asked; one whose fallback steps were
// all skipped or deferred, or that has no steps, was not; and one whose owed
// source was re-asked and failed again is degraded, not "not asked".
func TestCandidateFetchTally_CachedRowsAreNotAskedOnlyWhenNothingWasAsked(t *testing.T) {
	cases := []struct {
		name string
		r    CandidateResult
		want string
	}{
		{"cached, no fallback", CandidateResult{Status: "matched", Cached: candidateCachedCandidates},
			"refreshed 0, degraded 0, failed 0, deferred 0, not asked 1, gave up on a source 0"},
		{"cached, fallback matched live", CandidateResult{Status: "matched", Cached: candidateCachedCandidates,
			Fallback: []metabatch.FallbackStep{{Provider: "openlibrary", Outcome: metabatch.FallbackMatched}}},
			"refreshed 1, degraded 0, failed 0, deferred 0, not asked 0, gave up on a source 0"},
		{"cached, fallback answered no match", CandidateResult{Status: "no_match", Cached: candidateCachedKnownEmpty,
			Fallback: []metabatch.FallbackStep{{Provider: "openlibrary", Outcome: metabatch.FallbackNoMatch}}},
			"refreshed 1, degraded 0, failed 0, deferred 0, not asked 0, gave up on a source 0"},
		{"cached, fallback errored live", CandidateResult{Status: "no_match", Cached: candidateCachedKnownEmpty,
			Fallback: []metabatch.FallbackStep{{Provider: "openlibrary", Outcome: metabatch.FallbackError}}},
			"refreshed 1, degraded 0, failed 0, deferred 0, not asked 0, gave up on a source 0"},
		{"cached, fallback only skipped", CandidateResult{Status: "matched", Cached: candidateCachedCandidates,
			Fallback: []metabatch.FallbackStep{{Provider: "google-books", Outcome: metabatch.FallbackSkipped}}},
			"refreshed 0, degraded 0, failed 0, deferred 0, not asked 1, gave up on a source 0"},
		{"cached, owed source re-asked and failed", CandidateResult{Status: "matched", Cached: candidateCachedCandidates,
			FailedSources: []string{"Audnexus (Audible)"}, ErrorCause: metadata.CauseTimeout},
			"refreshed 0, degraded 1 (timeout 1), failed 0, deferred 0, not asked 0, gave up on a source 0"},
		{"owed source re-asked, failed, used its last ask", CandidateResult{Status: "matched", Cached: candidateCachedCandidates,
			FailedSources: []string{"Audnexus (Audible)"}, ErrorCause: metadata.CauseTimeout, OwedGaveUp: []string{"Audnexus (Audible)"}},
			"refreshed 0, degraded 1 (timeout 1), failed 0, deferred 0, not asked 0, gave up on a source 1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var tally candidateFetchTally
			tally.record(c.r)
			if got, want := tally.refreshSummary(), "refresh summary: "+c.want; got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}

// A resumed run seeds its tally from the stored result rows, so the causes
// must survive the JSON the rows are stored as.
func TestCandidateFetchTally_ResumeSeedsCausesFromStoredRows(t *testing.T) {
	stored := []CandidateResult{
		{Status: "error", ErrorCause: metadata.CauseTimeout, Error: "search failed"},
		{Status: "matched", FailedSources: []string{"Audnexus (Audible)"}, ErrorCause: metadata.CauseBadASIN},
		{Status: "matched"},
	}
	var seeded candidateFetchTally
	for _, r := range stored {
		blob, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var back CandidateResult
		if err := json.Unmarshal(blob, &back); err != nil {
			t.Fatal(err)
		}
		seeded.record(back)
	}
	want := "refresh summary: refreshed 1, degraded 1 (bad_asin 1), failed 1 (timeout 1), deferred 0, not asked 0, gave up on a source 0"
	if got := seeded.refreshSummary(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A result row written before these fields existed decodes to a clean book,
// and a clean book writes no new keys.
func TestCandidateResult_RefreshFieldsAreAdditive(t *testing.T) {
	var old CandidateResult
	if err := json.Unmarshal([]byte(`{"book":{"id":"b1"},"status":"matched"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.FailedSources != nil || old.ErrorCause != "" {
		t.Fatalf("old row decoded with refresh fields: %+v", old)
	}
	blob, _ := json.Marshal(CandidateResult{Status: "matched"})
	if strings.Contains(string(blob), "failed_sources") || strings.Contains(string(blob), "error_cause") {
		t.Fatalf("a clean row wrote refresh keys: %s", blob)
	}
}
