// file: internal/server/candidate_fallback_followups_test.go
// version: 1.1.0
// guid: 094d2b26-ad58-4773-969d-15e89565cb5f
// last-edited: 2026-10-06

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metadata/dailyquota"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// Follow-ups to #3788 (owner decisions and review items, 2026-10-06).

func candidateSources(t *testing.T, entry *database.MetadataCandidateCache) []string {
	t.Helper()
	var out []string
	for _, raw := range entry.Candidates {
		var c metafetch.MetadataCandidate
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatalf("decode candidate: %v", err)
		}
		out = append(out, c.Source)
	}
	return out
}

func rejectCandidate(t *testing.T, store database.Store, bookID, source, title string) {
	t.Helper()
	if err := store.SetRaw("rejected_candidate:"+bookID+":"+source+"|"+title, []byte("1")); err != nil {
		t.Fatalf("reject: %v", err)
	}
}

// A (rejected): every Audible candidate was rejected by the owner, so the
// book has no usable candidate and Open Library is asked.
func TestCandidateFallback_TriggerAllCandidatesRejected(t *testing.T) {
	f := newFallbackFixture(t, 800)
	f.audible.answer = answersTitle("Twice Told Tale", "C. Writer")
	f.openlib.answer = answersTitle("Twice Told Tale", "C. Writer")
	b := f.book(t, "Twice Told Tale")
	rejectCandidate(t, f.store, b.ID, "Audible", "Twice Told Tale")

	r := f.run(t, b.ID)[b.ID]
	if got := f.openlib.calls.Load(); got == 0 {
		t.Fatalf("Open Library not asked for a book whose only candidate the owner rejected (status %q)", r.Status)
	}
	if r.Candidate == nil || r.Candidate.Source != "Open Library" {
		t.Fatalf("result candidate = %+v, want Open Library's", r.Candidate)
	}
	if got, want := stepOutcomes(r.Fallback), []string{"openlibrary=matched"}; !slices.Equal(got, want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
}

// A (below floor): Audible's only candidate scores under the apply floor, so
// the fallback is asked -- and its candidates are MERGED into the row, not
// put in place of Audible's.
func TestCandidateFallback_TriggerBelowFloorMergesCandidates(t *testing.T) {
	f := newFallbackFixture(t, 800)
	f.audible.answer = answersRecord(metadata.BookMetadata{Title: "Harbor Lights Collected", Author: "D. Poet",
		CoverURL: "https://example.invalid/a.jpg"})
	f.openlib.answer = answersTitle("Harbor Lights", "D. Poet")
	b := f.book(t, "Harbor Lights")

	r := f.run(t, b.ID)[b.ID]
	if got := f.openlib.calls.Load(); got == 0 {
		t.Fatalf("Open Library not asked for a below-floor Audible candidate (steps %v)", stepOutcomes(r.Fallback))
	}
	entry, err := f.store.GetMetadataCache(b.ID)
	if err != nil || entry == nil {
		t.Fatalf("cache row: %v %v", entry, err)
	}
	srcs := candidateSources(t, entry)
	if !slices.Contains(srcs, "Audible") || !slices.Contains(srcs, "Open Library") {
		t.Fatalf("row candidates from %v, want Audible's kept and Open Library's merged in", srcs)
	}
	if r.Candidate == nil || r.Candidate.Source != "Open Library" {
		t.Fatalf("result candidate = %+v, want the higher-ranked Open Library one", r.Candidate)
	}
}

// metabatch.NoUsableCandidate, leg by leg: none, rejected, asin_conflict, ASIN replaced
// (identity_stale), below floor -- and a usable candidate.
func TestNoUsableCandidate_Legs(t *testing.T) {
	f := newFallbackFixture(t, 800)
	asin := "B00000BOOK"
	b := f.book(t, "Leg Test", func(b *database.Book) { b.ASIN = &asin })
	row := func(cands ...metafetch.MetadataCandidate) *database.MetadataCandidateCache {
		e := &database.MetadataCandidateCache{BookID: b.ID, FetchedForASIN: asin}
		for _, c := range cands {
			raw, _ := json.Marshal(c)
			e.Candidates = append(e.Candidates, raw)
		}
		return e
	}
	good := metafetch.MetadataCandidate{Title: "Leg Test", Source: "Audible", Score: 0.95}
	cases := []struct {
		name   string
		entry  *database.MetadataCandidateCache
		setup  func()
		usable bool
	}{
		{"none", row(), nil, false},
		{"usable", row(good), nil, true},
		{"below floor", row(metafetch.MetadataCandidate{Title: "Leg Test", Source: "Audible", Score: 0.5}), nil, false},
		{"asin conflict", row(metafetch.MetadataCandidate{Title: "Leg Test", Source: "Audible", Score: 0.99, ASIN: "B0OTHERBK1"}), nil, false},
		{"asin replaced", func() *database.MetadataCandidateCache {
			e := row(good)
			e.FetchedForASIN = "B0EARLIER1"
			return e
		}(), nil, false},
		{"rejected", row(metafetch.MetadataCandidate{Title: "Leg Test", Source: "Open Library", Score: 0.95}),
			func() { rejectCandidate(t, f.store, b.ID, "Open Library", "Leg Test") }, false},
	}
	for _, tc := range cases {
		if tc.setup != nil {
			tc.setup()
		}
		v := metabatch.NoUsableCandidate(f.store, b, tc.entry)
		if v.Usable != tc.usable {
			t.Errorf("%s: usable = %v (%s), want %v", tc.name, v.Usable, v.Why, tc.usable)
		}
	}
}

// A (stuck rows): a row holding only unusable candidates, written before the
// fallback existed (no fallback attempts, chain = Audible only), used to be
// served from the cache forever and never selected. It is now selected, and
// the fetch asks Open Library.
func TestCandidateFallback_StuckRowBecomesSelectable(t *testing.T) {
	f := newFallbackFixture(t, 800)
	f.audible.answer = answersRecord(metadata.BookMetadata{Title: "Quiet Valley Omnibus Edition", Author: "E. Author",
		CoverURL: "https://example.invalid/a.jpg"})
	f.openlib.answer = answersTitle("Quiet Valley", "E. Author")
	b := f.book(t, "Quiet Valley")
	// The pre-fallback world: the chain was Audible alone.
	f.mfs.SetOverrideSources([]metadata.MetadataSource{f.audible})
	if r := f.run(t, b.ID)[b.ID]; r.Status != "matched" || len(r.Fallback) != 0 {
		t.Fatalf("setup fetch = %q steps %v, want an Audible-only matched row", r.Status, stepOutcomes(r.Fallback))
	}
	f.mfs.SetOverrideSources([]metadata.MetadataSource{f.audible, f.openlib, f.google})

	sel, err := unfetchedCandidateBookIDs(context.Background(), f.store, f.mfs, f.s.newFolderMemo(f.store), nil, 800)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if !slices.Contains(sel.IDs, b.ID) || sel.FallbackUnusable != 1 {
		t.Fatalf("selection = %v (unusable %d), want the stuck book selected", sel.IDs, sel.FallbackUnusable)
	}
	f.resetCalls()
	r := f.run(t, b.ID)[b.ID]
	if f.openlib.calls.Load() == 0 {
		t.Fatalf("fetch of the selected stuck book did not ask Open Library (status %q)", r.Status)
	}
	if f.audible.calls.Load() != 0 {
		t.Fatal("fetch re-asked Audible for fresh cached candidates")
	}
	// Answered: it is not selected again.
	sel, err = unfetchedCandidateBookIDs(context.Background(), f.store, f.mfs, f.s.newFolderMemo(f.store), nil, 800)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if slices.Contains(sel.IDs, b.ID) {
		t.Fatal("book selected again after the fallback answered it")
	}
}

// S2: a title-searching source of the chain FAILED (Audible 503) while
// another answered empty -- the chain's question went unanswered, so no
// fallback quota is spent and the result is an error, not a no_match.
func TestCandidateFallback_TitleSourceFailureRefusesFallback(t *testing.T) {
	f := newFallbackFixture(t, 800)
	hardcover := &idSource{id: "hardcover", name: "Hardcover", log: f.log}
	f.mfs.SetOverrideSources([]metadata.MetadataSource{f.audible, hardcover, f.openlib, f.google})
	f.audible.err = &metadata.ProviderStatusError{Provider: metadata.SourceIDAudible, Status: 503, Body: "down"}
	f.openlib.answer = answersTitle("Unlucky Day", "F. Author")
	b := f.book(t, "Unlucky Day")

	r := f.run(t, b.ID)[b.ID]
	if ol, gb := f.openlib.calls.Load(), f.google.calls.Load(); ol != 0 || gb != 0 {
		t.Fatalf("fallback asked (Open Library %d, Google %d) after Audible failed", ol, gb)
	}
	if r.Status != "error" {
		t.Fatalf("status = %q (%s), want error", r.Status, r.Error)
	}
}

// S2: only a TITLE-searching source's failure refuses the fallback. The
// ASIN-only Audnexus failing says nothing about the title.
func TestFailedTitleSource_ExcludesASINOnlySources(t *testing.T) {
	// The chain holds Audible only: the lookup files Audnexus's failure under
	// its fixed label, which must still read as ASIN-only.
	idByName := sourceIDsByName(map[string]string{metadata.SourceIDAudible: "Audible"})
	resp := &metafetch.SearchMetadataResponse{SourcesFailed: map[string]string{"Audnexus (Audible)": "ASIN lookup: 503"}}
	if got := failedTitleSource(resp, idByName); got != "" {
		t.Fatalf("Audnexus failure reported as %q, want none", got)
	}
	resp.SourcesFailed["Audible"] = "503"
	if got := failedTitleSource(resp, idByName); got == "" {
		t.Fatal("Audible failure not reported")
	}
}

// S3: Open Library failing (a 503) does not stop Google Books.
func TestCandidateFallback_OpenLibraryFailureMovesOnToGoogle(t *testing.T) {
	f := newFallbackFixture(t, 800)
	f.openlib.err = &metadata.ProviderStatusError{Provider: metadata.SourceIDOpenLibrary, Status: 503, Body: "down"}
	f.google.answer = answersTitle("Rare Pamphlet", "G. Author")
	b := f.book(t, "Rare Pamphlet")

	r := f.run(t, b.ID)[b.ID]
	if r.Candidate == nil || r.Candidate.Source != "Google Books" {
		t.Fatalf("result = {%q, %+v}, want matched from Google Books", r.Status, r.Candidate)
	}
	if got, want := stepOutcomes(r.Fallback), []string{"openlibrary=error", "google-books=matched"}; !slices.Equal(got, want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
}

// S4: a permanent Google refusal (a 400) is an error and is remembered: the
// book is not re-selected for Google every run to burn the budget.
func TestCandidateFallback_GooglePermanentErrorIsSettled(t *testing.T) {
	f := newFallbackFixture(t, 800)
	f.google.err = &metadata.ProviderStatusError{Provider: metadata.SourceIDGoogleBooks, Status: 400, Body: `{"error":"bad request"}`}
	b := f.book(t, "Malformed Query Book")

	r := f.run(t, b.ID)[b.ID]
	if r.Status != "error" {
		t.Fatalf("status = %q (%s), want error (not deferred) for a 400", r.Status, r.Error)
	}
	entry, _ := f.store.GetMetadataCache(b.ID)
	if a, ok := entry.FallbackAttempts["Google Books"]; !ok || !a.Settled {
		t.Fatalf("Google attempt = %+v (present %v), want a settled attempt", a, ok)
	}
	sel, err := unfetchedCandidateBookIDs(context.Background(), f.store, f.mfs, f.s.newFolderMemo(f.store), nil, 800)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if slices.Contains(sel.IDs, b.ID) {
		t.Fatal("a book Google refused for good was selected again")
	}
}

// S4: the capped selection takes the Google-owed book attempted LEAST
// recently, not the lowest id -- a book whose lookup keeps being deferred
// does not hold the day's place.
func TestUnfetchedSelection_GoogleCapOrdersByOldestAttempt(t *testing.T) {
	f := newFallbackFixture(t, 0) // every Google lookup deferred
	a := f.book(t, "Deferred Book Alpha")
	b := f.book(t, "Deferred Book Beta")
	f.run(t, a.ID, b.ID)
	// The higher id is the one attempted longest ago.
	older, newer := a.ID, b.ID
	if older < newer {
		older, newer = newer, older
	}
	for id, at := range map[string]time.Time{older: time.Now().Add(-48 * time.Hour), newer: time.Now().Add(-time.Hour)} {
		e, err := f.store.GetMetadataCache(id)
		if err != nil || e == nil {
			t.Fatalf("row %s: %v", id, err)
		}
		att := e.FallbackAttempts["Google Books"]
		if att.Settled || att.Outcome != metabatch.FallbackDeferred {
			t.Fatalf("book %s Google attempt = %+v, want an unsettled deferral", id, att)
		}
		att.At = at
		e.FallbackAttempts["Google Books"] = att
		if err := f.store.PutMetadataCache(e); err != nil {
			t.Fatal(err)
		}
	}
	sel, err := unfetchedCandidateBookIDs(context.Background(), f.store, f.mfs, f.s.newFolderMemo(f.store), nil, 1)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if !slices.Equal(sel.IDs, []string{older}) || sel.FallbackCapped != 1 {
		t.Fatalf("selected %v (capped %d), want only the least recently attempted %s", sel.IDs, sel.FallbackCapped, older)
	}
}

// S6: the owner-manual check failing to read defers Google Books (never
// skips it, never records no_match); Open Library is still asked.
type manualReadFailStore struct{ database.Store }

func (manualReadFailStore) GetBookFiles(string) ([]database.BookFile, error) {
	return nil, errBookFilesUnreadable
}

var errBookFilesUnreadable = &metadata.ProviderStatusError{Provider: "test", Status: 500, Body: "book files unreadable"}

func TestCandidateFallback_ManualCheckReadErrorDefersGoogleOnly(t *testing.T) {
	f := newFallbackFixture(t, 800)
	f.google.answer = answersTitle("Unreadable Files Book", "H. Author")
	b := f.book(t, "Unreadable Files Book")
	in := candidateFallbackInput{
		store: manualReadFailStore{f.store}, book: b, bookInfo: CandidateBookInfo{ID: b.ID, Title: b.Title},
		query: b.Title, pending: []fallbackProvider{{id: metadata.SourceIDOpenLibrary, name: "Open Library"},
			{id: metadata.SourceIDGoogleBooks, name: "Google Books"}},
		withQuery: func(r CandidateResult) CandidateResult { return r },
	}
	r := f.s.runCandidateFallback(context.Background(), f.mfs, in)
	if r.Status != candidateStatusDeferred {
		t.Fatalf("status = %q (%s), want deferred", r.Status, r.Error)
	}
	if got, want := stepOutcomes(r.Fallback), []string{"openlibrary=no_match", "google-books=deferred"}; !slices.Equal(got, want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	if f.google.calls.Load() != 0 {
		t.Fatal("Google asked although the owner-manual check could not be read")
	}
}

// B: the fallback and the interactive search dialog draw on ONE counter:
// background stops at its share, the dialog still gets the reserved rest.
func TestCandidateFallback_SharesBudgetWithInteractiveSearch(t *testing.T) {
	f := newFallbackFixture(t, 1) // background share 1, total 201
	a := f.book(t, "Quota Sharing One")
	b := f.book(t, "Quota Sharing Two")
	res := f.run(t, a.ID, b.ID)
	if got := f.google.calls.Load(); got != 1 {
		t.Fatalf("background Google calls = %d, want 1 (results %+v)", got, res)
	}
	// The interactive search dialog (BypassProviderThrottle) asks Google
	// with the background share spent: it is let through, on the same count.
	f.google.answer = answersTitle("Quota Sharing Two", "x")
	if _, err := f.mfs.SearchMetadataForBookWithOptions(b.ID, "", "", "", "",
		metafetch.SearchOptions{BypassProviderThrottle: true, BypassFetchCache: true}); err != nil {
		t.Fatalf("interactive search: %v", err)
	}
	if got := f.google.calls.Load(); got != 2 {
		t.Fatalf("Google calls after the interactive search = %d, want 2", got)
	}
	if got := f.budget.Used(); got != 2 {
		t.Fatalf("shared counter = %d, want 2", got)
	}
	// A background search outside the fallback (no interactive mark) is held
	// at the share too.
	if _, err := f.mfs.SearchMetadataForBookWithOptions(a.ID, "", "", "", "",
		metafetch.SearchOptions{BypassFetchCache: true}); err != nil {
		t.Fatalf("background search: %v", err)
	}
	if got := f.google.calls.Load(); got != 2 {
		t.Fatalf("an unmarked search reached Google past the background share (calls %d)", got)
	}
	if got := f.budget.Remaining(dailyquota.Interactive); got != 199 {
		t.Fatalf("interactive remaining = %d, want 199", got)
	}
}

// S7: the result endpoints count deferred and skipped books.
func TestOperationResults_CountDeferredAndSkipped(t *testing.T) {
	f := newFallbackFixture(t, 0)
	noMatch := "no_match"
	deferredBook := f.book(t, "Counted Deferred Book")
	skipped := f.book(t, "Counted Skipped Book", func(b *database.Book) { b.MetadataReviewStatus = &noMatch })
	opID := "op-counters"
	if _, err := f.store.CreateOperation(opID, "metadata_candidate_fetch", nil); err != nil {
		t.Fatalf("create op: %v", err)
	}
	runCandidateFetch(t, f.s, opID, []string{deferredBook.ID, skipped.ID}, false)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: opID}}
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/operations/"+opID+"/results", nil)
	f.s.handleGetOperationResults(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data struct {
			Deferred      int `json:"deferred"`
			Skipped       int `json:"skipped"`
			TotalDeferred int `json:"total_deferred"`
			TotalSkipped  int `json:"total_skipped"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	body := env.Data
	if body.TotalDeferred != 1 || body.TotalSkipped != 1 || body.Deferred != 1 || body.Skipped != 1 {
		t.Fatalf("counters = %+v, want 1 deferred and 1 skipped", body)
	}
}

// A forced (or stale) refetch asks the chain again and REPLACES the row.
// The candidates a fallback provider found for the same questions and ASIN
// survive it: the settled attempt says the provider answered, so it would
// not be asked again, and dropping its candidates would leave the book
// without a usable one until the attempt aged out.
func TestCandidateFallback_ForcedRefetchKeepsFallbackCandidates(t *testing.T) {
	f := newFallbackFixture(t, 800)
	f.audible.answer = answersRecord(metadata.BookMetadata{Title: "Lantern Street Omnibus Edition", Author: "J. Author",
		CoverURL: "https://example.invalid/a.jpg"})
	f.openlib.answer = answersTitle("Lantern Street", "J. Author")
	b := f.book(t, "Lantern Street")
	if r := f.run(t, b.ID)[b.ID]; r.Candidate == nil || r.Candidate.Source != "Open Library" {
		t.Fatalf("setup: result %+v, want an Open Library match", r.Candidate)
	}
	f.resetCalls()
	f.opCounter++
	r := runCandidateFetch(t, f.s, "op-forced-keep", []string{b.ID}, true)[b.ID]
	entry, err := f.store.GetMetadataCache(b.ID)
	if err != nil || entry == nil {
		t.Fatalf("row: %v %v", entry, err)
	}
	if srcs := candidateSources(t, entry); !slices.Contains(srcs, "Open Library") {
		t.Fatalf("after a forced refetch the row holds candidates from %v; Open Library's was dropped (Open Library asked %d times)",
			srcs, f.openlib.calls.Load())
	}
	if r.Candidate == nil || r.Candidate.Source != "Open Library" {
		t.Fatalf("forced refetch result = %+v, want the kept Open Library candidate", r.Candidate)
	}
}
