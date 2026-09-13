// file: internal/applygate/partial_test.go
// version: 1.1.0
// guid: 6a2d8f31-0b9e-4c74-8e15-c3f7a9d02b86
// last-edited: 2026-09-13

package applygate

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

func TestCheckPartialBook(t *testing.T) {
	whole := metafetch.MetadataCandidate{Title: "A Fall of Moondust", Author: "Arthur C. Clarke", ASIN: "B0TESTMOON"}
	cases := []struct {
		name    string
		book    database.Book
		cand    metafetch.MetadataCandidate
		runtime string
		claims  func() *ClaimIndex
		want    string
	}{
		{
			name: "Disc 2 of a book matched to the whole book",
			book: database.Book{ID: "b1", Title: "A Fall of Moondust", FilePath: "/lib/Arthur C. Clarke/A Fall of Moondust/Disc 2"},
			cand: whole, runtime: OutcomeBlock, want: ReasonPartialBook,
		},
		{
			name: "Part II against Part 3",
			book: database.Book{ID: "b1", Title: "Shogun Part II"},
			cand: metafetch.MetadataCandidate{Title: "Shogun, Part 3"}, runtime: OutcomeUnknown, want: ReasonPartialBook,
		},
		{
			name: "box set matched to one volume",
			book: database.Book{ID: "b1", Title: "Dream Stream Reality Publisher's Pack: Books 1-2"},
			cand: metafetch.MetadataCandidate{Title: "Dream Stream Reality"}, runtime: OutcomeNeutral, want: ReasonPartialBook,
		},
		{
			name: "sibling folder holds part 2 of the same candidate",
			book: database.Book{ID: "b1", Title: "A Fall of Moondust", FilePath: "/lib/Arthur C. Clarke/A Fall of Moondust/A Fall of Moondust.m4b"},
			cand: whole, runtime: OutcomeUnknown,
			claims: func() *ClaimIndex {
				x := NewClaimIndex()
				x.Add(&database.Book{ID: "b2", Title: "A Fall of Moondust", FilePath: "/lib/Arthur C. Clarke/A Fall of Moondust/Part 2"}, &whole)
				return x
			},
			want: ReasonPartialBook,
		},
		{
			name: "part marker only in a single file's name",
			book: database.Book{ID: "b1", Title: "Lonesome Dove", FilePath: "/lib/Larry McMurtry/Lonesome Dove/Lonesome Dove Part 1.m4b"},
			cand: metafetch.MetadataCandidate{Title: "Lonesome Dove"}, runtime: OutcomeUnknown, want: ReasonPartialBook,
		},
		{
			name: "a disc range is the whole book",
			book: database.Book{ID: "b1", Title: "A Fall of Moondust", FilePath: "/lib/Arthur C. Clarke/Moondust (Disc 1-3)"},
			cand: whole, runtime: OutcomeUnknown,
		},
		{
			name: "same part on both sides",
			book: database.Book{ID: "b1", Title: "Shogun Part 2"},
			cand: metafetch.MetadataCandidate{Title: "Shogun (Part 2)"}, runtime: OutcomeUnknown,
		},
		{
			name: "runtime agrees: the folder's Disc label is stale",
			book: database.Book{ID: "b1", Title: "A Fall of Moondust", FilePath: "/lib/Arthur C. Clarke/A Fall of Moondust/Disc 2"},
			cand: whole, runtime: OutcomeAgree,
		},
		{
			name: "duplicate import with no part marker is dedup's job",
			book: database.Book{ID: "b1", Title: "86", FilePath: "/lib/Unknown Author/86-Neon"},
			cand: metafetch.MetadataCandidate{Title: "86", ASIN: "B0TEST86"}, runtime: OutcomeUnknown,
			claims: func() *ClaimIndex {
				x := NewClaimIndex()
				x.Add(&database.Book{ID: "b2", Title: "86", FilePath: "/lib/Unknown Author/86"}, &metafetch.MetadataCandidate{Title: "86", ASIN: "b0test86"})
				return x
			},
		},
		{
			name: "part in an unrelated folder tree",
			book: database.Book{ID: "b1", Title: "A Fall of Moondust", FilePath: "/lib/Arthur C. Clarke/A Fall of Moondust/A Fall of Moondust.m4b"},
			cand: whole, runtime: OutcomeUnknown,
			claims: func() *ClaimIndex {
				x := NewClaimIndex()
				x.Add(&database.Book{ID: "b2", Title: "Moondust Part 2", FilePath: "/other/Rips/Moondust Part 2"}, &whole)
				return x
			},
		},
		{
			name: "no batch data",
			book: database.Book{ID: "b1", Title: "A Fall of Moondust", FilePath: "/lib/Arthur C. Clarke/A Fall of Moondust/A Fall of Moondust.m4b"},
			cand: whole, runtime: OutcomeUnknown,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var claims *ClaimIndex
			if c.claims != nil {
				claims = c.claims()
			}
			r := checkPartialBook(&c.book, &c.cand, c.runtime, claims)
			if r.Outcome == OutcomeAgree {
				t.Fatalf("partial_book must never agree: %+v", r)
			}
			if r.Reason != c.want {
				t.Fatalf("reason %q (%s), want %q", r.Reason, r.Detail, c.want)
			}
		})
	}
}

// TestEvaluateInBatch_SiblingPart confirms the index reaches the evidence leg
// through EvaluateInBatch, and that Evaluate (no batch) does not see it.
func TestEvaluateInBatch_SiblingPart(t *testing.T) {
	cand := metafetch.MetadataCandidate{Title: "The Long Road", Author: "Jane Roe", ASIN: "B0TESTROAD", Score: 0.99}
	book := &database.Book{ID: "b1", Title: "The Long Road", FilePath: "/lib/Jane Roe/The Long Road/The Long Road.m4b",
		Author: &database.Author{Name: "Jane Roe"}}
	claims := NewClaimIndex()
	claims.Add(book, &cand)
	claims.Add(&database.Book{ID: "b2", Title: "The Long Road", FilePath: "/lib/Jane Roe/The Long Road/CD 2"}, &cand)
	if claims.Len() != 2 {
		t.Fatalf("Len = %d, want 2", claims.Len())
	}
	if v := EvaluateInBatch(book, &cand, nil, claims); v.Allowed || v.Evidence.Reason != ReasonPartialBook {
		t.Fatalf("with claims: allowed=%v evidence reason %q (%s)", v.Allowed, v.Evidence.Reason, v.Evidence.Detail)
	}
	if v := Evaluate(book, &cand, nil); v.Evidence.Reason == ReasonPartialBook {
		t.Fatalf("without claims the sibling test must be skipped: %+v", v.Evidence)
	}
}
