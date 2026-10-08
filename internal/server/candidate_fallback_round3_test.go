// file: internal/server/candidate_fallback_round3_test.go
// version: 1.0.5
// guid: 7f057c74-2af0-44a5-bf9a-214d739ab5f0
// last-edited: 2026-10-06

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
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
		out := applyCachedCandidateForBookTimed(svc, books(), "b1", false, nil, metafetch.NewApplyPhaseTimings(), nil, ownerMarker(), "")
		if out.Applied || out.OwnerReviewed || out.Reason != applySkipGateBlocked ||
			out.Gate == nil || out.Gate.Reason != applygate.ReasonReviewOnlySource {
			t.Fatalf("%s via hashless marker: outcome %+v (gate %+v), want gate_blocked / review_only_source", source, out, out.Gate)
		}
		if len(svc.applyOpts) != 0 {
			t.Fatalf("%s via hashless marker: applied %+v", source, svc.applyOpts)
		}

		for name, pin := range map[string]*metafetch.CandidatePin{"bulk pin": bulkPin(cand), "row pin": rowPin(cand)} {
			svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
			out := applyCachedCandidateForBookTimed(svc, books(), "b1", false, nil, metafetch.NewApplyPhaseTimings(), nil, pin, "")
			if !out.Applied || !out.OwnerReviewed {
				t.Fatalf("%s via %s matching the shown candidate: outcome %+v, want applied as owner-reviewed", source, name, out)
			}
		}
	}
}

// The select-all (all_cached) preview reports the hashless marker's rule, the
// one its apply runs under: a review-only candidate is not "would apply"
// there, while a listed-book preview still says a single-row Apply would land
// it, and a chain candidate refused on certainty legs is "would apply" under
// both rules.
func TestBulkApplyPreview_SelectAllUsesTheUnseenRule(t *testing.T) {
	tenHours := 36000
	books := fakeBooks{"b1": {ID: "b1", Title: "Moon Book", FilePath: "/lib/Zed Quill/Moon Book/Moon Book.m4b", Duration: &tenHours}}
	for _, source := range []string{"Google Books", "Open Library"} {
		cand := metafetch.MetadataCandidate{Title: "Moon Book", Author: "Zed Quill", Source: source, Score: 0.99, DurationSec: 36000}
		svc := fakePreviewSvc{&fakeApplySvc{candidates: candidateJSON(t, cand)}}
		plan := planCachedApply(svc, books, "b1", nil, nil)
		if plan.Gate == nil || plan.Gate.Reason != applygate.ReasonReviewOnlySource {
			t.Fatalf("%s: plan gate %+v, want review_only_source", source, plan.Gate)
		}
		if row := previewBulkApplyRow(svc, "b1", plan, false, false); !row.OwnerReviewedWouldApply {
			t.Fatalf("%s listed preview: %+v, want a single-row Apply to land it", source, row)
		}
		if row := previewBulkApplyRow(svc, "b1", plan, false, true); row.OwnerReviewedWouldApply || row.Verdict != previewVerdictBlocked {
			t.Fatalf("%s select-all preview: %+v, want blocked and not owner-reviewable", source, row)
		}
	}

	chainBooks, chain := refusedByAuthorAndTranscription()
	svc := fakePreviewSvc{&fakeApplySvc{candidates: candidateJSON(t, chain)}}
	plan := planCachedApply(svc, chainBooks, "b1", nil, nil)
	if row := previewBulkApplyRow(svc, "b1", plan, false, true); !row.OwnerReviewedWouldApply {
		t.Fatalf("chain candidate, select-all preview: %+v, want the marker to lift the certainty legs", row)
	}

	// The op wires it: all_cached runs the marker's rule, a book_ids list the
	// single-row rule.
	cand := metafetch.MetadataCandidate{Title: "Moon Book", Author: "Zed Quill", Source: "Google Books", Score: 0.99, DurationSec: 36000}
	for _, tc := range []struct {
		name   string
		params bulkApplyPreviewParams
		want   bool
	}{
		{"all_cached", bulkApplyPreviewParams{AllCached: true}, false},
		{"book_ids", bulkApplyPreviewParams{BookIDs: []string{"b1"}}, true},
	} {
		svc := listingPreviewSvc{fakePreviewSvc: fakePreviewSvc{&fakeApplySvc{candidates: candidateJSON(t, cand)}}, ids: []string{"b1"}}
		res := &previewResults{}
		if err := runBulkApplyPreview(context.Background(), &previewTestReporter{}, svc, books, res, "op-preview", tc.params); err != nil {
			t.Fatalf("%s: runBulkApplyPreview: %v", tc.name, err)
		}
		found := false
		for _, r := range res.rows {
			if r.BookID != "b1" {
				continue
			}
			var row bulkApplyPreviewRow
			if err := json.Unmarshal([]byte(r.ResultJSON), &row); err != nil {
				t.Fatal(err)
			}
			found = true
			if row.OwnerReviewedWouldApply != tc.want {
				t.Fatalf("%s preview: owner_reviewed_would_apply %v, want %v (%+v)", tc.name, row.OwnerReviewedWouldApply, tc.want, row)
			}
		}
		if !found {
			t.Fatalf("%s preview wrote no row for b1", tc.name)
		}
	}
}

