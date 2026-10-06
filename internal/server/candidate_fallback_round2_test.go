// file: internal/server/candidate_fallback_round2_test.go
// version: 1.0.0
// guid: 34fd4490-2dd6-4158-b2d2-a0064a8cabb6
// last-edited: 2026-10-06

package server

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

// Second review round on #3796 and the owner decisions of 2026-10-06. All
// fixtures are synthetic.

// runFetchOp runs metadata.candidate-fetch with p and returns its results.
func runFetchOp(t *testing.T, s *Server, opID string, p metadataCandidateFetchOpParams) map[string]CandidateResult {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.runMetadataCandidateFetchOp(context.Background(), raw, &resumeRecorder{opID: opID}); err != nil {
		t.Fatalf("run %s: %v", opID, err)
	}
	rows, err := s.storeForWiring().GetOperationResults(opID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]CandidateResult{}
	for _, r := range rows {
		var cr CandidateResult
		if err := json.Unmarshal([]byte(r.ResultJSON), &cr); err != nil {
			t.Fatal(err)
		}
		out[r.BookID] = cr
	}
	return out
}

// Owner decision: "Search again" on ONE book is interactive. Only an explicit
// mark on exactly one book id counts -- never a count that happens to be one
// (a large selection's last chunk), a selection or the stale refetch.
func TestSingleBookSearch_RequiresTheMarkOnOneBook(t *testing.T) {
	one := []string{"b1"}
	cases := []struct {
		name string
		req  metabatch.BatchFetchRequest
		ids  []string
		want bool
	}{
		{"marked, one book", metabatch.BatchFetchRequest{BookIDs: one, Interactive: true}, one, true},
		{"unmarked one-book chunk", metabatch.BatchFetchRequest{BookIDs: one}, one, false},
		{"marked, two books", metabatch.BatchFetchRequest{BookIDs: []string{"b1", "b2"}, Interactive: true}, []string{"b1", "b2"}, false},
		{"marked selection", metabatch.BatchFetchRequest{BookIDs: one, Interactive: true, Selection: &operations.SelectionSpec{}}, one, false},
		{"marked stale refetch", metabatch.BatchFetchRequest{BookIDs: one, Interactive: true, Stale: true}, one, false},
		{"marked, the one book already being fetched", metabatch.BatchFetchRequest{BookIDs: one, Interactive: true}, nil, false},
	}
	for _, tc := range cases {
		if got := singleBookSearch(tc.req, tc.ids); got != tc.want {
			t.Errorf("%s: singleBookSearch = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Owner decision: a single-book Search again may use the 200 Google Books
// lookups background work leaves reserved; the same book in a background
// fetch is deferred.
func TestCandidateFallback_SingleBookSearchUsesTheReserve(t *testing.T) {
	f := newFallbackFixture(t, 0) // background share spent; 200 reserved
	f.google.answer = answersTitle("Reserve Example", "R. Writer")
	b := f.book(t, "Reserve Example")

	bg := runFetchOp(t, f.s, "op-bg", metadataCandidateFetchOpParams{BookIDs: []string{b.ID}, Force: true})[b.ID]
	if bg.Status != candidateStatusDeferred || f.google.calls.Load() != 0 {
		t.Fatalf("background fetch: status %q, Google calls %d; want deferred, 0", bg.Status, f.google.calls.Load())
	}

	r := runFetchOp(t, f.s, "op-one", metadataCandidateFetchOpParams{BookIDs: []string{b.ID}, Force: true, Interactive: true})[b.ID]
	if f.google.calls.Load() != 1 || r.Candidate == nil || r.Candidate.Source != "Google Books" {
		t.Fatalf("single-book search: status %q candidate %+v, Google calls %d; want Google asked and matched",
			r.Status, r.Candidate, f.google.calls.Load())
	}
	if got := f.budget.Used(); got != 1 {
		t.Fatalf("shared counter = %d, want 1", got)
	}
}

// S4: a book Open Library and Google Books both still owe is selected for the
// free Open Library step even when the day's Google share cannot cover it;
// its Google step is put off unasked and unrecorded.
func TestUnfetchedSelection_CappedBookStillAskedOfOpenLibrary(t *testing.T) {
	f := newFallbackFixture(t, 0)
	f.openlib.err = &metadata.ProviderStatusError{Provider: metadata.SourceIDOpenLibrary, Status: 503, Body: "down"}
	b := f.book(t, "Owed Twice Example")
	f.run(t, b.ID) // Open Library fails (still owed), Google deferred (still owed)

	sel, err := unfetchedCandidateBookIDs(context.Background(), f.store, f.mfs, f.s.newFolderMemo(f.store), nil, 0)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if !slices.Equal(sel.IDs, []string{b.ID}) || !slices.Equal(sel.GoogleCapped, []string{b.ID}) || sel.FallbackCapped != 1 {
		t.Fatalf("selected %v, google-capped %v, capped %d; want the book selected for Open Library only",
			sel.IDs, sel.GoogleCapped, sel.FallbackCapped)
	}

	// Google now has budget, but the selection allotted it elsewhere.
	f.limit = 800
	f.openlib.err = nil
	f.resetCalls()
	before, _ := f.store.GetMetadataCache(b.ID)
	googleAt := before.FallbackAttempts["Google Books"].At
	r := runFetchOp(t, f.s, "op-capped", metadataCandidateFetchOpParams{BookIDs: sel.IDs, GoogleCappedBookIDs: sel.GoogleCapped})[b.ID]
	if f.openlib.calls.Load() == 0 || f.google.calls.Load() != 0 {
		t.Fatalf("Open Library calls %d, Google calls %d; want Open Library asked, Google not", f.openlib.calls.Load(), f.google.calls.Load())
	}
	if got, want := stepOutcomes(r.Fallback), []string{"openlibrary=no_match", "google-books=deferred"}; !slices.Equal(got, want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	after, _ := f.store.GetMetadataCache(b.ID)
	if !after.FallbackAttempts["Google Books"].At.Equal(googleAt) {
		t.Fatal("the capped Google step was recorded as an attempt: the book would lose its place in the next day's order")
	}
}

// S5: a usable candidate landing after a deferred fallback (here a forced
// chain refetch) clears the deferral, so the review page stops listing the
// book as waiting on a lookup the selection will never make.
func TestCandidateFallback_UsableChainAnswerClearsDeferral(t *testing.T) {
	f := newFallbackFixture(t, 0)
	b := f.book(t, "Late Answer Example")
	f.run(t, b.ID)
	entry, _ := f.store.GetMetadataCache(b.ID)
	if entry == nil || !entry.FallbackDeferred() {
		t.Fatalf("setup: row %+v, want a deferred Google attempt", entry)
	}

	f.audible.answer = answersTitle("Late Answer Example", "L. Writer")
	runFetchOp(t, f.s, "op-late", metadataCandidateFetchOpParams{BookIDs: []string{b.ID}, Force: true})
	entry, _ = f.store.GetMetadataCache(b.ID)
	if entry == nil || len(entry.Candidates) == 0 {
		t.Fatalf("refetch: row %+v, want the chain's candidate", entry)
	}
	if entry.FallbackDeferred() {
		t.Fatalf("the book has a usable candidate but its row still reads deferred: %+v", entry.FallbackAttempts)
	}
}

// Owner decision: Open Library / Google Books candidates are review-only. The
// transcription auto-apply, which applies without the bulk gate, neither
// offers nor writes one; the same candidate from another source applies.
func TestTranscriptionApply_ReviewOnlySourceWritesNothing(t *testing.T) {
	for _, source := range []string{"Open Library", "Google Books"} {
		bookID := "book-review-only"
		book := &database.Book{ID: bookID, Title: ""}
		cand := metafetch.MetadataCandidate{Title: "Synthetic Review Book", Author: "Synthetic Author", Score: 0.95, Source: source}
		entry := mustCandidateCache(t, bookID, cand)
		store, updateCalls := newTOCTOUCacheStore(t, book, entry, entry)
		withCreatableAuthors(store)
		s := &Server{store: store, metadataFetchService: metafetch.NewService(store)}

		if _, ok, err := s.SearchTranscriptionCandidate(context.Background(), bookID, "", ""); err != nil || ok {
			t.Fatalf("%s: SearchTranscriptionCandidate offered a review-only candidate (ok=%v err=%v)", source, ok, err)
		}
		err := s.ApplyTranscriptionCandidate(context.Background(), bookID, cand.Title, cand.Author)
		if !errors.Is(err, errTranscriptionReviewOnly) || len(*updateCalls) != 0 {
			t.Fatalf("%s: ApplyTranscriptionCandidate() = %v with %d writes, want errTranscriptionReviewOnly and none",
				source, err, len(*updateCalls))
		}
	}
	cand := metafetch.MetadataCandidate{Title: "Synthetic Review Book", Author: "Synthetic Author", Score: 0.95, Source: "Audible"}
	entry := mustCandidateCache(t, "book-audible", cand)
	store, updateCalls := newTOCTOUCacheStore(t, &database.Book{ID: "book-audible"}, entry, entry)
	withCreatableAuthors(store)
	s := &Server{store: store, metadataFetchService: metafetch.NewService(store)}
	if err := s.ApplyTranscriptionCandidate(context.Background(), "book-audible", cand.Title, cand.Author); err != nil || len(*updateCalls) == 0 {
		t.Fatalf("fixture: an Audible candidate did not apply (err %v, writes %d)", err, len(*updateCalls))
	}
}

// B1 through the batch path: the batch verdict serves a row written under the
// raw author credit (before 2026-10-06's cleaning) as fresh, and the fallback
// that runs for it hashes the cleaned credit. Its answer must merge into that
// row (SearchOptions.MergeFromSourceHash), never replace the chain's
// candidates.
func TestCandidateFallback_MergesIntoRowVouchedUnderRawCredit(t *testing.T) {
	f := newFallbackFixture(t, 800)
	const rawCredit, title = "zzSynthetic Writer", "Vouched Row Example"
	a, err := f.store.CreateAuthor(rawCredit)
	if err != nil {
		t.Fatal(err)
	}
	b := f.book(t, title, func(bk *database.Book) { bk.AuthorID = &a.ID })
	if metafetch.SearchAuthorHint(rawCredit) == rawCredit {
		t.Fatal("fixture: the credit must clean to another hint")
	}
	f.audible.answer = answersRecord(metadata.BookMetadata{Title: title + " Collected", Author: "Synthetic Writer",
		CoverURL: "https://example.invalid/a.jpg"})
	if _, _, err := f.mfs.FetchAndCacheWithResponse(context.Background(), nil, b.ID, title, rawCredit, "", "",
		metafetch.SearchOptions{OnlySources: []string{"Audible"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	book, err := f.store.GetBookByID(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, verdict, _ := f.mfs.CachedBatchVerdict(book, title, metafetch.SearchAuthorHint(rawCredit)); verdict != metafetch.BatchVerdictFreshCandidates {
		t.Fatalf("fixture: verdict %v, want the raw-credit row served as fresh", verdict)
	}

	// The owner rejected the chain's candidate: no usable one, so the
	// fallback runs -- and the rejected candidate stays on the row.
	rejectCandidate(t, f.store, b.ID, "Audible", title+" Collected")
	f.openlib.answer = answersTitle(title, "Synthetic Writer")
	f.resetCalls()
	r := f.run(t, b.ID)[b.ID]
	if f.openlib.calls.Load() == 0 {
		t.Fatalf("Open Library not asked (steps %v, status %q, cached %q, cand %+v, err %q)", stepOutcomes(r.Fallback), r.Status, r.Cached, r.Candidate, r.Error)
	}
	entry, err := f.store.GetMetadataCache(b.ID)
	if err != nil || entry == nil {
		t.Fatalf("row: %v %v", entry, err)
	}
	if srcs := candidateSources(t, entry); !slices.Contains(srcs, "Audible") || !slices.Contains(srcs, "Open Library") {
		t.Fatalf("row candidates from %v; want Audible's kept and Open Library's merged in", srcs)
	}
}
