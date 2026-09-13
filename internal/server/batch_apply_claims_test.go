// file: internal/server/batch_apply_claims_test.go
// version: 1.3.0
// guid: e4b9c7a2-1f36-4d80-b5c9-8a0d2e6f3b71
// last-edited: 2026-09-13

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// GetBookFiles gives fakeBooks the file read a claim needs: no file rows, so
// nothing says a book's files are gone.
func (f fakeBooks) GetBookFiles(string) ([]database.BookFile, error) { return nil, nil }

// filesBooks is fakeBooks with per-book file rows and read errors.
type filesBooks struct {
	fakeBooks
	files   map[string][]database.BookFile
	readErr map[string]error
}

func (f filesBooks) GetBookByID(id string) (*database.Book, error) {
	if err := f.readErr[id]; err != nil {
		return nil, err
	}
	return f.fakeBooks.GetBookByID(id)
}

func (f filesBooks) GetBookFiles(id string) ([]database.BookFile, error) { return f.files[id], nil }

// listingSvc adds the cache listing to fakeApplySvc: the books with cached
// candidates, the universe cachedClaimIndex draws from.
type listingSvc struct {
	*fakeApplySvc
	ids     []string
	listErr error
}

func (l listingSvc) ListCachedSummaries(context.Context) ([]metafetch.MetadataCacheSummary, error) {
	out := make([]metafetch.MetadataCacheSummary, 0, len(l.ids))
	for _, id := range l.ids {
		out = append(out, metafetch.MetadataCacheSummary{BookID: id, CandidateCount: 1})
	}
	return out, l.listErr
}

func opRows(rows map[string]CandidateResult) func(string) (CandidateResult, bool, error) {
	return func(id string) (CandidateResult, bool, error) {
		cr, ok := rows[id]
		return cr, ok, nil
	}
}

// TestBuildClaimIndex_FeedsPlan: two folders of one book take the same
// cached candidate. With the claim index the gate refuses the whole-book
// folder (its sibling is CD 2); without one (nil) that test is skipped.
func TestBuildClaimIndex_FeedsPlan(t *testing.T) {
	cand := metafetch.MetadataCandidate{Title: "The Long Road", Author: "Jane Roe", ASIN: "B0TESTROAD", Score: 0.99}
	books := fakeBooks{
		"b1": {ID: "b1", Title: "The Long Road", FilePath: "/lib/Jane Roe/The Long Road/The Long Road.m4b", Author: &database.Author{Name: "Jane Roe"}},
		"b2": {ID: "b2", Title: "The Long Road", FilePath: "/lib/Jane Roe/The Long Road/CD 2", Author: &database.Author{Name: "Jane Roe"}},
	}
	svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}

	claims, err := buildClaimIndex(context.Background(), []string{"b1", "b2"}, cachedClaimLoader(svc, books))
	if err != nil || claims.Len() != 2 {
		t.Fatalf("claims = %d, err %v, want 2", claims.Len(), err)
	}
	p := planCachedApply(svc, books, "b1", claims)
	if p.Reason != applySkipGateBlocked || p.Gate == nil || p.Gate.Evidence.Reason != applygate.ReasonPartialBook {
		t.Fatalf("with claims: reason=%q gate=%+v", p.Reason, p.Gate)
	}
	if p := planCachedApply(svc, books, "b1", nil); p.Gate != nil && p.Gate.Evidence.Reason == applygate.ReasonPartialBook {
		t.Fatalf("without claims the sibling test must be skipped: %+v", p.Gate.Evidence)
	}

	crs := map[string]CandidateResult{"b1": {Status: "matched", Candidate: &cand}, "b2": {Status: "matched", Candidate: &cand}}
	opClaims, err := buildClaimIndex(context.Background(), []string{"b1", "b2"}, opResultClaimLoader(books, opRows(crs)))
	if err != nil || opClaims.Len() != claims.Len() {
		t.Fatalf("op-result index %d claims (err %v), cache index %d: the two paths must agree", opClaims.Len(), err, claims.Len())
	}
}

