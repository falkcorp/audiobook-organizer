// file: internal/server/batch_apply_claims_test.go
// version: 1.2.0
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

// TestClaimIndex_FailsClosed: any book the index cannot read fails the whole
// build, like a failed listing; a smaller index would let a sibling through.
func TestClaimIndex_FailsClosed(t *testing.T) {
	cand := moondust()
	books := moondustBooks(database.Book{ID: "b2", Title: "A Fall of Moondust", FilePath: "/lib/Arthur C. Clarke/A Fall of Moondust/Part 2"})
	ok := listingSvc{fakeApplySvc: &fakeApplySvc{candidates: candidateJSON(t, cand)}, ids: []string{"b1", "b2"}}
	cases := map[string]func() error{
		"listing error": func() error {
			_, err := cachedClaimIndex(context.Background(), listingSvc{fakeApplySvc: ok.fakeApplySvc, listErr: errors.New("boom")}, books)
			return err
		},
		"book read error": func() error {
			_, err := cachedClaimIndex(context.Background(), ok, filesBooks{fakeBooks: books, readErr: map[string]error{"b2": errors.New("disk")}})
			return err
		},
		"cache read error": func() error {
			svc := listingSvc{fakeApplySvc: &fakeApplySvc{candidates: candidateJSON(t, cand), getErr: errors.New("cache")}, ids: []string{"b1", "b2"}}
			_, err := cachedClaimIndex(context.Background(), svc, books)
			return err
		},
		"undecodable operation row": func() error {
			_, err := buildClaimIndex(context.Background(), []string{"b1", "b2"}, opResultClaimLoader(books, func(string) (CandidateResult, bool, error) {
				return CandidateResult{}, false, errors.New("bad json")
			}))
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
