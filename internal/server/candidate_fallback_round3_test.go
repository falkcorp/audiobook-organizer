// file: internal/server/candidate_fallback_round3_test.go
// version: 1.0.3
// guid: 7f057c74-2af0-44a5-bf9a-214d739ab5f0
// last-edited: 2026-10-06

package server

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// Third review pass on #3796 (its second review round). All fixtures are
// synthetic.

// The hashless review_bulk marker is sent for a selected book whose row the
// lane never loaded or held no hash for, so nobody saw its candidate. It must
// not apply a review-only (Open Library / Google Books) candidate: those are
// applied only by an owner who saw them. A hash-bearing pin that matches the
// shown candidate still does.
func TestUnseenOwnerMarker_DoesNotLiftReviewOnlySource(t *testing.T) {
	tenHours := 36000
	books := func() fakeBooks {
		return fakeBooks{"b1": {ID: "b1", Title: "Moon Book", FilePath: "/lib/Zed Quill/Moon Book/Moon Book.m4b", Duration: &tenHours}}
	}
	for _, source := range []string{"Google Books", "Open Library"} {
		cand := metafetch.MetadataCandidate{Title: "Moon Book", Author: "Zed Quill", Source: source, Score: 0.99, DurationSec: 36000}

		svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
		out := applyCachedCandidateForBookTimed(svc, books(), nil, "b1", false, nil, metafetch.NewApplyPhaseTimings(), nil, ownerMarker(), "")
		if out.Applied || out.OwnerReviewed || out.Reason != applySkipGateBlocked ||
			out.Gate == nil || out.Gate.Reason != applygate.ReasonReviewOnlySource {
			t.Fatalf("%s via hashless marker: outcome %+v (gate %+v), want gate_blocked / review_only_source", source, out, out.Gate)
		}
		if len(svc.applyOpts) != 0 {
			t.Fatalf("%s via hashless marker: applied %+v", source, svc.applyOpts)
		}

		for name, pin := range map[string]*metafetch.CandidatePin{"bulk pin": bulkPin(cand), "row pin": rowPin(cand)} {
			svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
			out := applyCachedCandidateForBookTimed(svc, books(), nil, "b1", false, nil, metafetch.NewApplyPhaseTimings(), nil, pin, "")
			if !out.Applied || !out.OwnerReviewed {
				t.Fatalf("%s via %s matching the shown candidate: outcome %+v, want applied as owner-reviewed", source, name, out)
			}
		}
	}
}

// The marker still lifts the certainty legs on a chain (Audible) candidate,
// as the owner ruled for every review-page bulk button.
func TestUnseenOwnerMarker_StillLiftsCertaintyOnChainCandidate(t *testing.T) {
	books, cand := refusedByAuthorAndTranscription()
	svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
	out := applyCachedCandidateForBookTimed(svc, books, nil, "b1", false, nil, metafetch.NewApplyPhaseTimings(), nil, ownerMarker(), "")
	if !out.Applied || !out.OwnerReviewed {
		t.Fatalf("outcome %+v, want the marker to lift the certainty legs on an Audible candidate", out)
	}
}