// TestClaimIndex_SubsetApplySeesSibling: the apply is asked about b1 alone,
// yet the sibling folder b2 holding Part 2 of the same candidate still
// blocks it, as it does in a preview of both.
func TestClaimIndex_SubsetApplySeesSibling(t *testing.T) {
	cand := moondust()
	books := moondustBooks(database.Book{ID: "b2", Title: "A Fall of Moondust", FilePath: "/lib/Arthur C. Clarke/A Fall of Moondust/Part 2"})
	svc := listingSvc{fakeApplySvc: &fakeApplySvc{candidates: candidateJSON(t, cand)}, ids: []string{"b1", "b2"}}

	claims, err := cachedClaimIndex(context.Background(), svc, books)
	if err != nil {
		t.Fatal(err)
	}
	if p := planCachedApply(svc, books, "b1", claims); p.Reason != applySkipGateBlocked || p.Gate == nil || p.Gate.Evidence.Reason != applygate.ReasonPartialBook {
		t.Fatalf("cached subset apply of b1: reason=%q gate=%+v", p.Reason, p.Gate)
	}

	rows := map[string]CandidateResult{"b1": {Status: "matched", Candidate: &cand}, "b2": {Status: "matched", Candidate: &cand}}
	opClaims, err := buildClaimIndex(context.Background(), keysOf(rows), opResultClaimLoader(books, opRows(rows)))
	if err != nil {
		t.Fatal(err)
	}
	if p := planOpResultApply(books, "b1", rows["b1"], opClaims); p.Reason != applySkipGateBlocked || p.Gate.Evidence.Reason != applygate.ReasonPartialBook {
		t.Fatalf("op-result subset apply of b1: reason=%q gate=%+v", p.Reason, p.Gate)
	}
}

// TestClaimIndex_SkipsRowsThatAreNotParts: a soft-deleted "CD2" row, a
// non-primary version and a part whose files are all missing share the live
// primary's ASIN and folder, and none of them may block it. The live control
// proves the same fixture does block.
func TestClaimIndex_SkipsRowsThatAreNotParts(t *testing.T) {
	yes, no := true, false
	cd2 := func() database.Book {
		return database.Book{ID: "b2", Title: "A Fall of Moondust CD2", FilePath: "/lib/Arthur C. Clarke/A Fall of Moondust/CD2"}
	}
	cases := []struct {
		name    string
		sibling func() database.Book
		files   []database.BookFile
		blocks  bool
	}{
		{name: "live CD2 sibling (control)", sibling: cd2, blocks: true},
		{name: "soft-deleted CD2 sibling", sibling: func() database.Book { b := cd2(); b.MarkedForDeletion = &yes; return b }},
		{name: "non-primary version", sibling: func() database.Book { b := cd2(); b.IsPrimaryVersion = &no; return b }},
		{name: "part row whose files are all missing", sibling: cd2,
			files: []database.BookFile{{BookID: "b2", Missing: true}, {BookID: "b2", Missing: true}}},
		{name: "part row with one file still present (control)", sibling: cd2,
			files: []database.BookFile{{BookID: "b2", Missing: true}, {BookID: "b2"}}, blocks: true},
	}
	cand := moondust()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			books := filesBooks{fakeBooks: moondustBooks(tc.sibling()), files: map[string][]database.BookFile{"b2": tc.files}}
			svc := listingSvc{fakeApplySvc: &fakeApplySvc{candidates: candidateJSON(t, cand)}, ids: []string{"b1", "b2"}}
			claims, err := cachedClaimIndex(context.Background(), svc, books)
			if err != nil {
				t.Fatal(err)
			}
			p := planCachedApply(svc, books, "b1", claims)
			blocked := p.Gate != nil && p.Gate.Evidence.Reason == applygate.ReasonPartialBook
			if blocked != tc.blocks {
				t.Fatalf("partial_book blocked=%v, want %v (reason=%q gate=%+v)", blocked, tc.blocks, p.Reason, p.Gate)
			}
		})
	}
}

// badCacheSvc fails the cache read for one book only.
type badCacheSvc struct {
	listingSvc
	bad string
}

func (s badCacheSvc) GetCachedCandidates(id string) (*metafetch.MetadataCandidateCache, bool, error) {
	if id == s.bad {
		return nil, false, errors.New("cache read failed")
	}
	return s.listingSvc.GetCachedCandidates(id)
}

