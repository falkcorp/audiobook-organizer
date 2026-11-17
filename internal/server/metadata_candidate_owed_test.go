// file: internal/server/metadata_candidate_owed_test.go
// version: 1.1.0
// guid: c41e7a96-5d03-4f28-9b6a-2e8f1d70a3c5
// last-edited: 2026-11-16

package server

import (
	"context"
	"encoding/json"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// A book whose Audnexus lookup failed in the run that found its Open Library or
// Google Books candidate must not be parked on that candidate for the row's 30
// days: Audnexus is OWED an answer (database.MetadataCandidateCache.OwedSources)
// and is re-asked, alone, on a bounded backoff. Every test here goes through
// the real op (runMetadataCandidateFetchOp) and the real store.

// owedLookup is a switchable Audnexus: while down it answers 502, then it
// answers with the product for the ASIN. It counts the lookups that reached it.
type owedLookup struct {
	down atomic.Bool
	rec  metadata.BookMetadata
}

func (o *owedLookup) lookup(provider string) (*metadata.BookMetadata, error) {
	if provider != metadata.SourceIDAudnexus {
		return nil, nil
	}
	if o.down.Load() {
		return nil, &metadata.ProviderStatusError{Provider: provider, Status: 502, Body: "bad gateway"}
	}
	r := o.rec
	return &r, nil
}

const owedTitle = "Gotham Year One"

func newOwedServerFixture(t *testing.T) (*fallbackFixture, *callCounter, *owedLookup) {
	t.Helper()
	ol := &owedLookup{rec: metadata.BookMetadata{
		Title: owedTitle, Author: "Bruce Wayne", Narrator: "N. Reader", ASIN: refreshASIN,
		DurationSec: 36000, CoverURL: "https://example.invalid/c.jpg"}}
	ol.down.Store(true)
	f, cc := newRefreshFixtureRecord(t, ol.lookup)
	f.google.answer = answersTitle(owedTitle, "Bruce Wayne")
	return f, cc, ol
}

// ageOwed moves every owed source of the book's row back by d, as the clock
// moving forward would (the backoff counts from LastAttempt).
func ageOwed(t *testing.T, f *fallbackFixture, bookID string, d time.Duration) {
	t.Helper()
	e, err := f.store.GetMetadataCache(bookID)
	if err != nil || e == nil {
		t.Fatalf("cache row for %s: %v %v", bookID, e, err)
	}
	for name, o := range e.OwedSources {
		o.LastAttempt, o.Since = o.LastAttempt.Add(-d), o.Since.Add(-d)
		e.OwedSources[name] = o
	}
	if err := f.store.PutMetadataCache(e); err != nil {
		t.Fatal(err)
	}
}

func owedRow(t *testing.T, f *fallbackFixture, bookID string) *database.MetadataCandidateCache {
	t.Helper()
	e, err := f.store.GetMetadataCache(bookID)
	if err != nil || e == nil {
		t.Fatalf("cache row for %s: %v %v", bookID, e, err)
	}
	return e
}

type providerCalls struct{ audible, openlib, google int64 }

func callsOf(f *fallbackFixture) providerCalls {
	return providerCalls{f.audible.calls.Load(), f.openlib.calls.Load(), f.google.calls.Load()}
}

// The scenario of the review: run 1 Audnexus fails and Google Books matches; run
// 2 Audnexus has recovered. It must be asked, its candidate stored ahead of the
// review-only one, the debt cleared, and neither fallback provider asked again.
func TestCandidateOwed_RecoveredAudnexusIsAskedAndItsCandidateStored(t *testing.T) {
	f, cc, ol := newOwedServerFixture(t)
	b := f.book(t, owedTitle, withASIN(refreshASIN))

	first := f.run(t, b.ID)[b.ID]
	if first.Status != "matched" || first.Candidate == nil || first.Candidate.Source != "Google Books" {
		t.Fatalf("run 1 = {%q, %+v} (%s), want matched from Google Books", first.Status, first.Candidate, first.Error)
	}
	if !slices.Equal(first.FailedSources, []string{"Audnexus (Audible)"}) {
		t.Fatalf("run 1 FailedSources = %v, want [Audnexus (Audible)]", first.FailedSources)
	}
	row := owedRow(t, f, b.ID)
	o, owed := row.OwedSources["Audnexus (Audible)"]
	if !owed || o.Attempts != 1 || o.Cause != metadata.CauseUpstream {
		t.Fatalf("run 1 left OwedSources = %+v, want Audnexus asked once, upstream_5xx", row.OwedSources)
	}
	if got := candidateSources(t, row); !slices.Equal(got, []string{"Google Books"}) {
		t.Fatalf("run 1 candidates = %v, want [Google Books]", got)
	}
	if got := cc.get(metadata.SourceIDAudnexus); got != 1 {
		t.Fatalf("run 1 Audnexus lookups = %d, want 1", got)
	}

	// Not due yet: served from the row, nothing asked.
	callsBefore := callsOf(f)
	again := f.run(t, b.ID)[b.ID]
	if again.Cached != candidateCachedCandidates || len(again.FailedSources) != 0 {
		t.Fatalf("not-due run = {cached %q, failed %v}, want a clean cache hit", again.Cached, again.FailedSources)
	}
	if got := cc.get(metadata.SourceIDAudnexus); got != 1 || callsOf(f) != callsBefore {
		t.Fatalf("not-due run asked someone: audnexus %d, calls %+v -> %+v", got, callsBefore, callsOf(f))
	}

	// Run 2: Audnexus has recovered and its backoff has run out.
	ol.down.Store(false)
	metadata.ResetThrottlesForTesting() // run 1's failure held Audnexus
	ageOwed(t, f, b.ID, 7*time.Hour)
	callsBefore = callsOf(f)
	second := f.run(t, b.ID)[b.ID]

	if got := cc.get(metadata.SourceIDAudnexus); got != 2 {
		t.Fatalf("Audnexus lookups after run 2 = %d, want 2 (asked once more)", got)
	}
	if got := callsOf(f); got != callsBefore {
		t.Fatalf("fallback/title providers were asked for Audnexus's re-ask: %+v -> %+v", callsBefore, got)
	}
	if second.Status != "matched" || second.Candidate == nil || second.Candidate.Source != "Audnexus (Audible)" {
		t.Fatalf("run 2 = {%q, %+v} (%s), want matched from Audnexus (Audible)", second.Status, second.Candidate, second.Error)
	}
	if len(second.FailedSources) != 0 || second.ErrorCause != "" {
		t.Fatalf("run 2 still degraded: %v %q", second.FailedSources, second.ErrorCause)
	}
	row = owedRow(t, f, b.ID)
	if len(row.OwedSources) != 0 {
		t.Fatalf("run 2 left OwedSources = %+v, want none", row.OwedSources)
	}
	// The review-only Google candidate is kept, and ranked after the Audnexus one.
	if got := candidateSources(t, row); !slices.Equal(got, []string{"Audnexus (Audible)", "Google Books"}) {
		t.Fatalf("run 2 candidates = %v, want [Audnexus (Audible) Google Books]", got)
	}

	// Run 3: nothing is owed any more; nothing is asked.
	callsBefore = callsOf(f)
	third := f.run(t, b.ID)[b.ID]
	if third.Cached != candidateCachedCandidates || cc.get(metadata.SourceIDAudnexus) != 2 || callsOf(f) != callsBefore {
		t.Fatalf("run 3 asked someone: cached %q audnexus %d calls %+v -> %+v",
			third.Cached, cc.get(metadata.SourceIDAudnexus), callsBefore, callsOf(f))
	}
}

// An ASIN Audnexus never answers is not re-asked every tick forever: the asks
// follow database.OwedBackoff (6 h, 24 h, 3 d, 7 d), the fifth failed ask ends
// them, and no run in between asks a fallback provider (the Google Books budget).
func TestCandidateOwed_PermanentFailureBacksOffAndStopsAtTheCap(t *testing.T) {
	f, cc, _ := newOwedServerFixture(t)
	b := f.book(t, owedTitle, withASIN(refreshASIN))
	f.run(t, b.ID) // ask 1: Audnexus fails, Google matches
	fallbackBefore := f.openlib.calls.Load() + f.google.calls.Load()
	lookups := func() int { return cc.get(metadata.SourceIDAudnexus) }
	if lookups() != 1 {
		t.Fatalf("lookups after run 1 = %d, want 1", lookups())
	}

	for i, wait := range database.OwedBackoff { // asks 2..5
		// One minute short of due: not asked.
		metadata.ResetThrottlesForTesting()
		ageOwed(t, f, b.ID, wait-time.Minute)
		before := lookups()
		r := f.run(t, b.ID)[b.ID]
		if lookups() != before || r.Cached != candidateCachedCandidates {
			t.Fatalf("ask %d: re-asked %v early (lookups %d -> %d, cached %q)", i+2, time.Minute, before, lookups(), r.Cached)
		}
		// Due: asked exactly once, fails again, the book is degraded (it keeps
		// its candidate and is not an error), and the debt is one ask older.
		ageOwed(t, f, b.ID, 2*time.Minute)
		r = f.run(t, b.ID)[b.ID]
		if lookups() != before+1 {
			t.Fatalf("ask %d: lookups %d -> %d, want exactly one more", i+2, before, lookups())
		}
		if r.Status == "error" || !slices.Equal(r.FailedSources, []string{"Audnexus (Audible)"}) || r.ErrorCause != metadata.CauseUpstream {
			t.Fatalf("ask %d result = {%q failed %v cause %q} (%s), want degraded upstream_5xx",
				i+2, r.Status, r.FailedSources, r.ErrorCause, r.Error)
		}
		if got := owedRow(t, f, b.ID).OwedSources["Audnexus (Audible)"].Attempts; got != i+2 {
			t.Fatalf("after ask %d the row records %d attempts", i+2, got)
		}
		if got := candidateSources(t, owedRow(t, f, b.ID)); !slices.Equal(got, []string{"Google Books"}) {
			t.Fatalf("ask %d lost the candidate the other sources found: %v", i+2, got)
		}
	}

	// Cap reached: however long it waits, Audnexus is not asked again, and the
	// scheduled selection does not hand the book back.
	metadata.ResetThrottlesForTesting()
	ageOwed(t, f, b.ID, 10_000*time.Hour)
	before := lookups()
	r := f.run(t, b.ID)[b.ID]
	if lookups() != before || r.Cached != candidateCachedCandidates {
		t.Fatalf("past the cap Audnexus was asked again: lookups %d -> %d, cached %q", before, lookups(), r.Cached)
	}
	if got := lookups(); got != database.OwedMaxAsks {
		t.Fatalf("total Audnexus asks = %d, want the cap %d", got, database.OwedMaxAsks)
	}
	sel, err := unfetchedCandidateBookIDs(t.Context(), f.store, f.mfs, f.s.newFolderMemo(f.store), nil, 800)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(sel.IDs, b.ID) || sel.OwedDue != 0 {
		t.Fatalf("selection past the cap = %+v, want the book left alone", sel)
	}
	if got := f.openlib.calls.Load() + f.google.calls.Load(); got != fallbackBefore {
		t.Fatalf("fallback provider calls %d -> %d across the re-asks, want none", fallbackBefore, got)
	}
}

// A provider hold sent nothing: it does not use up one of the five asks, or one
// Audnexus outage would exhaust every book at once.
func TestCandidateOwed_AHoldDoesNotSpendAnAsk(t *testing.T) {
	f, cc, _ := newOwedServerFixture(t)
	b := f.book(t, owedTitle, withASIN(refreshASIN))
	f.run(t, b.ID) // ask 1; the failure holds Audnexus
	ageOwed(t, f, b.ID, 7*time.Hour)
	before := cc.get(metadata.SourceIDAudnexus)

	r := f.run(t, b.ID)[b.ID] // due, but Audnexus is held: no lookup is sent
	if cc.get(metadata.SourceIDAudnexus) != before {
		t.Fatal("a lookup was sent to a held provider")
	}
	if r.ErrorCause != metadata.CauseHeld || r.Status == "error" {
		t.Fatalf("result = {%q cause %q}, want degraded provider_held", r.Status, r.ErrorCause)
	}
	o := owedRow(t, f, b.ID).OwedSources["Audnexus (Audible)"]
	if o.Attempts != 1 {
		t.Fatalf("a hold used an ask: attempts = %d, want 1", o.Attempts)
	}
	if time.Since(o.LastAttempt) > time.Minute {
		t.Fatalf("a hold did not restart the backoff: last attempt %v ago", time.Since(o.LastAttempt))
	}
}

// A degraded result never replaces or outranks an existing candidate from the
// better source: a forced refetch while Audnexus is down, with another source
// answering, keeps the Audnexus candidate the row already holds, first, and the
// review-only Google candidate too.
func TestCandidateOwed_DegradedResultKeepsTheBetterCandidate(t *testing.T) {
	f, _, ol := newOwedServerFixture(t)
	b := f.book(t, owedTitle, withASIN(refreshASIN))
	f.run(t, b.ID) // Audnexus down, Google matches
	ol.down.Store(false)
	metadata.ResetThrottlesForTesting()
	ageOwed(t, f, b.ID, 7*time.Hour)
	f.run(t, b.ID) // Audnexus recovers: [Audnexus Google]
	if got := candidateSources(t, owedRow(t, f, b.ID)); !slices.Equal(got, []string{"Audnexus (Audible)", "Google Books"}) {
		t.Fatalf("setup: candidates = %v", got)
	}

	ol.down.Store(true)
	metadata.ResetThrottlesForTesting()
	// Another chain source answers (a weaker match), so the row is rewritten by
	// a search with results, the path that used to drop the failed source's
	// candidate.
	f.audible.answer = answersTitle("Gotham Year Zero", "Selina Kyle")
	f.opCounter++
	res := runCandidateFetch(t, f.s, "op-owed-force", []string{b.ID}, true)[b.ID]
	if res.Status == "error" {
		t.Fatalf("forced refetch = error (%s), want degraded", res.Error)
	}
	got := candidateSources(t, owedRow(t, f, b.ID))
	if len(got) == 0 || got[0] != "Audnexus (Audible)" {
		t.Fatalf("after a forced refetch with Audnexus down candidates = %v, want Audnexus first", got)
	}
	if !slices.Contains(got, "Google Books") {
		t.Fatalf("candidates = %v, lost the Google Books candidate", got)
	}
	if res.Candidate == nil || res.Candidate.Source != "Audnexus (Audible)" {
		t.Fatalf("the degraded result's candidate = %+v, want the Audnexus one", res.Candidate)
	}
	if _, owed := owedRow(t, f, b.ID).OwedSources["Audnexus (Audible)"]; !owed {
		t.Fatal("the failed source is not owed after the forced refetch")
	}
}

// A row written before the field existed owes nothing and keeps today's meaning:
// parked on its candidates, never re-asked, never selected.
func TestCandidateOwed_RowWithoutTheFieldKeepsItsMeaning(t *testing.T) {
	f, cc, _ := newOwedServerFixture(t)
	b := f.book(t, owedTitle, withASIN(refreshASIN))
	f.run(t, b.ID)
	row := owedRow(t, f, b.ID)
	row.OwedSources = nil // the row as it was before this field
	if err := f.store.PutMetadataCache(row); err != nil {
		t.Fatal(err)
	}
	metadata.ResetThrottlesForTesting()
	before, calls := cc.get(metadata.SourceIDAudnexus), callsOf(f)

	r := f.run(t, b.ID)[b.ID]
	if r.Cached != candidateCachedCandidates || cc.get(metadata.SourceIDAudnexus) != before || callsOf(f) != calls {
		t.Fatalf("a row without owed_sources was re-asked: cached %q", r.Cached)
	}
	sel, err := unfetchedCandidateBookIDs(t.Context(), f.store, f.mfs, f.s.newFolderMemo(f.store), nil, 800)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(sel.IDs, b.ID) {
		t.Fatalf("selection returned a row that owes nothing: %+v", sel)
	}
}

// The scheduled "only unmatched" selection hands back a book whose row holds
// candidates ONLY when a source it owes is due; not-due, exhausted and
// owes-nothing rows are left alone, and the selection says how many it picked
// for this reason and how many wait.
func TestCandidateOwed_SelectionPicksOnlyDueRows(t *testing.T) {
	f, _, _ := newOwedServerFixture(t)
	mk := func(title string) *database.Book {
		b := f.book(t, title, withASIN(refreshASIN))
		f.run(t, b.ID)
		return b
	}
	due, waiting, spent, plain := mk("Gotham Knights One"), mk("Gotham Knights Two"), mk("Gotham Knights Three"), mk("Gotham Knights Four")
	metadata.ResetThrottlesForTesting()

	ageOwed(t, f, due.ID, 7*time.Hour)
	ageOwed(t, f, waiting.ID, 5*time.Hour)
	e := owedRow(t, f, spent.ID)
	o := e.OwedSources["Audnexus (Audible)"]
	o.Attempts = database.OwedMaxAsks
	e.OwedSources["Audnexus (Audible)"] = o
	if err := f.store.PutMetadataCache(e); err != nil {
		t.Fatal(err)
	}
	ageOwed(t, f, spent.ID, 10_000*time.Hour)
	e = owedRow(t, f, plain.ID)
	e.OwedSources = nil
	if err := f.store.PutMetadataCache(e); err != nil {
		t.Fatal(err)
	}

	sel, err := unfetchedCandidateBookIDs(t.Context(), f.store, f.mfs, f.s.newFolderMemo(f.store), nil, 800)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sel.IDs, []string{due.ID}) {
		t.Fatalf("selected %v, want only the due one %s (waiting %s, spent %s, plain %s)",
			sel.IDs, due.ID, waiting.ID, spent.ID, plain.ID)
	}
	if sel.OwedDue != 1 || sel.OwedWaiting != 1 {
		t.Fatalf("OwedDue %d OwedWaiting %d, want 1 and 1", sel.OwedDue, sel.OwedWaiting)
	}
	if len(sel.GoogleCapped) != 0 || sel.FallbackPending != 0 {
		t.Fatalf("an owed re-ask was funded as a fallback pass: %+v", sel)
	}
}