// The marker still lifts the certainty legs on a chain (Audible) candidate,
// as the owner ruled for every review-page bulk button.
func TestUnseenOwnerMarker_StillLiftsCertaintyOnChainCandidate(t *testing.T) {
	books, cand := refusedByAuthorAndTranscription()
	svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
	out := applyCachedCandidateForBookTimed(svc, books, "b1", false, nil, metafetch.NewApplyPhaseTimings(), nil, ownerMarker(), "")
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

// Review round 3, SF-1: a legacy row with no SourceHash (written before the
// field existed) and candidates is in the stale-refetch set; the forced
// refetch that set drives, answering nothing, keeps its candidates. The apply
// gate accepts such a row (ValidateCachedIdentity fails open), so the carry
// vouches it the same way.
func TestEmptyForcedRefetch_KeepsCandidatesOfHashlessLegacyRow(t *testing.T) {
	f := newFallbackFixture(t, 800)
	b := f.book(t, "Hashless Legacy Example")
	if err := f.store.PutMetadataCache(&database.MetadataCandidateCache{
		BookID: b.ID, FetchedAt: time.Now().UTC().AddDate(0, 0, -200),
		Candidates: candidateJSON(t, metafetch.MetadataCandidate{Source: "Audible", Title: "Hashless Legacy Example", Score: 0.5}),
	}); err != nil {
		t.Fatal(err)
	}
	stale, err := handlers.StaleCachedBookIDs(context.Background(), f.store, f.mfs)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(stale, b.ID) {
		t.Fatalf("fixture: stale set %v does not hold the hashless row", stale)
	}
	runCandidateFetch(t, f.s, "op-hashless", stale, true)
	if f.audible.calls.Load() == 0 {
		t.Fatal("the chain was not asked: the probe proves nothing")
	}
	row, err := f.store.GetMetadataCache(b.ID)
	if err != nil || row == nil {
		t.Fatalf("row: %+v %v", row, err)
	}
	if srcs := candidateSources(t, row); len(srcs) != 1 || srcs[0] != "Audible" {
		t.Fatalf("row candidates from %v after an empty forced refetch; want the legacy Audible candidate kept", srcs)
	}
}

// Review round 3, SF-2: the search dialog's plain fetch (no typed query)
// hashes its row with no inputs. Neither the scheduled fetch nor a forced
// refetch may wipe that row's candidates with an empty answer: its search
// fingerprint proves it asked the book's current questions.
func TestEmptyRefetch_KeepsCandidatesOfPlainFetchRow(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprintf("force=%v", force), func(t *testing.T) {
			f := newFallbackFixture(t, 800)
			const title = "Plain Fetch Example"
			a, err := f.store.CreateAuthor("Synthetic Writer")
			if err != nil {
				t.Fatal(err)
			}
			b := f.book(t, title, func(bk *database.Book) { bk.AuthorID = &a.ID })
			f.audible.answer = answersRecord(metadata.BookMetadata{Title: title + " Collected", Author: "Synthetic Writer",
				CoverURL: "https://example.invalid/a.jpg"})
			// The dialog's plain fetch: no query, author, narrator or series.
			if _, err := f.mfs.FetchAndCache(context.Background(), b.ID, "", "", "", "",
				metafetch.SearchOptions{OnlySources: []string{"Audible"}}); err != nil {
				t.Fatalf("seed: %v", err)
			}
			row, err := f.store.GetMetadataCache(b.ID)
			if err != nil || row == nil || len(row.Candidates) != 1 ||
				row.SourceHash != metafetch.BatchSourceHash(b.ID, "", "") {
				t.Fatalf("fixture: seed row %+v %v, want one candidate under the no-inputs hash", row, err)
			}
			row.FetchedAt = time.Now().UTC().AddDate(0, 0, -200)
			if err := f.store.PutMetadataCache(row); err != nil {
				t.Fatal(err)
			}
			rejectCandidate(t, f.store, b.ID, "Audible", title+" Collected")
			if err := database.InvalidateAllCachedMetadataFetchesForBook(f.store, b.ID); err != nil {
				t.Fatal(err)
			}
			if !force {
				sel, err := unfetchedCandidateBookIDs(context.Background(), f.store, f.mfs, f.s.newFolderMemo(f.store), nil, 800)
				if err != nil {
					t.Fatalf("select: %v", err)
				}
				if !slices.Contains(sel.IDs, b.ID) {
					t.Fatalf("fixture: the scheduled selection %v does not pick the book", sel.IDs)
				}
			}
			f.audible.answer, f.openlib.answer, f.google.answer = nil, nil, nil
			f.resetCalls()

			runCandidateFetch(t, f.s, fmt.Sprintf("op-plain-%v", force), []string{b.ID}, force)
			if f.audible.calls.Load() == 0 {
				t.Fatal("the chain was not asked: the probe proves nothing")
			}
			got, err := f.store.GetMetadataCache(b.ID)
			if err != nil || got == nil {
				t.Fatalf("row: %+v %v", got, err)
			}
			if srcs := candidateSources(t, got); len(srcs) != 1 || srcs[0] != "Audible" {
				t.Fatalf("row candidates from %v after an empty refetch; want the dialog's Audible candidate kept", srcs)
			}
		})
	}
}
