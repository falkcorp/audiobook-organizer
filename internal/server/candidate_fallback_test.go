// file: internal/server/candidate_fallback_test.go
// version: 1.0.0
// guid: e354c0f7-eb13-49b7-94fa-e4fe6d9b985a
// last-edited: 2026-10-06

package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// callLog records the order providers are first asked in.
type callLog struct {
	mu  sync.Mutex
	seq []string
}

func (l *callLog) add(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !slices.Contains(l.seq, name) {
		l.seq = append(l.seq, name)
	}
}

func (l *callLog) order() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.seq)
}

// idSource is a fake provider that declares a provider id, so the fallback
// plan recognises it. No network: every answer is canned.
type idSource struct {
	id, name string
	log      *callLog
	calls    atomic.Int64
	mu       sync.Mutex
	// answer returns the results for a title query; nil = nothing.
	answer func(title string) []metadata.BookMetadata
	err    error
}

func (f *idSource) ProviderID() string { return f.id }
func (f *idSource) Name() string       { return f.name }
func (f *idSource) search(title string) ([]metadata.BookMetadata, error) {
	f.calls.Add(1)
	if f.log != nil {
		f.log.add(f.name)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if f.answer == nil {
		return nil, nil
	}
	return f.answer(title), nil
}
func (f *idSource) SearchByTitle(_ context.Context, title string) ([]metadata.BookMetadata, error) {
	return f.search(title)
}
func (f *idSource) SearchByTitleAndAuthor(_ context.Context, title, _ string) ([]metadata.BookMetadata, error) {
	return f.search(title)
}

// answersTitle answers any query with one record for the given title.
func answersTitle(title, author string) func(string) []metadata.BookMetadata {
	return func(string) []metadata.BookMetadata {
		return []metadata.BookMetadata{{Title: title, Author: author, CoverURL: "https://example.invalid/c.jpg"}}
	}
}

type fallbackFixture struct {
	s         *Server
	store     database.Store
	mfs       *metafetch.Service
	log       *callLog
	audible   *idSource
	openlib   *idSource
	google    *idSource
	budget    *metafetch.DailyBudget
	clock     time.Time
	limit     int
	opCounter int
}

// newFallbackFixture builds a server with Audible, Open Library and Google
// Books fakes (in that chain order) and a Google budget of limit lookups per
// day, persisted in the server's own store.
func newFallbackFixture(t *testing.T, limit int) *fallbackFixture {
	t.Helper()
	s, cleanup := setupTestServer(t)
	t.Cleanup(cleanup)
	f := &fallbackFixture{s: s, store: s.storeForWiring(), log: &callLog{}, limit: limit,
		clock: time.Date(2026, 10, 6, 18, 0, 0, 0, time.UTC)}
	f.audible = &idSource{id: metadata.SourceIDAudible, name: "Audible", log: f.log}
	f.openlib = &idSource{id: metadata.SourceIDOpenLibrary, name: "Open Library", log: f.log}
	f.google = &idSource{id: metadata.SourceIDGoogleBooks, name: "Google Books", log: f.log}
	f.mfs = metafetch.NewService(f.store)
	f.mfs.SetOverrideSources([]metadata.MetadataSource{f.audible, f.openlib, f.google})
	s.metadataFetchService = f.mfs
	f.budget = metafetch.NewDailyBudget(f.store, metadata.SourceIDGoogleBooks, func() int { return f.limit })
	f.budget.SetClock(func() time.Time { return f.clock })
	s.candidateFallback.budget = f.budget
	return f
}

func (f *fallbackFixture) book(t *testing.T, title string, mutate ...func(*database.Book)) *database.Book {
	t.Helper()
	b := &database.Book{Title: title, FilePath: "/lib/" + title + "/book.m4b"}
	for _, m := range mutate {
		m(b)
	}
	created, err := f.store.CreateBook(b)
	if err != nil {
		t.Fatalf("CreateBook %q: %v", title, err)
	}
	return created
}

func (f *fallbackFixture) run(t *testing.T, ids ...string) map[string]CandidateResult {
	t.Helper()
	f.opCounter++
	return runCandidateFetch(t, f.s, fmt.Sprintf("op-fallback-%d", f.opCounter), ids, false)
}

func (f *fallbackFixture) resetCalls() {
	for _, src := range []*idSource{f.audible, f.openlib, f.google} {
		src.calls.Store(0)
	}
	f.log = &callLog{}
	f.audible.log, f.openlib.log, f.google.log = f.log, f.log, f.log
}

func stepOutcomes(steps []metabatch.FallbackStep) []string {
	out := make([]string, 0, len(steps))
	for _, st := range steps {
		out = append(out, st.Provider+"="+st.Outcome)
	}
	return out
}

// Audible finds nothing, Open Library finds nothing, Google Books finds the
// book: the providers are asked in that order and the Google candidate lands
// as an ordinary fetched candidate with its source recorded.
func TestCandidateFallback_AsksOpenLibraryThenGoogleAfterAudibleMiss(t *testing.T) {
	f := newFallbackFixture(t, 800)
	f.google.answer = answersTitle("Obscure Field Guide", "A. Writer")
	b := f.book(t, "Obscure Field Guide")

	r := f.run(t, b.ID)[b.ID]
	if r.Status != "matched" || r.Candidate == nil || r.Candidate.Source != "Google Books" {
		t.Fatalf("result = {status %q, candidate %+v}, want matched from Google Books", r.Status, r.Candidate)
	}
	if got, want := f.log.order(), []string{"Audible", "Open Library", "Google Books"}; !slices.Equal(got, want) {
		t.Fatalf("provider order = %v, want %v", got, want)
	}
	if got, want := stepOutcomes(r.Fallback), []string{"openlibrary=no_match", "google-books=matched"}; !slices.Equal(got, want) {
		t.Fatalf("fallback steps = %v, want %v", got, want)
	}
	if got := f.google.calls.Load(); got != 1 {
		t.Fatalf("Google Books calls = %d, want 1", got)
	}
	if got := f.budget.Remaining(); got != 799 {
		t.Fatalf("budget remaining = %d, want 799", got)
	}
	entry, err := f.store.GetMetadataCache(b.ID)
	if err != nil || entry == nil || len(entry.Candidates) == 0 {
		t.Fatalf("cache row = %+v (err %v), want the Google candidate cached for review", entry, err)
	}
}

// An Audible match means neither fallback provider is asked.
func TestCandidateFallback_NoFallbackWhenAudibleMatched(t *testing.T) {
	f := newFallbackFixture(t, 800)
	f.audible.answer = answersTitle("The Hobbit", "J.R.R. Tolkien")
	f.openlib.answer = answersTitle("The Hobbit", "J.R.R. Tolkien")
	f.google.answer = answersTitle("The Hobbit", "J.R.R. Tolkien")
	b := f.book(t, "The Hobbit")

	r := f.run(t, b.ID)[b.ID]
	if r.Status != "matched" || r.Candidate == nil || r.Candidate.Source != "Audible" {
		t.Fatalf("result = {status %q, candidate %+v}, want matched from Audible", r.Status, r.Candidate)
	}
	if ol, gb := f.openlib.calls.Load(), f.google.calls.Load(); ol != 0 || gb != 0 {
		t.Fatalf("fallback calls: Open Library %d, Google Books %d; want 0 and 0 after an Audible match", ol, gb)
	}
	if len(r.Fallback) != 0 {
		t.Fatalf("fallback steps = %v, want none", stepOutcomes(r.Fallback))
	}
	if got := f.budget.Remaining(); got != 800 {
		t.Fatalf("budget remaining = %d, want 800 (untouched)", got)
	}
}

// An Open Library match stops the chain before Google Books.
func TestCandidateFallback_OpenLibraryMatchSkipsGoogle(t *testing.T) {
	f := newFallbackFixture(t, 800)
	f.openlib.answer = answersTitle("Small Press Novel", "B. Author")
	f.google.answer = answersTitle("Small Press Novel", "B. Author")
	b := f.book(t, "Small Press Novel")

	r := f.run(t, b.ID)[b.ID]
	if r.Status != "matched" || r.Candidate == nil || r.Candidate.Source != "Open Library" {
		t.Fatalf("result = {status %q, candidate %+v}, want matched from Open Library", r.Status, r.Candidate)
	}
	if got := f.google.calls.Load(); got != 0 {
		t.Fatalf("Google Books calls = %d, want 0", got)
	}
	if got, want := stepOutcomes(r.Fallback), []string{"openlibrary=matched"}; !slices.Equal(got, want) {
		t.Fatalf("fallback steps = %v, want %v", got, want)
	}
}

// A spent daily budget DEFERS the book: it is not a no_match, its cache row
// records no Google answer, and on the next quota day only Google is asked --
// Audible and Open Library already said no for the same search identity.
func TestCandidateFallback_BudgetExhaustionDefersAndNextDayAsksOnlyGoogle(t *testing.T) {
	f := newFallbackFixture(t, 1)
	a := f.book(t, "Uncatalogued Memoir One")
	b := f.book(t, "Uncatalogued Memoir Two")

	res := f.run(t, a.ID, b.ID)
	if got := f.google.calls.Load(); got != 1 {
		t.Fatalf("Google Books calls = %d, want exactly 1 (budget 1)", got)
	}
	var deferredID, askedID string
	for id, r := range res {
		switch r.Status {
		case candidateStatusDeferred:
			deferredID = id
		case "no_match":
			askedID = id
		default:
			t.Fatalf("book %s status = %q (%s), want one deferred and one no_match", id, r.Status, r.Error)
		}
	}
	if deferredID == "" || askedID == "" {
		t.Fatalf("results = %+v, want one deferred and one no_match", res)
	}
	if got, want := stepOutcomes(res[deferredID].Fallback), []string{"openlibrary=no_match", "google-books=deferred"}; !slices.Equal(got, want) {
		t.Fatalf("deferred book's steps = %v, want %v", got, want)
	}
	entry, err := f.store.GetMetadataCache(deferredID)
	if err != nil || entry == nil {
		t.Fatalf("deferred book's cache row: %v %v", entry, err)
	}
	if _, ok := entry.EmptyAnswers["Google Books"]; ok {
		t.Fatal("deferred book's cache row records a Google Books answer; Google was never asked")
	}
	for _, name := range []string{"Audible", "Open Library"} {
		if _, ok := entry.EmptyAnswers[name]; !ok {
			t.Fatalf("deferred book's cache row lacks %s's empty answer (tried-provider memory): %v", name, entry.EmptyAnswers)
		}
	}

	// Same day: still deferred, and nobody is asked again.
	f.resetCalls()
	again := f.run(t, deferredID)[deferredID]
	if again.Status != candidateStatusDeferred {
		t.Fatalf("same-day rerun status = %q, want deferred", again.Status)
	}
	if n := f.audible.calls.Load() + f.openlib.calls.Load() + f.google.calls.Load(); n != 0 {
		t.Fatalf("same-day rerun made %d provider calls, want 0", n)
	}

	// Next quota day: only Google is asked.
	f.clock = f.clock.Add(24 * time.Hour)
	f.resetCalls()
	next := f.run(t, deferredID, askedID)
	if got := f.google.calls.Load(); got != 1 {
		t.Fatalf("next-day Google Books calls = %d, want 1", got)
	}
	if n := f.audible.calls.Load() + f.openlib.calls.Load(); n != 0 {
		t.Fatalf("next day re-asked Audible/Open Library %d times; they already answered this identity", n)
	}
	if r := next[deferredID]; r.Status != "no_match" {
		t.Fatalf("next-day status = %q, want no_match", r.Status)
	}
	if r := next[askedID]; r.Status != "no_match" || r.Cached != candidateCachedKnownEmpty {
		t.Fatalf("fully-answered book = {%q, cached %q}, want known-empty no_match", r.Status, r.Cached)
	}
}

// A throttle hold on Google Books defers without spending budget or calling.
func TestCandidateFallback_GoogleThrottleHoldDefers(t *testing.T) {
	f := newFallbackFixture(t, 800)
	if _, ok := metadata.DefaultThrottleRegistry().RecordFailure(metadata.SourceIDGoogleBooks, &metadata.ProviderStatusError{
		Provider: metadata.SourceIDGoogleBooks, Status: 429,
		Body: "Quota exceeded for quota metric 'Queries' and limit 'Queries per day'",
	}); !ok {
		t.Fatal("could not install a Google Books throttle hold")
	}
	t.Cleanup(metadata.ResetThrottlesForTesting)
	b := f.book(t, "Held Title")

	r := f.run(t, b.ID)[b.ID]
	if r.Status != candidateStatusDeferred {
		t.Fatalf("status = %q (%s), want deferred", r.Status, r.Error)
	}
	if got := f.google.calls.Load(); got != 0 {
		t.Fatalf("Google Books calls = %d, want 0 while held", got)
	}
	if got := f.budget.Remaining(); got != 800 {
		t.Fatalf("budget remaining = %d, want 800 (no reservation while held)", got)
	}
}

// A Google Books failure defers too (it is not an answer), and the
// reservation is not refunded.
func TestCandidateFallback_GoogleFailureDefers(t *testing.T) {
	f := newFallbackFixture(t, 800)
	f.google.err = errors.New("upstream 503")
	b := f.book(t, "Flaky Title")

	r := f.run(t, b.ID)[b.ID]
	if r.Status != candidateStatusDeferred {
		t.Fatalf("status = %q (%s), want deferred", r.Status, r.Error)
	}
	if got := f.budget.Remaining(); got != 799 {
		t.Fatalf("budget remaining = %d, want 799 (a failed lookup still counts)", got)
	}
}

// The fetch-side gates: a book whose metadata is applied, and an
// owner-manual-only (Doctor Who / Big Finish) book, never spend fallback
// quota. A book the owner marked "no match" is skipped before any search.
func TestCandidateFallback_Exclusions(t *testing.T) {
	f := newFallbackFixture(t, 800)
	f.openlib.answer = answersTitle("anything", "x")
	f.google.answer = answersTitle("anything", "x")
	applied, noMatch := "matched", "no_match"
	appliedBook := f.book(t, "Already Applied Book", func(b *database.Book) { b.MetadataReviewStatus = &applied })
	dw := f.book(t, "Doctor Who - The Chimes of Midnight")
	rejected := f.book(t, "Owner Rejected Book", func(b *database.Book) { b.MetadataReviewStatus = &noMatch })

	res := f.run(t, appliedBook.ID, dw.ID, rejected.ID)
	if ol, gb := f.openlib.calls.Load(), f.google.calls.Load(); ol != 0 || gb != 0 {
		t.Fatalf("fallback calls: Open Library %d, Google Books %d; want 0 for gated books", ol, gb)
	}
	for _, id := range []string{appliedBook.ID, dw.ID} {
		r := res[id]
		if r.Status != "no_match" {
			t.Fatalf("book %s status = %q, want no_match (primary found nothing; fallback gated)", id, r.Status)
		}
		for _, st := range r.Fallback {
			if st.Outcome != metabatch.FallbackSkipped {
				t.Fatalf("book %s fallback steps = %v, want every step skipped", id, stepOutcomes(r.Fallback))
			}
		}
		if len(r.Fallback) != 2 {
			t.Fatalf("book %s fallback steps = %v, want both providers recorded as skipped", id, stepOutcomes(r.Fallback))
		}
	}
	if r := res[rejected.ID]; r.Status != "skipped" {
		t.Fatalf("owner-rejected book status = %q, want skipped", r.Status)
	}
	if got := f.budget.Remaining(); got != 800 {
		t.Fatalf("budget remaining = %d, want 800", got)
	}
}

// Many workers racing for a small budget spend exactly the budget.
func TestCandidateFallback_ConcurrentWorkersSpendExactlyTheBudget(t *testing.T) {
	f := newFallbackFixture(t, 3)
	var ids []string
	for i := range 12 {
		ids = append(ids, f.book(t, fmt.Sprintf("Lost Title Number %02d", i)).ID)
	}
	res := f.run(t, ids...)
	if got := f.google.calls.Load(); got != 3 {
		t.Fatalf("Google Books calls = %d, want exactly 3", got)
	}
	deferred := 0
	for _, r := range res {
		if r.Status == candidateStatusDeferred {
			deferred++
		}
	}
	if deferred != 9 {
		t.Fatalf("deferred = %d, want 9", deferred)
	}
}

// A process restart keeps the day's count: the Server's budget, rebuilt from
// the same store as a new process would build it (config default 800/day),
// sees the lookup the previous one spent. (The on-disk close/reopen case is
// metafetch.TestDailyBudget_SurvivesRestart.)
func TestCandidateFallback_RestartKeepsBudgetCount(t *testing.T) {
	f := newFallbackFixture(t, 800)
	b := f.book(t, "Restart Survivor")
	f.run(t, b.ID)
	if got := f.google.calls.Load(); got != 1 {
		t.Fatalf("Google Books calls = %d, want 1", got)
	}

	f.s.candidateFallback = candidateFallbackState{}
	rebuilt := f.s.googleFallbackBudget()
	if rebuilt == f.budget {
		t.Fatal("budget was not rebuilt")
	}
	rebuilt.SetClock(func() time.Time { return f.clock })
	if got := rebuilt.Remaining(); got != metafetch.DefaultGoogleBooksFallbackDailyLimit-1 {
		t.Fatalf("rebuilt budget remaining = %d, want %d (one lookup spent before the restart)",
			got, metafetch.DefaultGoogleBooksFallbackDailyLimit-1)
	}
}

// The scheduled selection picks up a book a fallback provider still owes an
// answer, caps Google-only books at today's remaining budget, and leaves out
// a gated (owner-manual-only) book.
func TestUnfetchedSelection_FallbackPendingBooks(t *testing.T) {
	f := newFallbackFixture(t, 0) // every Google lookup deferred
	owed := f.book(t, "Google Still Owes This")
	dw := f.book(t, "Doctor Who - Spare Parts")
	f.run(t, owed.ID, dw.ID)
	if got := f.google.calls.Load(); got != 0 {
		t.Fatalf("Google calls = %d, want 0 with a zero budget", got)
	}

	sel, err := unfetchedCandidateBookIDs(context.Background(), f.store, f.mfs, f.s.newFolderMemo(f.store), nil, 0)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if slices.Contains(sel.IDs, owed.ID) || sel.FallbackCapped != 1 {
		t.Fatalf("with no budget left: selected %v, capped %d; want the Google-owed book capped, not selected", sel.IDs, sel.FallbackCapped)
	}

	sel, err = unfetchedCandidateBookIDs(context.Background(), f.store, f.mfs, f.s.newFolderMemo(f.store), nil, 5)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if !slices.Contains(sel.IDs, owed.ID) || sel.FallbackPending != 1 {
		t.Fatalf("with budget: selected %v (pending %d); want the Google-owed book", sel.IDs, sel.FallbackPending)
	}
	if slices.Contains(sel.IDs, dw.ID) {
		t.Fatal("an owner-manual-only book was selected for a fallback lookup")
	}
}