// A row with NO candidates (every other source answered with nothing) that owes
// Audnexus an answer is re-asked on the same bounded schedule: the failed ask on
// the chain-only path (no candidate to keep, no fallback left to ask) still
// counts, and the cap still ends it.
func TestCandidateOwed_FailedAskOnAnEmptyRowCountsAndIsBounded(t *testing.T) {
	f, cc, _ := newOwedServerFixture(t)
	f.google.answer = nil // nobody finds the book: the row holds no candidate
	b := f.book(t, owedTitle, withASIN(refreshASIN))

	f.run(t, b.ID)
	row := owedRow(t, f, b.ID)
	if len(row.Candidates) != 0 || row.OwedSources["Audnexus (Audible)"].Attempts != 1 {
		t.Fatalf("setup: candidates %d, owed %+v, want an empty row owing Audnexus after one ask", len(row.Candidates), row.OwedSources)
	}
	for ask := 2; ask <= database.OwedMaxAsks; ask++ {
		metadata.ResetThrottlesForTesting()
		ageOwed(t, f, b.ID, 8*24*time.Hour)
		before := cc.get(metadata.SourceIDAudnexus)
		r := f.run(t, b.ID)[b.ID]
		if cc.get(metadata.SourceIDAudnexus) != before+1 {
			t.Fatalf("ask %d: lookups %d -> %d, want one more", ask, before, cc.get(metadata.SourceIDAudnexus))
		}
		if r.Status != "error" {
			t.Fatalf("ask %d status = %q, want error (nothing answered for the ASIN)", ask, r.Status)
		}
		if got := owedRow(t, f, b.ID).OwedSources["Audnexus (Audible)"].Attempts; got != ask {
			t.Fatalf("after ask %d the row records %d attempts: a failed re-ask on an empty row was not recorded", ask, got)
		}
	}
	// Selection: not handed back once the asks are spent.
	metadata.ResetThrottlesForTesting()
	ageOwed(t, f, b.ID, 10_000*time.Hour)
	sel, err := unfetchedCandidateBookIDs(t.Context(), f.store, f.mfs, f.s.newFolderMemo(f.store), nil, 800)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(sel.IDs, b.ID) {
		t.Fatalf("selection returned a spent owed row: %+v", sel)
	}
}