// TestClaimIndex_UnreadableBlocksOnlyLookAlikes is the owner decision of
// 2026-09-13: a book the index cannot read neither fails the run nor is
// silently dropped. It is counted, and rows that look like it are blocked.
func TestClaimIndex_UnreadableBlocksOnlyLookAlikes(t *testing.T) {
	cand := moondust()
	books := moondustBooks(database.Book{ID: "b2", Title: "A Fall of Moondust CD2", FilePath: "/lib/Arthur C. Clarke/A Fall of Moondust/CD2"})
	books["b3"] = &database.Book{ID: "b3", Title: "Rendezvous with Rama", FilePath: "/other/Rips/Rama/Rama.m4b", Author: &database.Author{Name: "Arthur C. Clarke"}}
	base := listingSvc{fakeApplySvc: &fakeApplySvc{candidates: candidateJSON(t, cand)}, ids: []string{"b1", "b2", "b3"}}
	partial := func(svc cachedApplyService, books bookReader, id string, claims *applygate.ClaimIndex) (bool, string) {
		p := planCachedApply(svc, books, id, claims)
		if p.Gate != nil && p.Gate.Evidence.Reason == applygate.ReasonPartialBook {
			return true, p.Gate.Evidence.Detail
		}
		return false, ""
	}

	// (a) b2's cache read fails but its folder is known: its look-alike b1
	// is blocked, the unrelated b3 is not, and the run does not fail.
	svc := badCacheSvc{listingSvc: base, bad: "b2"}
	claims, err := cachedClaimIndex(context.Background(), svc, books)
	if err != nil {
		t.Fatalf("an unreadable book must not fail the run: %v", err)
	}
	if claims.Unreadable() != 1 {
		t.Fatalf("Unreadable = %d, want 1", claims.Unreadable())
	}
	if blocked, detail := partial(svc, books, "b1", claims); !blocked || detail != "sibling b2 unreadable; manual review" {
		t.Fatalf("look-alike b1: blocked=%v detail=%q", blocked, detail)
	}
	if blocked, detail := partial(svc, books, "b3", claims); blocked {
		t.Fatalf("unrelated b3 blocked: %q", detail)
	}

	// The book row itself unreadable, the cache readable: the candidate's
	// ASIN is still known, so a row taking the same ASIN is blocked.
	byASIN, err := cachedClaimIndex(context.Background(), base, filesBooks{fakeBooks: books, readErr: map[string]error{"b2": errors.New("disk")}})
	if err != nil || byASIN.Unreadable() != 1 {
		t.Fatalf("unreadable=%d err=%v", byASIN.Unreadable(), err)
	}
	if blocked, _ := partial(base, books, "b1", byASIN); !blocked {
		t.Fatal("same-ASIN row not blocked")
	}

	// (b) nothing known (book row and cache both unreadable): counted, the
	// run goes on, and it blocks nothing.
	nothing, err := cachedClaimIndex(context.Background(), svc, filesBooks{fakeBooks: books, readErr: map[string]error{"b2": errors.New("disk")}})
	if err != nil {
		t.Fatalf("an unknown unreadable book must not fail the run: %v", err)
	}
	if nothing.Unreadable() != 1 {
		t.Fatalf("Unreadable = %d, want 1", nothing.Unreadable())
	}
	if blocked, detail := partial(svc, books, "b1", nothing); blocked {
		t.Fatalf("nothing is known about b2, yet b1 was blocked: %q", detail)
	}

	// An undecodable operation row is counted the same way.
	opIdx, err := buildClaimIndex(context.Background(), []string{"b1", "b2"}, opResultClaimLoader(books, func(id string) (CandidateResult, bool, error) {
		if id == "b2" {
			return CandidateResult{}, false, errors.New("bad json")
		}
		return CandidateResult{Status: "matched", Candidate: &cand}, true, nil
	}))
	if err != nil || opIdx.Unreadable() != 1 {
		t.Fatalf("op rows: unreadable=%d err=%v", opIdx.Unreadable(), err)
	}
}

// TestClaimIndex_FailsClosed: without the cache listing, or with a cancelled
// context, there is no index to trust, and the run fails before any write.
func TestClaimIndex_FailsClosed(t *testing.T) {
	cand := moondust()
	books := moondustBooks(database.Book{ID: "b2", Title: "A Fall of Moondust", FilePath: "/lib/Arthur C. Clarke/A Fall of Moondust/Part 2"})
	ok := listingSvc{fakeApplySvc: &fakeApplySvc{candidates: candidateJSON(t, cand)}, ids: []string{"b1", "b2"}}
	cases := map[string]func() error{
		"listing error": func() error {
			_, err := cachedClaimIndex(context.Background(), listingSvc{fakeApplySvc: ok.fakeApplySvc, listErr: errors.New("boom")}, books)
			return err
		},
		"cancelled context": func() error {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err := cachedClaimIndex(ctx, ok, books)
			return err
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			if err := run(); err == nil {
				t.Fatal("error swallowed: the index must fail closed")
			}
		})
	}
}

func moondust() metafetch.MetadataCandidate {
	return metafetch.MetadataCandidate{Title: "A Fall of Moondust", Author: "Arthur C. Clarke", ASIN: "B0TESTMOON", Score: 0.99}
}

// moondustBooks is the live primary b1 plus the given sibling b2.
func moondustBooks(sibling database.Book) fakeBooks {
	sibling.Author = &database.Author{Name: "Arthur C. Clarke"}
	return fakeBooks{
		"b1": {ID: "b1", Title: "A Fall of Moondust", FilePath: "/lib/Arthur C. Clarke/A Fall of Moondust/A Fall of Moondust.m4b", Author: &database.Author{Name: "Arthur C. Clarke"}},
		"b2": &sibling,
	}
}