// seedNoAuthorRow reproduces the round-2 reviewer's probe: a book with an
// author whose candidate row was written before 2026-09-28 with no author in
// its hash (the batch fetch then hashed the Book.Author snapshot, empty), its
// one candidate rejected by the owner, the row past its fresh TTL, and the
// per-provider fetch cache cleared so the refetch really asks.
func seedNoAuthorRow(t *testing.T, f *fallbackFixture, title string) *database.Book {
	t.Helper()
	a, err := f.store.CreateAuthor("Synthetic Writer")
	if err != nil {
		t.Fatal(err)
	}
	b := f.book(t, title, func(bk *database.Book) { bk.AuthorID = &a.ID })
	f.audible.answer = answersRecord(metadata.BookMetadata{Title: title + " Collected", Author: "Synthetic Writer",
		CoverURL: "https://example.invalid/a.jpg"})
	if _, _, err := f.mfs.FetchAndCacheWithResponse(context.Background(), nil, b.ID, title, "", "", "",
		metafetch.SearchOptions{OnlySources: []string{"Audible"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	row, err := f.store.GetMetadataCache(b.ID)
	if err != nil || row == nil || len(row.Candidates) != 1 {
		t.Fatalf("seed row: %+v %v", row, err)
	}
	row.FetchedAt = time.Now().UTC().AddDate(0, 0, -200)
	if err := f.store.PutMetadataCache(row); err != nil {
		t.Fatal(err)
	}
	rejectCandidate(t, f.store, b.ID, "Audible", title+" Collected")
	if err := database.InvalidateAllCachedMetadataFetchesForBook(f.store, b.ID); err != nil {
		t.Fatal(err)
	}
	book, err := f.store.GetBookByID(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if f.mfs.VouchedCachedRow(book, title) == nil {
		t.Fatal("fixture: the no-author row must still be vouched for the book")
	}
	if _, verdict, _ := f.mfs.CachedBatchVerdict(book, title, metafetch.SearchAuthorHint("Synthetic Writer")); verdict != metafetch.BatchVerdictNone {
		t.Fatalf("fixture: verdict %v, want None (stale row, no usable candidate)", verdict)
	}
	// The scheduled tick selects it: a current fingerprint, no usable
	// candidate, and a fallback provider still owed.
	sel, err := unfetchedCandidateBookIDs(context.Background(), f.store, f.mfs, f.s.newFolderMemo(f.store), nil, 800)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if !slices.Contains(sel.IDs, b.ID) || sel.FallbackUnusable != 1 {
		t.Fatalf("fixture: selection %v (unusable %d), want the book selected as holding only unusable candidates", sel.IDs, sel.FallbackUnusable)
	}
	// Every provider has nothing to say this time.
	f.audible.answer, f.openlib.answer, f.google.answer = nil, nil, nil
	f.resetCalls()
	return book
}

// An empty chain refetch -- the scheduled fetch's non-forced one, or a forced
// refetch -- of a row vouched for under other hashed inputs keeps its
// candidates. Before the fix it wrote Candidates: [] over the row: the
// rejected (still reviewable) candidate was lost on every scheduled tick.
func TestEmptyRefetch_KeepsCandidatesOfRowVouchedUnderOtherHash(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprintf("force=%v", force), func(t *testing.T) {
			f := newFallbackFixture(t, 800)
			const title = "Carried Row Example"
			b := seedNoAuthorRow(t, f, title)

			runCandidateFetch(t, f.s, fmt.Sprintf("op-carry-%v", force), []string{b.ID}, force)
			if f.audible.calls.Load() == 0 {
				t.Fatal("the chain was not asked: the probe proves nothing")
			}
			row, err := f.store.GetMetadataCache(b.ID)
			if err != nil || row == nil {
				t.Fatalf("row: %+v %v", row, err)
			}
			if srcs := candidateSources(t, row); len(srcs) != 1 || srcs[0] != "Audible" {
				t.Fatalf("row candidates from %v after an empty refetch; want the stored Audible candidate kept", srcs)
			}
			if row.LastEmptyFetchAt == nil {
				t.Error("the empty look was not recorded (LastEmptyFetchAt)")
			}
			if time.Since(row.FetchedAt) < 24*time.Hour {
				t.Errorf("FetchedAt re-dated to %v: carried candidates must keep their own date", row.FetchedAt)
			}
		})
	}
}

// A row the book no longer answers to (another title) is not vouched: an
// empty refetch must not carry its candidates onto the book.
func TestEmptyRefetch_DoesNotCarryRowOfAnotherIdentity(t *testing.T) {
	f := newFallbackFixture(t, 800)
	b := f.book(t, "Current Title Example")
	if err := f.store.PutMetadataCache(&database.MetadataCandidateCache{
		BookID: b.ID, FetchedAt: time.Now().UTC().AddDate(0, 0, -200), SearchFingerprint: "x",
		SourceHash: "not-this-books-hash",
		Candidates: candidateJSON(t, metafetch.MetadataCandidate{Source: "Audible", Title: "Some Other Book", Score: 0.5}),
	}); err != nil {
		t.Fatal(err)
	}
	book, err := f.store.GetBookByID(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if f.mfs.VouchedCachedRow(book, book.Title) != nil {
		t.Fatal("a row hashed for another identity must not be vouched")
	}
	runCandidateFetch(t, f.s, "op-carry-other", []string{b.ID}, true)
	row, err := f.store.GetMetadataCache(b.ID)
	if err != nil || row == nil {
		t.Fatalf("row: %+v %v", row, err)
	}
	if len(row.Candidates) != 0 {
		t.Fatalf("row candidates %v; another identity's candidates must not be carried", candidateSources(t, row))
	}
}

// A never-fetched book can end in a Google step (its chain finds nothing),
// so the selection funds it too -- after every book already known to owe
// Google. With one lookup left today, the owed book gets it; the
// never-fetched books are still selected for the chain and Open Library,
// their Google step put off.
func TestUnfetchedSelection_NeverFetchedBooksCountAgainstGoogleBudget(t *testing.T) {
	f := newFallbackFixture(t, 0)
	owed := f.book(t, "Owed Google Example")
	f.run(t, owed.ID) // chain and Open Library empty, Google deferred (no budget)
	fresh1 := f.book(t, "Never Fetched One")
	fresh2 := f.book(t, "Never Fetched Two")

	perBook := googleRequestsPerBook()
	sel, err := unfetchedCandidateBookIDs(context.Background(), f.store, f.mfs, f.s.newFolderMemo(f.store), nil, perBook)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	want := []string{owed.ID, fresh1.ID, fresh2.ID}
	slices.Sort(want)
	wantCapped := []string{fresh1.ID, fresh2.ID}
	slices.Sort(wantCapped)
	if !slices.Equal(sel.IDs, want) || !slices.Equal(sel.GoogleCapped, wantCapped) || sel.ChainCapped != 2 || sel.FallbackCapped != 0 {
		t.Fatalf("selected %v, google-capped %v, chain-capped %d, fallback-capped %d; want all three selected, the never-fetched two Google-capped",
			sel.IDs, sel.GoogleCapped, sel.ChainCapped, sel.FallbackCapped)
	}

	// The run spends exactly the one funded lookup.
	f.limit = 800
	f.resetCalls()
	runFetchOp(t, f.s, "op-chain-capped", metadataCandidateFetchOpParams{BookIDs: sel.IDs, GoogleCappedBookIDs: sel.GoogleCapped})
	if got := f.google.calls.Load(); got != 1 {
		t.Fatalf("Google Books calls = %d, want 1 (only the funded, owed book)", got)
	}
	if f.openlib.calls.Load() < 2 {
		t.Fatalf("Open Library calls = %d, want the never-fetched books still asked of it", f.openlib.calls.Load())
	}
}