// The re-ask of an owed source is MERGED into the row: a candidate another CHAIN
// source (not a fallback provider, whose candidates a replacing write keeps by
// its own rule) supplied in the run that parked the book stays on the row.
func TestCandidateOwed_ReaskKeepsAnotherChainSourcesCandidate(t *testing.T) {
	f, _, ol := newOwedServerFixture(t)
	f.google.answer = nil // no fallback candidate: the row is Audible's alone
	f.audible.answer = answersTitle(owedTitle, "Selina Kyle")
	b := f.book(t, owedTitle, withASIN(refreshASIN))

	f.run(t, b.ID)
	if got := candidateSources(t, owedRow(t, f, b.ID)); !slices.Equal(got, []string{"Audible"}) {
		t.Fatalf("setup: run 1 candidates = %v, want [Audible]", got)
	}
	ol.down.Store(false)
	metadata.ResetThrottlesForTesting()
	ageOwed(t, f, b.ID, 7*time.Hour)
	f.run(t, b.ID)
	got := candidateSources(t, owedRow(t, f, b.ID))
	if !slices.Contains(got, "Audnexus (Audible)") || !slices.Contains(got, "Audible") {
		t.Fatalf("after the re-ask candidates = %v, want Audnexus (Audible) added to Audible", got)
	}
}

