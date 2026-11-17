// file: internal/server/metadata_candidate_refresh_test.go
// version: 1.0.0
// guid: 0d7c3e59-a418-4b62-93f7-5e2b8c1d6a04
// last-edited: 2026-11-16

package server

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// None of this touches the network: the Audnexus lookup is the metafetch
// service's asinLookupOverride and every provider is a canned fake.

const refreshASIN = "B0GOTHAM99" // fake

// newRefreshFixture is the fallback fixture with an ASIN-only Audnexus added to
// the chain, whose lookups go to lookup.
func newRefreshFixture(t *testing.T, lookup func(provider string) error) (*fallbackFixture, *callCounter) {
	t.Helper()
	f := newFallbackFixture(t, 800)
	audnexus := &idSource{id: metadata.SourceIDAudnexus, name: "Audnexus", log: f.log}
	f.mfs.SetOverrideSources([]metadata.MetadataSource{f.audible, audnexus, f.openlib, f.google})
	cc := &callCounter{}
	f.mfs.SetASINLookupOverride(func(_ context.Context, provider, _ string) (*metadata.BookMetadata, error) {
		cc.add(provider)
		return nil, lookup(provider)
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
	if len(r.SourcesFailed) != 0 || r.ErrorCause != "" {
		t.Fatalf("degraded = %v cause %q, want none", r.SourcesFailed, r.ErrorCause)
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
	if len(r.SourcesFailed) != 1 || r.SourcesFailed[0] != "Audnexus" {
		t.Fatalf("SourcesFailed = %v, want [Audnexus]", r.SourcesFailed)
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
	if len(second.SourcesFailed) != 1 || second.SourcesFailed[0] != "Audnexus" || second.ErrorCause != metadata.CauseHeld {
		t.Fatalf("second run SourcesFailed = %v cause %q, want [Audnexus] provider_held", second.SourcesFailed, second.ErrorCause)
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
		{Status: "matched", SourcesFailed: []string{"Audnexus"}, ErrorCause: metadata.CauseTimeout},
		{Status: candidateStatusDeferred, SourcesFailed: []string{"Audnexus"}, ErrorCause: metadata.CauseTimeout},
		{Status: "no_match", SourcesFailed: []string{"Audnexus"}, ErrorCause: metadata.CauseUpstream},
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

	want := "refresh summary: refreshed 3, degraded 3 (timeout 2, upstream_5xx 1), " +
		"failed 4 (other 1, rate_limited 2, result_not_recorded 1), not asked 2"
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
	if got, want := tally.refreshSummary(), "refresh summary: refreshed 0, degraded 0, failed 0, not asked 0"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A resumed run seeds its tally from the stored result rows, so the causes
// must survive the JSON the rows are stored as.
func TestCandidateFetchTally_ResumeSeedsCausesFromStoredRows(t *testing.T) {
	stored := []CandidateResult{
		{Status: "error", ErrorCause: metadata.CauseTimeout, Error: "search failed"},
		{Status: "matched", SourcesFailed: []string{"Audnexus"}, ErrorCause: metadata.CauseBadASIN},
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
	want := "refresh summary: refreshed 1, degraded 1 (bad_asin 1), failed 1 (timeout 1), not asked 0"
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
	if old.SourcesFailed != nil || old.ErrorCause != "" {
		t.Fatalf("old row decoded with refresh fields: %+v", old)
	}
	blob, _ := json.Marshal(CandidateResult{Status: "matched"})
	if strings.Contains(string(blob), "sources_failed") || strings.Contains(string(blob), "error_cause") {
		t.Fatalf("a clean row wrote refresh keys: %s", blob)
	}
}
