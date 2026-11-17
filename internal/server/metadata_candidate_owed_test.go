// file: internal/server/metadata_candidate_owed_test.go
// version: 1.2.0
// guid: c41e7a96-5d03-4f28-9b6a-2e8f1d70a3c5
// last-edited: 2026-11-17

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
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

// A degraded result does not discard the candidate an earlier answer of the
// better source left on the row, or rank another source's candidate over it: a forced refetch while Audnexus is down, with another source
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

// metafetch.OwedUpdateOf names only the chain's own sources as owed: a failure filed under
// a label the verdict cannot re-ask (the direct ASIN lookup's) is no debt.
func TestOwedUpdateOf_OnlyActiveChainSourcesAreOwed(t *testing.T) {
	resp := &metafetch.SearchMetadataResponse{SourcesFailed: map[string]string{
		"Audnexus (Audible)": "502", "Some Other Label": "boom"}}
	u := metafetch.OwedUpdateOf(resp, []string{"Audnexus (Audible)", "Google Books"})
	if len(u.Failed) != 1 || u.Failed["Audnexus (Audible)"] == "" {
		t.Fatalf("Failed = %v, want only Audnexus (Audible)", u.Failed)
	}
	if got := metafetch.OwedUpdateOf(nil, nil); len(got.Failed) != 0 || len(got.Answered) != 0 {
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

// setOwed rewrites the book's owed source on its row: asked n times, last asked
// (and first failed) ago ago.
func setOwed(t *testing.T, f *fallbackFixture, bookID string, n int, ago time.Duration) {
	t.Helper()
	e := owedRow(t, f, bookID)
	at := time.Now().UTC().Add(-ago)
	e.OwedSources = map[string]database.OwedSource{"Audnexus (Audible)": {Since: at, LastAttempt: at, Attempts: n, Cause: metadata.CauseUpstream}}
	if err := f.store.PutMetadataCache(e); err != nil {
		t.Fatal(err)
	}
}

// The owner opens the Search Metadata dialog on a parked book between two runs.
// Its plain fetch writes the row under another input hash than the op's, and
// must not drop the debt Audnexus owes (it charges its own failed ask, once): the
// next selection still hands the book back and the op still asks Audnexus.
func TestCandidateOwed_DialogFetchBetweenRunsKeepsTheDebt(t *testing.T) {
	f, _, ol := newOwedServerFixture(t)
	b := f.book(t, owedTitle, withASIN(refreshASIN))
	f.run(t, b.ID) // Audnexus down, Google matches: Audnexus is owed
	if _, owed := owedRow(t, f, b.ID).OwedSources["Audnexus (Audible)"]; !owed {
		t.Fatal("setup: run 1 recorded no debt")
	}

	// What the dialog's handler runs (handlers/metadata plain fetch): the books's
	// own title, the throttle bypassed. Audnexus is still down in it.
	if _, err := f.mfs.FetchAndCache(context.Background(), b.ID, "", "", "", "",
		metafetch.SearchOptions{BypassProviderThrottle: true}); err != nil {
		t.Fatalf("dialog fetch: %v", err)
	}
	row := owedRow(t, f, b.ID)
	o, owed := row.OwedSources["Audnexus (Audible)"]
	if !owed {
		t.Fatalf("the dialog's fetch dropped the debt: %+v", row.OwedSources)
	}
	if o.Attempts != 2 {
		t.Fatalf("attempts after a failed dialog ask = %d, want 2 (one ask for the failed dialog search)", o.Attempts)
	}

	// Next selection, once the backoff has run: the book is handed back.
	ol.down.Store(false)
	metadata.ResetThrottlesForTesting()
	ageOwed(t, f, b.ID, 25*time.Hour)
	sel, err := unfetchedCandidateBookIDs(t.Context(), f.store, f.mfs, f.s.newFolderMemo(f.store), nil, 800)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(sel.IDs, b.ID) {
		t.Fatalf("selection after the dialog fetch = %+v, want the book handed back for its owed source", sel)
	}
	r := f.run(t, b.ID)[b.ID]
	if r.Candidate == nil || r.Candidate.Source != "Audnexus (Audible)" || len(owedRow(t, f, b.ID).OwedSources) != 0 {
		t.Fatalf("the re-ask after the dialog fetch = {%+v}, owed %+v, want Audnexus answered and the debt cleared",
			r.Candidate, owedRow(t, f, b.ID).OwedSources)
	}
}

// The same sequence with Audnexus still down at the re-ask: the dialog's row has
// another input hash than the op's, and the op's failed re-ask must still be
// charged. If its record missed that row the book would be due on every run and
// never exhaust, the unbounded re-ask the schedule exists to stop.
func TestCandidateOwed_FailedReaskAfterADialogRewriteIsStillCharged(t *testing.T) {
	for _, failAll := range []bool{false, true} {
		t.Run(fmt.Sprintf("every_source_fails=%t", failAll), func(t *testing.T) {
			failedReaskAfterADialogRewrite(t, failAll)
		})
	}
}

func failedReaskAfterADialogRewrite(t *testing.T, failAll bool) {
	t.Helper()
	f, _, _ := newOwedServerFixture(t)
	b := f.book(t, owedTitle, withASIN(refreshASIN))
	f.run(t, b.ID) // Audnexus down: owed, attempt 1
	if _, err := f.mfs.FetchAndCache(context.Background(), b.ID, "", "", "", "",
		metafetch.SearchOptions{BypassProviderThrottle: true}); err != nil {
		t.Fatalf("dialog fetch: %v", err)
	}
	metadata.ResetThrottlesForTesting()
	ageOwed(t, f, b.ID, 25*time.Hour)
	before := owedRow(t, f, b.ID).OwedSources["Audnexus (Audible)"]

	if failAll {
		// Nothing answers the re-ask, so the search writes no row of its own.
		f.google.answer = nil
		f.google.err = &metadata.ProviderStatusError{Provider: metadata.SourceIDGoogleBooks, Status: 503, Body: "backend error"}
	}
	f.run(t, b.ID) // the due re-ask: Audnexus is still down
	after, owed := owedRow(t, f, b.ID).OwedSources["Audnexus (Audible)"]
	if !owed {
		t.Fatal("the failed re-ask cleared the debt")
	}
	if after.Attempts != before.Attempts+1 {
		t.Fatalf("attempts after a failed re-ask on the dialog's row = %d, want %d", after.Attempts, before.Attempts+1)
	}
	if !after.LastAttempt.After(before.LastAttempt) {
		t.Fatalf("last attempt did not move (%v -> %v): the book would be due again at once", before.LastAttempt, after.LastAttempt)
	}
}

// A dialog search that hears from Audnexus clears the debt it owed; one that does
// not leaves it. (The dialog is the other writer of the row.)
func TestCandidateOwed_DialogFetchThatAnswersClearsTheDebt(t *testing.T) {
	f, _, ol := newOwedServerFixture(t)
	b := f.book(t, owedTitle, withASIN(refreshASIN))
	f.run(t, b.ID)
	ol.down.Store(false)
	metadata.ResetThrottlesForTesting()
	if _, err := f.mfs.FetchAndCache(context.Background(), b.ID, "", "", "", "",
		metafetch.SearchOptions{BypassProviderThrottle: true}); err != nil {
		t.Fatalf("dialog fetch: %v", err)
	}
	if got := owedRow(t, f, b.ID).OwedSources; len(got) != 0 {
		t.Fatalf("a dialog search Audnexus answered left the debt: %+v", got)
	}
}

// The fifth failed ask of a source is the last: the run says so (the result's
// owed_gave_up, the refresh summary), the selection counts the book, and an
// owner's forced retry then starts the schedule over instead of stepping on a
// spent one.
func TestCandidateOwed_GivingUpIsReportedAndAForcedRetryStartsOver(t *testing.T) {
	f, _, _ := newOwedServerFixture(t)
	b := f.book(t, owedTitle, withASIN(refreshASIN))
	f.run(t, b.ID)

	// The 4th ask has been spent; the 5th, due now, fails again.
	metadata.ResetThrottlesForTesting()
	setOwed(t, f, b.ID, database.OwedMaxAsks-1, 8*24*time.Hour)
	r := f.run(t, b.ID)[b.ID]
	if !slices.Equal(r.OwedGaveUp, []string{"Audnexus (Audible)"}) {
		t.Fatalf("5th failed ask result OwedGaveUp = %v, want [Audnexus (Audible)]", r.OwedGaveUp)
	}
	if got := owedRow(t, f, b.ID).OwedSources["Audnexus (Audible)"].Attempts; got != database.OwedMaxAsks {
		t.Fatalf("attempts = %d, want %d", got, database.OwedMaxAsks)
	}
	var tally candidateFetchTally
	tally.record(r)
	if got := tally.refreshSummary(); got != "refresh summary: refreshed 0, degraded 1 (upstream_5xx 1), failed 0, deferred 0, not asked 0, gave up on a source 1" {
		t.Fatalf("summary = %q", got)
	}
	sel, err := unfetchedCandidateBookIDs(t.Context(), f.store, f.mfs, f.s.newFolderMemo(f.store), nil, 800)
	if err != nil {
		t.Fatal(err)
	}
	if sel.OwedExhausted != 1 || slices.Contains(sel.IDs, b.ID) {
		t.Fatalf("selection = %+v, want the book counted as gave-up (1) and left alone", sel)
	}
	// The row says so to the review lane.
	if got := owedRow(t, f, b.ID).OwedExhaustedSources(); !slices.Equal(got, []string{"Audnexus (Audible)"}) {
		t.Fatalf("OwedExhaustedSources = %v", got)
	}

	// Spent: the plain run leaves it alone ...
	metadata.ResetThrottlesForTesting()
	before := callsOf(f)
	plain := f.run(t, b.ID)[b.ID]
	if plain.Cached != candidateCachedCandidates || len(plain.OwedGaveUp) != 0 || callsOf(f) != before {
		t.Fatalf("a spent source was asked by a plain run: %+v", plain)
	}
	// ... and the owner's forced retry asks it again and, when it fails again,
	// restarts at the first ask: not a sixth.
	f.opCounter++
	forced := runCandidateFetch(t, f.s, fmt.Sprintf("op-owed-forced-%d", f.opCounter), []string{b.ID}, true)[b.ID]
	o := owedRow(t, f, b.ID).OwedSources["Audnexus (Audible)"]
	if o.Attempts != 1 || o.Exhausted() || time.Since(o.Since) > time.Minute {
		t.Fatalf("after a forced retry the source is %+v, want a fresh schedule (attempts 1, since now)", o)
	}
	if len(forced.OwedGaveUp) != 0 {
		t.Fatalf("a forced retry that restarted the schedule reported giving up: %v", forced.OwedGaveUp)
	}
	// It is asked again on the new schedule's first backoff (6 h), not left spent.
	ageOwed(t, f, b.ID, 7*time.Hour)
	sel, err = unfetchedCandidateBookIDs(t.Context(), f.store, f.mfs, f.s.newFolderMemo(f.store), nil, 800)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(sel.IDs, b.ID) || sel.OwedExhausted != 0 {
		t.Fatalf("selection after the forced retry = %+v, want the book due again and nothing gave up", sel)
	}
}

// A row refetched after its TTL does not carry the debt it exhausted: a source
// that fails in the refetch is recorded as a first failure with a fresh schedule.
func TestCandidateOwed_TTLRefetchAfterExhaustionGetsAFreshSchedule(t *testing.T) {
	f, _, _ := newOwedServerFixture(t)
	b := f.book(t, owedTitle, withASIN(refreshASIN))
	f.run(t, b.ID)
	setOwed(t, f, b.ID, database.OwedMaxAsks, 40*24*time.Hour)
	e := owedRow(t, f, b.ID)
	e.FetchedAt = time.Now().UTC().Add(-(database.MetadataCacheTTL + 24*time.Hour))
	if err := f.store.PutMetadataCache(e); err != nil {
		t.Fatal(err)
	}
	metadata.ResetThrottlesForTesting()

	f.run(t, b.ID) // the row is past its TTL: the book is searched afresh
	o := owedRow(t, f, b.ID).OwedSources["Audnexus (Audible)"]
	if o.Attempts != 1 || o.Exhausted() || time.Since(o.Since) > time.Minute {
		t.Fatalf("a TTL refetch left the source %+v, want a fresh schedule (attempts 1, since now)", o)
	}
}

// Two ops on one book in the same window charge ONE ask. The server's per-book
// claim (bookFetchClaims) makes the second wait for the first and then read the
// row it left, so it is served from the cache and asks Audnexus nothing; the
// charge guard (metafetch.OwedUpdate.Seen) is the second line, below.
func TestCandidateOwed_TwoConcurrentOpsChargeOneAsk(t *testing.T) {
	f, cc, _ := newOwedServerFixture(t)
	b := f.book(t, owedTitle, withASIN(refreshASIN))
	f.run(t, b.ID)
	setOwed(t, f, b.ID, 1, 7*time.Hour)
	metadata.ResetThrottlesForTesting()
	before := cc.get(metadata.SourceIDAudnexus)

	params, err := json.Marshal(metadataCandidateFetchOpParams{BookIDs: []string{b.ID}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = f.s.runMetadataCandidateFetchOp(context.Background(), params,
				&resumeRecorder{opID: fmt.Sprintf("op-owed-concurrent-%d", i)})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("op %d: %v", i, err)
		}
	}
	if got := owedRow(t, f, b.ID).OwedSources["Audnexus (Audible)"].Attempts; got != 2 {
		t.Fatalf("attempts after two concurrent ops = %d, want 2 (one ask charged, not two)", got)
	}
	if got := cc.get(metadata.SourceIDAudnexus) - before; got != 1 {
		t.Fatalf("Audnexus lookups across two concurrent ops = %d, want 1", got)
	}
}

// The writer the per-book claim does NOT cover is the search dialog's plain
// fetch. While an op is mid-ask (its row read, Audnexus not yet answered), the
// owner's dialog search asks Audnexus too and charges that window's ask first; the
// op, recording after it, sees the row's last attempt has moved and charges nothing.
// Run with -race: the two writers share the row.
func TestCandidateOwed_OpAndDialogInTheSameWindowChargeOneAsk(t *testing.T) {
	var gate atomic.Bool
	var calls atomic.Int32
	inLookup, release := make(chan struct{}), make(chan struct{})
	ol := &owedLookup{}
	f, _ := newRefreshFixtureRecord(t, func(provider string) (*metadata.BookMetadata, error) {
		if provider == metadata.SourceIDAudnexus && gate.Load() && calls.Add(1) == 1 {
			close(inLookup) // the op's own ask: hold it until the dialog has answered
			select {
			case <-release:
			case <-time.After(10 * time.Second):
			}
		}
		return ol.lookup(provider)
	})
	ol.down.Store(true)
	f.google.answer = answersTitle(owedTitle, "Bruce Wayne")
	b := f.book(t, owedTitle, withASIN(refreshASIN))
	f.run(t, b.ID)
	setOwed(t, f, b.ID, 1, 7*time.Hour)
	metadata.ResetThrottlesForTesting()
	gate.Store(true)

	params, err := json.Marshal(metadataCandidateFetchOpParams{BookIDs: []string{b.ID}})
	if err != nil {
		t.Fatal(err)
	}
	opDone := make(chan error, 1)
	go func() {
		opDone <- f.s.runMetadataCandidateFetchOp(context.Background(), params, &resumeRecorder{opID: "op-owed-vs-dialog"})
	}()
	select {
	case <-inLookup:
	case <-time.After(10 * time.Second):
		t.Fatal("the op never reached Audnexus")
	}
	// The dialog searches the same title with the same author hint, so its row
	// has the op's source hash and fingerprint: the op's record would find a
	// row it may write (with another hash it would find none, and the guard would
	// never be reached).
	if _, err := f.mfs.FetchAndCache(context.Background(), b.ID, owedTitle, "", "", "",
		metafetch.SearchOptions{BypassProviderThrottle: true}); err != nil {
		t.Fatalf("dialog fetch: %v", err)
	}
	// The dialog is the first writer of the window and charges its ask.
	if got := owedRow(t, f, b.ID).OwedSources["Audnexus (Audible)"].Attempts; got != 2 {
		t.Fatalf("attempts after the dialog's fetch = %d, want 2", got)
	}
	close(release)
	if err := <-opDone; err != nil {
		t.Fatalf("op: %v", err)
	}
	if got := owedRow(t, f, b.ID).OwedSources["Audnexus (Audible)"].Attempts; got != 2 {
		t.Fatalf("attempts after an op and a dialog search in one window = %d, want 2 (one ask charged, not two)", got)
	}
}

// The selection reads the service's clock, the one the verdict and the recorder
// read, not the wall clock: moving it forward makes a row's backoff run out in
// the selection exactly as it does in the verdict.
func TestCandidateOwed_SelectionReadsTheServiceClock(t *testing.T) {
	f, _, _ := newOwedServerFixture(t)
	b := f.book(t, owedTitle, withASIN(refreshASIN))
	f.run(t, b.ID) // owed Audnexus, asked just now: due in 6 h
	pick := func() unfetchedSelection {
		sel, err := unfetchedCandidateBookIDs(t.Context(), f.store, f.mfs, f.s.newFolderMemo(f.store), nil, 800)
		if err != nil {
			t.Fatal(err)
		}
		return sel
	}
	if sel := pick(); slices.Contains(sel.IDs, b.ID) || sel.OwedWaiting != 1 {
		t.Fatalf("selection before the backoff = %+v, want the book waiting", sel)
	}
	t.Cleanup(metafetch.SetClockForTesting(func() time.Time { return time.Now().UTC().Add(7 * time.Hour) }))
	if sel := pick(); !slices.Contains(sel.IDs, b.ID) || sel.OwedDue != 1 {
		t.Fatalf("selection 7 h on the service clock = %+v, want the book due", sel)
	}
}