// owedUpdateOf names only the chain's own sources as owed: a failure filed under
// a label the verdict cannot re-ask (the direct ASIN lookup's) is no debt.
func TestOwedUpdateOf_OnlyActiveChainSourcesAreOwed(t *testing.T) {
	resp := &metafetch.SearchMetadataResponse{SourcesFailed: map[string]string{
		"Audnexus (Audible)": "502", "Some Other Label": "boom"}}
	u := owedUpdateOf(resp, []string{"Audnexus (Audible)", "Google Books"})
	if len(u.Failed) != 1 || u.Failed["Audnexus (Audible)"] == "" {
		t.Fatalf("Failed = %v, want only Audnexus (Audible)", u.Failed)
	}
	if got := owedUpdateOf(nil, nil); len(got.Failed) != 0 || len(got.Answered) != 0 {
		t.Fatalf("a nil response said something: %+v", got)
	}
}

// A run cancelled while Audnexus is being asked says nothing about Audnexus: the
// failure it sees is the cancellation, and no ask may be spent on it.
func TestCandidateOwed_CancelledRunRecordsNoDebt(t *testing.T) {
	f, _, _ := newOwedServerFixture(t)
	// Another chain source answers, so the chain writes its row: the one place a
	// debt could be recorded for the failed Audnexus.
	f.google.answer = nil
	f.audible.answer = answersTitle(owedTitle, "Selina Kyle")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f.mfs.SetASINLookupOverride(func(_ context.Context, provider, _ string) (*metadata.BookMetadata, error) {
		cancel()
		return nil, &metadata.ProviderStatusError{Provider: provider, Status: 502, Body: "bad gateway"}
	})
	b := f.book(t, owedTitle, withASIN(refreshASIN))

	params, err := json.Marshal(metadataCandidateFetchOpParams{BookIDs: []string{b.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_ = f.s.runMetadataCandidateFetchOp(ctx, params, &resumeRecorder{opID: "op-owed-cancel"})

	e, _ := f.store.GetMetadataCache(b.ID)
	if e == nil {
		t.Fatal("no cache row was written, so this tests nothing: the chain must have written its row")
	}
	if len(e.OwedSources) != 0 {
		t.Fatalf("a cancelled run left OwedSources = %+v", e.OwedSources)
	}
}
